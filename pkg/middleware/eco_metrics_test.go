package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"

	"github.com/matheusjuliosantana/green-stack-monitor/pkg/domain"
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/estimator"
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/middleware"
)

// --- fakeRepo ---------------------------------------------------------------

type fakeRepo struct {
	count atomic.Int64
	last  chan domain.RequestTrace
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{last: make(chan domain.RequestTrace, 128)}
}

func (f *fakeRepo) Save(_ context.Context, t domain.RequestTrace) error {
	f.count.Add(1)
	f.last <- t
	return nil
}

func (f *fakeRepo) Aggregates(_ context.Context) (domain.CacheMetrics, error) {
	// Drena o canal para contar hits e misses.
	// Usado apenas em TestRepository_HitRate — não afeta outros testes.
	var hits, misses uint64
	var saved float64
	for {
		select {
		case tr := <-f.last:
			if tr.CacheHit {
				hits++
				saved += tr.CO2Saved
			} else {
				misses++
			}
		default:
			return domain.CacheMetrics{Hits: hits, Misses: misses, TotalSaved: saved}, nil
		}
	}
}

func (f *fakeRepo) waitSaved(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if int(f.count.Load()) >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// --- helpers ----------------------------------------------------------------

func newEst(t testing.TB) *estimator.Estimator {
	t.Helper()
	est, err := estimator.New(domain.CO2Config{
		TDPWatts: 4.0, PUE: 1.2,
		CarbonIntensityGCO2PerKWh: 100.0,
		MemoryWattsPerGB:          0.375,
	})
	if err != nil {
		t.Fatal("estimator.New:", err)
	}
	return est
}

// noopMP retorna um MeterProvider que descarta tudo — ideal para testes
// que verificam comportamento, não métricas OTEL.
func noopMP() noop.MeterProvider {
	return noop.NewMeterProvider()
}

// buildMiddleware cria worker + middleware com noop MeterProvider.
// Retorna o handler e um cleanup que para o worker ao fim do teste.
func buildMiddleware(t testing.TB, repo domain.EcoRepository, sampleRate float64) (http.Handler, func()) {
	t.Helper()
	est := newEst(t)
	mp := noopMP()

	w := middleware.NewWorker(
		middleware.WorkerConfig{BufferSize: 128, Count: 2},
		est, repo, mp,
	)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	mw := middleware.EcoMetrics(middleware.Options{
		Estimator:     est,
		Worker:        w,
		MeterProvider: mp,
		SampleRate:    sampleRate,
	})

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	return mw(inner), func() { cancel(); w.Stop() }
}

// --- testes de comportamento ------------------------------------------------

func TestEcoMetrics_SampleRate1_RecordsTrace(t *testing.T) {
	repo := newFakeRepo()
	h, cleanup := buildMiddleware(t, repo, 1.0)
	defer cleanup()

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/test", nil))

	if !repo.waitSaved(1, 500*time.Millisecond) {
		t.Fatal("trace não foi salvo dentro do timeout")
	}
	trace := <-repo.last
	if trace.CO2Grams < 0 {
		t.Errorf("CO2Grams deve ser >= 0, got %v", trace.CO2Grams)
	}
}

func TestEcoMetrics_SampleRate0_NeverRecords(t *testing.T) {
	repo := newFakeRepo()
	h, cleanup := buildMiddleware(t, repo, 0.0)
	defer cleanup()

	for range 20 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/noop", nil))
	}
	time.Sleep(100 * time.Millisecond)

	if n := repo.count.Load(); n != 0 {
		t.Errorf("SampleRate=0.0: esperava 0 traces, got %d", n)
	}
}

func TestEcoMetrics_SampleRate_Partial(t *testing.T) {
	if testing.Short() {
		t.Skip("teste estatístico pulado com -short")
	}

	repo := newFakeRepo()
	h, cleanup := buildMiddleware(t, repo, 0.5)
	defer cleanup()

	const requests = 2000
	for range requests {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/s", nil))
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if repo.count.Load() > requests*(0.5-0.08) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	got := int(repo.count.Load())
	min, max := int(requests*(0.5-0.08)), int(requests*(0.5+0.08))
	if got < min || got > max {
		t.Errorf("SampleRate=0.5: esperava [%d, %d] traces, got %d", min, max, got)
	}
}

func TestEcoMetrics_MarkCacheHit(t *testing.T) {
	repo := newFakeRepo()
	est := newEst(t)
	mp := noopMP()

	w := middleware.NewWorker(middleware.WorkerConfig{BufferSize: 16, Count: 1}, est, repo, mp)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	h := middleware.EcoMetrics(middleware.Options{
		Estimator: est, Worker: w, MeterProvider: mp, SampleRate: 1.0,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		middleware.MarkCacheHit(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/cached", nil))

	if !repo.waitSaved(1, 500*time.Millisecond) {
		t.Fatal("trace não salvo dentro do timeout")
	}
	trace := <-repo.last
	if !trace.CacheHit {
		t.Error("esperava CacheHit = true")
	}
	if trace.CO2Saved <= 0 {
		t.Errorf("CO2Saved deve ser > 0 em cache hit, got %v", trace.CO2Saved)
	}
}

func TestEcoMetrics_MarkCacheHit_NotSampled_IsNoOp(t *testing.T) {
	repo := newFakeRepo()
	h, cleanup := buildMiddleware(t, repo, 0.0)
	defer cleanup()

	// Não deve panicar com SampleRate=0.0.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

// --- repositório ------------------------------------------------------------

func TestRepository_HitRate(t *testing.T) {
	// Usa fakeRepo (já definido neste arquivo) — pkg/ não depende de internal/.
	repo := newFakeRepo()

	for range 7 {
		_ = repo.Save(context.Background(), domain.RequestTrace{CacheHit: true, CO2Saved: 0.001})
	}
	for range 3 {
		_ = repo.Save(context.Background(), domain.RequestTrace{CacheHit: false})
	}

	m, err := repo.Aggregates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.Hits != 7 {
		t.Errorf("hits: got %d, want 7", m.Hits)
	}
	if m.Misses != 3 {
		t.Errorf("misses: got %d, want 3", m.Misses)
	}
	if got := m.HitRate(); absf(got-0.7) > 0.001 {
		t.Errorf("HitRate: got %.3f, want 0.700", got)
	}
}

// --- benchmarks -------------------------------------------------------------

func BenchmarkMiddleware_Rate1(b *testing.B) {
	repo := newFakeRepo()
	est := newEst(b)
	mp := noopMP()

	w := middleware.NewWorker(middleware.WorkerConfig{BufferSize: 4096, Count: 4}, est, repo, mp)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	h := middleware.EcoMetrics(middleware.Options{
		Estimator: est, Worker: w, MeterProvider: mp, SampleRate: 1.0,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/bench", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}

func BenchmarkMiddleware_Rate0(b *testing.B) {
	repo := newFakeRepo()
	est := newEst(b)
	mp := noopMP()

	w := middleware.NewWorker(middleware.WorkerConfig{BufferSize: 16, Count: 1}, est, repo, mp)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	h := middleware.EcoMetrics(middleware.Options{
		Estimator: est, Worker: w, MeterProvider: mp, SampleRate: 0.0,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/bench", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}

func absf(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
