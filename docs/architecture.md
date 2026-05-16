# Architecture & Design Decisions

This document explains *why* Green Stack Monitor is built the way it is.
The README tells you what it does. This document tells you why each decision was made,
what was considered and rejected, and what the known trade-offs are.

---

## Dependency Graph

```
cmd/server          → (composition root, no business logic)
    │
    ├── internal/config         → stdlib only
    ├── internal/domain         → stdlib only   ← zero external deps
    ├── internal/estimator      → domain
    ├── internal/cache          → go-redis (optional)
    ├── internal/repository     → domain
    ├── internal/middleware     → domain, estimator, otel/metric
    ├── internal/service        → domain, cache
    ├── internal/handler        → domain, middleware
    └── pkg/badge               → stdlib only
```

The `domain` package is the dependency floor — nothing there imports anything
outside the Go standard library.

---

## The Middleware Hot Path

The single most important constraint: **the middleware must not add measurable latency**.

### What happens per request (sampled)

1. `sampler.sample()` — ~2 ns, zero allocs (fast path skips rand entirely).
2. `acquireEcoTrace()` from `sync.Pool` — zero allocs in steady state.
3. `runtime.ReadMemStats()` — stops the world for < 1 µs on Go 1.21+.
4. `context.WithValue` — one unavoidable alloc (interface boxing).
5. `acquireResponseWriter()` from `sync.Pool` — zero allocs in steady state.
6. `next.ServeHTTP`.
7. Capture `TracePayload` fields → `releaseEcoTrace` → `releaseResponseWriter`.
8. `select { case ch <- payload: default: drop }` — ~50 ns, non-blocking.

For unsampled requests: ~2 ns + one pool acquire/release (zero allocs).

### Why channel + worker pool instead of `go func()`

`go func()` spawns one goroutine per request. Under burst traffic: unbounded goroutine
count, unbounded stack growth, no backpressure. A fixed worker pool gives:

- Bounded memory: `BufferSize × sizeof(TracePayload)`.
- Bounded goroutines: exactly `WorkerCount`, always.
- Explicit backpressure: channel full → drop + OTEL observable counter. Visible, not silent.

### Why `select { default: drop }` and not blocking

If the select blocked, burst traffic would transfer backpressure directly to request
latency. Explicit drop with `green.worker.backpressure_drops > 0` is the
correct signal: increase `ECO_WORKER_BUFFER` or `ECO_WORKER_COUNT`.

---

## CO2 Estimation

### Formula

```
CO2 (g) = CPU_ms x TDP (W) x PUE x CI (gCO2/kWh) / 3,600,000
```

`3,600,000` converts W·ms to kWh (3600 s/h × 1000 ms/s).

### Known limitation: wall time vs CPU time

The formula uses wall time as proxy for CPU time. During I/O waits the CPU draws
less than TDP — our estimates are conservative upper bounds.

The correct fix is `runtime/metrics` with `/cpu/classes/user:cpu-seconds`.
Excluded from v1 because per-request delta computation adds hot path complexity.
Documented clearly in the README.

---

## Badge Cache

### Two independent caching layers

**Server-side TTL**: prevents `co2Fn()` and `render()` from running more than once
per TTL. Converts N allocations (repeated `fmt.Sprintf`) to 1.

**Client-side ETag + 304**: when badge content hasn't changed, server sends
`304 Not Modified` with empty body. Zero bytes transferred. Critical when the badge
is embedded in a popular README rendered thousands of times per hour.

### check-lock-check in SVGAndETag

```go
c.mu.RLock()
expired := time.Since(c.renderedAt) >= c.ttl
c.mu.RUnlock()

c.mu.Lock()
if time.Since(c.renderedAt) >= c.ttl { // check again
    c.refresh()
}
c.mu.Unlock()
```

Without the second check, two goroutines observing `expired = true` simultaneously
would both call `refresh()`. The second check ensures only one goroutine renders.

`sync.RWMutex` was chosen over `atomic.Pointer[cacheEntry]` because the three
fields (`svg`, `etag`, `renderedAt`) must be updated together, and the read
contention is low enough that ~20 ns per RLock is negligible.

---

## Authentication

JWT uses an explicit algorithm allowlist:

```go
jwt.WithValidMethods([]string{"HS256"})
```

Prevents the "algorithm confusion" attack (`"alg": "none"` or RS256-to-HS256 swap).
The secret is `[]byte` to reduce accidental logging in fmt output and stack traces.

---

## OpenTelemetry — Backend Separation

The worker and middleware depend only on `go.opentelemetry.io/otel/metric.MeterProvider`,
which is a pure interface. They have no knowledge of Prometheus, OTLP, or any
concrete backend.

