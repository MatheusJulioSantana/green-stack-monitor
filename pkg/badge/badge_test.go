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

func TestCache_PrewarmsOnCreation(t *testing.T) {
	fn, calls := countingFn(0.005)
	_ = newCache(t, fn, time.Minute)

	if calls.Load() != 1 {
		t.Errorf("expected 1 prewarm call, got %d", calls.Load())
	}
}

func TestCache_DoesNotRecalculateBeforeTTL(t *testing.T) {
	fn, calls := countingFn(0.005)
	c := newCache(t, fn, time.Minute)

	for range 10 {
		c.SVGAndETag()
	}

	if calls.Load() != 1 {
		t.Errorf("expected 1 total call, got %d", calls.Load())
	}
}

func TestCache_RecalculatesAfterTTL(t *testing.T) {
	fn, calls := countingFn(0.005)
	c := newCache(t, fn, 10*time.Millisecond)

	time.Sleep(20 * time.Millisecond)

	c.SVGAndETag()

	if calls.Load() < 2 {
		t.Errorf("expected >= 2 calls after TTL expiry, got %d", calls.Load())
	}
}

func TestCache_ETagChangesWhenValueChanges(t *testing.T) {
	var value atomic.Int64
	value.Store(1)

	fn := func() float64 {
		if value.Load() == 1 {
			return 0.0001
		}
		return 0.5
	}

	c := newCache(t, fn, 5*time.Millisecond)
	_, etag1 := c.SVGAndETag()

	value.Store(2)
	time.Sleep(10 * time.Millisecond)

	_, etag2 := c.SVGAndETag()

	if etag1 == etag2 {
		t.Error("ETag should change when CO2 value changes")
	}
}

func TestCache_ETagStableWhenValueStable(t *testing.T) {
	fn, _ := countingFn(0.005)
	c := newCache(t, fn, time.Minute)

	_, etag1 := c.SVGAndETag()
	_, etag2 := c.SVGAndETag()

	if etag1 != etag2 {
		t.Error("ETag should remain stable within TTL")
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
					t.Error("SVG must not be empty")
				}
				if etag == "" {
					t.Error("ETag must not be empty")
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}
	wg.Wait()
}

func TestHandler_Returns200WithSVG(t *testing.T) {
	fn, _ := countingFn(0.005)
	c := newCache(t, fn, time.Minute)
	h := badge.Handler(c)

	req := httptest.NewRequest(http.MethodGet, "/badge", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type: got %q, want %q", ct, "image/svg+xml")
	}
	body := rr.Body.String()
	if !strings.HasPrefix(body, "<svg") {
		t.Errorf("body should start with <svg, got: %.40s", body)
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
		t.Error("ETag header must not be empty")
	}
	if !strings.HasPrefix(etag, `W/"`) {
		t.Errorf("ETag should be weak (W/\"...\"), got %q", etag)
	}
}

func TestHandler_Returns304WhenETagMatches(t *testing.T) {
	fn, calls := countingFn(0.005)
	c := newCache(t, fn, time.Minute)
	h := badge.Handler(c)

	req1 := httptest.NewRequest(http.MethodGet, "/badge", nil)
	rr1 := httptest.NewRecorder()
	h(rr1, req1)
	etag := rr1.Header().Get("ETag")

	callsAfterFirst := calls.Load()

	req2 := httptest.NewRequest(http.MethodGet, "/badge", nil)
	req2.Header.Set("If-None-Match", etag)
	rr2 := httptest.NewRecorder()
	h(rr2, req2)

	if rr2.Code != http.StatusNotModified {
		t.Errorf("expected 304, got %d", rr2.Code)
	}
	if rr2.Body.Len() != 0 {
		t.Errorf("304 body should be empty, got %d bytes", rr2.Body.Len())
	}
	if calls.Load() != callsAfterFirst {
		t.Errorf("co2Fn should not be called again for 304, calls: %d -> %d",
			callsAfterFirst, calls.Load())
	}
}

func TestHandler_Returns200WhenETagDiffers(t *testing.T) {
	fn, _ := countingFn(0.005)
	c := newCache(t, fn, time.Minute)
	h := badge.Handler(c)

	req := httptest.NewRequest(http.MethodGet, "/badge", nil)
	req.Header.Set("If-None-Match", `W/"00000000deadbeef"`)
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("different ETag should return 200, got %d", rr.Code)
	}
	if rr.Body.Len() == 0 {
		t.Error("200 body must not be empty")
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
		t.Errorf("Cache-Control should contain 'public', got %q", cc)
	}
	if !strings.Contains(cc, "max-age=120") {
		t.Errorf("Cache-Control should contain 'max-age=120' for 2min TTL, got %q", cc)
	}
	if !strings.Contains(cc, "stale-while-revalidate") {
		t.Errorf("Cache-Control should contain 'stale-while-revalidate', got %q", cc)
	}
}

func TestClassify_Colors(t *testing.T) {
	cases := []struct {
		g     float64
		color string
		desc  string
	}{
		{0.0001, "#44cc11", "excellent (< 0.001)"},
		{0.005, "#97ca00", "good (< 0.01)"},
		{0.05, "#dfb317", "fair (< 0.1)"},
		{0.5, "#e05d44", "poor (>= 0.1)"},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			fn := func() float64 { return tc.g }
			c := badge.NewCache(fn, time.Minute)
			svg, _ := c.SVGAndETag()
			if !strings.Contains(svg, tc.color) {
				t.Errorf("SVG for %.4f g should contain color %s", tc.g, tc.color)
			}
		})
	}
}

func BenchmarkHandler_CacheHit(b *testing.B) {
	fn, _ := countingFn(0.005)
	c := badge.NewCache(fn, time.Minute)
	h := badge.Handler(c)
	req := httptest.NewRequest(http.MethodGet, "/badge", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
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
	for range b.N {
		rr := httptest.NewRecorder()
		h(rr, req)
	}
}

func TestClassify_ColorsMatchConstants(t *testing.T) {
	cases := []struct {
		g     float64
		want  string
		label string
	}{
		{0.0001, badge.ColorExcellent, "excellent"},
		{0.005, badge.ColorGood, "good"},
		{0.05, badge.ColorFair, "fair"},
		{0.5, badge.ColorPoor, "poor"},
	}
	for _, tc := range cases {
		_, got := badge.Classify(tc.g)
		if got != tc.want {
			t.Errorf("[%s] Classify(%.4f) = %q, want %q", tc.label, tc.g, got, tc.want)
		}
	}
}

func TestClassify_LabelFormats(t *testing.T) {
	cases := []struct {
		g          float64
		wantSuffix string
		wantPrec   string
	}{
		{0.0001, "g CO₂/req", "%.4f"},
		{0.005, "g CO₂/req", "%.3f"},
		{0.05, "g CO₂/req", "%.2f"},
		{0.5, "g CO₂/req", "%.2f"},
	}
	for _, tc := range cases {
		label, _ := badge.Classify(tc.g)
		if !strings.Contains(label, tc.wantSuffix) {
			t.Errorf("Classify(%.4f) label %q does not contain suffix %q", tc.g, label, tc.wantSuffix)
		}
	}
}
