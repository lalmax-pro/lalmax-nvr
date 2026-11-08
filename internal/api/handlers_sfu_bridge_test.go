package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/sfubridge"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
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
	missing := sfuTestRouter(&model.User{ID: 1, Role: model.RoleSuperAdmin, Enabled: true}, nil)
	rec = sfuRequest(missing, "GET", "/api/sfu/bridges/bridge", "", "")
	if rec.Code != 503 {
		t.Fatalf("missing runtime: %d", rec.Code)
	}
}

type sfuIntegrationPolicy struct{}

func (sfuIntegrationPolicy) Authorize(_ context.Context, req access.Request) error {
	if req.Principal != "user:42" {
		return errors.New("denied")
	}
	switch req.Operation {
	case access.Join, access.Subscribe, access.ManageBridge:
		return nil
	}
	return errors.New("denied")
}

type sfuIntegrationPublisher struct {
	opened chan struct{}
	closed chan struct{}
}

func (p *sfuIntegrationPublisher) AddCustomizePubSession(_ string) (logic.ICustomizePubSessionContext, error) {
	close(p.opened)
	return &sfuIntegrationSession{}, nil
}
func (p *sfuIntegrationPublisher) DelCustomizePubSession(logic.ICustomizePubSessionContext) {
	close(p.closed)
}

type sfuIntegrationSession struct {
	logic.ICustomizePubSessionContext
}

