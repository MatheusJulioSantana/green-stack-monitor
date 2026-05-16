package badge_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matheusjuliosantana/green-stack-monitor/pkg/badge"
)

// --- helpers ----------------------------------------------------------------

// countingFn retorna uma função que registra quantas vezes foi chamada
// e devolve o valor configurado. Permite verificar que o cache evita
// chamadas desnecessárias à fonte de dados.
func countingFn(value float64) (fn func() float64, calls *atomic.Int64) {
	var n atomic.Int64
	return func() float64 {
		n.Add(1)
		return value
	}, &n
}

func newCache(t *testing.T, fn func() float64, ttl time.Duration) *badge.Cache {
	t.Helper()
	return badge.NewCache(fn, ttl)
}

// --- testes do Cache --------------------------------------------------------

func TestCache_PrewarmsOnCreation(t *testing.T) {
	fn, calls := countingFn(0.005)
	_ = newCache(t, fn, time.Minute)

	// NewCache deve chamar co2Fn uma vez para pré-aquecer.
	if calls.Load() != 1 {
		t.Errorf("esperava 1 chamada no pré-aquecimento, got %d", calls.Load())
	}
}

func TestCache_DoesNotRecalculateBeforeTTL(t *testing.T) {
	fn, calls := countingFn(0.005)
	c := newCache(t, fn, time.Minute) // TTL longo — não vai expirar

	// Múltiplas leituras dentro do TTL não devem chamar co2Fn novamente.
	for range 10 {
		c.SVGAndETag()
	}

	if calls.Load() != 1 {
		t.Errorf("esperava 1 chamada total (só o pré-aquecimento), got %d", calls.Load())
	}
}

func TestCache_RecalculatesAfterTTL(t *testing.T) {
	fn, calls := countingFn(0.005)
	c := newCache(t, fn, 10*time.Millisecond) // TTL muito curto

	time.Sleep(20 * time.Millisecond) // deixa o cache expirar

	c.SVGAndETag() // deve disparar um refresh

	if calls.Load() < 2 {
		t.Errorf("esperava >= 2 chamadas após TTL expirar, got %d", calls.Load())
	}
}

func TestCache_ETagChangesWhenValueChanges(t *testing.T) {
	var value atomic.Int64
	value.Store(1) // 1 = baixo CO₂

	fn := func() float64 {
		// Retorna valores muito diferentes para garantir SVG diferente.
		if value.Load() == 1 {
			return 0.0001 // verde
		}
		return 0.5 // vermelho
	}

	c := newCache(t, fn, 5*time.Millisecond)
	_, etag1 := c.SVGAndETag()

	// Muda o valor e espera o TTL expirar.
	value.Store(2)
	time.Sleep(10 * time.Millisecond)

	_, etag2 := c.SVGAndETag()

	if etag1 == etag2 {
		t.Error("ETag deveria mudar quando o valor de CO₂ muda")
	}
}

func TestCache_ETagStableWhenValueStable(t *testing.T) {
	fn, _ := countingFn(0.005)
	c := newCache(t, fn, time.Minute)

	_, etag1 := c.SVGAndETag()
	_, etag2 := c.SVGAndETag()

	if etag1 != etag2 {
		t.Error("ETag não deve mudar entre leituras dentro do TTL")
	}
}

func TestCache_ConcurrentReads_NoRace(t *testing.T) {
	fn, _ := countingFn(0.005)
	c := newCache(t, fn, 5*time.Millisecond)

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				svg, etag := c.SVGAndETag()
				if svg == "" {
					t.Error("SVG não pode ser vazio")
				}
				if etag == "" {
					t.Error("ETag não pode ser vazio")
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}
	wg.Wait()
}

// --- testes do Handler ------------------------------------------------------

func TestHandler_Returns200WithSVG(t *testing.T) {
	fn, _ := countingFn(0.005)
	c := newCache(t, fn, time.Minute)
	h := badge.Handler(c)

	req := httptest.NewRequest(http.MethodGet, "/badge", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("esperava 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type: got %q, want %q", ct, "image/svg+xml")
	}
	body := rr.Body.String()
	if !strings.HasPrefix(body, "<svg") {
		t.Errorf("body deve começar com <svg, got: %.40s", body)
	}
}

func TestHandler_SetsETagHeader(t *testing.T) {
	fn, _ := countingFn(0.005)
	c := newCache(t, fn, time.Minute)
	h := badge.Handler(c)

	req := httptest.NewRequest(http.MethodGet, "/badge", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Error("ETag header não deve ser vazio")
	}
	// Formato esperado: W/"<hex>"
	if !strings.HasPrefix(etag, `W/"`) {
		t.Errorf("ETag deve ser fraco (W/\"...\"), got %q", etag)
	}
}

