package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

type haTestEngine struct{ *stubMediaEngine }

func (e *haTestEngine) BuildPlayURL(_ context.Context, req media.PlayURLRequest) (*media.PlayURL, error) {
	u := &url.URL{Scheme: "rtsp", Host: "127.0.0.1:15544", Path: "/" + req.AppName + "/" + req.StreamID}
	return &media.PlayURL{URL: u.String(), Protocol: req.Protocol}, nil
}

func TestHomeAssistantPublicationLifecycle(t *testing.T) {
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	engine := &haTestEngine{&stubMediaEngine{streams: []media.StreamInfo{{StreamID: "push-1", AppName: "custom", Active: true, VideoCodec: "H264", AudioCodec: "AAC"}}, stream: &media.StreamInfo{StreamID: "push-1", AppName: "custom", Active: true}}}
	cfg := &config.Config{Auth: config.AuthConfig{PlaybackTokens: map[string]string{"push-1": "playback-secret"}}}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, "", nil, nil)
	h.SetMediaEngine(engine)
	router := h.Routes()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		return doRequest(t, router, method, path, strings.NewReader(body), "", "")
	}
	initial, err := db.GetHomeAssistantSettings(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, initial.InstanceID)
	require.False(t, initial.Enabled)
	require.Equal(t, 404, request("GET", "/api/integrations/home-assistant/streams/push-1/snapshot", "").Code)
	require.Equal(t, 200, request("PUT", "/api/integrations/home-assistant/streams/push-1", `{"enabled":true}`).Code)
	res := request("PUT", "/api/settings/home-assistant", `{"enabled":true,"rtsp_base_url":"rtsp://nvr.lan:25444","streams":[{"stream_id":"push-1","app_name":"untrusted"}]}`)
	require.Equal(t, 200, res.Code, res.Body.String())
	var settings storage.HomeAssistantSettings
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &settings))
	require.Equal(t, "custom", settings.Streams[0].AppName)
	require.Equal(t, initial.InstanceID, settings.InstanceID)
	res = request("GET", "/api/integrations/home-assistant", "")
	require.Equal(t, 200, res.Code, res.Body.String())
	var catalog homeAssistantCatalog
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &catalog))
	require.Len(t, catalog.Streams, 1)
	require.Equal(t, "rtsp://nvr.lan:25444/custom/push-1?token=playback-secret", catalog.Streams[0].RTSPURL)
	require.True(t, catalog.Streams[0].Active)
	require.NotContains(t, res.Body.String(), "source_url")
	require.NotContains(t, res.Body.String(), "password")
	engine.streams = nil
	res = request("GET", "/api/integrations/home-assistant", "")
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &catalog))
	require.Len(t, catalog.Streams, 1)
	require.False(t, catalog.Streams[0].Active)
	// A new handler must read the same persisted identity and publication.
	h2 := NewHandler(db, store, noopAuthMW(), cfg, nil, "", nil, nil)
	res = doRequest(t, h2.Routes(), "GET", "/api/settings/home-assistant", nil, "", "")
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &settings))
	require.Equal(t, initial.InstanceID, settings.InstanceID)
	require.Len(t, settings.Streams, 1)
	require.Equal(t, 200, request("PUT", "/api/integrations/home-assistant/streams/push-1", `{"enabled":false}`).Code)
	res = request("GET", "/api/integrations/home-assistant", "")
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &catalog))
	require.Empty(t, catalog.Streams)
	require.Equal(t, 404, request("GET", "/api/integrations/home-assistant/streams/push-1/snapshot", "").Code)
	plan, err := db.GetRecordingPlanByStream(ctx, "push-1")
	require.NoError(t, err)
	require.Nil(t, plan)
}

func TestHomeAssistantCatalogMoreThan100(t *testing.T) {
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	cfg, err := db.GetHomeAssistantSettings(context.Background())
	require.NoError(t, err)
	cfg.Enabled = true
	for i := range 125 {
		cfg.Streams = append(cfg.Streams, storage.HomeAssistantPublication{StreamID: fmt.Sprintf("s-%03d", i), AppName: "live"})
	}
	require.NoError(t, db.SaveHomeAssistantSettings(context.Background(), cfg))
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(&haTestEngine{&stubMediaEngine{}})
	res := doRequest(t, h.Routes(), "GET", "/api/integrations/home-assistant", nil, "", "")
	var catalog homeAssistantCatalog
	require.Equal(t, 200, res.Code)
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &catalog))
	require.Len(t, catalog.Streams, 125)
	require.Equal(t, "s-000", catalog.Streams[0].StreamID)
}

func TestHomeAssistantSettingsValidationAndPermissions(t *testing.T) {
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	router := h.Routes()
	for _, body := range []string{
		`{"enabled":true,"rtsp_base_url":"http://nvr"}`,
		`{"rtsp_base_url":"rtsp://user:pass@nvr:554"}`,
		`{"rtsp_base_url":"rtsp://nvr/live/a"}`,
		`{"unexpected":true}`,
		`{} {}`,
		`{"streams":[{"stream_id":"a/b"}]}`,
		`{"streams":[{"stream_id":"a"},{"stream_id":"a"}]}`,
	} {
		res := doRequest(t, router, "PUT", "/api/settings/home-assistant", strings.NewReader(body), "", "")
		require.Equal(t, 400, res.Code, body)
	}
	h.multiUserMW = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, &model.User{Role: model.RoleUser})))
		})
	}
	res := doRequest(t, h.Routes(), "PUT", "/api/settings/home-assistant", strings.NewReader(`{"enabled":true}`), "", "")
	require.Equal(t, http.StatusForbidden, res.Code)
}
