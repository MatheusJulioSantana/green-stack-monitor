package middleware

import (
	"context"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/matheusjuliosantana/green-stack-monitor/pkg/domain"
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/estimator"
)

// TracePayload travels through the channel between middleware and worker.
// It is a value struct, so payloads are copied through the channel without
// pointer churn or extra GC pressure.
type TracePayload struct {
	Method     string
	Path       string
	StatusCode int
	StartAlloc uint64
	Elapsed    float64 // milliseconds
	CacheHit   bool
	StartedAt  time.Time
}

// WorkerConfig controls pool sizing.
type WorkerConfig struct {
	// BufferSize is the channel capacity between middleware and workers.
	// Rule: BufferSize >= peak_rps * avg_processing_ms / 1000
	// Default: 512.
	BufferSize int

	// Count is the number of worker goroutines. Default: 4.
	// Going beyond GOMAXPROCS rarely helps; the bottleneck is usually I/O.
	Count int
}

func (c WorkerConfig) withDefaults() WorkerConfig {
	if c.BufferSize <= 0 {
		c.BufferSize = 512
	}
	if c.Count <= 0 {
		c.Count = 4
	}
	return c
}

// Worker processes TracePayloads in the background without blocking requests.
//
// Lifecycle:
//
//	w := NewWorker(cfg, est, repo, mp)
//	w.Start(ctx)   // before accepting traffic
//	// server running...
//	w.Stop()       // after srv.Shutdown; drains the channel before exit
type Worker struct {
	ch   chan TracePayload
	cfg  WorkerConfig
	est  *estimator.Estimator
	repo domain.TraceWriter
	wg   sync.WaitGroup

	// drops counts payloads dropped because the channel was full.
	// It is incremented by the middleware select{default:} and read by the OTEL
	// ObservableCounter callback.
	drops atomic.Uint64

	// OTEL instruments are created in NewWorker and used in observe.
	co2Total    metric.Float64Counter
	co2Saved    metric.Float64Counter
	latencyHist metric.Float64Histogram
	allocHist   metric.Int64Histogram
	goroutines  metric.Int64ObservableGauge
	queueLen    metric.Int64ObservableGauge
	dropTotal   metric.Int64ObservableCounter
}

// NewWorker creates a Worker and registers OTEL instruments on the MeterProvider.
//
// mp is the only observability dependency. The backend (Prometheus, OTLP,
// stdout) is configured by the caller; the worker does not need to know which
// backend is active.
func NewWorker(
	cfg WorkerConfig,
	est *estimator.Estimator,
	repo domain.TraceWriter,
	mp metric.MeterProvider,
) *Worker {
	cfg = cfg.withDefaults()
	ch := make(chan TracePayload, cfg.BufferSize)

	w := &Worker{
		ch:   ch,
		cfg:  cfg,
		est:  est,
		repo: repo,
	}

	// Instrumentation scope identifies the library in the backend.
	// OTEL convention: Go module name without version.
	meter := mp.Meter("github.com/matheusjuliosantana/green-stack-monitor")

	// Counters only grow: CO2 emitted and avoided.
	var err error
	w.co2Total, err = meter.Float64Counter(
		"green.co2.grams",
		metric.WithDescription("Accumulated grams of CO2 emitted by HTTP requests."),
		metric.WithUnit("g"),
	)
	must(err, "green.co2.grams")

	w.co2Saved, err = meter.Float64Counter(
		"green.co2.saved_grams",
		metric.WithDescription("Grams of CO2 avoided through cache hits."),
		metric.WithUnit("g"),
	)
	must(err, "green.co2.saved_grams")

	// Histograms: latency and allocation distribution per request.
	w.latencyHist, err = meter.Float64Histogram(
		"green.request.duration",
		metric.WithDescription("HTTP request duration."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(
			0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
		),
	)
	must(err, "green.request.duration")

	w.allocHist, err = meter.Int64Histogram(
		"green.request.alloc_bytes",
		metric.WithDescription("Heap bytes allocated per request."),
		metric.WithUnit("By"),
		metric.WithExplicitBucketBoundaries(
			1024, 4096, 16384, 65536, 262144, 1048576, 4194304,
		),
	)
	must(err, "green.request.alloc_bytes")

	// Observable gauges are read by callback at scrape/export time.
	// They only read process or atomic state.
	w.goroutines, err = meter.Int64ObservableGauge(
		"green.runtime.goroutines",
		metric.WithDescription("Active goroutines at observation time."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(runtime.NumGoroutine()))
			return nil
		}),
	)
	must(err, "green.runtime.goroutines")

	w.queueLen, err = meter.Int64ObservableGauge(
		"green.worker.queue_len",
		metric.WithDescription(
			"Traces waiting in the channel. "+
				"If this stays near ECO_WORKER_BUFFER, increase Count or BufferSize.",
		),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(len(ch)))
			return nil
		}),
	)
	must(err, "green.worker.queue_len")

	// Observable counter for drops. A normal Counter would be recorded outside
	// the worker, while this value is observed from atomic state.
	w.dropTotal, err = meter.Int64ObservableCounter(
		"green.worker.backpressure_drops",
		metric.WithDescription(
			"Traces dropped because the channel was full. "+
				"Value > 0 indicates BufferSize or Count is undersized.",
		),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(w.drops.Load()))
			return nil
		}),
	)
	must(err, "green.worker.backpressure_drops")

	return w
}

