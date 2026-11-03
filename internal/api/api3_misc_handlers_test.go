package api

// api3_* helpers and tests for miscellaneous API handlers:
// timelapse config, merge settings, map tiles, observability, transcode jobs,
// database migration settings, fMP4 proxying, the stream registry and Emby.
//
// Only new test code lives here; no production or pre-existing test file is modified.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/emby"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/merge"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/recorder"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi"
	"github.com/stretchr/testify/require"
)

// api3Router builds a chi router with the supplied route registrations.
func api3Router(register func(r chi.Router)) http.Handler {
	r := chi.NewRouter()
	register(r)
	return r
}

// api3Request serves one request through the handler and returns the recorder.
func api3Request(t *testing.T, handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// api3Status asserts the response status code, printing the body on failure.
func api3Status(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	require.Equal(t, want, rr.Code, rr.Body.String())
}

// api3JSON decodes the response body into a generic map.
func api3JSON(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &decoded), rr.Body.String())
	return decoded
}

// api3TempConfigPath writes cfg to a temporary YAML file and returns its path.
func api3TempConfigPath(t *testing.T, cfg *config.Config) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, config.Save(path, cfg))
	return path
}

// api3ClosedDB returns an initialized SQLite handle that is already closed, so
// storage calls fail deterministically.
func api3ClosedDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.New(filepath.Join(t.TempDir(), "closed.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	require.NoError(t, db.Close())
	return db
}

// api3RepoWrapper hides optional capabilities (such as MigrateTo) from handlers.
type api3RepoWrapper struct{ storage.APIRepository }

func api3SeedCameraRow(t *testing.T, db *storage.DB, id, name string) {
	t.Helper()
	require.NoError(t, db.UpsertCamera(context.Background(), id, name, "rtsp_h264", "h264", "rtsp://192.0.2.10/live", "user", "secret", true, "", "", ""))
}

// --- timelapse configuration ---

func TestApi3TimelapseEndpoints(t *testing.T) {
	db, store := setupTestDB(t)
	api3SeedCameraRow(t, db, "cam-1", "Front Door")
	cfg := &config.Config{Cameras: []config.CameraConfig{{ID: "cam-1", Name: "Front Door", Enabled: true}}}
	cfgPath := api3TempConfigPath(t, cfg)
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, cfgPath, nil, nil)
	router := api3Router(func(r chi.Router) {
		r.Get("/cameras/{id}/timelapse", h.handleGetCameraTimelapse)
		r.Put("/cameras/{id}/timelapse", h.handlePutCameraTimelapse)
	})

	// No timelapse configured yet: the endpoint reports disabled defaults.
	rr := api3Request(t, router, http.MethodGet, "/cameras/cam-1/timelapse", "")
	api3Status(t, rr, http.StatusOK)
	body := api3JSON(t, rr)
	require.Equal(t, false, body["enabled"])
	require.Equal(t, "30s", body["interval"])

	// Unknown camera also returns defaults rather than an error.
	api3Status(t, api3Request(t, router, http.MethodGet, "/cameras/ghost/timelapse", ""), http.StatusOK)

	// Missing config and missing config path are hard errors.
	noCfg := api3Router(func(r chi.Router) {
		r.Get("/cameras/{id}/timelapse", (&Handler{}).handleGetCameraTimelapse)
		r.Put("/cameras/{id}/timelapse", (&Handler{}).handlePutCameraTimelapse)
	})
	api3Status(t, api3Request(t, noCfg, http.MethodGet, "/cameras/cam-1/timelapse", ""), http.StatusInternalServerError)
	api3Status(t, api3Request(t, noCfg, http.MethodPut, "/cameras/cam-1/timelapse", `{}`), http.StatusInternalServerError)

	noPath := &Handler{db: db, config: cfg}
	api3Status(t, api3Request(t, api3Router(func(r chi.Router) {
		r.Put("/cameras/{id}/timelapse", noPath.handlePutCameraTimelapse)
	}), http.MethodPut, "/cameras/cam-1/timelapse", `{}`), http.StatusInternalServerError)

	// Validation failures.
	for _, body := range []string{
		`{`,
		`{"interval":"abc"}`,
		`{"interval":"500ms"}`,
		`{"output_fps":61}`,
		`{"output_fps":-1}`,
		`{"video_codec":"vp9"}`,
	} {
		api3Status(t, api3Request(t, router, http.MethodPut, "/cameras/cam-1/timelapse", body), http.StatusBadRequest)
	}

	// Camera missing from the in-memory config.
	api3Status(t, api3Request(t, router, http.MethodPut, "/cameras/ghost/timelapse", `{"enabled":true}`), http.StatusNotFound)

	// Database unavailable.
	noDB := &Handler{config: cfg, configPath: cfgPath}
	api3Status(t, api3Request(t, api3Router(func(r chi.Router) {
		r.Put("/cameras/{id}/timelapse", noDB.handlePutCameraTimelapse)
	}), http.MethodPut, "/cameras/cam-1/timelapse", `{"enabled":true}`), http.StatusInternalServerError)

	// Database write failure.
	brokenDB := &Handler{db: api3ClosedDB(t), config: cfg, configPath: cfgPath}
	api3Status(t, api3Request(t, api3Router(func(r chi.Router) {
		r.Put("/cameras/{id}/timelapse", brokenDB.handlePutCameraTimelapse)
	}), http.MethodPut, "/cameras/cam-1/timelapse", `{"enabled":true}`), http.StatusInternalServerError)

	// Successful update with explicit values.
	rr = api3Request(t, router, http.MethodPut, "/cameras/cam-1/timelapse", `{"enabled":true,"interval":"5s","output_fps":15,"video_codec":"h265","delete_original":true}`)
	api3Status(t, rr, http.StatusOK)
	api3Status(t, api3Request(t, router, http.MethodGet, "/cameras/cam-1/timelapse", ""), http.StatusOK)
	require.NotNil(t, cfg.Cameras[0].Timelapse)
	require.Equal(t, "5s", cfg.Cameras[0].Timelapse.Interval)
	require.Equal(t, 15, cfg.Cameras[0].Timelapse.OutputFPS)

	// Zero-value fields are filled with defaults on update.
	api3Status(t, api3Request(t, router, http.MethodPut, "/cameras/cam-1/timelapse", `{"enabled":true}`), http.StatusOK)
	require.Equal(t, "30s", cfg.Cameras[0].Timelapse.Interval)
	require.Equal(t, 30, cfg.Cameras[0].Timelapse.OutputFPS)
	require.Equal(t, "h264", cfg.Cameras[0].Timelapse.VideoCodec)
}

// --- global merge settings ---

