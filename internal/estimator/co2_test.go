package estimator_test

import (
	"testing"

	"github.com/yourhandle/green-stack-monitor/internal/domain"
	"github.com/yourhandle/green-stack-monitor/internal/estimator"
)

// referenceConfig represents a realistic cloud vCPU in Brazil.
var referenceConfig = domain.CO2Config{
	TDPWatts:                  4.0,
	PUE:                       1.2,
	CarbonIntensityGCO2PerKWh: 100.0,
	MemoryWattsPerGB:          0.375,
}

func TestNew_InvalidConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  domain.CO2Config
		want error
	}{
		{"zero TDP", domain.CO2Config{TDPWatts: 0, PUE: 1.2, CarbonIntensityGCO2PerKWh: 100}, estimator.ErrInvalidTDP},
		{"PUE below 1", domain.CO2Config{TDPWatts: 4, PUE: 0.9, CarbonIntensityGCO2PerKWh: 100}, estimator.ErrInvalidPUE},
		{"zero CI", domain.CO2Config{TDPWatts: 4, PUE: 1.2, CarbonIntensityGCO2PerKWh: 0}, estimator.ErrInvalidCI},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := estimator.New(tc.cfg)
			if err != tc.want {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestEstimateCPU_Monotonic(t *testing.T) {
	est, err := estimator.New(referenceConfig)
	if err != nil {
		t.Fatal(err)
	}

	// More CPU time must always mean more CO₂.
	prev := 0.0
	for _, ms := range []float64{1, 10, 100, 1000, 10_000} {
		co2 := est.EstimateCPU(ms)
		if co2 <= prev {
			t.Errorf("EstimateCPU(%v ms) = %v, expected > %v", ms, co2, prev)
		}
		prev = co2
	}
}

func TestEstimateCPU_KnownValue(t *testing.T) {
	est, _ := estimator.New(referenceConfig)

	// Cálculo manual passo a passo:
	//   kWh = cpu_ms * TDP(W) / 3_600_000
	//       = 1000 * 4 / 3_600_000
	//       = 1.1111e-3 kWh          ← o comentário anterior errava aqui (escrevia 1.111e-6)
	//   CO2 = kWh * PUE * CI
	//       = 1.1111e-3 * 1.2 * 100
	//       = 1.3333e-1 g
	got := est.EstimateCPU(1000)
	want := 1.3333333333333333e-1

	if diff := abs(got - want); diff > 1e-12 {
		t.Errorf("EstimateCPU(1000ms) = %.6e, want %.6e (diff %.2e)", got, want, diff)
	}
}

func TestCacheSaving_IsPositive(t *testing.T) {
	est, _ := estimator.New(referenceConfig)
	saving := est.CacheSaving(0.01)
	if saving <= 0 {
		t.Errorf("CacheSaving must be > 0, got %v", saving)
	}
	if saving >= 0.01 {
		t.Errorf("CacheSaving must be < base cost, got %v", saving)
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// BenchmarkEstimateCPU confirms the estimator stays well under 100 ns/op.
// If it ever exceeds that, we've accidentally added I/O or allocations.
func BenchmarkEstimateCPU(b *testing.B) {
	est, _ := estimator.New(referenceConfig)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = est.EstimateCPU(42.5)
	}
}
