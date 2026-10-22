package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

type haWHEPEngine struct {
	*stubMediaEngine
	target string
	last   media.PlayURLRequest
}

func (e *haWHEPEngine) BuildPlayURL(_ context.Context, req media.PlayURLRequest) (*media.PlayURL, error) {
	e.last = req
	return &media.PlayURL{URL: e.target}, nil
}

func TestHomeAssistantWHEPScopedSession(t *testing.T) {
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	cfg, err := db.GetHomeAssistantSettings(context.Background())
	require.NoError(t, err)
	cfg.Enabled = true
	cfg.Streams = []storage.HomeAssistantPublication{{StreamID: "main"}, {StreamID: "other"}}
	require.NoError(t, db.SaveHomeAssistantSettings(context.Background(), cfg))
	location, answer := "/session/test", "v=0\r\n"
	var deletes int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Basic test", r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("Cookie"))
		if r.Method == "DELETE" {
			require.Equal(t, "/session/test", r.URL.Path)
			deletes++
			w.WriteHeader(204)
			return
		}
		body, _ := io.ReadAll(r.Body)
		require.Equal(t, "v=0\r\n", string(body))
		w.Header().Set("Location", location)
		w.WriteHeader(201)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(upstream.Close)
	engine := &haWHEPEngine{stubMediaEngine: &stubMediaEngine{stream: &media.StreamInfo{Active: true, AppName: "custom-app", VideoCodec: "H264"}}, target: upstream.URL + "/whep"}
	h := NewHandler(db, store, noopAuthMW(), &config.Config{Auth: config.AuthConfig{PlaybackTokens: map[string]string{"main": "media-secret"}}}, nil, "", nil, nil)
	h.SetMediaEngine(engine)
	router := h.Routes()
	request := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader("v=0\r\n"))
		r.Header.Set("Content-Type", "application/sdp")
		r.Header.Set("Authorization", "Basic test")
		r.Header.Set("Cookie", "session=private")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	path := "/api/integrations/home-assistant/streams/main/webrtc"
	res := request("POST", path)
	require.Equal(t, 201, res.Code, res.Body.String())
	require.Equal(t, "custom-app", engine.last.AppName)
	require.Equal(t, "media-secret", engine.last.Token)
	sessionPath := res.Header().Get("Location")
	token := strings.TrimPrefix(sessionPath, path+"/")
	require.Equal(t, 400, request("DELETE", sessionPath+"x").Code)
	require.Equal(t, 400, request("DELETE", strings.Replace(sessionPath, "/main/", "/other/", 1)).Code)
	signed, err := h.openHomeAssistantSession(token)
	require.NoError(t, err)
	signed.Expires = time.Now().Add(-time.Second).Unix()
	require.Equal(t, 400, request("DELETE", path+"/"+h.sealHomeAssistantSession(signed)).Code)
	// Revoke creation immediately, but allow cleanup of a previously minted session.
	cfg.Enabled = false
	require.NoError(t, db.SaveHomeAssistantSettings(context.Background(), cfg))
	require.Equal(t, 404, request("POST", path).Code)
	require.Equal(t, 204, request("DELETE", sessionPath).Code)
	require.Equal(t, 1, deletes)
	cfg.Enabled = true
	require.NoError(t, db.SaveHomeAssistantSettings(context.Background(), cfg))
	location = "http://untrusted.invalid/delete"
	require.Equal(t, 502, request("POST", path).Code)
	location, answer = "/session/test", "not SDP"
	require.Equal(t, 502, request("POST", path).Code)
	engine.stream.VideoCodec = "H265"
	require.Equal(t, 400, request("POST", path).Code)
	// Origin comparisons never permit other schemes or hosts.
	a, _ := url.Parse(upstream.URL)
	b, _ := url.Parse("file:///tmp/unsafe")
	require.False(t, sameWHEPOrigin(a, b))
}