func (*sfuIntegrationSession) WithOption(func(*base.AvPacketStreamOption)) {}
func (*sfuIntegrationSession) FeedAvPacket(base.AvPacket) error            { return nil }
func TestSFUBridgeHTTPRegistryLifecycle(t *testing.T) {
	rooms := sfu.NewManager()
	room, err := rooms.Create(sfu.Key{Namespace: "nvr", ID: "test"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := room.JoinSession(sfu.Participant{ID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := room.PublishSession("source", gen, "video", "camera", "video/H264")
	if err != nil {
		t.Fatal(err)
	}
	if err = room.EnablePublicationRTP("source", gen, pub.ID, 2, 8); err != nil {
		t.Fatal(err)
	}
	publisher := &sfuIntegrationPublisher{opened: make(chan struct{}), closed: make(chan struct{})}
	grants := auth.NewMediaAuthorizer()
	service, err := sfubridge.New(room, sfuIntegrationPolicy{}, publisher, grants, func(context.Context, sfu.Publication) error { return nil }, 2)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := sfubridge.NewRegistry(service, func(_ context.Context, r sfubridge.ManagementRequest) error {
		if r.Principal != r.Owner {
			return errors.New("denied")
		}
		return nil
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := registry.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	router := sfuTestRouter(&model.User{ID: 42, Role: model.RoleSuperAdmin, Enabled: true}, registry)
	rec := sfuRequest(router, "POST", "/api/sfu/bridges/", `{"publication_id":"`+pub.ID+`"}`, "application/json")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	var status sfuBridgeStatusResponse
	if err = json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	select {
	case <-publisher.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not started")
	}
	other := sfuTestRouter(&model.User{ID: 43, Role: model.RoleSuperAdmin, Enabled: true}, registry)
	viewer := sfuTestRouter(&model.User{ID: 7, Role: model.RoleUser, Enabled: true}, registry)
	if rec = sfuRequest(viewer, "GET", "/api/sfu/bridges/", "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer list: %d %s", rec.Code, rec.Body.String())
	}
	if rec = sfuRequest(other, "GET", "/api/sfu/bridges/", "", ""); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("other user list: %d %s", rec.Code, rec.Body.String())
	}
	if rec = sfuRequest(router, "GET", "/api/sfu/bridges/", "", ""); rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), status.ID) || strings.Contains(rec.Body.String(), "token") {
		t.Fatalf("owner list: %d %s", rec.Code, rec.Body.String())
	}
	for _, tc := range []struct{ method, suffix, body, contentType string }{
		{http.MethodGet, "", "", ""},
		{http.MethodGet, "/playback", "", ""},
		{http.MethodPost, "/stop", `{}`, "application/json"},
		{http.MethodDelete, "", "", ""},
	} {
		path := "/api/sfu/bridges/" + status.ID + tc.suffix
		got := sfuRequest(other, tc.method, path, tc.body, tc.contentType)
		missing := sfuRequest(other, tc.method, "/api/sfu/bridges/missing"+tc.suffix, tc.body, tc.contentType)
		if got.Code != http.StatusNotFound || got.Code != missing.Code || got.Body.String() != missing.Body.String() {
			t.Fatalf("other user %s %s: got %d %s, missing %d %s", tc.method, tc.suffix, got.Code, got.Body.String(), missing.Code, missing.Body.String())
		}
	}
	select {
	case <-publisher.closed:
		t.Fatal("another administrator stopped the publisher")
	default:
	}
	rec = sfuRequest(router, "GET", "/api/sfu/bridges/"+status.ID+"/playback", "", "")
	var playback struct {
		StreamID string `json:"stream_id"`
		Token    string `json:"token"`
	}
	if rec.Code != 200 {
		t.Fatalf("playback: %d", rec.Code)
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &playback); err != nil {
		t.Fatal(err)
	}
	if err = grants.AuthorizePlayback("rtsp", playback.StreamID, playback.Token); err != nil {
		t.Fatal(err)
	}
	rec = sfuRequest(router, "POST", "/api/sfu/bridges/"+status.ID+"/stop", `{}`, "application/json")
	if rec.Code != 204 {
		t.Fatalf("stop: %d", rec.Code)
	}
	if rec = sfuRequest(router, "POST", "/api/sfu/bridges/"+status.ID+"/stop", `{}`, "application/json"); rec.Code != http.StatusNoContent {
		t.Fatalf("repeat stop: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case <-publisher.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not closed")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rec = sfuRequest(router, "GET", "/api/sfu/bridges/"+status.ID, "", "")
		if strings.Contains(rec.Body.String(), `"stopped"`) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(rec.Body.String(), `"stopped"`) {
		t.Fatalf("status: %s", rec.Body.String())
	}
	if rec = sfuRequest(router, "POST", "/api/sfu/bridges/"+status.ID+"/stop", `{}`, "application/json"); rec.Code != http.StatusNoContent {
		t.Fatalf("stop after completion: %d %s", rec.Code, rec.Body.String())
	}
	if err = grants.AuthorizePlayback("rtsp", playback.StreamID, playback.Token); err == nil {
		t.Fatal("token not revoked")
	}
	rec = sfuRequest(router, "DELETE", "/api/sfu/bridges/"+status.ID, "", "")
	if rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec = sfuRequest(router, "DELETE", "/api/sfu/bridges/"+status.ID, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("repeat delete: %d %s", rec.Code, rec.Body.String())
	}
}

type testSFUTicketIssuer struct{ user *model.User }

func (i *testSFUTicketIssuer) Issue(_ context.Context, u *model.User) (string, error) {
	i.user = u
	return "one-time-ticket", nil
}
func TestSFUTicketRequiresAdministrator(t *testing.T) {
	issuer := &testSFUTicketIssuer{}
	h := NewHandler(nil, nil, noopAuthMW(), &config.Config{SFU: config.SFUConfig{ICEServers: []config.SFUICEServer{{URLs: []string{"turn:relay.example.com"}, Username: "test-user", Credential: "test-password"}}}}, nil, "", nil, nil)
	h.SetMultiUserAuthMW(middleware.InjectUser(&model.User{ID: 9, Role: model.RoleSuperAdmin, Enabled: true}))
	h.SetSFUTicketIssuer(issuer)
	router := h.Routes()
	rec := sfuRequest(router, "POST", "/api/sfu/ticket", `{}`, "application/json")
	if rec.Code != 201 || rec.Header().Get("Cache-Control") != "no-store" || issuer.user == nil || issuer.user.ID != 9 || !strings.Contains(rec.Body.String(), "one-time-ticket") || !strings.Contains(rec.Body.String(), "test-password") {
		t.Fatalf("ticket: %d %s", rec.Code, rec.Body.String())
	}
	viewer := NewHandler(nil, nil, noopAuthMW(), nil, nil, "", nil, nil)
	viewer.SetMultiUserAuthMW(middleware.InjectUser(&model.User{ID: 10, Role: model.RoleUser, Enabled: true}))
	viewer.SetSFUTicketIssuer(issuer)
	issuer.user = nil
	rec = sfuRequest(viewer.Routes(), "POST", "/api/sfu/ticket", `{}`, "application/json")
	if rec.Code != 403 || issuer.user != nil {
		t.Fatalf("viewer ticket: %d", rec.Code)
	}
	rec = sfuRequest(router, "GET", "/api/sfu/ws?ticket=x", "", "")
	if rec.Code != 503 {
		t.Fatalf("unmounted signaling: %d", rec.Code)
	}
}
