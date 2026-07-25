// Package middleware provides HTTP middleware for Green Stack Monitor.
package middleware

import (
	"context"
	"net/http"
	"runtime"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/matheusjuliosantana/green-stack-monitor/pkg/estimator"
)

type ecoKey struct{}

// EcoTrace stores per-request instrumentation state.
type EcoTrace struct {
	startAlloc uint64
	startTime  time.Time
	CacheHit   bool
	sampled    bool
}

// MarkCacheHit marks the current sampled request as served from cache.
// It is a safe no-op when the request has no EcoTrace in context.
func MarkCacheHit(ctx context.Context) {
	if t, ok := ctx.Value(ecoKey{}).(*EcoTrace); ok {
		t.CacheHit = true
	}
}

// Options groups all EcoMetrics middleware dependencies.
type Options struct {
	Estimator *estimator.Estimator
	Worker    *Worker

	// MeterProvider records sampler drops. Request metrics are recorded by Worker.
	MeterProvider metric.MeterProvider

	// SampleRate controls how many requests are instrumented.
	// 1.0 = all requests. 0.1 = about 10%. 0.0 = none.
	SampleRate float64
}

// EcoMetrics returns HTTP middleware that samples requests and sends payloads
// to a background Worker without blocking the response path.
func EcoMetrics(opts Options) func(http.Handler) http.Handler {
	sampler := newSampler(opts.SampleRate)

	meter := opts.MeterProvider.Meter("github.com/matheusjuliosantana/green-stack-monitor")
	samplerDropped, err := meter.Int64ObservableCounter(
		"green.sampler.dropped",
		metric.WithDescription(
			"Requests skipped by the sampler. "+
				"dropped/(dropped+instrumented) converges to 1-SampleRate.",
		),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(sampler.Dropped()))
			return nil
		}),
	)
	if err != nil {
		panic("green-stack: failed to create OTEL instrument green.sampler.dropped: " + err.Error())
	}
	_ = samplerDropped

	workerCh := opts.Worker.Chan()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

			if !sampler.sample() {
				next.ServeHTTP(w, r)
				return
			}

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

			// Copy all fields before releasing pooled objects.
			payload := TracePayload{
				Method:     r.Method,
				Path:       sanitisePath(r.URL.Path),
				StatusCode: rw.status,
				StartAlloc: trace.startAlloc,
				Elapsed:    float64(time.Since(trace.startTime).Nanoseconds()) / 1e6,
				CacheHit:   trace.CacheHit,
				StartedAt:  trace.startTime,
			}

			releaseEcoTrace(trace)
			releaseResponseWriter(rw)

			select {
			case workerCh <- payload:
			default:
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