func TestApi3MergeSettingsEndpoints(t *testing.T) {
	db, store := setupTestDB(t)
	cfg := &config.Config{}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, cfgPath, nil, nil)
	router := api3Router(func(r chi.Router) {
		r.Get("/settings/merge", h.handleGetMergeSettings)
		r.Put("/settings/merge", h.handleUpdateMergeSettings)
	})

	rr := api3Request(t, router, http.MethodGet, "/settings/merge", "")
	api3Status(t, rr, http.StatusOK)
	require.Contains(t, api3JSON(t, rr), "rolling_enabled")

	noCfg := api3Router(func(r chi.Router) {
		r.Get("/settings/merge", (&Handler{}).handleGetMergeSettings)
		r.Put("/settings/merge", (&Handler{}).handleUpdateMergeSettings)
	})
	api3Status(t, api3Request(t, noCfg, http.MethodGet, "/settings/merge", ""), http.StatusInternalServerError)
	api3Status(t, api3Request(t, noCfg, http.MethodPut, "/settings/merge", `{}`), http.StatusInternalServerError)

	for _, body := range []string{
		`{`,
		`{"check_interval":"bad"}`,
		`{"window_size":"bad"}`,
		`{"batch_limit":0}`,
		`{"min_segment_age":"bad"}`,
		`{"min_segments_to_merge":0}`,
		`{"rolling_debounce":"bad"}`,
	} {
		api3Status(t, api3Request(t, router, http.MethodPut, "/settings/merge", body), http.StatusBadRequest)
	}

	rr = api3Request(t, router, http.MethodPut, "/settings/merge",
		`{"enabled":true,"check_interval":"30m","window_size":"48h","batch_limit":5,"min_segment_age":"1h","min_segments_to_merge":2,"rolling_enabled":true,"rolling_debounce":"5s"}`)
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, true, cfg.Merge.Enabled)
	require.Equal(t, "30m", cfg.Merge.CheckInterval)
	require.Equal(t, "48h", cfg.Merge.WindowSize)
	require.Equal(t, 5, cfg.Merge.BatchLimit)
	require.Equal(t, "1h", cfg.Merge.MinSegmentAge)
	require.Equal(t, 2, cfg.Merge.MinSegmentsToMerge)
	require.NotNil(t, cfg.Merge.RollingEnabled)
	require.True(t, *cfg.Merge.RollingEnabled)
	require.Equal(t, "5s", cfg.Merge.RollingDebounce)

	// The save path tolerates an empty config path (logs a warning, still 200).
	bare := NewHandler(db, store, noopAuthMW(), cfg, nil, "", nil, nil)
	api3Status(t, api3Request(t, api3Router(func(r chi.Router) {
		r.Put("/settings/merge", bare.handleUpdateMergeSettings)
	}), http.MethodPut, "/settings/merge", `{"enabled":false}`), http.StatusOK)
}

// --- per-camera merge overrides ---

