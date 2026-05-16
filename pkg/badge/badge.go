// Package badge generates SVG shields for embedding in GitHub READMEs.
//
// Fluxo por request:
//
//	GET /badge
//	    │
//	    ├── Cache válido? ──── sim ──► SVG do cache
//	    │       │                          │
//	    │      não                         ▼
//	    │       └──► co2Fn() + render() ──► atualiza cache
//	    │                                   │
//	    └───────────────────────────────────┘
//	                                        │
//	                          ETag == If-None-Match?
//	                               │           │
//	                              sim          não
//	                               │           │
//	                           304 (vazio)   200 + SVG
//
// O cliente (GitHub, browser) guarda o ETag e manda If-None-Match nas
// requisições seguintes. Se o badge não mudou, recebe 304 sem body —
// zero bytes transferidos, zero CPU de parsing no cliente.
package badge

import (
	"fmt"
	"math"
	"net/http"
)

// Handler retorna um http.HandlerFunc que serve o badge SVG com cache e ETag.
//
// Headers de resposta:
//   - Content-Type: image/svg+xml
//   - ETag: W/"<hash>" — permite revalidação eficiente pelo cliente
//   - Cache-Control: public, max-age=<ttl>, stale-while-revalidate=<ttl*2>
//     public: CDNs podem cachear (útil quando o badge está num README popular)
//     stale-while-revalidate: cliente usa a versão velha enquanto busca a nova
//   - X-Content-Type-Options: nosniff — defesa contra MIME sniffing
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

		// Se o cliente tem o mesmo ETag, o conteúdo não mudou.
		// 304 sem body: economiza banda e CPU de parsing no cliente.
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, svg)
	}
}

// classify mapeia gramas de CO₂ por request para um label e cor de badge.
//
// Thresholds baseados na distribuição típica de APIs Go em cloud:
//   - < 0.001 g: aplicação muito eficiente (poucos ms de CPU, cache agressivo)
//   - < 0.01 g:  boa eficiência (operações simples sem I/O pesado)
//   - < 0.1 g:   aceitável (queries moderadas, sem otimização agressiva)
//   - ≥ 0.1 g:   investigar (possível N+1, ausência de cache, CPU intensivo)
func classify(g float64) (label, color string) {
	switch {
	case g < 0.001:
		return fmt.Sprintf("%.4f g CO₂/req", g), "#44cc11"
	case g < 0.01:
		return fmt.Sprintf("%.3f g CO₂/req", g), "#97ca00"
	case g < 0.1:
		return fmt.Sprintf("%.2f g CO₂/req", g), "#dfb317"
	default:
		return fmt.Sprintf("%.2f g CO₂/req", math.Round(g*100)/100), "#e05d44"
	}
}

// render produz um SVG no formato Shields.io flat badge.
// Largura dinâmica para acomodar labels de tamanhos variados.
// Chamado apenas no refresh do cache — nunca por request direta.
func render(label, color string) string {
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
