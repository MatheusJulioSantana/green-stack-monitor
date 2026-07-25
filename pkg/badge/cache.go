package badge

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"time"
)

// Cache stores the pre-rendered SVG and its matching ETag.
//
// Two savings layers:
//  1. TTL: while the cache is valid, co2PerReqFn and render are not called.
//     This saves CPU and memory on each /badge request.
//  2. ETag: when the SVG has not changed since the last client response,
//     the handler returns 304 without a body.
//
// Thread safety: RWMutex keeps frequent reads cheap while refreshes remain exclusive.
// Multiple goroutines can read the cache concurrently without blocking each other.
// Refresh happens at most once per TTL window.
//
// Why not sync/atomic for the SVG?
// Go strings are immutable, but not atomic values. Swapping a string safely would
// require an atomic pointer. RWMutex is more readable, and uncontended read locks
// are cheap enough for this path.
type Cache struct {
	mu         sync.RWMutex
	svg        string    // pre-rendered SVG
	etag       string    // truncated SHA-256 of the current SVG
	renderedAt time.Time // when the cache was generated
	ttl        time.Duration

	co2Fn func() float64 // injected data source
}

// NewCache creates a Cache and warms it with an initial render.
//
// Recommended TTL:
//   - 1-2 min: heavily accessed README badge
//   - 5 min: internal dashboard
//   - 30 s: local development
//
// co2PerReqFn is called only on cache refresh, never on each request.
func NewCache(co2PerReqFn func() float64, ttl time.Duration) *Cache {
	c := &Cache{
		co2Fn: co2PerReqFn,
		ttl:   ttl,
	}
	c.refresh()
	return c
}

// SVGAndETag returns the current SVG and its ETag.
// If the cache is expired, it refreshes before returning.
// The first request after expiration always receives fresh data.
func (c *Cache) SVGAndETag() (svg, etag string) {
	// Fast path: read without exclusive locking.
	c.mu.RLock()
	expired := time.Since(c.renderedAt) >= c.ttl
	if !expired {
		svg, etag = c.svg, c.etag
		c.mu.RUnlock()
		return
	}
	c.mu.RUnlock()

	// Slow path: exclusive lock for refresh.
	// Check-lock-check handles the case where another goroutine refreshed while
	// this one was waiting for the lock.
	c.mu.Lock()
	defer c.mu.Unlock()

	if time.Since(c.renderedAt) >= c.ttl {
		c.refresh()
	}

	return c.svg, c.etag
}

// TTL returns the configured refresh interval.
func (c *Cache) TTL() time.Duration {
	return c.ttl
}

// refresh renders the SVG and updates the ETag.
// It must be called with the write mutex already held, or during initialization.
func (c *Cache) refresh() {
	g := c.co2Fn()
	label, color := classify(g)
	svg := render(label, color)

	// ETag: first 16 hex chars of the SVG SHA-256.
	// This is enough to detect content changes.
	hash := sha256.Sum256([]byte(svg))
	etag := fmt.Sprintf(`W/"%x"`, hash[:8])

	c.svg = svg
	c.etag = etag
	c.renderedAt = time.Now()
}
