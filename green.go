// green-stack-monitor/green.go
package greenstack

import (
	"context"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/matheusjuliosantana/green-stack-monitor/pkg/badge"
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/domain"
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/estimator"
	"github.com/matheusjuliosantana/green-stack-monitor/pkg/middleware"
)

// --- Domain Types ---

type RequestTrace = domain.RequestTrace
type CacheMetrics = domain.CacheMetrics
type CO2Config = domain.CO2Config
type TraceWriter = domain.TraceWriter
type TraceReader = domain.TraceReader
type EcoRepository = domain.EcoRepository

// --- Estimator ---

type Estimator = estimator.Estimator

func NewEstimator(cfg CO2Config) (*Estimator, error) {
	return estimator.New(cfg)
}

// --- Badge ---

type BadgeCache = badge.Cache

func NewBadgeCache(fn func() float64, ttl time.Duration) *BadgeCache {
	return badge.NewCache(fn, ttl)
}

func BadgeHandler(c *BadgeCache) http.HandlerFunc {
	return badge.Handler(c)
}

// BadgeColor returns the canonical badge color for a CO2-per-request value.
func BadgeColor(perReqG float64) string {
	_, color := badge.Classify(perReqG)
	return color
}

// BadgeSVG renders a badge without a long-lived cache.
// Production services should prefer NewBadgeCache with BadgeHandler.
func BadgeSVG(perReqG float64) string {
	c := badge.NewCache(func() float64 { return perReqG }, 0)
	svg, _ := c.SVGAndETag()
	return svg
}

func Snapshot(est *Estimator) estimator.Snapshot {
	return est.Snapshot()
}

// --- Middleware ---

type EcoMetricsOptions = middleware.Options
type WorkerConfig = middleware.WorkerConfig
type Worker = middleware.Worker

func NewWorker(cfg WorkerConfig, est *Estimator, repo TraceWriter, mp metric.MeterProvider) *Worker {
	return middleware.NewWorker(cfg, est, repo, mp)
}

func EcoMetrics(opts EcoMetricsOptions) func(http.Handler) http.Handler {
	return middleware.EcoMetrics(opts)
}

func MarkCacheHit(ctx context.Context) {
	middleware.MarkCacheHit(ctx)
}

func JWTAuth(secret []byte) func(http.Handler) http.Handler {
	return middleware.JWTAuth(secret)
}

// --- Config presets ---

func DefaultBrazilConfig() CO2Config {
	return CO2Config{
		TDPWatts:                  4.0,
		PUE:                       1.2,
		CarbonIntensityGCO2PerKWh: 100.0,
		MemoryWattsPerGB:          0.375,
	}
}

func DefaultEUConfig() CO2Config {
	return CO2Config{
		TDPWatts:                  4.0,
		PUE:                       1.15,
		CarbonIntensityGCO2PerKWh: 250.0,
		MemoryWattsPerGB:          0.375,
	}
}

func DefaultUSEastConfig() CO2Config {
	return CO2Config{
		TDPWatts:                  4.0,
		PUE:                       1.2,
		CarbonIntensityGCO2PerKWh: 400.0,
		MemoryWattsPerGB:          0.375,
	}
}
