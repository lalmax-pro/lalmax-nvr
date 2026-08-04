package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/observability"
	"github.com/stretchr/testify/require"
)

func TestAPIObservabilityEndpoint(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	routes := h.Routes()
	h.apiObserver.Aggregator().Observe(observability.Sample{
		Timestamp: time.Now(), Method: http.MethodGet, Route: "/api/capabilities", Status: http.StatusOK,
	})
	rr := httptest.NewRecorder()
	routes.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/observability/api?window=1m&series=1m&limit=10", nil))

	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "no-store", rr.Header().Get("Cache-Control"))
	var response observability.DashboardResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response))
	require.Equal(t, int64(60), response.WindowSeconds)
	require.Equal(t, uint64(1), response.Summary.Requests)
	require.Len(t, response.Routes, 1)
	require.Equal(t, "/api/capabilities", response.Routes[0].Route)
}

func TestAPIObservabilityEndpointRejectsInvalidLimit(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/observability/api?limit=101", nil))
	require.Equal(t, http.StatusBadRequest, rr.Code)
}