func TestApi3CameraMergeConfigEndpoints(t *testing.T) {
	db, store := setupTestDB(t)
	api3SeedCameraRow(t, db, "cam-1", "Front Door")
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	router := api3Router(func(r chi.Router) {
		r.Get("/cameras/{id}/merge-config", h.handleGetCameraMergeConfig)
		r.Put("/cameras/{id}/merge-config", h.handleUpdateCameraMergeConfig)
		r.Delete("/cameras/{id}/merge-config", h.handleDeleteCameraMergeConfig)
	})

	noDB := api3Router(func(r chi.Router) {
		r.Get("/cameras/{id}/merge-config", (&Handler{}).handleGetCameraMergeConfig)
		r.Put("/cameras/{id}/merge-config", (&Handler{}).handleUpdateCameraMergeConfig)
		r.Delete("/cameras/{id}/merge-config", (&Handler{}).handleDeleteCameraMergeConfig)
	})
	api3Status(t, api3Request(t, noDB, http.MethodGet, "/cameras/cam-1/merge-config", ""), http.StatusInternalServerError)
	api3Status(t, api3Request(t, noDB, http.MethodPut, "/cameras/cam-1/merge-config", `{}`), http.StatusInternalServerError)
	api3Status(t, api3Request(t, noDB, http.MethodDelete, "/cameras/cam-1/merge-config", ""), http.StatusInternalServerError)

	// Unknown camera and camera without overrides both report 404.
	api3Status(t, api3Request(t, router, http.MethodGet, "/cameras/ghost/merge-config", ""), http.StatusNotFound)
	api3Status(t, api3Request(t, router, http.MethodGet, "/cameras/cam-1/merge-config", ""), http.StatusNotFound)

	// Validation failures on update.
	for _, body := range []string{
		`{`,
		`{"check_interval":"bad"}`,
		`{"window_size":"bad"}`,
		`{"min_segment_age":"bad"}`,
		`{"rolling_debounce":"bad"}`,
		`{"batch_limit":0}`,
		`{"min_segments_to_merge":0}`,
	} {
		api3Status(t, api3Request(t, router, http.MethodPut, "/cameras/cam-1/merge-config", body), http.StatusBadRequest)
	}

	// Route without an id parameter exercises the empty-id validation.
	noParam := api3Router(func(r chi.Router) {
		r.Put("/merge-config", h.handleUpdateCameraMergeConfig)
		r.Delete("/merge-config", h.handleDeleteCameraMergeConfig)
	})
	api3Status(t, api3Request(t, noParam, http.MethodPut, "/merge-config", `{}`), http.StatusBadRequest)
	api3Status(t, api3Request(t, noParam, http.MethodDelete, "/merge-config", ""), http.StatusBadRequest)

	rr := api3Request(t, router, http.MethodPut, "/cameras/cam-1/merge-config",
		`{"enabled":true,"check_interval":"1h","window_size":"24h","batch_limit":3,"min_segment_age":"2h","min_segments_to_merge":2,"rolling_enabled":true,"rolling_debounce":"10s"}`)
	api3Status(t, rr, http.StatusOK)

	rr = api3Request(t, router, http.MethodGet, "/cameras/cam-1/merge-config", "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, true, api3JSON(t, rr)["enabled"])

	api3Status(t, api3Request(t, router, http.MethodDelete, "/cameras/cam-1/merge-config", ""), http.StatusOK)
	// Clearing passes nils, which COALESCE keeps at their previous values.
	api3Status(t, api3Request(t, router, http.MethodGet, "/cameras/cam-1/merge-config", ""), http.StatusOK)
}

func TestApi3MergeStatusEndpoints(t *testing.T) {
	disabled := api3Router(func(r chi.Router) {
		r.Get("/merge/status", (&Handler{}).handleMergeStatus)
		r.Get("/merge/pending", (&Handler{}).handleMergePending)
	})
	rr := api3Request(t, disabled, http.MethodGet, "/merge/status", "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, false, api3JSON(t, rr)["enabled"])
	rr = api3Request(t, disabled, http.MethodGet, "/merge/pending", "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, false, api3JSON(t, rr)["enabled"])

	db, store := setupTestDB(t)
	mgr := merge.NewMergeManager(db, store,
		func() config.MergeConfig { return config.MergeConfig{MinSegmentAge: "10m", WindowSize: "24h"} },
		func(string) *config.MergeConfig { return nil },
		func() []config.CameraConfig { return nil },
	)
	h := &Handler{db: db, mergeMgr: mgr}
	router := api3Router(func(r chi.Router) {
		r.Get("/merge/status", h.handleMergeStatus)
		r.Get("/merge/pending", h.handleMergePending)
	})
	rr = api3Request(t, router, http.MethodGet, "/merge/status", "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, true, api3JSON(t, rr)["enabled"])
	rr = api3Request(t, router, http.MethodGet, "/merge/pending", "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, true, api3JSON(t, rr)["enabled"])
}

// --- map tiles (deterministic, no external network) ---

type api3RoundTrip func(*http.Request) (*http.Response, error)

func (f api3RoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// api3InstallMapTransport forces the shared tile client to a stub transport.
// mapHTTPClient() only builds the client (it never dials), so calling it first
// makes the sync.Once deterministic before the client is swapped.
func api3InstallMapTransport(t *testing.T, fn http.RoundTripper) {
	t.Helper()
	_ = mapHTTPClient()
	previous := mapTileClient
	mapTileClient = &http.Client{Transport: fn}
	t.Cleanup(func() { mapTileClient = previous })
}

func api3ResetMapCache() {
	mapTileMu.Lock()
	mapTileCache = make(map[string]mapTileEntry, mapTileCacheN)
	mapTileOrder = nil
	mapTileMu.Unlock()
}

func api3TileResponse(body []byte, contentType string) *http.Response {
	header := http.Header{}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(string(body)))}
}

func TestApi3MapTiles(t *testing.T) {
	api3ResetMapCache()
	t.Cleanup(api3ResetMapCache)
	handler := &Handler{}
	router := api3Router(func(r chi.Router) { r.Get("/map/tiles/{z}/{x}/{y}.png", handler.handleMapTile) })

	// Invalid coordinates.
	for _, target := range []string{
		"/map/tiles/19/0/0.png",
		"/map/tiles/a/0/0.png",
		"/map/tiles/0/-1/0.png",
		"/map/tiles/1/2/0.png",
		"/map/tiles/1/0/2.png",
	} {
		api3Status(t, api3Request(t, router, http.MethodGet, target, ""), http.StatusBadRequest)
	}

	// Cached tiles are served without touching the network.
	cachePutTile("2/1/1", mapTileEntry{body: []byte(strings.Repeat("x", 64)), ctype: "image/webp", fetched: time.Now()})
	rr := api3Request(t, router, http.MethodGet, "/map/tiles/2/1/1.png", "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, "image/webp", rr.Header().Get("Content-Type"))
	require.Len(t, rr.Body.Bytes(), 64)

	// Upstream success (with an empty content type falling back to image/png).
	api3InstallMapTransport(t, api3RoundTrip(func(*http.Request) (*http.Response, error) {
		return api3TileResponse([]byte(strings.Repeat("y", 128)), ""), nil
	}))
	api3Status(t, api3Request(t, router, http.MethodGet, "/map/tiles/3/1/1.png", ""), http.StatusOK)
	require.Equal(t, "image/png", api3Request(t, router, http.MethodGet, "/map/tiles/3/1/1.png", "").Header().Get("Content-Type"))

	// Upstream transport failures exhaust every source and surface 502.
	api3InstallMapTransport(t, api3RoundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("tile source unreachable")
	}))
	api3Status(t, api3Request(t, router, http.MethodGet, "/map/tiles/4/1/1.png", ""), http.StatusBadGateway)

	// Too-small payloads and non-200 statuses are rejected as well.
	api3InstallMapTransport(t, api3RoundTrip(func(*http.Request) (*http.Response, error) {
		return api3TileResponse([]byte("tiny"), "image/png"), nil
	}))
	api3Status(t, api3Request(t, router, http.MethodGet, "/map/tiles/4/2/2.png", ""), http.StatusBadGateway)
	api3InstallMapTransport(t, api3RoundTrip(func(*http.Request) (*http.Response, error) {
		response := api3TileResponse([]byte(strings.Repeat("z", 64)), "image/png")
		response.StatusCode = http.StatusInternalServerError
		return response, nil
	}))
	api3Status(t, api3Request(t, router, http.MethodGet, "/map/tiles/4/3/3.png", ""), http.StatusBadGateway)

	// A request-construction failure falls through to the next source.
	_, _, err := fetchMapTile(6, 1, 1)
	require.Error(t, err)

	// The bounded cache evicts the oldest entry once it is full.
	api3ResetMapCache()
	for i := 0; i < mapTileCacheN; i++ {
		cachePutTile("fill-"+strings.Repeat("a", i%3)+string(rune('a'+i%26))+strings.Repeat("b", i/26), mapTileEntry{ctype: "image/png"})
	}
	cachePutTile("overflow-key", mapTileEntry{ctype: "image/png"})
	require.Len(t, mapTileCache, mapTileCacheN)
	_, ok := cacheGetTile("overflow-key")
	require.True(t, ok)
	// Re-putting an existing key must not grow the LRU order.
	cachePutTile("overflow-key", mapTileEntry{ctype: "image/png"})
	require.Len(t, mapTileOrder, mapTileCacheN)
	require.NotEmpty(t, mapTileSources(1, 0, 0))
}

// --- observability ---

func TestApi3ObservabilityEndpoints(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	cfg := &config.Config{Observability: config.ObservabilityConfig{TraceUIURLTemplate: "https://trace.example/{trace_id}"}}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, "", nil, nil)
	router := api3Router(func(r chi.Router) {
		r.Get("/observability/api", h.handleAPIObservability)
		r.Get("/observability/traces", h.handleListObservabilityTraces)
		r.Get("/observability/traces/{traceID}", h.handleGetObservabilityTrace)
	})

	for _, window := range []string{"1m", "5m", "15m", "30m", "1h", "bogus"} {
		rr := api3Request(t, router, http.MethodGet, "/observability/api?window="+window+"&series=1m&limit=5", "")
		api3Status(t, rr, http.StatusOK)
	}
	for _, limit := range []string{"0", "101", "abc"} {
		api3Status(t, api3Request(t, router, http.MethodGet, "/observability/api?limit="+limit, ""), http.StatusBadRequest)
	}

	// Persisted traces drive the list and detail endpoints.
	traceID := strings.Repeat("ab", 16)
	require.NoError(t, db.StoreTraceSpan(ctx, storage.TraceSpanRecord{
		TraceID: traceID, SpanID: "span-1", Name: "api.request", Service: "api", Kind: "server",
		Status: "ok", StatusCode: 200, StartUnixNS: time.Now().Add(-time.Minute).UnixNano(),
		EndUnixNS: time.Now().UnixNano(), StartTime: time.Now().Add(-time.Minute).UnixNano(), EndTime: time.Now().UnixNano(),
	}))
	rr := api3Request(t, router, http.MethodGet, "/observability/traces", "")
	api3Status(t, rr, http.StatusOK)
	body := api3JSON(t, rr)
	require.Equal(t, "https://trace.example/{trace_id}", body["external_trace_url_template"])
	require.NotEmpty(t, body["traces"])

	api3Status(t, api3Request(t, router, http.MethodGet, "/observability/traces?limit=0", ""), http.StatusOK)
	api3Status(t, api3Request(t, router, http.MethodGet, "/observability/traces?limit=201", ""), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodGet, "/observability/traces?offset=-1", ""), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodGet, "/observability/traces?offset=abc", ""), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodGet, "/observability/traces?limit=5&offset=0", ""), http.StatusOK)

	api3Status(t, api3Request(t, router, http.MethodGet, "/observability/traces/not-a-trace-id", ""), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodGet, "/observability/traces/"+strings.Repeat("cd", 16), ""), http.StatusNotFound)
	rr = api3Request(t, router, http.MethodGet, "/observability/traces/"+strings.ToUpper(traceID), "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, traceID, api3JSON(t, rr)["trace_id"])

	// Storage without trace support fails closed.
	noRepo := api3Router(func(r chi.Router) {
		r.Get("/observability/traces", (&Handler{}).handleListObservabilityTraces)
		r.Get("/observability/traces/{traceID}", (&Handler{}).handleGetObservabilityTrace)
	})
	api3Status(t, api3Request(t, noRepo, http.MethodGet, "/observability/traces", ""), http.StatusServiceUnavailable)
	api3Status(t, api3Request(t, noRepo, http.MethodGet, "/observability/traces/"+traceID, ""), http.StatusServiceUnavailable)

	// Failing storage surfaces 500 for both list and detail.
	broken := NewHandler(api3ClosedDB(t), store, noopAuthMW(), cfg, nil, "", nil, nil)
	brokenRouter := api3Router(func(r chi.Router) {
		r.Get("/observability/traces", broken.handleListObservabilityTraces)
		r.Get("/observability/traces/{traceID}", broken.handleGetObservabilityTrace)
	})
	api3Status(t, api3Request(t, brokenRouter, http.MethodGet, "/observability/traces", ""), http.StatusInternalServerError)
	api3Status(t, api3Request(t, brokenRouter, http.MethodGet, "/observability/traces/"+traceID, ""), http.StatusInternalServerError)
}

