// Package handler contains HTTP handlers.
//
// Handlers are intentionally thin:
//   - Decode the request (path params, query string, body).
//   - Call one service method.
//   - Encode the response.
//
// No business logic lives here. No direct DB access. No cache reads.
// This makes handlers trivially testable with httptest.
package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/yourhandle/green-stack-monitor/internal/domain"
	"github.com/yourhandle/green-stack-monitor/internal/middleware"
)

// EcoMetricsService is the subset of EcoService the handler needs.
// Defining a local interface keeps the handler decoupled from the concrete type.
type EcoMetricsService interface {
	CacheMetrics(ctx context.Context) (domain.CacheMetrics, error)
}

// EcoHandler handles requests to the eco-metrics HTTP API.
type EcoHandler struct {
	svc EcoMetricsService
}

func NewEcoHandler(svc EcoMetricsService) *EcoHandler {
	return &EcoHandler{svc: svc}
}

// GetCacheMetrics handles GET /eco/cache
// Returns current cache hit rate and total CO₂ saved.
func (h *EcoHandler) GetCacheMetrics(w http.ResponseWriter, r *http.Request) {
	m, err := h.svc.CacheMetrics(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not retrieve cache metrics")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"cache_hits":      m.Hits,
		"cache_misses":    m.Misses,
		"hit_rate":        roundTo(m.HitRate(), 4),
		"co2_saved_grams": roundTo(m.TotalSaved, 6),
	})
}

// Health handles GET /healthz — used by load balancers and k8s probes.
func Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Me handles GET /me — example of a protected endpoint that reads JWT claims.
func Me(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromCtx(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no claims in context")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"user_id": claims.UserID,
		"role":    claims.Role,
	})
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func roundTo(f float64, decimals int) float64 {
	pow := 1.0
	for range decimals {
		pow *= 10
	}
	return float64(int(f*pow+0.5)) / pow
}
