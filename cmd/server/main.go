// Command server is the composition root for Green Stack Monitor.
//
// Startup order:
//  1. Config       — environment variables, fail fast if invalid
//  2. OTEL         — MeterProvider with Prometheus bridge
//  3. Infra        — cache, repository, estimator
//  4. Worker       — started before accepting traffic
//  5. Services / Handlers
//  6. Router
//  7. HTTP server + graceful shutdown
//
// OTEL is created before infra because the MeterProvider is injected into the
// worker and middleware. Creating instruments up front prevents components from
// recording metrics before the provider is ready.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"

	// OTEL API, SDK and Prometheus bridge.
	// Only main.go knows the concrete backend. Worker and middleware depend
	// only on the metric.MeterProvider interface.
	prometheusExporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"

	// Prometheus is used only to serve /metrics and collect Go runtime stats.
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/matheusjuliosantana/green-stack-monitor/internal/cache"
	"github.com/matheusjuliosantana/green-stack-monitor/internal/config"
	"github.com/matheusjuliosantana/green-stack-monitor/internal/handler"
	"github.com/matheusjuliosantana/green-stack-monitor/internal/repository"
	"github.com/matheusjuliosantana/green-stack-monitor/internal/service"
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/badge"
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/estimator"
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/middleware"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	if err := run(); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. Config.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// 2. OTEL: MeterProvider with Prometheus bridge.
	//
	// Architecture:
	//   OTEL SDK (metric.MeterProvider)
	//       └── prometheusExporter.New()
	//               └── prometheus.Registry
	//                       └── /metrics
	//
	// Worker and middleware only know metric.MeterProvider. Switching to OTLP
	// changes the exporter here without touching the rest of the code.

	// Use an explicit registry, never DefaultRegisterer. This keeps tests clean
	// and allows multiple instances in the same process.
	promReg := prometheus.NewRegistry()

	// Go runtime and process metrics share the same registry.
	promReg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	// OTEL to Prometheus bridge: every OTEL instrument created by the SDK is
	// exposed as a Prometheus metric in this registry.
	promExporter, err := prometheusExporter.New(
		prometheusExporter.WithRegisterer(promReg),
	)
	if err != nil {
		return fmt.Errorf("prometheus exporter: %w", err)
	}

	// MeterProvider is the OTEL SDK entry point. WithReader connects the exporter
	// that Prometheus calls on /metrics scrapes.
	mp := metric.NewMeterProvider(
		metric.WithReader(promExporter),
	)
	defer func() {
		// Flush pending metrics on shutdown.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mp.Shutdown(ctx)
	}()

	// 3. Infra.
	var appCache cache.Cache
	if cfg.RedisAddr != "" {
		rc, err := cache.NewRedis(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
		if err != nil {
			return fmt.Errorf("redis: %w", err)
		}
		defer rc.Close()
		appCache = rc
		slog.Info("cache backend: redis", "addr", cfg.RedisAddr)
	} else {
		appCache = cache.NewMemory()
		slog.Info("cache backend: in-memory (set REDIS_ADDR to enable Redis)")
	}

	repo := &repository.InMemoryEcoRepository{}

	est, err := estimator.New(cfg.CO2)
	if err != nil {
		return fmt.Errorf("estimator: %w", err)
	}

	// 4. Worker.
	// It receives metric.MeterProvider and does not know the backend is Prometheus.
	// It starts before the server so no trace is lost.
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()

	worker := middleware.NewWorker(
		middleware.WorkerConfig{
			BufferSize: cfg.WorkerBufferSize,
			Count:      cfg.WorkerCount,
		},
		est,
		repo,
		mp, // metric.MeterProvider interface, not a concrete backend
	)
	worker.Start(workerCtx)

	// 5. Services / Handlers.
	ecoSvc := service.NewEcoService(repo, appCache)
	ecoH := handler.NewEcoHandler(ecoSvc)

	co2PerReqFn := func() float64 {
		m, err := repo.Aggregates(context.Background())
		if err != nil || (m.Hits+m.Misses) == 0 {
			return 0
		}
		return m.TotalSaved / float64(m.Hits+m.Misses)
	}
	badgeCache := badge.NewCache(co2PerReqFn, 2*time.Minute)

	// 6. Router.
	r := chi.NewRouter()
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.Recoverer)
	r.Use(chiMiddleware.Timeout(30 * time.Second))

	r.Use(middleware.EcoMetrics(middleware.Options{
		Estimator:     est,
		Worker:        worker,
		MeterProvider: mp,
		SampleRate:    cfg.SampleRate,
	}))

	// Public routes.
	r.Get("/healthz", handler.Health)
	r.Get("/badge", badge.Handler(badgeCache))

	// /metrics is served by promhttp with the OTEL bridge registry. Prometheus
	// scrapes this endpoint for both green.* and Go runtime metrics.
	r.Handle("/metrics", promhttp.HandlerFor(promReg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))

	// Protected routes.
	r.Group(func(r chi.Router) {
		r.Use(middleware.JWTAuth(cfg.JWTSecret))
		r.Get("/me", handler.Me)
		r.Get("/eco/cache", ecoH.GetCacheMetrics)
	})

	if cfg.PProfEnabled {
		r.Mount("/debug", http.DefaultServeMux)
		slog.Warn("pprof enabled; make sure /debug is not publicly exposed")
	}

	// 7. HTTP server + graceful shutdown.
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("server starting", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("ListenAndServe", "err", err)
			quit <- syscall.SIGTERM
		}
	}()

	<-quit
	slog.Info("shutdown started")

	// Critical order: see docs/architecture.md#graceful-shutdown-order.
	httpCtx, httpCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer httpCancel()
	if err := srv.Shutdown(httpCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	cancelWorker() // stop doing I/O; workers still drain the channel
	worker.Stop()  // close the channel and wait for wg.Wait()

	slog.Info("server stopped successfully")
	return nil
}
