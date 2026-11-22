package middleware_test

import (
	"runtime"
	"testing"
)

// BenchmarkReadMemStats measures the cost of runtime.ReadMemStats, which the
// EcoMetrics middleware calls before and after each sampled request.
// Measured at roughly 41-54 µs per call with zero allocations; see
// docs/architecture.md.
func BenchmarkReadMemStats(b *testing.B) {
	b.Run("sequential", func(b *testing.B) {
		var m runtime.MemStats
		b.ReportAllocs()
		for range b.N {
			runtime.ReadMemStats(&m)
		}
	})

	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			var m runtime.MemStats
			for pb.Next() {
				runtime.ReadMemStats(&m)
			}
		})
	})
}
