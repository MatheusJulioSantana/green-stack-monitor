package middleware

// Pool tests live in the internal package to access acquireEcoTrace,
// releaseEcoTrace, acquireResponseWriter and releaseResponseWriter directly.

import (
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestEcoTracePool_ResetOnRelease verifies that no field from a previous request
// leaks into the next one after release.
//
// This is the most important pool test: a non-reset field would cause a silent
// and hard to reproduce production bug.
func TestEcoTracePool_ResetOnRelease(t *testing.T) {
	// Acquire, populate with dirty data, release.
	t1 := acquireEcoTrace()
	t1.startAlloc = 99999
	t1.startTime = time.Now().Add(-time.Hour)
	t1.CacheHit = true
	t1.sampled = true
	releaseEcoTrace(t1)

	// Acquire again; this can be the same object reused by the pool.
	// Every field must be at its zero value.
	t2 := acquireEcoTrace()
	defer releaseEcoTrace(t2)

	if t2.startAlloc != 0 {
		t.Errorf("startAlloc leaked: got %d, want 0", t2.startAlloc)
	}
	if !t2.startTime.IsZero() {
		t.Errorf("startTime leaked: got %v, want zero", t2.startTime)
	}
	if t2.CacheHit {
		t.Error("CacheHit leaked: got true, want false")
	}
	if t2.sampled {
		t.Error("sampled leaked: got true, want false")
	}
}

// TestResponseWriterPool_ResetOnRelease verifies that status and wroteHeader are
// reset correctly, so one request status cannot become the next initial status.
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
		t.Error("ResponseWriter was not cleared on release")
	}
	if rw2.status != 0 {
		t.Errorf("status leaked: got %d, want 0", rw2.status)
	}
	if rw2.wroteHeader {
		t.Error("wroteHeader leaked: got true, want false")
	}
}

// TestEcoTracePool_ConcurrentSafe verifies that multiple goroutines can use the
// pool concurrently without races. Run with: go test -race ./pkg/middleware/
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

// TestResponseWriterPool_ConcurrentSafe does the same for responseWriter.
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

// BenchmarkEcoTracePool measures acquire+populate+release cost.
// Compare with BenchmarkEcoTraceAlloc to see the real gain.
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

// BenchmarkEcoTraceAlloc measures direct *EcoTrace allocation cost.
// It should be significantly slower than BenchmarkEcoTracePool.
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
