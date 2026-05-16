// Package repository contains implementations of domain repository interfaces.
package repository

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/matheusjuliosantana/green-stack-monitor/pkg/domain"
)

// InMemoryEcoRepository is a thread-safe, zero-dependency implementation
// of domain.EcoRepository, suitable for development and testing.
//
// Replace with a Postgres-backed implementation for production without
// changing any middleware or service code (interface segregation).
type InMemoryEcoRepository struct {
	mu     sync.RWMutex
	traces []domain.RequestTrace

	// Atomic counters avoid lock contention on the hot path.
	hits   atomic.Uint64
	misses atomic.Uint64
	saved  atomic.Uint64 // stored as micro-grams (µg) to keep integer precision
}

// Save appends the trace in a goroutine-safe way.
// The mutex here is acceptable: this runs off the hot path (async goroutine).
func (r *InMemoryEcoRepository) Save(_ context.Context, trace domain.RequestTrace) error {
	if trace.CacheHit {
		r.hits.Add(1)
		// Store µg to preserve decimal precision with integers.
		r.saved.Add(uint64(trace.CO2Saved * 1_000_000))
	} else {
		r.misses.Add(1)
	}

	r.mu.Lock()
	r.traces = append(r.traces, trace)
	r.mu.Unlock()
	return nil
}

// Aggregates returns a read-consistent snapshot of cache metrics.
func (r *InMemoryEcoRepository) Aggregates(_ context.Context) (domain.CacheMetrics, error) {
	return domain.CacheMetrics{
		Hits:       r.hits.Load(),
		Misses:     r.misses.Load(),
		TotalSaved: float64(r.saved.Load()) / 1_000_000,
	}, nil
}

// Snapshot returns a copy of all stored traces for inspection.
// Intended for tests and the /debug/eco endpoint, not production hot paths.
func (r *InMemoryEcoRepository) Snapshot() []domain.RequestTrace {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]domain.RequestTrace, len(r.traces))
	copy(out, r.traces)
	return out
}