// --- transcode jobs ---

func api3TranscodeRouter(h *Handler) http.Handler {
	return api3Router(func(r chi.Router) {
		r.Get("/jobs", h.handleTranscodeJobs)
		r.Post("/jobs", h.handleSaveTranscodeJob)
		r.Patch("/jobs/{id}", h.handleSaveTranscodeJob)
		r.Post("/jobs/{id}/start", h.handleStartTranscodeJob)
		r.Post("/jobs/{id}/stop", h.handleStopTranscodeJob)
		r.Delete("/jobs/{id}", h.handleDeleteTranscodeJob)
		r.Get("/streams/{stream_id}/variants", h.handleTranscodeVariants)
		r.Get("/streams/{stream_id}/jobs/{job_id}/play", h.handleTranscodePlayURL)
	})
}

func TestApi3TranscodeJobsLifecycle(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	profile := transcode.DefaultProfile("ffmpeg")
	profile.Revision = 0
	savedProfile, err := db.SaveTranscodeProfile(ctx, profile, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, savedProfile.Revision)

	engine := &stubMediaEngine{streamsByID: map[string]*media.StreamInfo{
		"cam-1": {StreamID: "cam-1", AppName: "live", Active: true, VideoCodec: "H264"},
	}}
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)
	h.SetTranscodeService(transcode.NewService(db, nil, nil, transcode.Config{}))
	router := api3TranscodeRouter(h)

	jobBody := func(id string, generation uint64, enabled bool) string {
		payload := map[string]any{
			"id": id,
			"source": map[string]any{
				"engine_id": "embedded",
				"app_name":  "live",
				"stream_id": "cam-1",
			},
			"profile_id":       savedProfile.ID,
			"profile_revision": savedProfile.Revision,
			"enabled":          enabled,
			"run_policy":       "manual",
		}
		if generation != 0 {
			payload["generation"] = generation
		}
		encoded, err := json.Marshal(payload)
		require.NoError(t, err)
		return string(encoded)
	}

	// Invalid bodies and identity mismatches.
	api3Status(t, api3Request(t, router, http.MethodPost, "/jobs", `{`), http.StatusBadRequest)
	badGeneration := jobBody("job-badgen", 4, false)
	api3Status(t, api3Request(t, router, http.MethodPost, "/jobs", badGeneration), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodPatch, "/jobs/job-x", jobBody("job-x", 0, false)), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodPatch, "/jobs/job-x", jobBody("other-id", 1, false)), http.StatusBadRequest)

	externalEngine := strings.Replace(jobBody("job-ext", 0, false), `"engine_id":"embedded"`, `"engine_id":"external"`, 1)
	api3Status(t, api3Request(t, router, http.MethodPost, "/jobs", externalEngine), http.StatusBadRequest)

	// No media engine configured.
	noEngine := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	noEngine.SetTranscodeService(transcode.NewService(db, nil, nil, transcode.Config{}))
	api3Status(t, api3Request(t, api3TranscodeRouter(noEngine), http.MethodPost, "/jobs", jobBody("job-noengine", 0, false)), http.StatusBadRequest)

	// External media engine mode is rejected.
	externalCfg := &config.Config{Media: config.MediaConfig{Mode: "http"}}
	externalH := NewHandler(db, store, noopAuthMW(), externalCfg, nil, "", nil, nil)
	externalH.SetMediaEngine(engine)
	externalH.SetTranscodeService(transcode.NewService(db, nil, nil, transcode.Config{}))
	api3Status(t, api3Request(t, api3TranscodeRouter(externalH), http.MethodPost, "/jobs", jobBody("job-extmode", 0, false)), http.StatusBadRequest)

	// Unknown source stream and mismatched application.
	missingSource := strings.Replace(jobBody("job-missing", 0, false), `"stream_id":"cam-1"`, `"stream_id":"ghost"`, 1)
	api3Status(t, api3Request(t, router, http.MethodPost, "/jobs", missingSource), http.StatusBadRequest)
	mismatchApp := strings.Replace(jobBody("job-app", 0, false), `"app_name":"live"`, `"app_name":"other"`, 1)
	api3Status(t, api3Request(t, router, http.MethodPost, "/jobs", mismatchApp), http.StatusBadRequest)

	// Unknown profile revision is rejected by the service.
	badProfile := strings.Replace(jobBody("job-profile", 0, false), `"profile_revision":1`, `"profile_revision":99`, 1)
	api3Status(t, api3Request(t, router, http.MethodPost, "/jobs", badProfile), http.StatusNotFound)

	// Successful creation, list, update, start failure, stop and delete.
	rr := api3Request(t, router, http.MethodPost, "/jobs", jobBody("job-1", 0, false))
	api3Status(t, rr, http.StatusCreated)
	var created transcode.Job
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	require.EqualValues(t, 1, created.Generation)

	rr = api3Request(t, router, http.MethodGet, "/jobs", "")
	api3Status(t, rr, http.StatusOK)
	jobsBody := api3JSON(t, rr)
	require.Equal(t, true, jobsBody["runtime_available"])
	require.NotEmpty(t, jobsBody["jobs"])
	require.NotEmpty(t, jobsBody["variants"])

	rr = api3Request(t, router, http.MethodPatch, "/jobs/job-1", jobBody("job-1", created.Generation, false))
	api3Status(t, rr, http.StatusOK)
	var updated transcode.Job
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &updated))
	require.EqualValues(t, 2, updated.Generation)

	// Transcoding is disabled in the runtime config, so start fails closed.
	api3Status(t, api3Request(t, router, http.MethodPost, "/jobs/job-1/start?generation=2", ""), http.StatusPreconditionFailed)
	api3Status(t, api3Request(t, router, http.MethodPost, "/jobs/job-1/start", ""), http.StatusBadRequest)

	api3Status(t, api3Request(t, router, http.MethodPost, "/jobs/job-1/stop?generation=2", ""), http.StatusNoContent)
	api3Status(t, api3Request(t, router, http.MethodPost, "/jobs/job-1/stop?generation=99", ""), http.StatusConflict)
	api3Status(t, api3Request(t, router, http.MethodDelete, "/jobs/job-1?generation=2", ""), http.StatusNoContent)
	api3Status(t, api3Request(t, router, http.MethodDelete, "/jobs/job-1?generation=2", ""), http.StatusNotFound)

	// Variants listing for a stream.
	rr = api3Request(t, router, http.MethodGet, "/streams/cam-1/variants", "")
	api3Status(t, rr, http.StatusOK)
	require.Contains(t, api3JSON(t, rr), "variants")

	// Runtime with probes enabled but no backend reports 503 on start.
	enabledSvc := transcode.NewService(db, nil, nil, transcode.Config{Enabled: true})
	enabledH := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	enabledH.SetMediaEngine(engine)
	enabledH.SetTranscodeService(enabledSvc)
	enabledRouter := api3TranscodeRouter(enabledH)
	_, err = db.SaveTranscodeJob(ctx, transcode.Job{
		ID: "job-2", Source: transcode.SourceRef{EngineID: "embedded", StreamID: "cam-1", AppName: "live"},
		ProfileID: savedProfile.ID, ProfileRevision: savedProfile.Revision, RunPolicy: transcode.Manual,
	}, 0)
	require.NoError(t, err)
	api3Status(t, api3Request(t, enabledRouter, http.MethodPost, "/jobs/job-2/start?generation=1", ""), http.StatusServiceUnavailable)

	// Unconfigured runtime fails closed for the mutation handlers.
	unconfigured := api3TranscodeRouter(NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil))
	// The job list still works without a runtime; it reports runtime_available=false.
	rr = api3Request(t, unconfigured, http.MethodGet, "/jobs", "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, false, api3JSON(t, rr)["runtime_available"])
	for _, call := range []struct {
		method string
		target string
		body   string
	}{
		{http.MethodPost, "/jobs", jobBody("job-none", 0, false)},
		{http.MethodPost, "/jobs/job-none/start?generation=1", ""},
		{http.MethodPost, "/jobs/job-none/stop?generation=1", ""},
		{http.MethodDelete, "/jobs/job-none?generation=1", ""},
		{http.MethodGet, "/streams/cam-1/variants", ""},
		{http.MethodGet, "/streams/cam-1/jobs/job-1/play", ""},
	} {
		api3Status(t, api3Request(t, unconfigured, call.method, call.target, call.body), http.StatusServiceUnavailable)
	}
}