// Chan returns the write-only channel used by middleware.
func (w *Worker) Chan() chan<- TracePayload {
	return w.ch
}

// Drops returns the accumulated backpressure drop count.
func (w *Worker) Drops() uint64 {
	return w.drops.Load()
}

// SimulateDrop increments the drop counter for tests.
func (w *Worker) SimulateDrop() {
	w.drops.Add(1)
}

// Start launches Count worker goroutines.
// It must be called before the server starts accepting traffic.
func (w *Worker) Start(ctx context.Context) {
	for range w.cfg.Count {
		w.wg.Add(1)
		go w.loop(ctx)
	}
	slog.Info("eco worker started",
		"workers", w.cfg.Count,
		"buffer", w.cfg.BufferSize,
	)
}

// Stop closes the channel and waits for all goroutines to finish.
// It must be called after srv.Shutdown, never before.
func (w *Worker) Stop() {
	close(w.ch)
	w.wg.Wait()
	slog.Info("eco worker stopped", "drops", w.drops.Load())
}

func (w *Worker) loop(ctx context.Context) {
	defer w.wg.Done()
	for payload := range w.ch {
		if ctx.Err() != nil {
			w.observe(ctx, payload, true /* skipSave */)
			continue
		}
		w.observe(ctx, payload, false)
	}
}

// observe calculates CO2 and records OTEL instruments.
// ctx is explicit because OTEL instruments need context to propagate trace IDs
// when tracing is also enabled.
func (w *Worker) observe(ctx context.Context, p TracePayload, skipSave bool) {
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	allocDelta := int64(0)
	if memAfter.TotalAlloc > p.StartAlloc {
		allocDelta = int64(memAfter.TotalAlloc - p.StartAlloc)
	}

	co2 := w.est.EstimateCPU(p.Elapsed) +
		w.est.EstimateMemory(uint64(allocDelta), p.Elapsed)

	saved := 0.0
	if p.CacheHit {
		saved = w.est.CacheSaving(co2)
	}

	// OTEL attributes become Prometheus labels through the exporter.
	attrs := metric.WithAttributes(
		attribute.String("http.method", p.Method),
		attribute.String("http.route", p.Path),
		attribute.String("http.status_class", httpStatusLabel(p.StatusCode)),
	)

	w.co2Total.Add(ctx, co2, attrs)
	if p.CacheHit {
		w.co2Saved.Add(ctx, saved, metric.WithAttributes(
			attribute.String("http.method", p.Method),
			attribute.String("http.route", p.Path),
		))
	}
	w.latencyHist.Record(ctx, p.Elapsed/1000, attrs)
	w.allocHist.Record(ctx, allocDelta, metric.WithAttributes(
		attribute.String("http.method", p.Method),
		attribute.String("http.route", p.Path),
	))

	if skipSave {
		return
	}

	if err := w.repo.Save(ctx, domain.RequestTrace{
		Method:          p.Method,
		Path:            p.Path,
		StatusCode:      p.StatusCode,
		StartedAt:       p.StartedAt,
		AllocDelta:      uint64(allocDelta),
		CacheHit:        p.CacheHit,
		CPUMilliseconds: p.Elapsed,
		CO2Grams:        co2,
		CO2Saved:        saved,
	}); err != nil {
		slog.Warn("eco worker: repo.Save failed", "err", err)
	}
}

// must fails fast when OTEL instrument creation fails.
func must(err error, name string) {
	if err != nil {
		panic("green-stack: failed to create OTEL instrument " + name + ": " + err.Error())
	}
}
