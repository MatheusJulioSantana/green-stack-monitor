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

	"github.com/yourhandle/green-stack-monitor/internal/domain"
	"github.com/yourhandle/green-stack-monitor/internal/estimator"
)

// TracePayload é o que viaja pelo canal entre o middleware e o worker.
// Struct de valor (não ponteiro) — o canal aloca o slice interno uma vez,
// os payloads são copiados. Sem pressão no GC.
type TracePayload struct {
	Method     string
	Path       string
	StatusCode int
	StartAlloc uint64
	Elapsed    float64 // milliseconds
	CacheHit   bool
	StartedAt  time.Time
}

// WorkerConfig controla o dimensionamento do pool.
type WorkerConfig struct {
	// BufferSize é a capacidade do canal entre middleware e workers.
	// Regra: BufferSize >= peak_rps × avg_processing_ms / 1000
	// Padrão: 512.
	BufferSize int

	// Count é o número de goroutines worker. Padrão: 4.
	// Aumentar além de GOMAXPROCS raramente ajuda — gargalo é I/O.
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

// Worker processa TracePayloads em background sem bloquear o caminho da request.
//
// Lifecycle:
//
//	w := NewWorker(cfg, est, repo, mp)
//	w.Start(ctx)   // antes de aceitar tráfego
//	// servidor rodando...
//	w.Stop()       // após srv.Shutdown() — drena o canal antes de sair
type Worker struct {
	ch   chan TracePayload
	cfg  WorkerConfig
	est  *estimator.Estimator
	repo domain.TraceWriter
	wg   sync.WaitGroup

	// drops: payloads descartados por canal cheio.
	// Incrementado no select{default:} do middleware.
	// Lido pelo ObservableCounter OTEL via callback.
	drops atomic.Uint64

	// Instrumentos OTEL — criados em NewWorker, usados em observe().
	co2Total    metric.Float64Counter
	co2Saved    metric.Float64Counter
	latencyHist metric.Float64Histogram
	allocHist   metric.Int64Histogram
	goroutines  metric.Int64ObservableGauge
	queueLen    metric.Int64ObservableGauge
	dropTotal   metric.Int64ObservableCounter
}

// NewWorker cria um Worker e registra os instrumentos OTEL no MeterProvider.
//
// mp é a única dependência de observabilidade. O backend (Prometheus, OTLP,
// stdout) é configurado pelo chamador em main.go — o worker não sabe nem
// precisa saber qual backend está ativo.
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

	// Meter com nome de instrumentação que identifica a lib no backend.
	// Convenção OTEL: nome do módulo Go sem versão.
	meter := mp.Meter("github.com/yourhandle/green-stack-monitor")

	// Counters: valores que só crescem — CO₂ emitido e economizado.
	var err error
	w.co2Total, err = meter.Float64Counter(
		"green.co2.grams",
		metric.WithDescription("Gramas acumuladas de CO₂ emitidas por requests HTTP."),
		metric.WithUnit("g"),
	)
	must(err, "green.co2.grams")

	w.co2Saved, err = meter.Float64Counter(
		"green.co2.saved_grams",
		metric.WithDescription("Gramas de CO₂ evitadas via cache hits."),
		metric.WithUnit("g"),
	)
	must(err, "green.co2.saved_grams")

	// Histogramas: distribuição de latência e alocações por request.
	w.latencyHist, err = meter.Float64Histogram(
		"green.request.duration",
		metric.WithDescription("Duração das requests HTTP."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(
			0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
		),
	)
	must(err, "green.request.duration")

	w.allocHist, err = meter.Int64Histogram(
		"green.request.alloc_bytes",
		metric.WithDescription("Bytes de heap alocados por request."),
		metric.WithUnit("By"),
		metric.WithExplicitBucketBoundaries(
			1024, 4096, 16384, 65536, 262144, 1048576, 4194304,
		),
	)
	must(err, "green.request.alloc_bytes")

	// Observable gauges: lidos por callback no momento do scrape/export.
	// Zero alocações por coleta — apenas leitura de estado atômico.
	w.goroutines, err = meter.Int64ObservableGauge(
		"green.runtime.goroutines",
		metric.WithDescription("Goroutines ativas no momento de conclusão da request."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(runtime.NumGoroutine()))
			return nil
		}),
	)
	must(err, "green.runtime.goroutines")

	w.queueLen, err = meter.Int64ObservableGauge(
		"green.worker.queue_len",
		metric.WithDescription(
			"Traces aguardando no canal. "+
				"Se próximo de ECO_WORKER_BUFFER constantemente, aumente Count ou BufferSize.",
		),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(len(ch)))
			return nil
		}),
	)
	must(err, "green.worker.queue_len")

	// Observable counter para drops — não pode usar Counter normal porque
	// o incremento ocorre no middleware (fora do worker), não em observe().
	w.dropTotal, err = meter.Int64ObservableCounter(
		"green.worker.backpressure_drops",
		metric.WithDescription(
			"Traces descartados por canal cheio. "+
				"Valor > 0 indica subdimensionamento de BufferSize ou Count.",
		),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(w.drops.Load()))
			return nil
		}),
	)
	must(err, "green.worker.backpressure_drops")

	return w
}

// Chan retorna o canal somente-escrita para o middleware.
func (w *Worker) Chan() chan<- TracePayload {
	return w.ch
}

// Drops retorna o contador acumulado de drops por backpressure.
func (w *Worker) Drops() uint64 {
	return w.drops.Load()
}

// SimulateDrop incrementa o contador de drops.
// Usado exclusivamente em testes — em produção, o select{default:} do
// middleware é quem incrementa diretamente via w.drops.Add(1).
func (w *Worker) SimulateDrop() {
	w.drops.Add(1)
}

// Start lança Count goroutines worker.
// Deve ser chamado antes do servidor começar a aceitar tráfego.
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

// Stop fecha o canal e aguarda todas as goroutines terminarem.
// Deve ser chamado após srv.Shutdown() — nunca antes.
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

// observe calcula o CO₂ e registra os instrumentos OTEL.
// ctx é passado explicitamente — os instrumentos OTEL precisam de contexto
// para propagar trace IDs quando OTEL tracing também estiver ativo.
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

	// Atributos OTEL — equivalem aos labels do Prometheus.
	// Usamos attribute.String para consistência com a convenção OTEL semconv.
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

// must é um helper para erros de criação de instrumento OTEL.
// Falha em startup — melhor do que silenciosamente não registrar métricas.
func must(err error, name string) {
	if err != nil {
		panic("green-stack: failed to create OTEL instrument " + name + ": " + err.Error())
	}
}