type api3DerivedPlayback struct {
	url    string
	active bool
	err    error
}

func (p *api3DerivedPlayback) DerivedPlayURL(_ context.Context, _ transcode.SourceRef, outputID, protocol string) (*media.PlayURL, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &media.PlayURL{URL: p.url + "/" + protocol + "/" + outputID, Protocol: protocol}, nil
}

func TestApi3TranscodeDerivedPlayback(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	profile := transcode.DefaultProfile("ffmpeg")
	savedProfile, err := db.SaveTranscodeProfile(ctx, profile, 0)
	require.NoError(t, err)
	_, err = db.SaveTranscodeJob(ctx, transcode.Job{
		ID: "job-1", Source: transcode.SourceRef{EngineID: "embedded", StreamID: "cam-1", AppName: "live"},
		ProfileID: savedProfile.ID, ProfileRevision: savedProfile.Revision, RunPolicy: transcode.Manual,
	}, 0)
	require.NoError(t, err)

	engine := &stubMediaEngine{streamsByID: map[string]*media.StreamInfo{
		"nvr_tc_job-1": {StreamID: "nvr_tc_job-1", AppName: "live", Active: true},
	}}
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)
	h.SetTranscodeService(transcode.NewService(db, nil, nil, transcode.Config{}))
	router := api3TranscodeRouter(h)

	// Only RTSP is exposed.
	api3Status(t, api3Request(t, router, http.MethodGet, "/streams/cam-1/jobs/job-1/play?protocol=hls", ""), http.StatusBadRequest)
	// No derived playback provider configured yet.
	api3Status(t, api3Request(t, router, http.MethodGet, "/streams/cam-1/jobs/job-1/play", ""), http.StatusServiceUnavailable)

	// A disabled job is reported as an unavailable output before any provider call.
	h.SetTranscodePlayback(&api3DerivedPlayback{url: "rtsp://127.0.0.1:15544"})
	api3Status(t, api3Request(t, router, http.MethodGet, "/streams/cam-1/jobs/job-1/play", ""), http.StatusBadRequest)

	// Unknown jobs keep their domain status.
	api3Status(t, api3Request(t, router, http.MethodGet, "/streams/cam-1/jobs/missing/play", ""), http.StatusNotFound)
}

// --- database migration settings ---

