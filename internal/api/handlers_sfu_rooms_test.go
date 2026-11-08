package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/q191201771/lalmax/sfu"
)

type testRoomManager struct{ principal, id, generation string }

func (m *testRoomManager) ListRooms(_ context.Context, p string) ([]sfu.Snapshot, error) {
	m.principal = p
	return []sfu.Snapshot{{Key: sfu.Key{ID: "room"}, Generation: "gen", Participants: []sfu.Participant{{ID: "secret"}, {ID: "bridge_worker"}}}}, nil
}
func (m *testRoomManager) CreateRoom(_ context.Context, p, id string) (sfu.Snapshot, error) {
	m.principal = p
	m.id = id
	return sfu.Snapshot{Key: sfu.Key{ID: id}, Generation: "gen"}, nil
}
func (m *testRoomManager) CloseRoom(_ context.Context, p, id, gen string) error {
	m.principal = p
	m.id = id
	m.generation = gen
	return nil
}

func TestSFURoomRoutes(t *testing.T) {
	m := &testRoomManager{}
	h := NewHandler(nil, nil, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMultiUserAuthMW(middleware.InjectUser(&model.User{ID: 42, Role: model.RoleSuperAdmin, Enabled: true}))
	h.SetSFURoomManager(m)
	router := h.Routes()
	rec := sfuRequest(router, "POST", "/api/sfu/rooms/", `{"id":"room"}`, "application/json")
	if rec.Code != http.StatusCreated || m.principal != "user:42" || m.id != "room" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	rec = sfuRequest(router, "GET", "/api/sfu/rooms/", "", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "secret") || !strings.Contains(rec.Body.String(), `"participants":1`) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	rec = sfuRequest(router, "DELETE", "/api/sfu/rooms/room?generation=gen", "", "")
	if rec.Code != http.StatusNoContent || m.id != "room" || m.generation != "gen" {
		t.Fatalf("close: %d", rec.Code)
	}
	viewer := NewHandler(nil, nil, noopAuthMW(), nil, nil, "", nil, nil)
	viewer.SetMultiUserAuthMW(middleware.InjectUser(&model.User{ID: 1, Role: model.RoleUser, Enabled: true}))
	viewer.SetSFURoomManager(m)
	rec = sfuRequest(viewer.Routes(), "GET", "/api/sfu/rooms/", "", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer: %d", rec.Code)
	}
}
