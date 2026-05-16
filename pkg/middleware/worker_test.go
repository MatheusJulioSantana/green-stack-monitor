package middleware_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"

	"github.com/matheusjuliosantana/green-stack-monitor/pkg/domain"
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/estimator"
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/middleware"
)

// --- workerRepo -------------------------------------------------------------

type workerRepo struct {
	count atomic.Int64
	saved chan domain.RequestTrace
}

func newWorkerRepo(cap int) *workerRepo {
	return &workerRepo{saved: make(chan domain.RequestTrace, cap)}
}

func (r *workerRepo) Save(_ context.Context, t domain.RequestTrace) error {
	r.count.Add(1)
	r.saved <- t
	return nil
}

func (r *workerRepo) Aggregates(_ context.Context) (domain.CacheMetrics, error) {
	return domain.CacheMetrics{}, nil
}

func (r *workerRepo) waitCount(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if int(r.count.Load()) >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// --- helper -----------------------------------------------------------------

func newTestWorker(t testing.TB, repo domain.TraceWriter, cfg middleware.WorkerConfig) *middleware.Worker {
	t.Helper()
	est, err := estimator.New(domain.CO2Config{
		TDPWatts: 4.0, PUE: 1.2,
		CarbonIntensityGCO2PerKWh: 100.0,
		MemoryWattsPerGB:          0.375,
	})
	if err != nil {
		t.Fatal(err)
	}
	return middleware.NewWorker(cfg, est, repo, noop.NewMeterProvider())
}

// --- testes -----------------------------------------------------------------

func TestWorker_ProcessesAllPayloads(t *testing.T) {
	const n = 50
	repo := newWorkerRepo(n)
	w := newTestWorker(t, repo, middleware.WorkerConfig{BufferSize: 128, Count: 2})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	for i := range n {
		w.Chan() <- middleware.TracePayload{
			Method: "GET", Path: "/test", StatusCode: 200,
			Elapsed: float64(i + 1), StartedAt: time.Now(),
		}
	}

	w.Stop()

	if int(repo.count.Load()) != n {
		t.Errorf("esperava %d traces, got %d", n, repo.count.Load())
	}
}

func TestWorker_StopDrainsChannel(t *testing.T) {
	const n = 20
	repo := newWorkerRepo(n)
	w := newTestWorker(t, repo, middleware.WorkerConfig{BufferSize: 64, Count: 1})

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	for range n {
		w.Chan() <- middleware.TracePayload{
			Method: "POST", Path: "/drain", StatusCode: 201,
			Elapsed: 5.0, StartedAt: time.Now(),
		}
	}

	// Cancela antes do canal esvaziar — workers devem drenar sem panicar.
	cancel()
	w.Stop() // não deve bloquear nem vazar goroutines
}

func TestWorker_Backpressure_DropsWhenFull(t *testing.T) {
	const bufferSize = 3
	repo := newWorkerRepo(100)

	est, _ := estimator.New(domain.CO2Config{
		TDPWatts: 4.0, PUE: 1.2,
		CarbonIntensityGCO2PerKWh: 100.0,
		MemoryWattsPerGB:          0.375,
	})
	w := middleware.NewWorker(
		middleware.WorkerConfig{BufferSize: bufferSize, Count: 1},
		est, repo, noop.NewMeterProvider(),
	)

	ch := w.Chan()
	dropped := 0

	for range bufferSize + 10 {
		select {
		case ch <- middleware.TracePayload{
			Method: "GET", Path: "/", StatusCode: 200, StartedAt: time.Now(),
		}:
		default:
			dropped++
			w.SimulateDrop()
		}
	}

	if dropped == 0 {
		t.Error("esperava drops com canal cheio, got 0")
	}
	if w.Drops() != uint64(dropped) {
		t.Errorf("Drops(): got %d, want %d", w.Drops(), dropped)
	}

	// Drena sem fazer I/O (contexto já cancelado).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Start(ctx)
	w.Stop()
}

func TestWorker_CacheHit_RecordsSaving(t *testing.T) {
	repo := newWorkerRepo(10)
	w := newTestWorker(t, repo, middleware.WorkerConfig{BufferSize: 16, Count: 1})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	w.Chan() <- middleware.TracePayload{
		Method: "GET", Path: "/cached", StatusCode: 200,
		Elapsed: 10.0, CacheHit: true, StartedAt: time.Now(),
	}

	if !repo.waitCount(1, 500*time.Millisecond) {
		t.Fatal("worker não processou o payload")
	}
	w.Stop()

	trace := <-repo.saved
	if !trace.CacheHit {
		t.Error("esperava CacheHit = true")
	}
	if trace.CO2Saved <= 0 {
		t.Errorf("CO2Saved deve ser > 0 em cache hit, got %v", trace.CO2Saved)
	}
}

// --- benchmarks -------------------------------------------------------------

func BenchmarkWorker_Throughput(b *testing.B) {
	repo := newWorkerRepo(b.N + 1)
	est, _ := estimator.New(domain.CO2Config{
		TDPWatts: 4.0, PUE: 1.2,
		CarbonIntensityGCO2PerKWh: 100.0,
		MemoryWattsPerGB:          0.375,
	})
	w := middleware.NewWorker(
		middleware.WorkerConfig{BufferSize: 4096, Count: 4},
		est, repo, noop.NewMeterProvider(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	payload := middleware.TracePayload{
		Method: "GET", Path: "/bench", StatusCode: 200,
		Elapsed: 1.0, StartedAt: time.Now(),
	}

	b.ReportAllocs()
	b.ResetTimer()
	ch := w.Chan()
	for b.Loop() {
		ch <- payload
	}
	w.Stop()
}