func TestApi3DatabaseMigrationSettings(t *testing.T) {
	db, store := setupTestDB(t)
	sqliteDSN := filepath.Join(t.TempDir(), "target.db")
	cfg := &config.Config{}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, cfgPath, nil, nil)
	router := api3Router(func(r chi.Router) {
		r.Get("/db/migration", h.handleGetDatabaseMigrationSettings)
		r.Put("/db/migration", h.handleUpdateDatabaseMigrationSettings)
		r.Post("/db/migration/test", h.handleTestDatabaseMigrationTarget)
		r.Post("/db/migration/run", h.handleRunDatabaseMigration)
	})

	rr := api3Request(t, router, http.MethodGet, "/db/migration", "")
	api3Status(t, rr, http.StatusOK)
	body := api3JSON(t, rr)
	require.Equal(t, false, body["migration_available"])
	require.Equal(t, false, body["target_configured"])

	// A configured target makes migration available.
	cfg.Storage.MigrationTarget = &config.DatabaseTargetConfig{Driver: string(storage.DriverPostgres), DSN: "postgres://127.0.0.1:1/nvr?sslmode=disable"}
	rr = api3Request(t, router, http.MethodGet, "/db/migration", "")
	api3Status(t, rr, http.StatusOK)
	body = api3JSON(t, rr)
	require.Equal(t, true, body["migration_available"])
	require.Equal(t, true, body["target_configured"])
	require.Equal(t, string(storage.DriverPostgres), body["target_driver"])

	// A non-SQLite source changes the availability reason.
	cfg.Storage.DatabaseDriver = string(storage.DriverPostgres)
	rr = api3Request(t, router, http.MethodGet, "/db/migration", "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, false, api3JSON(t, rr)["migration_available"])
	cfg.Storage.DatabaseDriver = ""

	// Update validation.
	api3Status(t, api3Request(t, router, http.MethodPut, "/db/migration", `{`), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodPut, "/db/migration", `{"driver":"oracle","dsn":"x"}`), http.StatusBadRequest)
	cfg.Storage.MigrationTarget = nil
	api3Status(t, api3Request(t, router, http.MethodPut, "/db/migration", `{"driver":"sqlite"}`), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodPut, "/db/migration", `{"driver":"postgres","dsn":"postgres://127.0.0.1:1/nvr?sslmode=disable"}`), http.StatusBadRequest)

	// Empty DSN reuses the previously configured target.
	cfg.Storage.MigrationTarget = &config.DatabaseTargetConfig{Driver: string(storage.DriverSQLite), DSN: sqliteDSN}
	rr = api3Request(t, router, http.MethodPut, "/db/migration", `{"driver":"sqlite"}`)
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, "saved", api3JSON(t, rr)["status"])
	require.Equal(t, sqliteDSN, cfg.Storage.MigrationTarget.DSN)

	// Successful explicit target.
	rr = api3Request(t, router, http.MethodPut, "/db/migration", `{"driver":"sqlite","dsn":"`+sqliteDSN+`"}`)
	api3Status(t, rr, http.StatusOK)

	// Save failure is reported when no config path is available.
	noSave := NewHandler(db, store, noopAuthMW(), cfg, nil, "", nil, nil)
	api3Status(t, api3Request(t, api3Router(func(r chi.Router) {
		r.Put("/db/migration", noSave.handleUpdateDatabaseMigrationSettings)
	}), http.MethodPut, "/db/migration", `{"driver":"sqlite","dsn":"`+sqliteDSN+`"}`), http.StatusInternalServerError)

	// Connection testing endpoint.
	api3Status(t, api3Request(t, router, http.MethodPost, "/db/migration/test", `{`), http.StatusBadRequest)
	cfg.Storage.MigrationTarget = nil
	api3Status(t, api3Request(t, router, http.MethodPost, "/db/migration/test", `{}`), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodPost, "/db/migration/test", `{"driver":"postgres","dsn":"postgres://127.0.0.1:1/nvr?sslmode=disable"}`), http.StatusBadRequest)
	cfg.Storage.MigrationTarget = &config.DatabaseTargetConfig{Driver: string(storage.DriverSQLite), DSN: sqliteDSN}
	rr = api3Request(t, router, http.MethodPost, "/db/migration/test", `{}`)
	api3Status(t, rr, http.StatusOK)
	body = api3JSON(t, rr)
	require.Equal(t, "connected", body["status"])
	require.Equal(t, false, body["migration_available"])
	cfg.Storage.MigrationTarget = &config.DatabaseTargetConfig{Driver: string(storage.DriverPostgres), DSN: "postgres://127.0.0.1:1/nvr?sslmode=disable"}
	rr = api3Request(t, router, http.MethodPost, "/db/migration/test", `{"driver":"postgres","dsn":"postgres://127.0.0.1:1/nvr?sslmode=disable"}`)
	api3Status(t, rr, http.StatusBadRequest)

	// Run migration validation paths (no live destination is contacted).
	cfg.Storage.MigrationTarget = nil
	api3Status(t, api3Request(t, router, http.MethodPost, "/db/migration/run", ""), http.StatusBadRequest)
	cfg.Storage.MigrationTarget = &config.DatabaseTargetConfig{Driver: string(storage.DriverSQLite), DSN: sqliteDSN}
	api3Status(t, api3Request(t, router, http.MethodPost, "/db/migration/run", ""), http.StatusBadRequest)
	cfg.Storage.MigrationTarget = &config.DatabaseTargetConfig{Driver: string(storage.DriverPostgres), DSN: "postgres://127.0.0.1:1/nvr?sslmode=disable"}
	cfg.Storage.DatabaseDriver = string(storage.DriverPostgres)
	api3Status(t, api3Request(t, router, http.MethodPost, "/db/migration/run", ""), http.StatusConflict)
	cfg.Storage.DatabaseDriver = ""
	plainRepo := NewHandler(api3RepoWrapper{db}, store, noopAuthMW(), cfg, nil, cfgPath, nil, nil)
	api3Status(t, api3Request(t, api3Router(func(r chi.Router) {
		r.Post("/db/migration/run", plainRepo.handleRunDatabaseMigration)
	}), http.MethodPost, "/db/migration/run", ""), http.StatusNotImplemented)
}

// --- fMP4 proxying ---

func TestApi3FMP4StreamProxy(t *testing.T) {
	db, store := setupTestDB(t)
	api3SeedCameraRow(t, db, "cam-1", "Front Door")
	require.NoError(t, db.BindStreamToCamera(context.Background(), "cam-1", "cam-1"))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fmp4-init-and-moof"))
	}))
	t.Cleanup(upstream.Close)

	engine := &stubMediaEngine{playURLs: map[string]string{"fmp4": upstream.URL + "/live/cam-1.mp4"}}
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)
	router := api3Router(func(r chi.Router) { r.Get("/cameras/{id}/stream.m4s", h.handleFMP4Stream) })

	rr := api3Request(t, router, http.MethodGet, "/cameras/cam-1/stream.m4s?token=abc", "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, "video/mp4", rr.Header().Get("Content-Type"))
	require.Contains(t, rr.Body.String(), "fmp4-init-and-moof")

	// Missing media engine.
	noEngine := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	api3Status(t, api3Request(t, api3Router(func(r chi.Router) {
		r.Get("/cameras/{id}/stream.m4s", noEngine.handleFMP4Stream)
	}), http.MethodGet, "/cameras/cam-1/stream.m4s", ""), http.StatusServiceUnavailable)

	// Camera without a bound stream.
	api3Status(t, api3Request(t, router, http.MethodGet, "/cameras/unbound/stream.m4s", ""), http.StatusNotFound)

	// Engine failure to build the play URL.
	failing := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	failing.SetMediaEngine(&stubMediaEngine{playURLs: map[string]string{}})
	api3Status(t, api3Request(t, api3Router(func(r chi.Router) {
		r.Get("/cameras/{id}/stream.m4s", failing.handleFMP4Stream)
	}), http.MethodGet, "/cameras/cam-1/stream.m4s", ""), http.StatusBadGateway)

	// Proxy transport failure is only logged; nothing is written by the handler.
	brokenUpstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	brokenUpstream.Close()
	broken := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	broken.SetMediaEngine(&stubMediaEngine{playURLs: map[string]string{"fmp4": brokenUpstream.URL + "/live/x.mp4"}})
	rr = api3Request(t, api3Router(func(r chi.Router) {
		r.Get("/cameras/{id}/stream.m4s", broken.handleFMP4Stream)
	}), http.MethodGet, "/cameras/cam-1/stream.m4s", "")
	require.Empty(t, rr.Body.String())
}

