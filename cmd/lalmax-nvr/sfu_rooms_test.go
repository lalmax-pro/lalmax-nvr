package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/sfubridge"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
)

func TestNVRSFURoomAdministration(t *testing.T) {
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "nvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Init(ctx); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "admin", PasswordHash: "hash", Role: model.RoleSuperAdmin, Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	principal := fmt.Sprintf("user:%d", user.ID)
	manager := sfu.NewManager()
	defaultRoom, err := manager.CreateWithLimits(nvrSFURoom, nvrSFURoomLimits)
	if err != nil {
		t.Fatal(err)
	}
	policy := &nvrSFUPolicy{app: &App{db: db, cfg: &config.Config{}}, generation: defaultRoom.Snapshot().Generation, rooms: manager}
	admin := newNVRSFURoomAdmin(manager, defaultRoom, policy)
	tickets := newNVRSFUTickets(policy)
	tickets.resolveRoom = admin.resolve
	if _, err = admin.CreateRoom(ctx, "user:999999", "room"); !errors.Is(err, sfubridge.ErrDenied) {
		t.Fatalf("unauthorized creation: %v", err)
	}
	for _, id := range []string{"", "a/b", "a b"} {
		if _, err = admin.CreateRoom(ctx, principal, id); !errors.Is(err, sfu.ErrInvalidID) {
			t.Fatalf("invalid %q: %v", id, err)
		}
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := admin.CreateRoom(ctx, principal, "room")
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, sfu.ErrRoomExists) {
				t.Errorf("create: %v", err)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("created %d rooms", successes.Load())
	}
	rooms, err := admin.ListRooms(ctx, principal)
	if err != nil || len(rooms) != 2 {
		t.Fatalf("rooms: %v, %v", rooms, err)
	}
	key, old, ok := admin.resolve("room")
	if !ok {
		t.Fatal("missing room")
	}
	ticket, err := tickets.IssueForRoom(ctx, user, "room")
	if err != nil {
		t.Fatal(err)
	}
	if err = admin.CloseRoom(ctx, principal, "default", defaultRoom.Snapshot().Generation); !errors.Is(err, sfu.ErrInvalidID) {
		t.Fatalf("closed default: %v", err)
	}
	if err = admin.CloseRoom(ctx, principal, "room", old); err != nil {
		t.Fatal(err)
	}
	if _, err = tickets.IssueForRoom(ctx, user, "room"); !errors.Is(err, sfu.ErrRoomNotFound) {
		t.Fatalf("issued ticket for closed room: %v", err)
	}
	fresh, err := admin.CreateRoom(ctx, principal, "room")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Generation == old {
		t.Fatal("generation reused")
	}
	if err = admin.CloseRoom(ctx, principal, "room", old); !errors.Is(err, sfu.ErrRoomNotFound) {
		t.Fatalf("stale close: %v", err)
	}
	identity, err := tickets.consume(ctx, ticket)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Room != key || identity.RoomGeneration != old {
		t.Fatal("ticket scope changed")
	}
	req := access.Request{Principal: principal, Room: key, RoomGeneration: old, ParticipantID: "p", Operation: access.Join}
	if err = policy.Authorize(ctx, req); err == nil {
		t.Fatal("old generation authorized")
	}
	req.RoomGeneration = fresh.Generation
	if err = policy.Authorize(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err = admin.closeAll(); err != nil {
		t.Fatal(err)
	}
	if _, _, ok = admin.resolve("room"); ok {
		t.Fatal("room survived shutdown")
	}
}
