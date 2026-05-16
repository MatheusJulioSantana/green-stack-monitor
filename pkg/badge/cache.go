package badge

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"time"
)

// Cache mantém o SVG pré-renderizado e o ETag correspondente.
//
// Dois níveis de economia:
//  1. TTL: enquanto o cache for válido, co2PerReqFn() e render() não são chamados.
//     CPU e memória poupados em cada request ao /badge.
//  2. ETag: se o SVG não mudou desde a última resposta ao cliente,
//     respondemos 304 sem body — zero bytes transferidos.
//
// Thread-safety: RWMutex porque leituras são muito mais frequentes que escritas.
// Múltiplas goroutines leem o cache simultaneamente sem contenção.
// A escrita (refresh) ocorre no máximo uma vez por TTL.
//
// Por que não usar sync/atomic para o SVG?
// strings em Go são imutáveis mas não são tipos atômicos — trocar uma string
// exige um ponteiro atômico (atomic.Pointer[string], Go 1.19+). RWMutex é
// mais legível e o overhead de lock em leituras sem contenção é < 50 ns.
type Cache struct {
	mu          sync.RWMutex
	svg         string    // SVG pré-renderizado
	etag        string    // SHA-256 truncado do SVG atual
	renderedAt  time.Time // quando o cache foi gerado
	ttl         time.Duration

	co2Fn func() float64 // fonte de dados — injetada, não acoplada
}

// NewCache cria um Cache e pré-aquece com uma renderização inicial.
//
// ttl recomendado:
//   - 1–2 min: badge em README muito acessado
//   - 5 min: dashboard interno
//   - 30 s: desenvolvimento local
//
// co2PerReqFn é chamada apenas na renovação do cache, nunca por request.
func NewCache(co2PerReqFn func() float64, ttl time.Duration) *Cache {
	c := &Cache{
		co2Fn: co2PerReqFn,
		ttl:   ttl,
	}
	c.refresh() // aquece o cache na criação — sem cold start
	return c
}

// SVGAndETag retorna o SVG atual e seu ETag.
// Se o cache estiver expirado, dispara um refresh antes de retornar.
// Garante que a primeira request após expiração sempre recebe dados frescos.
func (c *Cache) SVGAndETag() (svg, etag string) {
	// Fast path: leitura sem lock exclusivo.
	c.mu.RLock()
	expired := time.Since(c.renderedAt) >= c.ttl
	if !expired {
		svg, etag = c.svg, c.etag
		c.mu.RUnlock()
		return
	}
	c.mu.RUnlock()

	// Cache expirado: precisa de lock exclusivo para refresh.
	// Padrão check-lock-check: outro goroutine pode ter renovado enquanto
	// esperávamos o lock — verificamos novamente antes de renderizar.
	c.mu.Lock()
	defer c.mu.Unlock()

	if time.Since(c.renderedAt) >= c.ttl {
		c.refresh() // ainda expirado: renderiza agora
	}

	return c.svg, c.etag
}

// TTL retorna o intervalo de renovação configurado.
func (c *Cache) TTL() time.Duration {
	return c.ttl
}

// refresh renderiza o SVG e atualiza o ETag.
// Deve ser chamado com o mutex de escrita já adquirido (ou durante a inicialização).
func (c *Cache) refresh() {
	g := c.co2Fn()
	label, color := classify(g)
	svg := render(label, color)

	// ETag: primeiros 16 chars do SHA-256 do SVG.
	// Suficiente para detectar qualquer mudança de conteúdo.
	// Formato: W/"<hash>" — ETag fraco, semanticamente equivalente.
	hash := sha256.Sum256([]byte(svg))
	etag := fmt.Sprintf(`W/"%x"`, hash[:8])

	c.svg = svg
	c.etag = etag
	c.renderedAt = time.Now()
}
