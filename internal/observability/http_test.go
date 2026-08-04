package observability

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func TestHTTPObserverUsesChiRoutePattern(t *testing.T) {
	t.Parallel()
	aggregator := NewAggregator()
	observer := NewHTTPObserver(aggregator)
	router := chi.NewRouter()
	router.Use(observer.Middleware)
	router.Get("/api/cameras/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("ok"))
	})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/cameras/secret-camera", nil))

	got := aggregator.Snapshot(time.Minute, time.Minute, 10)
	require.Equal(t, uint64(1), got.Summary.Requests)
	require.Len(t, got.Routes, 1)
	require.Equal(t, "/api/cameras/{id}", got.Routes[0].Route)
	require.Equal(t, uint64(2), got.Summary.ResponseBytes)
}

func TestHTTPObserverExcludesTelemetryAndStreaming(t *testing.T) {
	t.Parallel()
	aggregator := NewAggregator()
	observer := NewHTTPObserver(aggregator)
	handler := observer.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{
		"/api/health",
		"/api/readyz",
		"/api/observability/api",
		"/api/cameras/front-door/stream/ws",
		"/api/gb28181/talk/ws",
		"/metrics",
	} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	got := aggregator.Snapshot(time.Minute, time.Minute, 10)
	require.Zero(t, got.Summary.Requests)
}

func TestRequestScheme(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/api/test", nil)
	req.Header.Set("X-Forwarded-Proto", "HTTPS, http")
	require.Equal(t, "https", requestScheme(req))
}

func TestHTTPObserverRecordsPanicAsServerError(t *testing.T) {
	t.Parallel()
	aggregator := NewAggregator()
	observer := NewHTTPObserver(aggregator)
	handler := observer.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	require.Panics(t, func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/test", nil))
	})
	got := aggregator.Snapshot(time.Minute, time.Minute, 10)
	require.Equal(t, uint64(1), got.Summary.Status5xx)
	require.Len(t, got.RecentErrors, 1)
}
