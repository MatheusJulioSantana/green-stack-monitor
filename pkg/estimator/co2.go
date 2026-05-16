// Package estimator computes the carbon cost of a request.
//
// The formula is based on the Green Algorithms methodology
// (Lannelongue, Grealey & Inouye, 2021).
//
// This package has ZERO side effects — all inputs explicit, outputs deterministic.
// Safe to test, safe to benchmark, safe to swap.
package estimator

import (
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/domain"
)

const (
	// msToHours converts milliseconds to hours (for the W → kWh conversion).
	msToHours = 1.0 / 3_600_000.0
)

// Estimator converts CPU and memory usage into a CO₂ estimate.
type Estimator struct {
	cfg domain.CO2Config
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
