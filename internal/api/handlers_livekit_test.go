package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

type fakeLiveKitJoinIssuer struct {
	principal, room string
	err             error
}

func (f *fakeLiveKitJoinIssuer) IssueLiveKitJoin(_ context.Context, principal, room string) (LiveKitJoin, error) {
	f.principal, f.room = principal, room
	if f.err != nil {
		return LiveKitJoin{}, f.err
	}
	return LiveKitJoin{URL: "wss://example.test", Token: "test-token", Room: room, Identity: "participant", ExpiresIn: 300}, nil
}

func TestLiveKitJoinRequiresAdmin(t *testing.T) {
	issuer := &fakeLiveKitJoinIssuer{}
	h := NewHandler(nil, nil, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMultiUserAuthMW(middleware.InjectUser(&model.User{ID: 9, Role: model.RoleSuperAdmin, Enabled: true}))
	h.SetLiveKitJoinIssuer(issuer)
	r := sfuRequest(h.Routes(), "POST", "/api/sfu/livekit/join", `{"room":"room1"}`, "application/json")
	if r.Code != http.StatusCreated || r.Header().Get("Cache-Control") != "no-store" || issuer.principal != "user:9" || issuer.room != "room1" {
		t.Fatalf("unexpected response %d %s principal=%q room=%q", r.Code, r.Body.String(), issuer.principal, issuer.room)
	}
	issuer.err = errors.New("denied")
	if r = sfuRequest(h.Routes(), "POST", "/api/sfu/livekit/join", `{}`, "application/json"); r.Code != http.StatusForbidden {
		t.Fatalf("denied: %d", r.Code)
	}
	viewer := NewHandler(nil, nil, noopAuthMW(), nil, nil, "", nil, nil)
	viewer.SetMultiUserAuthMW(middleware.InjectUser(&model.User{ID: 10, Role: model.RoleUser, Enabled: true}))
	viewer.SetLiveKitJoinIssuer(issuer)
	if r = sfuRequest(viewer.Routes(), "POST", "/api/sfu/livekit/join", `{}`, "application/json"); r.Code != http.StatusForbidden {
		t.Fatalf("viewer: %d", r.Code)
	}
}
