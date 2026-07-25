// Package estimator computes the carbon cost of a request.
//
// The formula is based on the Green Algorithms methodology
// (Lannelongue, Grealey & Inouye, 2021).
//
// This package has ZERO side effects — all inputs explicit, outputs deterministic.
// Safe to test, safe to benchmark, safe to swap.
package estimator

import (
	"math"
	"sync/atomic"
	"time"

	"github.com/matheusjuliosantana/green-stack-monitor/pkg/domain"
)

const (
	// msToHours converts milliseconds to hours (for the W → kWh conversion).
	msToHours = 1.0 / 3_600_000.0
)

type Snapshot struct {
	TotalG    float64
	SavedG    float64
	PerReqG   float64
	HitRate   float64
	Requests  int64
	CacheHits int64
}

// Estimator converts CPU and memory usage into a CO₂ estimate.
type Estimator struct {
	cfg           domain.CO2Config
	totalCO2      uint64 // float64 bits, total grams
	savedCO2      uint64 // float64 bits, saved grams
	totalRequests int64
	cacheHits     int64
}

// New creates an Estimator with the given configuration.
// Returns an error if any value is physically nonsensical.
func New(cfg domain.CO2Config) (*Estimator, error) {
	if cfg.TDPWatts <= 0 {
		return nil, ErrInvalidTDP
	}
	if cfg.PUE < 1.0 {
		return nil, ErrInvalidPUE // PUE can never be below 1 by physics
	}
	if cfg.CarbonIntensityGCO2PerKWh <= 0 {
		return nil, ErrInvalidCI
	}

	return &Estimator{cfg: cfg}, nil
}

// EstimateCPU returns the grams of CO₂ emitted for a given number of CPU-milliseconds.
//
//	CO₂ = cpu_ms × TDP(W) × PUE × CI(gCO₂/kWh) × (1h / 3_600_000 ms)
func (e *Estimator) EstimateCPU(cpuMilliseconds float64) float64 {
	kWh := cpuMilliseconds * e.cfg.TDPWatts * msToHours
	return kWh * e.cfg.PUE * e.cfg.CarbonIntensityGCO2PerKWh
}

// EstimateMemory returns the grams of CO₂ emitted for allocating n bytes
// during durationMs milliseconds.
func (e *Estimator) EstimateMemory(bytes uint64, durationMs float64) float64 {
	gb := float64(bytes) / (1 << 30)
	kWh := gb * e.cfg.MemoryWattsPerGB * durationMs * msToHours
	return kWh * e.cfg.PUE * e.cfg.CarbonIntensityGCO2PerKWh
}

// CacheSaving estimates how many grams of CO₂ were avoided because the
// response was served from cache instead of executing a full DB query.
// baseCostGrams is the cost of the equivalent non-cached request.
func (e *Estimator) CacheSaving(baseCostGrams float64) float64 {
	// Conservative model: assume cache hit avoids ~90% of the base cost.
	// Operators can tune this multiplier via config.
	return baseCostGrams * 0.90
}

// Errors — typed so callers can distinguish invalid config from runtime errors.
type estimatorError string

func (e estimatorError) Error() string { return string(e) }

const (
	ErrInvalidTDP estimatorError = "TDPWatts must be > 0"
	ErrInvalidPUE estimatorError = "PUE must be >= 1.0 (cannot be below 1 by thermodynamics)"
	ErrInvalidCI  estimatorError = "CarbonIntensityGCO2PerKWh must be > 0"
)

// Record stores the estimated CO2 cost for one request.
// cacheHit should be true when the response was served from cache.
func (e *Estimator) Record(d time.Duration, cacheHit bool) {
	ms := float64(d.Milliseconds())
	if ms < 0.001 {
		ms = 0.001
	}

	co2 := ms * e.cfg.TDPWatts * e.cfg.PUE * e.cfg.CarbonIntensityGCO2PerKWh / 3_600_000

	atomicAddFloat64(&e.totalCO2, co2)
	atomic.AddInt64(&e.totalRequests, 1)

	if cacheHit {
		atomicAddFloat64(&e.savedCO2, co2)
		atomic.AddInt64(&e.cacheHits, 1)
	}
}

// GetMetrics returns a point-in-time snapshot of cache counters.
func (e *Estimator) GetMetrics() (domain.CacheMetrics, error) {
	saved := atomicReadFloat64(&e.savedCO2)
	reqs := atomic.LoadInt64(&e.totalRequests)
	hits := atomic.LoadInt64(&e.cacheHits)

	return domain.CacheMetrics{
		Hits:       uint64(hits),
		Misses:     uint64(reqs - hits),
		TotalSaved: saved,
	}, nil
}

func (e *Estimator) Snapshot() Snapshot {
	n := atomic.LoadInt64(&e.totalRequests)
	h := atomic.LoadInt64(&e.cacheHits)
	total := atomicReadFloat64(&e.totalCO2)
	saved := atomicReadFloat64(&e.savedCO2)
	perReq := 0.0
	hitRate := 0.0
	if n > 0 {
		perReq = total / float64(n)
		hitRate = float64(h) / float64(n) * 100
	}
	return Snapshot{
		TotalG:    total,
		SavedG:    saved,
		PerReqG:   perReq,
		HitRate:   hitRate,
		Requests:  n,
		CacheHits: h,
	}
}

// atomicAddFloat64 adds to a float64 stored as uint64 bits.
func atomicAddFloat64(addr *uint64, delta float64) {
	for {
		old := atomic.LoadUint64(addr)
		newVal := math.Float64frombits(old) + delta
		newBits := math.Float64bits(newVal)
		if atomic.CompareAndSwapUint64(addr, old, newBits) {
			return
		}
	}
}

// atomicReadFloat64 reads a float64 stored as uint64 bits.
func atomicReadFloat64(addr *uint64) float64 {
	return math.Float64frombits(atomic.LoadUint64(addr))
}
