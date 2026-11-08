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
	if rec = sfuRequest(other, "GET", "/api/sfu/bridges/"+status.ID+"/playback", "", ""); rec.Code != 404 {
		t.Fatalf("other user: %d", rec.Code)
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
	if err = grants.AuthorizePlayback("rtsp", playback.StreamID, playback.Token); err == nil {
		t.Fatal("token not revoked")
	}
	rec = sfuRequest(router, "DELETE", "/api/sfu/bridges/"+status.ID, "", "")
	if rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
}

type testSFUTicketIssuer struct{ user *model.User }

func (i *testSFUTicketIssuer) Issue(_ context.Context, u *model.User) (string, error) {
	i.user = u
	return "one-time-ticket", nil
}
func TestSFUTicketRequiresAdministrator(t *testing.T) {
	issuer := &testSFUTicketIssuer{}
	h := NewHandler(nil, nil, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMultiUserAuthMW(middleware.InjectUser(&model.User{ID: 9, Role: model.RoleSuperAdmin, Enabled: true}))
	h.SetSFUTicketIssuer(issuer)
	router := h.Routes()
	rec := sfuRequest(router, "POST", "/api/sfu/ticket", `{}`, "application/json")
	if rec.Code != 201 || rec.Header().Get("Cache-Control") != "no-store" || issuer.user == nil || issuer.user.ID != 9 || !strings.Contains(rec.Body.String(), "one-time-ticket") {
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