// --- stream registry and protocol handlers ---

type api3PlainRecorder struct {
	codec model.Format
	hub   *model.StreamHub
}

func (r *api3PlainRecorder) Start(context.Context) error  { return nil }
func (r *api3PlainRecorder) Stop() error                  { return nil }
func (r *api3PlainRecorder) Status() model.RecorderStatus { return "" }

type api3CodecRecorder struct {
	api3PlainRecorder
	delegate      model.Recorder
	codec         model.Format
	sps, pps, vps []byte
	audioCodec    string
	audioConfig   []byte
	hub           *model.StreamHub
}

func (r *api3CodecRecorder) Delegate() model.Recorder { return r.delegate }
func (r *api3CodecRecorder) CodecParams() (model.Format, []byte, []byte, []byte) {
	return r.codec, r.sps, r.pps, r.vps
}
func (r *api3CodecRecorder) GetHub() *model.StreamHub { return r.hub }

type api3AudioRecorder struct {
	api3PlainRecorder
	delegate model.Recorder
	codec    string
	config   []byte
}

func (r *api3AudioRecorder) Delegate() model.Recorder { return r.delegate }
func (r *api3AudioRecorder) AudioCodec() string       { return r.codec }
func (r *api3AudioRecorder) AudioMuxerConfig() []byte { return r.config }

type api3ConditionalHandler struct {
	ConditionalStaticStreamHandler
}

func TestApi3StreamRegistryAndHandlers(t *testing.T) {
	registry := NewStreamRegistry()
	handlers := []StreamHandler{
		&HLSStreamHandler{},
		&WebRTCStreamHandler{},
		&FLVStreamHandler{},
		&WSFLVStreamHandler{},
		&WSStreamHandler{},
		&FMP4StreamHandler{},
		&StaticStreamHandler{Protocol: "static", Codecs: []model.Format{model.FormatH264}},
		&ConditionalStaticStreamHandler{
			StaticStreamHandler: StaticStreamHandler{Protocol: "ll-hls", Codecs: []model.Format{model.FormatH264}},
			Available:           false,
			Reason:              "low latency disabled",
		},
	}
	for _, handler := range handlers {
		registry.Register(handler)
		require.NotEmpty(t, handler.Name())
		require.NoError(t, handler.StartStream("cam-1", nil, StreamStartOptions{MaxFPS: 10}))
		require.NoError(t, handler.StopStream("cam-1"))
	}

	require.True(t, registry.Handler("hls").CanHandle(model.FormatH264))
	require.Nil(t, registry.Handler("missing"))
	require.Len(t, registry.HandlersForCodec(model.FormatH264), 7)
	require.Contains(t, registry.ProtocolsForCodec(model.FormatH265), "hls")
	require.NotContains(t, registry.ProtocolsForCodec(model.FormatH265), "webrtc")

	details := registry.ProtocolsDetailForCodec(model.FormatH264)
	require.True(t, api3HasProtocol(details, "hls", true))
	// The conditional handler supports H.264 but is currently unavailable.
	require.True(t, api3HasProtocol(details, "ll-hls", false))
	for _, detail := range details {
		if detail.Protocol == "ll-hls" {
			require.Equal(t, "low latency disabled", detail.Reason)
		}
	}
	// H.265 is not a supported codec for the conditional handler, so it is omitted.
	for _, detail := range registry.ProtocolsDetailForCodec(model.FormatH265) {
		require.NotEqual(t, "static", detail.Protocol)
	}

	// Conditional wrapper delegates capability queries.
	conditional := &ConditionalStaticStreamHandler{
		StaticStreamHandler: StaticStreamHandler{Protocol: "wasm", Codecs: []model.Format{model.FormatH264}},
		Available:           false,
	}
	require.False(t, conditional.CanHandle(model.FormatH264))
	require.True(t, conditional.SupportedCodec(model.FormatH264))
	require.Equal(t, "reason", (&ConditionalStaticStreamHandler{Reason: "reason"}).UnavailabilityReason(model.FormatH264))
}

func api3HasProtocol(details []ProtocolDetail, name string, available bool) bool {
	for _, detail := range details {
		if detail.Protocol == name && detail.Available == available {
			return true
		}
	}
	return false
}

