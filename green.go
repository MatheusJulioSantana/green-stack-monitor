// green-stack-monitor/green.go
package greenstack

import (
	"context"
	"fmt"
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

// BadgeColor retorna a cor do badge em hex baseada no CO₂/req
func BadgeColor(perReqG float64) string {
	switch {
	case perReqG < 0.001:
		return "#3cb878" // verde
	case perReqG < 0.01:
		return "#8cc63f" // amarelo-verde
	case perReqG < 0.1:
		return "#f7941d" // âmbar
	default:
		return "#e53935" // vermelho
	}
}

// BadgeSVG retorna o SVG do badge com a cor baseada no CO₂/req
// Para usar: passe um *BadgeCache e chame c.SVGAndETag() para ter svg e etag
// Este helper gera o SVG on-the-fly sem cache (para casos simples)
func BadgeSVG(perReqG float64) string {
	// Gera o SVG dinamicamente sem usar cache
	label, color := classifyForBadge(perReqG)
	return renderBadgeSVG(label, color)
}

// classifyForBadge mapeia CO₂ para label e cor (duplica logic de badge.classify)
func classifyForBadge(g float64) (label, color string) {
	switch {
	case g < 0.001:
		return labelForCO2(g, "%.4f g CO₂/req"), "#3cb878"
	case g < 0.01:
		return labelForCO2(g, "%.3f g CO₂/req"), "#8cc63f"
	case g < 0.1:
		return labelForCO2(g, "%.2f g CO₂/req"), "#f7941d"
	default:
		return labelForCO2(g, "%.2f g CO₂/req"), "#e53935"
	}
}

func labelForCO2(g float64, format string) string {
	var v float64
	if g >= 0.1 {
		// Arredonda para 2 decimais
		v = float64(int(g*100)) / 100
	} else {
		v = g
	}
	return fmt.Sprintf(format, v)
}

// renderBadgeSVG produz um SVG no formato Shields.io flat badge.
// Cópia simplificada de badge.render() para uso standalone
func renderBadgeSVG(label, color string) string {
	const leftText = "carbon"
	const lw = 58 // largura fixa do lado esquerdo ("carbon")

	rightText := label
	rw := len(rightText)*7 + 10
	total := lw + rw

	return fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="carbon: %s">`+
			`<title>carbon: %s</title>`+
			`<linearGradient id="s" x2="0" y2="100%%">`+
			`<stop offset="0" stop-color="#bbb" stop-opacity=".1"/>`+
			`<stop offset="1" stop-opacity=".1"/>`+
			`</linearGradient>`+
			`<clipPath id="r"><rect width="%d" height="20" rx="3" fill="#fff"/></clipPath>`+
			`<g clip-path="url(#r)">`+
			`<rect width="%d" height="20" fill="#555"/>`+
			`<rect x="%d" width="%d" height="20" fill="%s"/>`+
			`<rect width="%d" height="20" fill="url(#s)"/>`+
			`</g>`+
			`<g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="110">`+
			`<text x="%d" y="150" fill="#010101" fill-opacity=".3" transform="scale(.1)" textLength="%d" lengthAdjust="spacing">%s</text>`+
			`<text x="%d" y="140" transform="scale(.1)" textLength="%d" lengthAdjust="spacing">%s</text>`+
			`<text x="%d" y="150" fill="#010101" fill-opacity=".3" transform="scale(.1)" textLength="%d" lengthAdjust="spacing">%s</text>`+
			`<text x="%d" y="140" transform="scale(.1)" textLength="%d" lengthAdjust="spacing">%s</text>`+
			`</g></svg>`,
		total, label, label,
		total,
		lw, lw, rw, color, total,
		lw*5, (lw-10)*10, leftText,
		lw*5, (lw-10)*10, leftText,
		(lw+rw/2)*10, (rw-10)*10, rightText,
		(lw+rw/2)*10, (rw-10)*10, rightText,
	)
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
