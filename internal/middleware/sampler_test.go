package middleware

// Testes do sampler ficam no mesmo package (acesso ao tipo interno).

import (
	"testing"
)

func TestSampler_Rate1_AlwaysSamples(t *testing.T) {
	s := newSampler(1.0)
	for range 1000 {
		if !s.sample() {
			t.Fatal("SampleRate=1.0 deve amostrar 100% das requests")
		}
	}
	if s.Dropped() != 0 {
		t.Errorf("Dropped deve ser 0 com rate=1.0, got %d", s.Dropped())
	}
}

func TestSampler_Rate0_NeverSamples(t *testing.T) {
	s := newSampler(0.0)
	for range 1000 {
		if s.sample() {
			t.Fatal("SampleRate=0.0 nunca deve amostrar")
		}
	}
	if s.Dropped() != 1000 {
		t.Errorf("Dropped deve ser 1000 com rate=0.0, got %d", s.Dropped())
	}
}

func TestSampler_AboveRange_ClampedTo1(t *testing.T) {
	// Valor acima de 1.0 deve ser tratado como 1.0 (clamp defensivo).
	s := newSampler(1.5)
	for range 100 {
		if !s.sample() {
			t.Fatal("rate > 1.0 deve ser clampado para 1.0")
		}
	}
}

func TestSampler_BelowRange_ClampedTo0(t *testing.T) {
	// Valor negativo deve ser tratado como 0.0 (clamp defensivo).
	s := newSampler(-0.5)
	for range 100 {
		if s.sample() {
			t.Fatal("rate < 0.0 deve ser clampado para 0.0")
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
