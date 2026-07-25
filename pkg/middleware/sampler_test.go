package middleware

// Sampler tests live in the same package to access the internal type.

import (
	"testing"
)

func TestSampler_Rate1_AlwaysSamples(t *testing.T) {
	s := newSampler(1.0)
	for range 1000 {
		if !s.sample() {
			t.Fatal("SampleRate=1.0 must sample 100% of requests")
		}
	}
	if s.Dropped() != 0 {
		t.Errorf("Dropped must be 0 with rate=1.0, got %d", s.Dropped())
	}
}

func TestSampler_Rate0_NeverSamples(t *testing.T) {
	s := newSampler(0.0)
	for range 1000 {
		if s.sample() {
			t.Fatal("SampleRate=0.0 must never sample")
		}
	}
	if s.Dropped() != 1000 {
		t.Errorf("Dropped must be 1000 with rate=0.0, got %d", s.Dropped())
	}
}

func TestSampler_AboveRange_ClampedTo1(t *testing.T) {
	// Values above 1.0 are clamped to 1.0.
	s := newSampler(1.5)
	for range 100 {
		if !s.sample() {
			t.Fatal("rate > 1.0 must be clamped to 1.0")
		}
	}
}

func TestSampler_BelowRange_ClampedTo0(t *testing.T) {
	// Negative values are clamped to 0.0.
	s := newSampler(-0.5)
	for range 100 {
		if s.sample() {
			t.Fatal("rate < 0.0 must be clamped to 0.0")
		}
	}
}

func TestSampler_DroppedCounter_IsAccurate(t *testing.T) {
	s := newSampler(0.0)
	const n = 500
	for range n {
		s.sample()
	}
	if s.Dropped() != n {
		t.Errorf("Dropped: got %d, want %d", s.Dropped(), n)
	}
}

func BenchmarkSampler_Float(b *testing.B) {
	s := newSampler(0.5)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s.sample()
	}
}

func BenchmarkSampler_FastPath_Rate1(b *testing.B) {
	s := newSampler(1.0)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s.sample()
	}
}
