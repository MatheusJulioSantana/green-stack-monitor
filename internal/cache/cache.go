// Package cache provides a two-tier caching abstraction:
//   - Primary: Redis (production)
//   - Fallback: sync.Map in-memory (development / testing)
//
// Both implement the same Cache interface, so the application layer
// is completely unaware of which backend is active.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// ErrMiss is returned when a key is not found in the cache.
var ErrMiss = errors.New("cache miss")

// Cache is the port (interface) the service layer depends on.
// Keep it minimal — only what the domain actually needs.
type Cache interface {
	// Get retrieves a value. Returns ErrMiss if the key is absent.
	Get(ctx context.Context, key string, dest any) error
	// Set stores a value with a TTL.
	Set(ctx context.Context, key string, value any, ttl time.Duration) error
	// Delete removes a key.
	Delete(ctx context.Context, key string) error
}

// --------------------------------------------------------------------------
// In-memory implementation (zero dependencies, suitable for tests and dev)
// --------------------------------------------------------------------------

type entry struct {
	data      []byte
	expiresAt time.Time
}

// Memory is a goroutine-safe in-memory cache backed by sync.Map.
type Memory struct {
	m sync.Map
}

func NewMemory() *Memory { return &Memory{} }

func (c *Memory) Get(ctx context.Context, key string, dest any) error {
	raw, ok := c.m.Load(key)
	if !ok {
		return ErrMiss
	}
	e := raw.(entry)
	if time.Now().After(e.expiresAt) {
		c.m.Delete(key)
		return ErrMiss
	}
	return json.Unmarshal(e.data, dest)
}

func (c *Memory) Set(_ context.Context, key string, value any, ttl time.Duration) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.m.Store(key, entry{data: b, expiresAt: time.Now().Add(ttl)})
	return nil
}

func (c *Memory) Delete(_ context.Context, key string) error {
	c.m.Delete(key)
	return nil
}

// --------------------------------------------------------------------------
// Redis implementation
// --------------------------------------------------------------------------
// Import is guarded by a build tag so the binary stays slim when Redis
// is not needed. See cache_redis.go for the Redis implementation.
// This file intentionally contains only the interface and the memory impl.
