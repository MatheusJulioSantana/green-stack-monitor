// Package middleware provides HTTP middleware for Green Stack Monitor.
//
// Fluxo de dados:
//
//	HTTP request
//	    │
//	    ├── sampler.sample() → false → handler direto, ~2 ns, zero allocs
//	    │
//	    └── true → ReadMemStats (pré) → handler → captura elapsed/cacheHit
//	                                                    │
//	                                          select { ch <- payload }
//	                                                    │
//	                                              default: drop + contador atômico
//	                                                    │
//	                                           Worker.loop() [goroutine pool]
//	                                                    │
//	                                       ReadMemStats (pós) → CO₂ → OTEL → repo
package middleware

import (
	"context"
	"net/http"
	"runtime"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/yourhandle/green-stack-monitor/internal/estimator"
)

// ecoKey é a chave de contexto privada deste pacote.
type ecoKey struct{}

// EcoTrace armazena o estado de instrumentação durante a request.
type EcoTrace struct {
	startAlloc uint64
	startTime  time.Time
	CacheHit   bool
	sampled    bool
}

// MarkCacheHit sinaliza um cache hit para o middleware.
// No-op seguro em requests não amostradas ou sem trace no contexto.
func MarkCacheHit(ctx context.Context) {
	if t, ok := ctx.Value(ecoKey{}).(*EcoTrace); ok {
		t.CacheHit = true
	}
}

// Options agrupa toda a configuração do EcoMetrics middleware.
//
// MeterProvider é a única dependência de observabilidade.
// O backend (Prometheus, OTLP, stdout) é escolhido pelo chamador em main.go.
// O middleware não sabe — e não precisa saber — qual backend está ativo.
type Options struct {
	Estimator *estimator.Estimator
	Worker    *Worker

	// MeterProvider é usado para registrar o contador de sampler drops.
	// As métricas de CO₂/latência são registradas pelo próprio Worker.
	MeterProvider metric.MeterProvider

	// SampleRate controla a fração de requests instrumentadas.
	// 1.0 = todas (padrão). 0.1 = ~10%. 0.0 = nenhuma.
	SampleRate float64
}

// EcoMetrics retorna um middleware HTTP que:
//  1. Decide se a request será amostrada (~2 ns, zero allocs se descartada).
//  2. Captura snapshot de memória pré-request.
//  3. Injeta *EcoTrace no contexto para anotação pelos handlers.
//  4. Após a response, envia um TracePayload ao Worker via canal (não-bloqueante).
func EcoMetrics(opts Options) func(http.Handler) http.Handler {
	sampler := newSampler(opts.SampleRate)

	// samplerDropped: contador OTEL para requests ignoradas pelo sampler.
	// Fica no middleware (não no worker) porque é responsabilidade do sampler.
	// Sem este contador, SampleRate=0.1 pareceria "90% das requests sumindo".
	meter := opts.MeterProvider.Meter("github.com/yourhandle/green-stack-monitor")
	samplerDropped, err := meter.Int64ObservableCounter(
		"green.sampler.dropped",
		metric.WithDescription(
			"Requests não instrumentadas pelo sampler (comportamento esperado). "+
				"dropped/(dropped+instrumentadas) converge para 1-SampleRate.",
		),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(sampler.Dropped()))
			return nil
		}),
	)
	if err != nil {
		panic("green-stack: failed to create OTEL instrument green.sampler.dropped: " + err.Error())
	}
	// Referência mantida para evitar que o GC colete o instrumento.
	_ = samplerDropped

	workerCh := opts.Worker.Chan()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

			// ── Fast path: request não amostrada ──────────────────────────
			if !sampler.sample() {
				// Mesmo requests não amostradas recebem um *EcoTrace no contexto
				// para que MarkCacheHit() seja um no-op seguro (sem checar nil).
				// Usamos o pool para evitar a alocação — o trace é liberado
				// imediatamente após o handler retornar.
				t := acquireEcoTrace()
				t.sampled = false
				ctx := context.WithValue(r.Context(), ecoKey{}, t)
				next.ServeHTTP(w, r.WithContext(ctx))
				releaseEcoTrace(t)
				return
			}

			// ── Request amostrada ─────────────────────────────────────────
			var memBefore runtime.MemStats
			runtime.ReadMemStats(&memBefore)

			trace := acquireEcoTrace()
			trace.startAlloc = memBefore.TotalAlloc
			trace.startTime = time.Now()
			trace.sampled = true

			ctx := context.WithValue(r.Context(), ecoKey{}, trace)
			r = r.WithContext(ctx)

			rw := acquireResponseWriter()
			rw.ResponseWriter = w
			rw.status = http.StatusOK
			next.ServeHTTP(rw, r)

			// ── Pós-response: envia ao worker via select não-bloqueante ───
			// Variáveis capturadas no stack desta goroutine antes do select
			// para evitar race: `r` pode ser reciclado pelo pool do net/http.
			// Captura todos os campos necessários ANTES de liberar os pools.
			// A ordem importa: releaseEcoTrace e releaseResponseWriter
			// resetam os campos — ler após release seria um bug de race.
			payload := TracePayload{
				Method:     r.Method,
				Path:       sanitisePath(r.URL.Path),
				StatusCode: rw.status,
				StartAlloc: trace.startAlloc,
				Elapsed:    float64(time.Since(trace.startTime).Milliseconds()),
				CacheHit:   trace.CacheHit,
				StartedAt:  trace.startTime,
			}

			// Devolve ao pool agora que todos os dados foram copiados para payload.
			releaseEcoTrace(trace)
			releaseResponseWriter(rw)

			select {
			case workerCh <- payload:
			default:
				// Canal cheio — drop explícito, nunca bloqueia.
				// Visível via green.worker.backpressure_drops no backend OTEL.
				opts.Worker.drops.Add(1)
			}
		})
	}
}

func sanitisePath(p string) string {
	if len(p) > 64 {
		return p[:64]
	}
	return p
}

func httpStatusLabel(code int) string {
	switch {
	case code < 200:
		return "1xx"
	case code < 300:
		return "2xx"
	case code < 400:
		return "3xx"
	case code < 500:
		return "4xx"
	default:
		return "5xx"
	}
}

type responseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.wroteHeader {
		rw.status = code
		rw.wroteHeader = true
		rw.ResponseWriter.WriteHeader(code)
	}
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(b)
}
