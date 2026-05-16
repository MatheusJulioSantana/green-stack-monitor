package middleware

// Testes do pool ficam no package interno para acessar acquireEcoTrace,
// releaseEcoTrace, acquireResponseWriter e releaseResponseWriter diretamente.

import (
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestEcoTracePool_ResetOnRelease verifica que nenhum campo de uma request
// anterior vaza para a próxima após release.
//
// Este é o teste mais importante do pool: um campo não-resetado causaria
// um bug silencioso e difícil de reproduzir em produção.
func TestEcoTracePool_ResetOnRelease(t *testing.T) {
	// Adquire, popula com dados "sujos", libera.
	t1 := acquireEcoTrace()
	t1.startAlloc = 99999
	t1.startTime = time.Now().Add(-time.Hour)
	t1.CacheHit = true
	t1.sampled = true
	releaseEcoTrace(t1)

	// Adquire novamente — pode ser o mesmo objeto (pool reutilizou).
	// Todos os campos devem estar em zero value.
	t2 := acquireEcoTrace()
	defer releaseEcoTrace(t2)

	if t2.startAlloc != 0 {
		t.Errorf("startAlloc vazou: got %d, want 0", t2.startAlloc)
	}
	if !t2.startTime.IsZero() {
		t.Errorf("startTime vazou: got %v, want zero", t2.startTime)
	}
	if t2.CacheHit {
		t.Error("CacheHit vazou: got true, want false")
	}
	if t2.sampled {
		t.Error("sampled vazou: got true, want false")
	}
}

// TestResponseWriterPool_ResetOnRelease verifica que status e wroteHeader
// são resetados corretamente — evita que um 500 de uma request apareça
// como status inicial da próxima.
func TestResponseWriterPool_ResetOnRelease(t *testing.T) {
	rec := httptest.NewRecorder()

	rw := acquireResponseWriter()
	rw.ResponseWriter = rec
	rw.status = 500
	rw.wroteHeader = true
	releaseResponseWriter(rw)

	rw2 := acquireResponseWriter()
	defer releaseResponseWriter(rw2)

	if rw2.ResponseWriter != nil {
		t.Error("ResponseWriter não foi limpo no release")
	}
	if rw2.status != 0 {
		t.Errorf("status vazou: got %d, want 0", rw2.status)
	}
	if rw2.wroteHeader {
		t.Error("wroteHeader vazou: got true, want false")
	}
}

// TestEcoTracePool_ConcurrentSafe verifica que múltiplas goroutines
// podem usar o pool simultaneamente sem race condition.
// Execute com: go test -race ./pkg/middleware/
func TestEcoTracePool_ConcurrentSafe(t *testing.T) {
	const goroutines = 50
	const iterations = 200

	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				tr := acquireEcoTrace()
				tr.startAlloc = 42
				tr.CacheHit = true
				tr.sampled = true
				tr.startTime = time.Now()
				releaseEcoTrace(tr)
			}
		}()
	}
	wg.Wait()
}

// TestResponseWriterPool_ConcurrentSafe mesmo para responseWriter.
func TestResponseWriterPool_ConcurrentSafe(t *testing.T) {
	const goroutines = 50
	const iterations = 200

	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				rec := httptest.NewRecorder()
				rw := acquireResponseWriter()
				rw.ResponseWriter = rec
				rw.status = 200
				rw.wroteHeader = false
				releaseResponseWriter(rw)
			}
		}()
	}
	wg.Wait()
}

// BenchmarkEcoTracePool mede o custo de acquire+populate+release.
// Compare com BenchmarkEcoTraceAlloc para ver o ganho real.
func BenchmarkEcoTracePool_AcquireRelease(b *testing.B) {
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		tr := acquireEcoTrace()
		tr.startAlloc = 1024
		tr.startTime = now
		tr.sampled = true
		releaseEcoTrace(tr)
	}
}

// BenchmarkEcoTraceAlloc mede o custo de alocar *EcoTrace diretamente.
// Deve ser significativamente mais lento que BenchmarkEcoTracePool.
func BenchmarkEcoTraceAlloc(b *testing.B) {
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		tr := &EcoTrace{
			startAlloc: 1024,
			startTime:  now,
			sampled:    true,
		}
		_ = tr
	}
}
