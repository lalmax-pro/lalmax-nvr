package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	nvrsfu "github.com/lalmax-pro/lalmax-nvr/internal/sfu"
	"github.com/lalmax-pro/lalmax-nvr/internal/sfubridge"
)

type testSFUBridgeRegistry struct {
	principal, publication, id, action string
	err                                error
}

type testIdempotentBridgeRegistry struct {
	*testSFUBridgeRegistry
	key, audio string
}

func (s *testIdempotentBridgeRegistry) CreateIdempotent(ctx context.Context, p, pub, audio, key string) (sfubridge.Status, error) {
	s.key, s.audio = key, audio
	return s.Create(ctx, p, pub)
}
func (s *testIdempotentBridgeRegistry) ResolveIdempotent(_ context.Context, p, key string) (sfubridge.Status, error) {
	s.principal, s.key, s.action = p, key, "resolve"
	return sfubridge.Status{ID: "bridge", PublicationID: "video", StreamID: "stream", State: sfubridge.StatePending}, s.err
}

func TestSFUBridgeRequestLookup(t *testing.T) {
	mock := &testIdempotentBridgeRegistry{testSFUBridgeRegistry: &testSFUBridgeRegistry{}}
	router := sfuTestRouter(&model.User{ID: 42, Role: model.RoleSuperAdmin, Enabled: true}, mock)
	request := func(handler http.Handler, keys ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/sfu/bridge-requests", nil)
		for _, key := range keys {
			req.Header.Add("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := request(router, "request-1"); rec.Code != 200 || mock.key != "request-1" || mock.principal != "user:42" || mock.action != "resolve" || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), `"id":"bridge"`) {
		t.Fatalf("resolve: %d %s", rec.Code, rec.Body.String())
	}
	mock.err = sfubridge.ErrRequestPending
	if rec := request(router, "request-1"); rec.Code != 202 || strings.Contains(rec.Body.String(), `"id"`) {
		t.Fatalf("pending: %d %s", rec.Code, rec.Body.String())
	}
	mock.err = sfubridge.ErrNotFound
	if rec := request(router, "missing"); rec.Code != 404 {
		t.Fatalf("missing: %d", rec.Code)
	}
	for _, keys := range [][]string{nil, {""}, {"bad key"}, {"a", "b"}} {
		if rec := request(router, keys...); rec.Code != 400 {
			t.Fatalf("invalid keys %q: %d", keys, rec.Code)
		}
	}
	viewer := sfuTestRouter(&model.User{ID: 7, Role: model.RoleUser, Enabled: true}, mock)
	if rec := request(viewer, "request-1"); rec.Code != 403 {
		t.Fatalf("viewer: %d", rec.Code)
	}
	unsupported := sfuTestRouter(&model.User{ID: 42, Role: model.RoleSuperAdmin, Enabled: true}, &testSFUBridgeRegistry{})
	if rec := request(unsupported, "request-1"); rec.Code != 503 {
		t.Fatalf("unsupported: %d", rec.Code)
	}
}

func TestSFUBridgeIdempotencyHeader(t *testing.T) {
	mock := &testIdempotentBridgeRegistry{testSFUBridgeRegistry: &testSFUBridgeRegistry{}}
	router := sfuTestRouter(&model.User{ID: 42, Role: model.RoleSuperAdmin, Enabled: true}, mock)
	request := func(keys ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/sfu/bridges/", strings.NewReader(`{"publication_id":"video","audio_publication_id":"audio"}`))
		req.Header.Set("Content-Type", "application/json")
		for _, key := range keys {
			req.Header.Add("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := request("request-1"); rec.Code != 202 || mock.key != "request-1" || mock.audio != "audio" || mock.principal != "user:42" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	for _, keys := range [][]string{{""}, {"contains space"}, {strings.Repeat("x", 129)}, {"one", "two"}, {"one,two"}} {
		if rec := request(keys...); rec.Code != 400 {
			t.Fatalf("invalid key %q: %d", keys, rec.Code)
		}
	}
	mock.err = sfubridge.ErrIdempotencyConflict
	if rec := request("request-1"); rec.Code != 409 {
		t.Fatalf("conflict: %d %s", rec.Code, rec.Body.String())
	}
	unsupported := sfuTestRouter(&model.User{ID: 42, Role: model.RoleSuperAdmin, Enabled: true}, &testSFUBridgeRegistry{})
	req := httptest.NewRequest(http.MethodPost, "/api/sfu/bridges/", strings.NewReader(`{"publication_id":"video"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "request-1")
	rec := httptest.NewRecorder()
	unsupported.ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Fatalf("unsupported backend: %d", rec.Code)
	}
}

func (s *testSFUBridgeRegistry) Create(_ context.Context, p, pub string) (sfubridge.Status, error) {
	s.principal = p
	s.publication = pub
	s.action = "create"
	return sfubridge.Status{ID: "bridge", StreamID: "stream", PublicationID: pub, State: sfubridge.StatePending}, s.err
}
func (s *testSFUBridgeRegistry) Get(_ context.Context, p, id string) (sfubridge.Status, error) {
	s.principal = p
	s.id = id
	s.action = "get"
	return sfubridge.Status{ID: id, State: sfubridge.StatePending}, s.err
}
func (s *testSFUBridgeRegistry) Playback(_ context.Context, p, id string) (string, string, error) {
	s.principal = p
	s.id = id
	s.action = "playback"
	return "stream", "test-token", s.err
}
func (s *testSFUBridgeRegistry) Stop(_ context.Context, p, id string) error {
	s.principal = p
	s.id = id
	s.action = "stop"
	return s.err
}
func (s *testSFUBridgeRegistry) Remove(_ context.Context, p, id string) error {
	s.principal = p
	s.id = id
	s.action = "remove"
	return s.err
}
func sfuTestRouter(user *model.User, registry SFUBridgeRegistry) http.Handler {
	h := NewHandler(nil, nil, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMultiUserAuthMW(middleware.InjectUser(user))
	h.SetSFUBridgeRegistry(registry)
	return h.Routes()
}
func sfuRequest(router http.Handler, method, path, body, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}
func TestSFUBridgeRoutesAuthorizeAndProtectCredentials(t *testing.T) {
	mock := &testSFUBridgeRegistry{}
	router := sfuTestRouter(&model.User{ID: 42, Username: "alice", Role: model.RoleSuperAdmin, Enabled: true}, mock)
	rec := sfuRequest(router, "POST", "/api/sfu/bridges/", `{"publication_id":"pub"}`, "application/json")
	if rec.Code != http.StatusAccepted || mock.principal != "user:42" || mock.publication != "pub" || strings.Contains(rec.Body.String(), "test-token") {
		t.Fatalf("create: %d %s %+v", rec.Code, rec.Body.String(), mock)
	}
	rec = sfuRequest(router, "GET", "/api/sfu/bridges/bridge/playback", "", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), "test-token") || mock.action != "playback" {
		t.Fatalf("playback: %d %s", rec.Code, rec.Body.String())
	}
	rec = sfuRequest(router, "POST", "/api/sfu/bridges/bridge/stop", `{}`, "application/json")
	if rec.Code != http.StatusNoContent || mock.action != "stop" || mock.id != "bridge" {
		t.Fatalf("stop: %d", rec.Code)
	}
	rec = sfuRequest(router, "DELETE", "/api/sfu/bridges/bridge", "", "")
	if rec.Code != http.StatusNoContent || mock.action != "remove" {
		t.Fatalf("remove: %d", rec.Code)
	}
	viewer := sfuTestRouter(&model.User{ID: 7, Role: model.RoleUser, Enabled: true}, mock)
	mock.action = ""
	rec = sfuRequest(viewer, "GET", "/api/sfu/bridges/bridge/playback", "", "")
	if rec.Code != http.StatusForbidden || mock.action != "" {
		t.Fatalf("viewer access: %d", rec.Code)
	}
}
func TestSFUBridgeRoutesRejectInvalidAndHideErrors(t *testing.T) {
	mock := &testSFUBridgeRegistry{}
	router := sfuTestRouter(&model.User{Username: "admin", Role: model.RoleSuperAdmin, Enabled: true}, mock)
	for _, tc := range []struct {
		body, content string
		status        int
	}{{`{"publication_id":"pub","target":"unsafe"}`, "application/json", 400}, {`{"publication_id":"pub"} {}`, "application/json", 400}, {`{"publication_id":"pub"}`, "text/plain", 415}} {
		rec := sfuRequest(router, "POST", "/api/sfu/bridges/", tc.body, tc.content)
		if rec.Code != tc.status {
			t.Fatalf("invalid input: %d %s", rec.Code, rec.Body.String())
		}
	}
	mock.err = errors.New("secret internal error")
	rec := sfuRequest(router, "GET", "/api/sfu/bridges/bridge", "", "")
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") || mock.principal != "legacy:admin" {
		t.Fatalf("error: %d %s", rec.Code, rec.Body.String())
	}
	mock.err = sfubridge.ErrNotFound
	rec = sfuRequest(router, "GET", "/api/sfu/bridges/bridge", "", "")
	if rec.Code != 404 {
		t.Fatalf("not found: %d", rec.Code)
	}
	mock.err = nvrsfu.ErrRoomNotFound
	rec = sfuRequest(router, "GET", "/api/sfu/bridges/bridge", "", "")
	if rec.Code != 404 {
		t.Fatalf("stale room: %d", rec.Code)
	}
	missing := sfuTestRouter(&model.User{ID: 1, Role: model.RoleSuperAdmin, Enabled: true}, nil)
	rec = sfuRequest(missing, "GET", "/api/sfu/bridges/bridge", "", "")
	if rec.Code != 503 {
		t.Fatalf("missing runtime: %d", rec.Code)
	}
}
