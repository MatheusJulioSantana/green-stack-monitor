package middleware

import (
	"math/rand/v2"
	"sync/atomic"
)

// Sampler decide se uma request deve ser instrumentada.
//
// Design:
//   - rate == 1.0  → instrumenta 100% (padrão, sem overhead de rand)
//   - rate == 0.0  → nunca instrumenta (útil para desligar sem redeployar)
//   - 0 < rate < 1 → amostragem proporcional
//
// math/rand/v2 (Go 1.22+) usa uma fonte por-goroutine internamente,
// eliminando a necessidade de lock ou estado compartilhado.
// Float64() é ~2 ns/op e zero alocações.
//
// O campo dropped é um contador atômico exposto para o Prometheus saber
// quantas requests foram intencionalmente ignoradas. Isso é importante:
// sem ele, um SampleRate baixo pareceria "requests sumindo" nos logs.
type Sampler struct {
	rate    float64
	dropped atomic.Uint64
}

// newSampler cria um Sampler. rate deve estar em [0.0, 1.0].
// A validação já ocorreu no config.Load(), mas defensivamente retornamos
// um sampler seguro para qualquer valor fora do range.
func newSampler(rate float64) *Sampler {
	if rate < 0 {
		rate = 0
	}
	if rate > 1 {
		rate = 1
	}
	return &Sampler{rate: rate}
}

// sample retorna true se esta request deve ser instrumentada.
// É seguro chamar de múltiplas goroutines simultaneamente.
func (s *Sampler) sample() bool {
	// Fast path: evita a chamada ao rand completamente nos casos extremos.
	if s.rate >= 1.0 {
		return true
	}
	if s.rate <= 0.0 {
		s.dropped.Add(1)
		return false
	}

	if rand.Float64() < s.rate {
		return true
	}

	s.dropped.Add(1)
	return false
}

// Dropped retorna o número acumulado de requests não instrumentadas.
// Usado pelo Prometheus para expor green_sampler_dropped_total.
func (s *Sampler) Dropped() uint64 {
	return s.dropped.Load()
}
