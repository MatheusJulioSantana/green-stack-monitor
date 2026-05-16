// Package config loads and validates all application configuration from
// environment variables.
//
// Design rules:
//   - All config lives in one struct — easy to pass, easy to mock.
//   - No os.Getenv calls outside this package.
//   - Validation at startup: if the config is wrong, the process exits
//     immediately with a clear error message (fail fast).
//   - Secrets (JWT secret, DB password) are read as []byte to reduce
//     the risk of accidental logging as strings.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/yourhandle/green-stack-monitor/internal/domain"
)

// App holds the full runtime configuration of the server.
type App struct {
	// Server
	Port         string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration

	// Auth
	JWTSecret []byte

	// Redis (optional — if empty, falls back to in-memory cache)
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// CO₂ estimation parameters
	CO2 domain.CO2Config

	// Observability
	PProfEnabled bool

	// Sampling rate for EcoMetrics instrumentation.
	// 1.0 = measure every request (default).
	// 0.1 = measure ~10% of requests — recommended for > 1k req/s.
	// 0.0 = disable instrumentation entirely.
	SampleRate float64

	// Worker dimensioning.
	// WorkerBufferSize: capacidade do canal entre middleware e workers.
	//   Regra: BufferSize >= peak_rps * avg_processing_ms / 1000
	// WorkerCount: goroutines consumindo o canal.
	//   Além de GOMAXPROCS raramente ajuda — gargalo costuma ser I/O.
	WorkerBufferSize int
	WorkerCount      int
}

// Load reads environment variables and returns a validated App config.
// Callers should treat the returned error as fatal — log it and exit.
func Load() (*App, error) {
	cfg := &App{
		Port:         envStr("PORT", "8080"),
		ReadTimeout:  envDuration("READ_TIMEOUT", 5*time.Second),
		WriteTimeout: envDuration("WRITE_TIMEOUT", 10*time.Second),
		IdleTimeout:  envDuration("IDLE_TIMEOUT", 120*time.Second),

		JWTSecret: []byte(envStr("JWT_SECRET", "")),

		RedisAddr:     envStr("REDIS_ADDR", ""),
		RedisPassword: envStr("REDIS_PASSWORD", ""),
		RedisDB:       envInt("REDIS_DB", 0),

		CO2: domain.CO2Config{
			TDPWatts:                  envFloat("CO2_TDP_WATTS", 4.0),         // typical cloud vCPU share
			PUE:                       envFloat("CO2_PUE", 1.2),               // hyperscaler average
			CarbonIntensityGCO2PerKWh: envFloat("CO2_CARBON_INTENSITY", 100.0), // BR grid average
			MemoryWattsPerGB:          envFloat("CO2_MEMORY_WATTS_PER_GB", 0.375),
		},

		PProfEnabled: envBool("PPROF_ENABLED", false),
		SampleRate:   envFloat("ECO_SAMPLE_RATE", 1.0),

		WorkerBufferSize: envInt("ECO_WORKER_BUFFER", 512),
		WorkerCount:      envInt("ECO_WORKER_COUNT", 4),
	}

	return cfg, cfg.validate()
}

func (c *App) validate() error {
	var errs []error

	if len(c.JWTSecret) < 32 {
		errs = append(errs, fmt.Errorf(
			"JWT_SECRET must be at least 32 bytes long (got %d); "+
				"generate one with: openssl rand -hex 32",
			len(c.JWTSecret),
		))
	}
	if c.CO2.PUE < 1.0 {
		errs = append(errs, errors.New("CO2_PUE must be >= 1.0"))
	}
	if c.CO2.TDPWatts <= 0 {
		errs = append(errs, errors.New("CO2_TDP_WATTS must be > 0"))
	}
	if c.WorkerBufferSize < 0 {
		errs = append(errs, errors.New("ECO_WORKER_BUFFER must be >= 0"))
	}
	if c.WorkerCount < 1 {
		errs = append(errs, errors.New("ECO_WORKER_COUNT must be >= 1"))
	}
	if c.SampleRate < 0 || c.SampleRate > 1 {
		errs = append(errs, fmt.Errorf(
			"ECO_SAMPLE_RATE must be between 0.0 and 1.0 (got %.4f)",
			c.SampleRate,
		))
	}

	return errors.Join(errs...)
}

// --- helpers ----------------------------------------------------------------

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envFloat(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
