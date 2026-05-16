// Package service contains the application's business logic.
//
// Services depend only on interfaces (ports), never on concrete types.
// This makes them trivially testable with mocks and replaceable without
// touching the HTTP layer.
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/yourhandle/green-stack-monitor/internal/cache"
	"github.com/yourhandle/green-stack-monitor/internal/domain"
)

const defaultTTL = 5 * time.Minute

// EcoService aggregates eco metrics and exposes them to handlers.
type EcoService struct {
	repo  domain.TraceReader
	cache cache.Cache
}

// NewEcoService creates an EcoService with explicit dependencies.
func NewEcoService(repo domain.TraceReader, c cache.Cache) *EcoService {
	return &EcoService{repo: repo, cache: c}
}

// CacheMetrics returns cache aggregates, using the cache itself to avoid
// hammering the repository on every call to the metrics endpoint.
//
// Note: this intentionally does NOT call middleware.MarkCacheHit — meta-caching
// of metrics is exempt from CO₂ accounting to avoid recursion.
func (s *EcoService) CacheMetrics(ctx context.Context) (domain.CacheMetrics, error) {
	const key = "eco:cache_metrics"

	var m domain.CacheMetrics
	if err := s.cache.Get(ctx, key, &m); err == nil {
		return m, nil
	}

	m, err := s.repo.Aggregates(ctx)
	if err != nil {
		return domain.CacheMetrics{}, fmt.Errorf("eco service: aggregates: %w", err)
	}

	// Best-effort cache write — if it fails, we simply re-read next time.
	_ = s.cache.Set(ctx, key, m, defaultTTL)
	return m, nil
}
