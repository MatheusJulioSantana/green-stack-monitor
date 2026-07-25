// Package badge generates SVG shields for embedding in GitHub READMEs.
package badge

import (
	"fmt"
	"math"
	"net/http"
)

// Handler serves the current badge SVG with ETag and Cache-Control headers.
func Handler(c *Cache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svg, etag := c.SVGAndETag()

		ttlSeconds := int(c.TTL().Seconds())

		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", fmt.Sprintf(
			"public, max-age=%d, stale-while-revalidate=%d",
			ttlSeconds, ttlSeconds*2,
		))
		w.Header().Set("X-Content-Type-Options", "nosniff")

		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, svg)
	}
}

// Canonical badge colors by efficiency tier.
const (
	ColorExcellent = "#44cc11" // < 0.001 g CO2/req
	ColorGood      = "#97ca00" // < 0.01 g CO2/req
	ColorFair      = "#dfb317" // < 0.1 g CO2/req
	ColorPoor      = "#e05d44" // >= 0.1 g CO2/req
)

// Classify maps grams of CO2 per request to the badge label and color.
func Classify(g float64) (label, color string) {
	switch {
	case g < 0.001:
		return fmt.Sprintf("%.4f g CO₂/req", g), ColorExcellent
	case g < 0.01:
		return fmt.Sprintf("%.3f g CO₂/req", g), ColorGood
	case g < 0.1:
		return fmt.Sprintf("%.2f g CO₂/req", g), ColorFair
	default:
		return fmt.Sprintf("%.2f g CO₂/req", math.Round(g*100)/100), ColorPoor
	}
}

func classify(g float64) (label, color string) { return Classify(g) }

// render produces a Shields.io-compatible flat SVG badge.
func render(label, color string) string {
	const leftText = "carbon"
	const lw = 58

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