func TestApi3RecorderParameterExtraction(t *testing.T) {
	// CodecParamsProvider with complete parameter sets short-circuits.
	provider := &api3CodecRecorder{codec: model.FormatH265, sps: []byte{1}, pps: []byte{2}, vps: []byte{3}}
	codec, sps, pps, vps := getCodecParams(provider)
	require.Equal(t, model.FormatH265, codec)
	require.Equal(t, []byte{3}, vps)
	require.Equal(t, []byte{1}, sps)
	require.Equal(t, []byte{2}, pps)

	// Incomplete provider parameters fall through to the concrete recorder types.
	partial := &api3CodecRecorder{codec: model.FormatH265, sps: nil, pps: nil}
	codec, sps, pps, vps = getCodecParams(partial)
	require.Equal(t, model.FormatH265, codec)
	require.Nil(t, sps)
	require.Nil(t, pps)
	require.Nil(t, vps)

	h264Delegate := &api3CodecRecorder{codec: model.FormatH264, delegate: &recorder.H264Recorder{}}
	codec, _, _, _ = getCodecParams(h264Delegate)
	require.Equal(t, model.FormatH264, codec)

	h265Delegate := &api3CodecRecorder{codec: model.FormatH265, delegate: &recorder.H265Recorder{}}
	codec, _, _, _ = getCodecParams(h265Delegate)
	require.Equal(t, model.FormatH265, codec)

	// unwrapDelegate collapses nested delegates and tolerates nil delegates.
	nested := &api3CodecRecorder{delegate: &api3CodecRecorder{delegate: &api3PlainRecorder{}}}
	require.NotNil(t, unwrapDelegate(nested))
	require.Nil(t, unwrapDelegate((&api3CodecRecorder{delegate: nil})).(*api3CodecRecorder).delegate)

	// Audio parameters: delegate first, then the outer recorder, then nothing.
	audioDelegate := &api3AudioRecorder{delegate: &api3PlainRecorder{}, codec: "aac", config: []byte{1, 2}}
	require.Equal(t, "aac", func() string { c, _ := getAudioParams(audioDelegate); return c }())
	outerAudio := &api3AudioRecorder{delegate: &api3PlainRecorder{}, codec: "g711"}
	require.Equal(t, "g711", func() string { c, _ := getAudioParams(outerAudio); return c }())
	plainCodec, plainConfig := getAudioParams(&api3PlainRecorder{})
	require.Empty(t, plainCodec)
	require.Nil(t, plainConfig)

	// Stream hubs are read via the GetHub interface, the concrete types, or not at all.
	hub := model.NewStreamHub()
	require.Same(t, hub, getStreamHub(&api3CodecRecorder{hub: hub}))
	require.Nil(t, getStreamHub(&recorder.H264Recorder{}))
	require.Nil(t, getStreamHub(&recorder.H265Recorder{}))
	require.Nil(t, getStreamHub(&recorder.MJPEGRecorder{}))
	require.Nil(t, getStreamHub(&recorder.HTTPJPEGRecorder{}))
	require.Nil(t, getStreamHub(&recorder.ONVIFRecorder{}))
	require.Nil(t, getStreamHub(&xiaomi.XiaomiRecorder{}))
	require.Nil(t, getStreamHub(&api3PlainRecorder{}))

	// The registry can be attached to a handler for protocol queries.
	h := NewHandler(nil, nil, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetStreamRegistry(NewStreamRegistry())
	require.NotNil(t, h.streamRegistry)
	require.Nil(t, h.streamRegistry.Handler("hls"))
}

// --- Emby endpoints ---

func TestApi3EmbyEndpoints(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	svc := emby.NewService(db, nil)
	t.Cleanup(svc.Stop)
	h.SetEmbyService(svc)
	router := api3Router(func(r chi.Router) {
		r.Get("/emby/config", h.handleEmbyGetConfig)
		r.Put("/emby/config", h.handleEmbySaveConfig)
		r.Get("/emby/browse", h.handleEmbyBrowse)
		r.Get("/emby/items", h.handleEmbyListItems)
		r.Post("/emby/items", h.handleEmbyAddItem)
		r.Patch("/emby/items/{id}", h.handleEmbyUpdateItem)
		r.Delete("/emby/items/{id}", h.handleEmbyDeleteItem)
	})

	// Unconfigured server behaves predictably.
	rr := api3Request(t, router, http.MethodGet, "/emby/config", "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, false, api3JSON(t, rr)["configured"])
	api3Status(t, api3Request(t, router, http.MethodGet, "/emby/browse", ""), http.StatusConflict)
	api3Status(t, api3Request(t, router, http.MethodPost, "/emby/items", `{"emby_item_id":"x"}`), http.StatusConflict)

	// Validation failures.
	api3Status(t, api3Request(t, router, http.MethodPut, "/emby/config", `{`), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodPut, "/emby/config", `{"base_url":"not-a-url"}`), http.StatusBadGateway)
	api3Status(t, api3Request(t, router, http.MethodPut, "/emby/config", `{"base_url":"http://127.0.0.1:1"}`), http.StatusBadGateway)
	api3Status(t, api3Request(t, router, http.MethodPost, "/emby/items", `{`), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodPatch, "/emby/items/x", `{`), http.StatusBadRequest)

	// Managed items can be listed, updated and removed without an Emby server.
	require.NoError(t, db.SaveEmbyItem(ctx, storage.EmbyItem{
		ID: "item-1", EmbyItemID: "emby-1", Name: "Library Item", ItemType: "Movie", StreamID: "emby_stream_1",
	}))
	rr = api3Request(t, router, http.MethodGet, "/emby/items", "")
	api3Status(t, rr, http.StatusOK)
	require.NotEmpty(t, api3JSON(t, rr)["items"])

	rr = api3Request(t, router, http.MethodPatch, "/emby/items/item-1", `{"publish_enabled":false,"loop":true}`)
	api3Status(t, rr, http.StatusOK)
	item := api3JSON(t, rr)
	require.Equal(t, "Library Item", item["name"])

	api3Status(t, api3Request(t, router, http.MethodPatch, "/emby/items/ghost", `{}`), http.StatusNotFound)
	api3Status(t, api3Request(t, router, http.MethodDelete, "/emby/items/ghost", ""), http.StatusNotFound)
	api3Status(t, api3Request(t, router, http.MethodDelete, "/emby/items/item-1", ""), http.StatusOK)

	// Missing service fails closed everywhere.
	noService := api3Router(func(r chi.Router) {
		r.Get("/emby/config", (&Handler{}).handleEmbyGetConfig)
		r.Put("/emby/config", (&Handler{}).handleEmbySaveConfig)
		r.Get("/emby/browse", (&Handler{}).handleEmbyBrowse)
		r.Get("/emby/items", (&Handler{}).handleEmbyListItems)
		r.Post("/emby/items", (&Handler{}).handleEmbyAddItem)
		r.Patch("/emby/items/{id}", (&Handler{}).handleEmbyUpdateItem)
		r.Delete("/emby/items/{id}", (&Handler{}).handleEmbyDeleteItem)
	})
	for _, call := range []struct{ method, target, body string }{
		{http.MethodGet, "/emby/config", ""},
		{http.MethodPut, "/emby/config", `{}`},
		{http.MethodGet, "/emby/browse", ""},
		{http.MethodGet, "/emby/items", ""},
		{http.MethodPost, "/emby/items", `{}`},
		{http.MethodPatch, "/emby/items/item-1", `{}`},
		{http.MethodDelete, "/emby/items/item-1", ""},
	} {
		api3Status(t, api3Request(t, noService, call.method, call.target, call.body), http.StatusServiceUnavailable)
	}

	// Storage failures are surfaced as server errors.
	broken := NewHandler(api3ClosedDB(t), store, noopAuthMW(), nil, nil, "", nil, nil)
	brokenSvc := emby.NewService(broken.db.(*storage.DB), nil)
	t.Cleanup(brokenSvc.Stop)
	broken.SetEmbyService(brokenSvc)
	brokenRouter := api3Router(func(r chi.Router) {
		r.Get("/emby/config", broken.handleEmbyGetConfig)
		r.Get("/emby/items", broken.handleEmbyListItems)
		r.Patch("/emby/items/{id}", broken.handleEmbyUpdateItem)
	})
	api3Status(t, api3Request(t, brokenRouter, http.MethodGet, "/emby/config", ""), http.StatusInternalServerError)
	api3Status(t, api3Request(t, brokenRouter, http.MethodGet, "/emby/items", ""), http.StatusInternalServerError)
	api3Status(t, api3Request(t, brokenRouter, http.MethodPatch, "/emby/items/item-1", `{}`), http.StatusInternalServerError)
}

// keep time imported for cache entries in future additions.
var _ = time.Now
