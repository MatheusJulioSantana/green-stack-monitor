package middleware

import (
	"sync"
	"time"
)

// ecoTracePool reuses *EcoTrace values across sampled requests.
//
// Why pool here and not for TracePayload?
// TracePayload is a value struct copied through the channel, so it does not
// allocate on the heap. EcoTrace is a pointer injected into request context, so
// it escapes to the heap for each sampled request.
//
// Measured gain: removes 1 allocation per sampled request on the critical path.
// At 10k req/s with SampleRate=1.0, that is roughly 10k fewer allocs/s, reducing
// GC pressure and stop-the-world pause frequency.
//
// Safety invariant: every *EcoTrace field must be reset before returning it to
// the pool. Leaking data from a previous request would create a silent and hard
// to reproduce bug. Reset happens in releaseEcoTrace, not Get, because the
// releaser knows the current state and the acquirer always receives a clean one.
var ecoTracePool = sync.Pool{
	New: func() any {
		return &EcoTrace{}
	},
}

// acquireEcoTrace returns a clean *EcoTrace from the pool.
// The caller must call releaseEcoTrace when the trace is no longer needed.
func acquireEcoTrace() *EcoTrace {
	return ecoTracePool.Get().(*EcoTrace)
}

// releaseEcoTrace resets every field and returns the trace to the pool.
// It must be called after TracePayload is enqueued or dropped, never before,
// because middleware still reads trace.CacheHit and trace.startAlloc.
func releaseEcoTrace(t *EcoTrace) {
	// Reset each field explicitly to the correct zero value.
	t.startAlloc = 0
	t.startTime = time.Time{}
	t.CacheHit = false
	t.sampled = false
	ecoTracePool.Put(t)
}

// responseWriterPool reuses *responseWriter across requests.
//
// responseWriter is allocated once per request because even unsampled requests
// must capture the status code. The pool removes that hot-path allocation in
// both cases.
//
// Invariant: status is reset before returning to the pool so the status from a
// previous request cannot leak into the next one.
var responseWriterPool = sync.Pool{
	New: func() any {
		return &responseWriter{}
	},
}

func acquireResponseWriter() *responseWriter {
	return responseWriterPool.Get().(*responseWriter)
}

func releaseResponseWriter(rw *responseWriter) {
	rw.ResponseWriter = nil
	rw.status = 0
	rw.wroteHeader = false
	responseWriterPool.Put(rw)
}
