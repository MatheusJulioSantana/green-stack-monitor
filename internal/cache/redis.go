package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis wraps go-redis and implements the Cache interface.
// It serialises all values to JSON — consistent with the Memory impl
// so backends are interchangeable.
type Redis struct {
	client *redis.Client
}

// NewRedis creates a Redis cache client.
// Pass addr as "host:port" (e.g. "localhost:6379").
func NewRedis(addr, password string, db int) (*Redis, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
		// Connection pool — tune to your expected concurrency.
		PoolSize:     10,
		MinIdleConns: 2,
	})

	// Ping on startup to fail fast if Redis is unreachable.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis: ping failed: %w", err)
	}

	return &Redis{client: client}, nil
}

func (r *Redis) Get(ctx context.Context, key string, dest any) error {
	raw, err := r.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return ErrMiss
		}
		return fmt.Errorf("redis: get %q: %w", key, err)
	}
	return json.Unmarshal(raw, dest)
}

func (r *Redis) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := r.client.Set(ctx, key, b, ttl).Err(); err != nil {
		return fmt.Errorf("redis: set %q: %w", key, err)
	}
	return nil
}

func (r *Redis) Delete(ctx context.Context, key string) error {
	if err := r.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("redis: del %q: %w", key, err)
	}
	return nil
}

// Close releases Redis connections.
func (r *Redis) Close() error {
	return r.client.Close()
}
