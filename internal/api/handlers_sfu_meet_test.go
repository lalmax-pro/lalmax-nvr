package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSFUMeetPageAllowsSameOriginCamera(t *testing.T) {
	h := &Handler{}
	w := httptest.NewRecorder()
	h.serveSFUMeet(w, httptest.NewRequest(http.MethodGet, "/sfu-meet/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `data-mode="nvr"`) {
		t.Fatalf("unexpected page response: %d", w.Code)
	}
	if got := w.Header().Get("Permissions-Policy"); got != "camera=(self), microphone=(self), geolocation=()" {
		t.Fatalf("unexpected permissions policy: %q", got)
	}
}
