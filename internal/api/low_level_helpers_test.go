package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestUserAndStreamHelpers(t *testing.T) {
	user := toUserResponse(&model.User{ID: 7, Username: "admin", Role: "admin", Enabled: true})
	require.Equal(t, int64(7), user.ID)
	require.Equal(t, "admin", user.Username)
	require.Equal(t, "H264", homeAssistantWebRTCCodec("H264"))
	require.Equal(t, "H265", homeAssistantWebRTCCodec("hevc"))
	require.Empty(t, homeAssistantWebRTCCodec("mjpeg"))
	offer := []byte("v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=video 9 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\n")
	require.True(t, homeAssistantOfferSupportsCodec(offer, "h264"))
	require.False(t, homeAssistantOfferSupportsCodec(offer, "h265"))

	require.True(t, sameWHEPOrigin(mustURL(t, "http://example/a"), mustURL(t, "http://example/b")))
	require.False(t, sameWHEPOrigin(mustURL(t, "http://example"), mustURL(t, "https://example")))
	require.False(t, sameWHEPOrigin(nil, mustURL(t, "http://example")))

	require.Equal(t, " cam ", normalizeStreamID(" cam "))
	require.Equal(t, "", normalizeStreamID(""))
	require.True(t, validPullSourceURL("rtsp://example/live"))
	require.True(t, validPullSourceURL("http://example/live"))
	require.False(t, validPullSourceURL("file:///tmp/a"))
	require.False(t, validPullSourceURL("not a url"))
	require.True(t, isDupInStreamError(errors.New("in stream already exist")))
	require.False(t, isDupInStreamError(errors.New("other error")))
}

func TestSetupAndSystemHelpers(t *testing.T) {
	dir := t.TempDir()
	require.Equal(t, dir, mustSetupDataDir(t, dir))
	got, err := validateSetupDataDir(filepathJoin(dir, "missing"))
	require.NoError(t, err)
	require.DirExists(t, got)
	require.Equal(t, "1h 1m 1s", formatUptime(3661*time.Second))
	require.Equal(t, "0s", formatUptime(0))

	require.True(t, isDisplayableInterfaceAddress(&net.IPNet{IP: net.ParseIP("192.0.2.1")}))
	require.True(t, isDisplayableInterfaceAddress(&net.IPNet{IP: net.ParseIP("127.0.0.1")}))
	_, err = net.ResolveIPAddr("ip", "127.0.0.1")
	require.NoError(t, err)
}

func TestStreamRequestHelpers(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/stream", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("stream_id", "cam1")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	require.Equal(t, "cam1", streamIDFromRequest(r))
	r = httptest.NewRequest(http.MethodGet, "/api/stream", nil)
	require.Empty(t, streamIDFromRequest(r))

	require.Equal(t, "rtmp_push", pushSourceTypeFromProtocol("rtmp"))
	require.Equal(t, "srt_push", pushSourceTypeFromProtocol("SRT"))
	require.Equal(t, "whip_push", pushSourceTypeFromProtocol("whip"))
	require.Empty(t, pushSourceTypeFromProtocol("rtsp"))

	require.Equal(t, "iptv", inferStreamSourceTypeForID("iptv_cam", ""))
	require.Equal(t, "emby", inferStreamSourceTypeForID("emby_cam", ""))
	require.Equal(t, "voip", inferStreamSourceTypeForID("voip_cam", ""))
	require.Equal(t, "rtmp_push", inferStreamSourceTypeForID("cam", "rtmp"))
}

func TestWriteJSONUserResponse(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, map[string]string{"ok": "yes"})
	var out map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Equal(t, "yes", out["ok"])
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

func filepathJoin(a, b string) string                   { return a + "/" + b }
func mustSetupDataDir(t *testing.T, path string) string { t.Helper(); return path }