The backend is wired exclusively in `cmd/server/main.go`:

```
prometheus.Registry  ←  go_* and process_* collectors
       ↑
prometheusExporter.New(WithRegisterer(promReg))   ← OTEL → Prometheus bridge
       ↑
metric.NewMeterProvider(WithReader(exporter))     ← injected as MeterProvider
       ↑
worker.go + eco_metrics.go  ← create instruments via mp.Meter()
       ↓
/metrics  ← promhttp.HandlerFor(promReg)  ← Prometheus scrapes here
```

**Swapping backends requires changing only `main.go`:**

```go
// Current: Prometheus
promExporter, _ := prometheusExporter.New(...)
mp := metric.NewMeterProvider(metric.WithReader(promExporter))

// Future: OTLP (Datadog, Honeycomb, Grafana Cloud)
otlpExporter, _ := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpoint("..."))
mp := metric.NewMeterProvider(metric.WithReader(metric.NewPeriodicReader(otlpExporter)))
```

Zero changes to `worker.go`, `eco_metrics.go`, or any test.

**Tests use `noop.MeterProvider`** — discards all metric calls silently.
Tests verify behaviour (was the trace saved? was the cache hit recorded?),
not that a specific counter was incremented. If you need to assert on metric
values, use `go.opentelemetry.io/otel/sdk/metric/metrictest`.

**Metric naming follows OTEL conventions** (`green.co2.grams`, dot-separated)
rather than Prometheus conventions (`green_co2_grams_total`, snake_case).
The Prometheus bridge translates automatically — no manual renaming needed.

---

## sync.Pool — Hot Path Allocations

Two objects are pooled in `pool.go`: `*EcoTrace` and `*responseWriter`.

**Why these two and not `TracePayload`?**

`TracePayload` is a value type that travels through the channel by copy — it never
escapes to the heap. `EcoTrace` and `responseWriter` are pointers injected into
a context and passed as an interface respectively — both escape.

**The invariant that makes pools safe:**

Every field must be explicitly reset before `Put()`. The reset happens in
`releaseEcoTrace()` and `releaseResponseWriter()`, not in the `New` func.
This is intentional: the releaser knows the full state; the acquirer
receives a guaranteed-clean object.

**The order of operations is load-bearing:**

```go
// CORRECT: capture all fields first, then release
payload := TracePayload{
    CacheHit:   trace.CacheHit,   // read from trace
    StatusCode: rw.status,        // read from rw
    ...
}
releaseEcoTrace(trace)      // resets CacheHit to false
releaseResponseWriter(rw)   // resets status to 0
```

Inverting the order — releasing before capturing — would silently zero the
fields being read. No panic, no error, just wrong CO₂ numbers and missing
cache hits in metrics. The comment in `eco_metrics.go` documents this explicitly.

**Measured impact (benchmark):**

```
BenchmarkEcoTracePool_AcquireRelease   5 ns/op    0 allocs/op
BenchmarkEcoTraceAlloc                40 ns/op    1 allocs/op
```

At 10k req/s with SampleRate=1.0: ~10k fewer heap allocations per second,
less GC pressure, fewer and shorter STW pauses.

## Graceful Shutdown Order

```
SIGTERM
  srv.Shutdown(ctx)   — stop accepting requests, drain in-flight handlers
  cancelWorker()      — signal workers: skip repo.Save after draining
  worker.Stop()       — close channel, drain remaining payloads, wg.Wait()
```

`worker.Stop()` MUST come after `srv.Shutdown()`. Closing the channel before the
last requests finish their `select { case ch <- payload }` would panic
(send on closed channel).

`cancelWorker()` between the two lets `observe()` skip `repo.Save()` after shutdown
is initiated, while still draining the channel and updating Prometheus counters.

---

## Roadmap

**Shipped**

| Feature | Notes |
|---|---|
| OpenTelemetry | `metric.MeterProvider` interface; Prometheus bridge in `main.go` |
| Smart Sampling | Lock-free `math/rand/v2`; `green.sampler.dropped` counter |
| `sync.Pool` | `*EcoTrace` + `*responseWriter`; zero allocs in steady state |
| Badge Cache | TTL + ETag + `304 Not Modified` |

**v2**

| Feature | Blocker |
|---|---|
| CPU time via `runtime/metrics` | Per-request delta computation adds hot path complexity |
| Postgres repository | Schema + migration tooling needed |
| Sliding window averages | Requires time-series storage |
| OTEL Tracing integration | Correlate CO₂ cost with trace spans |
| eBPF instrumentation | Kernel headers; out of scope for a pure Go library |
