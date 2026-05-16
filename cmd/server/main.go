// Command server — composition root do Green Stack Monitor.
//
// Ordem de inicialização:
//  1. Config       — env vars, falha rápido se inválido
//  2. OTEL         — MeterProvider com bridge Prometheus
//  3. Infra        — cache, repository, estimator
//  4. Worker       — iniciado ANTES de aceitar tráfego
//  5. Services / Handlers
//  6. Router
//  7. HTTP server + graceful shutdown
//
// Por que OTEL antes da infra?
// O MeterProvider é injetado no Worker e no middleware. Criar os instrumentos
// OTEL antes de qualquer outra coisa garante que nenhum componente tente
// registrar uma métrica antes do provider estar pronto.
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

	// OTEL — API, SDK e bridge Prometheus.
	// Apenas main.go conhece o backend concreto.
	// O worker e o middleware dependem só da interface metric.MeterProvider.
	prometheusExporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"

	// Prometheus — apenas para servir /metrics e coletar Go runtime stats.
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/matheusjuliosantana/green-stack-monitor/internal/cache"
	"github.com/matheusjuliosantana/green-stack-monitor/internal/config"
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/estimator"
	"github.com/matheusjuliosantana/green-stack-monitor/internal/handler"
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/middleware"
	"github.com/matheusjuliosantana/green-stack-monitor/internal/repository"
	"github.com/matheusjuliosantana/green-stack-monitor/internal/service"
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/badge"
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
	// ── 1. Config ──────────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// ── 2. OTEL — MeterProvider com bridge Prometheus ──────────────────────
	//
	// Arquitetura:
	//   OTEL SDK (metric.MeterProvider)
	//       └── prometheusExporter.New()   ← bridge OTEL → Prometheus
	//               └── prometheus.Registry ← onde os metrics ficam
	//                       └── /metrics    ← scrapeado pelo Prometheus
	//
	// O worker e o middleware só conhecem metric.MeterProvider.
	// Trocar para OTLP (Datadog, Honeycomb etc.) é trocar o exporter
	// aqui em main.go — zero mudança no resto do código.

	// Registry explícito: nunca DefaultRegisterer.
	// Permite múltiplas instâncias e testes limpos.
	promReg := prometheus.NewRegistry()

	// Go runtime + process metrics no mesmo registry.
	promReg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	// Bridge OTEL → Prometheus: cada instrumento OTEL criado no SDK
	// aparece automaticamente como metric Prometheus no registry.
	promExporter, err := prometheusExporter.New(
		prometheusExporter.WithRegisterer(promReg),
	)
	if err != nil {
		return fmt.Errorf("prometheus exporter: %w", err)
	}

	// MeterProvider é o ponto de entrada do OTEL SDK.
	// WithReader conecta o exporter — ele será chamado no scrape do /metrics.
	mp := metric.NewMeterProvider(
		metric.WithReader(promExporter),
	)
	defer func() {
		// Flush de métricas pendentes no shutdown.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mp.Shutdown(ctx)
	}()

	// ── 3. Infra ───────────────────────────────────────────────────────────
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
		slog.Info("cache backend: in-memory (defina REDIS_ADDR para Redis)")
	}

	repo := &repository.InMemoryEcoRepository{}

	est, err := estimator.New(cfg.CO2)
	if err != nil {
		return fmt.Errorf("estimator: %w", err)
	}

	// ── 4. Worker ──────────────────────────────────────────────────────────
	// Recebe metric.MeterProvider — não sabe que o backend é Prometheus.
	// Iniciado antes do servidor para que nenhum trace seja perdido.
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()

	worker := middleware.NewWorker(
		middleware.WorkerConfig{
			BufferSize: cfg.WorkerBufferSize,
			Count:      cfg.WorkerCount,
		},
		est,
		repo,
		mp, // metric.MeterProvider — interface, não tipo concreto
	)
	worker.Start(workerCtx)

	// ── 5. Services / Handlers ─────────────────────────────────────────────
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

	// ── 6. Router ──────────────────────────────────────────────────────────
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

	// Rotas públicas
	r.Get("/healthz", handler.Health)
	r.Get("/badge", badge.Handler(badgeCache))

	// /metrics: servido pelo promhttp usando o registry do bridge OTEL.
	// O Prometheus scrape este endpoint e obtém tanto as métricas OTEL
	// (green.*) quanto as métricas de runtime Go (go_*, process_*).
	r.Handle("/metrics", promhttp.HandlerFor(promReg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))

	// Rotas protegidas
	r.Group(func(r chi.Router) {
		r.Use(middleware.JWTAuth(cfg.JWTSecret))
		r.Get("/me", handler.Me)
		r.Get("/eco/cache", ecoH.GetCacheMetrics)
	})

	if cfg.PProfEnabled {
		r.Mount("/debug", http.DefaultServeMux)
		slog.Warn("pprof habilitado — garanta que /debug não está exposto publicamente")
	}

	// ── 7. HTTP server + graceful shutdown ─────────────────────────────────
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
		slog.Info("servidor iniciando", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("ListenAndServe", "err", err)
			quit <- syscall.SIGTERM
		}
	}()

	<-quit
	slog.Info("shutdown iniciado")

	// Ordem crítica — ver docs/architecture.md#graceful-shutdown-order
	httpCtx, httpCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer httpCancel()
	if err := srv.Shutdown(httpCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	cancelWorker() // para de fazer I/O; workers ainda drenam o canal
	worker.Stop()  // fecha o canal e aguarda wg.Wait()

	slog.Info("servidor encerrado com sucesso")
	return nil
}
