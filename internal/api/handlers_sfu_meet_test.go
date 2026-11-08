package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
)

func TestSFUMeetPageAllowsSameOriginCamera(t *testing.T) {
	h := &Handler{}
	w := httptest.NewRecorder()
	h.serveSFUMeet(w, httptest.NewRequest(http.MethodGet, "/sfu-meet/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "livekit-sdk.js") {
		t.Fatalf("unexpected page response: %d", w.Code)
	}
	if got := w.Header().Get("Permissions-Policy"); got != "camera=(self), microphone=(self), geolocation=()" {
		t.Fatalf("unexpected permissions policy: %q", got)
	}
}

func TestSFUMeetServesLiveKitAssets(t *testing.T) {
	h := &Handler{config: &config.Config{SFU: config.SFUConfig{Enabled: true, Backend: "livekit", LiveKit: config.SFULiveKitConfig{PublicURL: "wss://sfu.example.test:7880"}}}}
	for path, expected := range map[string]string{
		"/sfu-meet/":               "livekit-sdk.js",
		"/sfu-meet/app.js":         "/api/sfu/livekit/join",
		"/sfu-meet/livekit-sdk.js": "LivekitClient",
		"/sfu-meet/licenses":       "@bufbuild/protobuf@1.10.1",
	} {
		w := httptest.NewRecorder()
		h.serveSFUMeet(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), expected) {
			t.Fatalf("%s: unexpected response %d", path, w.Code)
		}
		if path == "/sfu-meet/" && !strings.Contains(w.Header().Get("Content-Security-Policy"), "https://sfu.example.test:7880 wss://sfu.example.test:7880") {
			t.Fatal("LiveKit origin missing from content security policy")
		}
	}
	h.config.SFU.Backend = ""
	w := httptest.NewRecorder()
	h.serveSFUMeet(w, httptest.NewRequest(http.MethodGet, "/sfu-meet/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "livekit-sdk.js") {
		t.Fatalf("default backend served the wrong meeting page: %d", w.Code)
	}
}
