package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
)

type transcodeProbeStub struct{}

func (transcodeProbeStub) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte("version"), nil
}
func TestTranscodeProbeRejectsPathsAndUnknownBackend(t *testing.T) {
	h := &Handler{transcodeProbe: transcode.NewProbeService(transcodeProbeStub{})}
	for _, body := range []string{`{"backend":"ffmpeg","path":"/tmp/evil"}`, `{"backend":"shell"}`, `null`, `{"backend":"ffmpeg"}{}`} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/transcoding/capabilities/probe", strings.NewReader(body))
		h.handleTranscodeProbe(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid request %s: %d", body, w.Code)
		}
	}
}
func TestTranscodeProbeAvailableWhileDisabled(t *testing.T) {
	h := &Handler{transcodeProbe: transcode.NewProbeService(transcodeProbeStub{})}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/transcoding/capabilities/probe", strings.NewReader(`{"backend":"gstreamer"}`))
	h.handleTranscodeProbe(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"can_enable":false`) {
		t.Fatalf("unexpected response %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.handleTranscodeCapabilities(w, httptest.NewRequest("GET", "/api/transcoding/capabilities", nil))
	if !strings.Contains(w.Body.String(), "default_gstreamer") {
		t.Fatal("completed probe missing from catalog")
	}
}
func TestTranscodeProbeRequiresOperator(t *testing.T) {
	h := &Handler{transcodeProbe: transcode.NewProbeService(transcodeProbeStub{})}
	w := httptest.NewRecorder()
	middleware.RequireOperatePermission()(http.HandlerFunc(h.handleTranscodeProbe)).ServeHTTP(w, httptest.NewRequest("POST", "/api/transcoding/capabilities/probe", strings.NewReader(`{"backend":"gstreamer"}`)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated probe accepted: %d", w.Code)
	}
}
