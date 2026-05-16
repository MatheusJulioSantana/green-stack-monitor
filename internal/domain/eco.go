// Package domain holds the core types of Green Stack Monitor.
// Zero external dependencies — testable in isolation.
package domain

import (
	"context"
	"time"
)

// RequestTrace captures the full eco-cost of a single HTTP request.
// This is the central value object of the system.
type RequestTrace struct {
	TraceID    string
	Method     string
	Path       string
	StatusCode int

	// Timing
	StartedAt time.Time
	Duration  time.Duration

	// Memory delta (bytes allocated during this request)
	AllocDelta uint64

	// Cache outcome
	CacheHit bool

	// Derived eco metrics (filled by EcoEstimator)
	CPUMilliseconds float64
	CO2Grams        float64
	CO2Saved        float64 // estimated savings due to cache hit
}

// CacheMetrics aggregates cache efficiency over a time window.
type CacheMetrics struct {
	Hits        uint64
	Misses      uint64
	TotalSaved  float64 // grams of CO₂ avoided by cache
}

func (c CacheMetrics) HitRate() float64 {
	total := c.Hits + c.Misses
	if total == 0 {
		return 0
	}
	return float64(c.Hits) / float64(total)
}

// CO2Config holds the constants needed for the carbon estimation formula.
// All values should be sourced and documented per deployment region.
//
// Formula (based on Lannelongue et al. 2021 — Green Algorithms):
//
//	CO₂ = CPU_usage_ms × TDP_watts × PUE × CI_gCO2_per_kWh / 3_600_000
//
// where 3_600_000 converts W·ms → kWh.
type CO2Config struct {
	// TDPWatts is the Thermal Design Power of the CPU (watts).
	// Example: 150 W for a cloud vCPU share ≈ 4 W.
	TDPWatts float64

	// PUE is the Power Usage Effectiveness of the data center.
	// World average ≈ 1.58; hyperscalers ≈ 1.10.
	PUE float64

	// CarbonIntensityGCO2PerKWh is the grid carbon intensity in gCO₂/kWh.
	// Brazil grid ≈ 100, US East ≈ 400, EU average ≈ 250.
	CarbonIntensityGCO2PerKWh float64

	// MemoryWattsPerGB is power drawn per GB of RAM.
	// Typical DRAM: 0.375 W/GB.
	MemoryWattsPerGB float64
}

// TraceWriter is the write port — implemented by any storage backend.
//
// ctx must be forwarded to every I/O call so that timeouts, cancellations
// and trace propagation work correctly across all backends (SQL, NoSQL, etc).
// Implementations must be safe for concurrent use.
type TraceWriter interface {
	Save(ctx context.Context, trace RequestTrace) error
}

// TraceReader is the read port — aggregation queries differ significantly
// between backends, so it is kept separate from TraceWriter.
//
// Example: a DynamoDB backend may only implement TraceWriter (writes are
// cheap, full scans for Aggregates are not). A Postgres backend can
// implement both efficiently with a single GROUP BY query.
type TraceReader interface {
	Aggregates(ctx context.Context) (CacheMetrics, error)
}

// EcoRepository composes both ports. Use this type when you need a backend
// that supports both reading and writing — the common case for SQL databases.
//
// For append-only backends (queues, DynamoDB, ClickHouse write path),
// accept TraceWriter directly instead.
type EcoRepository interface {
	TraceWriter
	TraceReader
}
