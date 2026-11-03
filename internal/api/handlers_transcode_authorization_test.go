package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

func TestDerivedPlaybackRoutesRequireAdministrator(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	viewer := TestHandler(db, store)
	viewer.SetMultiUserAuthMW(middleware.InjectUser(&model.User{Role: model.RoleUser, Enabled: true}))
	hash, err := middleware.HashPassword("test-password")
	if err != nil {
		t.Fatal(err)
	}
	anonymous := TestHandlerWithAuth(db, store, "admin", hash)
	paths := []string{
		"/api/streams/camera/variants",
		"/api/streams/camera/variants/job/play",
		"/api/streams/camera/variants/job/stream.flv",
	}
	for _, path := range paths {
		for _, tc := range []struct {
			name    string
			handler *Handler
			want    int
		}{
			{"viewer", viewer, http.StatusForbidden},
			{"anonymous", anonymous, http.StatusUnauthorized},
		} {
			t.Run(tc.name+path, func(t *testing.T) {
				w := httptest.NewRecorder()
				tc.handler.Routes().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
				if w.Code != tc.want {
					t.Fatalf("got %d, want %d", w.Code, tc.want)
				}
			})
		}
	}
}

func TestHomeAssistantMediaRoutesRequireSourceAuthorization(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	viewer := TestHandler(db, store)
	viewer.SetMultiUserAuthMW(middleware.InjectUser(&model.User{Role: model.RoleUser, Enabled: true}))
	cases := []struct {
		method string
		path   string
	}{
		{"GET", "/api/settings/home-assistant"},
		{"GET", "/api/integrations/home-assistant/streams/camera/ptz/presets"},
		{"POST", "/api/integrations/home-assistant/streams/camera/ptz"},
		{"PUT", "/api/integrations/home-assistant/streams/camera"},
        {"POST", "/api/transcoding/jobs/job/leases?generation=1"},
        {"POST", "/api/transcoding/leases/renew"},
        {"POST", "/api/transcoding/leases/release"},
        {"POST", "/api/transcoding/jobs/job/record/start?generation=1"},
        {"POST", "/api/transcoding/jobs/job/record/stop?generation=1"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			viewer.Routes().ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != http.StatusForbidden {
				t.Fatalf("%s %s returned %d; want 403", tc.method, tc.path, w.Code)
			}
		})
	}
}

func TestHomeAssistantReadRoutesAllowOrdinaryUser(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	viewer := TestHandler(db, store)
	viewer.SetMultiUserAuthMW(middleware.InjectUser(&model.User{Role: model.RoleUser, Enabled: true}))

	for _, tc := range []struct {
		method string
		path string
	}{
		{"GET", "/api/integrations/home-assistant"},
		{"GET", "/api/integrations/home-assistant/streams/camera/recordings"},
		{"GET", "/api/integrations/home-assistant/streams/camera/recordings/id"},
		{"GET", "/api/integrations/home-assistant/streams/camera/recordings/id/file"},
		{"HEAD", "/api/integrations/home-assistant/streams/camera/recordings/id/file"},
		{"GET", "/api/integrations/home-assistant/streams/camera/snapshot"},
		{"DELETE", "/api/integrations/home-assistant/streams/camera/webrtc/session"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			viewer.Routes().ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code == http.StatusForbidden || w.Code == http.StatusUnauthorized {
				t.Fatalf("%s %s returned auth denial %d for reader", tc.method, tc.path, w.Code)
			}
		})
	}
}
