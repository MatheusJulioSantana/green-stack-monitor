package middleware

import (
	"math/rand/v2"
	"sync/atomic"
)

// Sampler decides whether a request should be instrumented.
//
// Design:
//   - rate == 1.0  → instrument everything, no rand overhead
//   - rate == 0.0  → instrument nothing, useful for disabling without redeploy
//   - 0 < rate < 1 → proportional sampling
//
// math/rand/v2 (Go 1.22+) uses an internal per-goroutine source, avoiding locks
// or shared state. Float64 is ~2 ns/op and zero allocations.
//
// dropped is an atomic counter exposed so Prometheus can tell how many requests
// were intentionally skipped. Without it, a low SampleRate can look like
// requests are disappearing from metrics.
type Sampler struct {
	rate    float64
	dropped atomic.Uint64
}

// newSampler creates a Sampler. rate should be in [0.0, 1.0].
// Config validation already happened, but this still clamps defensively.
func newSampler(rate float64) *Sampler {
	if rate < 0 {
		rate = 0
	}
	if rate > 1 {
		rate = 1
	}
	return &Sampler{rate: rate}
}

// sample returns true when this request should be instrumented.
// It is safe to call from multiple goroutines.
func (s *Sampler) sample() bool {
	// Fast path: avoid rand entirely for the two extremes.
	if s.rate >= 1.0 {
		return true
	}
	if s.rate <= 0.0 {
		s.dropped.Add(1)
		return false
	}

	if rand.Float64() < s.rate {
		return true
	}

	s.dropped.Add(1)
	return false
}

// Dropped returns the accumulated number of non-instrumented requests.
func (s *Sampler) Dropped() uint64 {
	return s.dropped.Load()
}
