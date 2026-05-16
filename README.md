# 🌿 Green Stack

> **Because performance is the purest form of sustainability.**
> A lightweight Go middleware to track your API's carbon footprint — in real time, with zero latency overhead.

[![Go 1.22+](https://img.shields.io/badge/go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/license-MIT-green?style=flat)](LICENSE)

---

## 🪴 Small Footprint, Big Impact

Every request your API handles burns CPU cycles, allocates memory, and draws power from a grid. Most backends measure *if* they're working. Green Stack measures *what it costs*.

Not to shame you. To help you build better.

We built this because the most ecological thing a software engineer can do isn't switching to solar — it's writing code that does less, caches more, and wastes nothing.

---

## ✨ The "Cache-is-Green" Effect

We don't just measure cost. We celebrate savings.

Every Redis hit is a database trip that didn't happen — and a handful of CO₂ grams that weren't emitted. Green Stack tracks both sides of the ledger: what your API spent, and what it saved.

```
GET /products  →  cache hit  →  saved 0.0089 g CO₂  🌱
GET /products  →  cache miss →  cost  0.0094 g CO₂  🔥
```

That delta, accumulated across millions of requests, is the real story of your infrastructure's efficiency.

---

## 🌱 Why This Exists

The carbon cost of software is invisible — and invisible costs don't get optimized.

Green Stack makes that cost visible, per request, in real time. When your team can see that a missing cache costs 10x more CO₂ than a hit, performance stops being an abstract goal and becomes an act of care.

> Estimated using the [Green Algorithms methodology](https://doi.org/10.1002/advs.202100707) (Lannelongue, Grealey & Inouye, 2021).

---

## ⚡ Features

- **🍃 Whisper Quiet** — A buffered channel + async worker pool means your request latency is untouched. The middleware's only job on the hot path is a non-blocking `select`.
- **📡 Smart Sampling** — Don't measure everything. At 10k req/s, `SampleRate: 0.1` gives you statistically sound estimates with 90% less overhead.
- **💎 Live Badge** — A dynamic SVG badge for your README that updates every few minutes. No external service. No API key. Just `![badge](https://your-host/badge)`.
- **📊 Grafana Ready** — Metrics land in Prometheus automatically. Import our dashboard and see your CO₂ footprint alongside latency and throughput.
- **🔒 Secure by Default** — JWT auth with explicit algorithm allowlist (no `alg: none` attacks), secrets as `[]byte`, pprof disabled by default.
- **⚡ Zero Waste** — `math/rand/v2` lock-free sampling, `sync.Pool` for hot-path objects, atomic counters, `sync.RWMutex` for the badge cache. Built for Go 1.22+.

---

## 🚀 Quick Start

```bash
# 1. Clone
git clone https://github.com/matheusjuliosantana/green-stack-monitor
cd green-stack-monitor

# 2. Generate a secret (minimum 32 bytes)
openssl rand -hex 32

# 3. Start everything: app + Redis + Prometheus + Grafana
make up
```

```
  App        →  http://localhost:8080
  Metrics    →  http://localhost:8080/metrics
  Badge      →  http://localhost:8080/badge
  Prometheus →  http://localhost:9090
  Grafana    →  http://localhost:3000  (admin / admin)
```

---

## 🔌 Embed in Your Own API

Green Stack is a standard Go middleware. Drop it into any `net/http` or `chi` stack:

```go
import (
    "github.com/yourhandle/green-stack-monitor/internal/estimator"
    "github.com/yourhandle/green-stack-monitor/internal/middleware"
    "github.com/yourhandle/green-stack-monitor/pkg/badge"
)

// 1. Configure the CO₂ estimator for your region and hardware.
est, _ := estimator.New(domain.CO2Config{
    TDPWatts:                  4.0,   // cloud vCPU share
    PUE:                       1.2,   // your data center's PUE
    CarbonIntensityGCO2PerKWh: 100.0, // Brazil grid — change for your region
    MemoryWattsPerGB:          0.375,
})

// 2. Build an OTEL MeterProvider (Prometheus bridge shown — swap for OTLP, Datadog, etc).
promExporter, _ := prometheusExporter.New(prometheusExporter.WithRegisterer(promReg))
mp := metric.NewMeterProvider(metric.WithReader(promExporter))
defer mp.Shutdown(ctx)

// 3. Start the background worker (before accepting traffic).
worker := middleware.NewWorker(
    middleware.WorkerConfig{BufferSize: 512, Count: 4},
    est, repo, mp, // metric.MeterProvider — backend-agnostic
)
worker.Start(ctx)
defer worker.Stop()

// 4. Attach the middleware.
r.Use(middleware.EcoMetrics(middleware.Options{
    Estimator:     est,
    Worker:        worker,
    MeterProvider: mp,
    SampleRate:    1.0, // reduce to 0.1 for > 1k req/s
}))

// 4. Serve the live badge.
badgeCache := badge.NewCache(co2PerReqFn, 2*time.Minute)
r.Get("/badge", badge.Handler(badgeCache))
```

---

## 📐 The CO₂ Formula

Based on the [Green Algorithms methodology](https://doi.org/10.1002/advs.202100707):

```
CO₂ (g) = CPU_ms × TDP (W) × PUE × CI (gCO₂/kWh)
           ─────────────────────────────────────────
                        3,600,000
```

| Variable | Default | What it means |
|---|---|---|
| `TDP` | `4 W` | Power draw of a cloud vCPU share |
| `PUE` | `1.2` | Data center overhead (1.0 = perfect, world avg ≈ 1.58) |
| `CI` | `100 gCO₂/kWh` | Brazil grid — EU ≈ 250, US East ≈ 400 |
| `Memory` | `0.375 W/GB` | DRAM power per gigabyte |

> **Note on precision:** The formula uses wall time (total request duration), not CPU time. During I/O waits, the CPU draws less than TDP. This means estimates are conservative — real emissions are likely lower. This limitation is documented and will be addressed in v2 via `runtime/metrics`.

---

## 📊 Metrics Reference

All metrics are prefixed with `green_` and exposed at `/metrics` in OpenMetrics format.

| OTEL Metric Name | Prometheus Name (via bridge) | Type | Description |
|---|---|---|---|
| `green.co2.grams` | `green_co2_grams_total` | Counter | CO₂ emitted, by method / path / status |
| `green.co2.saved_grams` | `green_co2_saved_grams_total` | Counter | CO₂ avoided via cache hits |
| `green.request.duration` | `green_request_duration_bucket` | Histogram | Latency distribution |
| `green.request.alloc_bytes` | `green_request_alloc_bytes_bucket` | Histogram | Heap allocated per request |
| `green.runtime.goroutines` | `green_runtime_goroutines` | Gauge | Active goroutines at request completion |
| `green.worker.queue_len` | `green_worker_queue_len` | Gauge | Traces waiting in the worker channel |
| `green.worker.backpressure_drops` | `green_worker_backpressure_drops_total` | Counter | Traces dropped — should be 0 |
| `green.sampler.dropped` | `green_sampler_dropped_total` | Counter | Requests skipped by sampler (expected) |

---

## 🎨 The Badge

Embed a live efficiency badge in your README:

```markdown
![Carbon Efficiency](https://your-host/badge)
```

The badge refreshes every 2 minutes (configurable) and changes color as your efficiency changes:

| Color | Threshold | Signal |
|---|---|---|
| 🟢 Green | `< 0.001 g/req` | Excellent — cache is working, algorithms are lean |
| 🟡 Yellow-green | `< 0.01 g/req` | Good — room to optimize, no urgency |
| 🟠 Amber | `< 0.1 g/req` | Worth investigating — check cache hit rates |
| 🔴 Red | `≥ 0.1 g/req` | Act now — likely N+1 queries or missing cache |

The badge uses `ETag` + `304 Not Modified` — when nothing changed, zero bytes cross the wire.

---

## ⚙️ Configuration

All configuration via environment variables. No config files, no YAML.

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `JWT_SECRET` | *(required)* | Min 32 bytes — generate with `openssl rand -hex 32` |
| `REDIS_ADDR` | *(empty)* | `host:port` — falls back to in-memory if unset |
| `REDIS_PASSWORD` | *(empty)* | Redis auth |
| `ECO_SAMPLE_RATE` | `1.0` | Fraction of requests to instrument (`0.1` = 10%) |
| `ECO_WORKER_BUFFER` | `512` | Channel capacity between middleware and workers |
| `ECO_WORKER_COUNT` | `4` | Background worker goroutines |
| `CO2_TDP_WATTS` | `4.0` | CPU thermal design power (W) |
| `CO2_PUE` | `1.2` | Data center PUE |
| `CO2_CARBON_INTENSITY` | `100.0` | Grid carbon intensity (gCO₂/kWh) |
| `CO2_MEMORY_WATTS_PER_GB` | `0.375` | DRAM power per GB |
| `PPROF_ENABLED` | `false` | Expose `/debug/pprof` — **never enable in public clusters** |

**Sizing `ECO_WORKER_BUFFER`:** `BufferSize ≥ peak_rps × avg_processing_ms / 1000`. At 1000 req/s with 50ms processing: 50 slots minimum. We default to 512 for headroom.

---

## 🧪 Testing

```bash
# Full suite with race detector
make test

# Statistical sampling test (slower)
go test ./... -race -count=1

# Benchmarks — confirms sampler is < 5 ns/op, zero allocs
make bench

# Run only fast tests
go test ./... -short -race
```

---

## 🏗 Architecture

```
HTTP Request
    │
    ├─ sampler.sample() ──── false ──► handler (zero overhead)
    │
    └─ true
        │
        ├─ ReadMemStats (pre)
        ├─ inject *EcoTrace into context
        ├─ handler chain
        │
        └─ select { ch <- TracePayload }  ← non-blocking, ~50ns
               │                  │
            enqueued            full → drop + metric
               │
           Worker pool (4 goroutines)
               │
               ├─ ReadMemStats (post)
               ├─ CO₂ calculation
               ├─ OTEL instruments (counter, histogram)
               └─ repo.Save()
```

Clean Architecture layers — nothing in `domain` imports anything outside the stdlib. Services depend only on interfaces. Handlers contain zero business logic.

---

## 🗺 Roadmap

**Shipped**
- [x] **OpenTelemetry** — backend-agnostic via `metric.MeterProvider`. Prometheus bridge included; swap for OTLP, Datadog, or Honeycomb in one place.
- [x] **Smart Sampling** — `ECO_SAMPLE_RATE` with lock-free `math/rand/v2`. Statistical accuracy at any throughput.
- [x] **`sync.Pool`** — `*EcoTrace` and `*responseWriter` pooled on the hot path. Zero allocs per request in steady state.
- [x] **Badge Cache** — TTL + ETag + `304 Not Modified`. SVG rendered once, served millions of times.

**Coming in v2**
- [ ] **Wall time vs CPU time** — `runtime/metrics` with `/cpu/classes/user:cpu-seconds` for tighter estimates without cgo.
- [ ] **Postgres repository** — persistent trace storage with time-series queries.
- [ ] **Sliding window averages** — per-endpoint CO₂ trends, not just all-time counters.
- [ ] **OTEL Tracing** — correlate CO₂ cost with individual trace spans.

---

## 📄 License

MIT — use it, fork it, make your infrastructure greener.

---

*Built with care in Go. Measured in grams.*