func TestHandler_Returns304WhenETagMatches(t *testing.T) {
	fn, calls := countingFn(0.005)
	c := newCache(t, fn, time.Minute)
	h := badge.Handler(c)

	// Primeira request: pega o ETag.
	req1 := httptest.NewRequest(http.MethodGet, "/badge", nil)
	rr1 := httptest.NewRecorder()
	h(rr1, req1)
	etag := rr1.Header().Get("ETag")

	callsAfterFirst := calls.Load()

	// Segunda request com If-None-Match: deve retornar 304 sem body.
	req2 := httptest.NewRequest(http.MethodGet, "/badge", nil)
	req2.Header.Set("If-None-Match", etag)
	rr2 := httptest.NewRecorder()
	h(rr2, req2)

	if rr2.Code != http.StatusNotModified {
		t.Errorf("esperava 304, got %d", rr2.Code)
	}
	if rr2.Body.Len() != 0 {
		t.Errorf("body de 304 deve ser vazio, got %d bytes", rr2.Body.Len())
	}
	// co2Fn não deve ter sido chamada novamente — cache ainda válido.
	if calls.Load() != callsAfterFirst {
		t.Errorf("co2Fn não deve ser chamada num 304, chamadas: %d → %d",
			callsAfterFirst, calls.Load())
	}
}

func TestHandler_Returns200WhenETagDiffers(t *testing.T) {
	fn, _ := countingFn(0.005)
	c := newCache(t, fn, time.Minute)
	h := badge.Handler(c)

	req := httptest.NewRequest(http.MethodGet, "/badge", nil)
	req.Header.Set("If-None-Match", `W/"00000000deadbeef"`) // ETag errado
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("ETag diferente deve retornar 200, got %d", rr.Code)
	}
	if rr.Body.Len() == 0 {
		t.Error("body de 200 não pode ser vazio")
	}
}

func TestHandler_SetsCacheControlHeader(t *testing.T) {
	fn, _ := countingFn(0.005)
	c := newCache(t, fn, 2*time.Minute)
	h := badge.Handler(c)

	req := httptest.NewRequest(http.MethodGet, "/badge", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	cc := rr.Header().Get("Cache-Control")
	if !strings.Contains(cc, "public") {
		t.Errorf("Cache-Control deve conter 'public', got %q", cc)
	}
	if !strings.Contains(cc, "max-age=120") {
		t.Errorf("Cache-Control deve conter 'max-age=120' para TTL de 2min, got %q", cc)
	}
	if !strings.Contains(cc, "stale-while-revalidate") {
		t.Errorf("Cache-Control deve conter 'stale-while-revalidate', got %q", cc)
	}
}

// --- testes de classify -----------------------------------------------------

func TestClassify_Colors(t *testing.T) {
	cases := []struct {
		g     float64
		color string
		desc  string
	}{
		{0.0001, "#44cc11", "excelente (< 0.001)"},
		{0.005, "#97ca00", "bom (< 0.01)"},
		{0.05, "#dfb317", "aceitável (< 0.1)"},
		{0.5, "#e05d44", "investigar (>= 0.1)"},
	}

	// Exercita classify indiretamente via SVGAndETag + conteúdo do SVG.
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			fn := func() float64 { return tc.g }
			c := badge.NewCache(fn, time.Minute)
			svg, _ := c.SVGAndETag()
			if !strings.Contains(svg, tc.color) {
				t.Errorf("SVG para %.4f g deve conter cor %s", tc.g, tc.color)
			}
		})
	}
}

// BenchmarkHandler mede o custo de uma request ao /badge com cache quente.
// Deve ser sub-microsegundo — apenas leitura de string + headers.
func BenchmarkHandler_CacheHit(b *testing.B) {
	fn, _ := countingFn(0.005)
	c := badge.NewCache(fn, time.Minute)
	h := badge.Handler(c)
	req := httptest.NewRequest(http.MethodGet, "/badge", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		rr := httptest.NewRecorder()
		h(rr, req)
	}
}

func BenchmarkHandler_304(b *testing.B) {
	fn, _ := countingFn(0.005)
	c := badge.NewCache(fn, time.Minute)
	h := badge.Handler(c)

	// Pega o ETag real.
	req0 := httptest.NewRequest(http.MethodGet, "/badge", nil)
	rr0 := httptest.NewRecorder()
	h(rr0, req0)
	etag := rr0.Header().Get("ETag")

	req := httptest.NewRequest(http.MethodGet, "/badge", nil)
	req.Header.Set("If-None-Match", etag)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		rr := httptest.NewRecorder()
		h(rr, req)
	}
}
