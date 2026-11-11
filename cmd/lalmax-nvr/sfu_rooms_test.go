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
	router := newNVRSFUBridgeRouter(policy.check)
	admin.bridges = router
	admin.newBridge = func(context.Context, *sfu.Room) (nvrRoomBridge, error) { return &fakeRoomBridge{}, nil }
	if err := router.add(defaultRoom, &fakeRoomBridge{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = router.Close(context.Background()) })
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
	oldRoom, _ := manager.Get(key)
	router.mu.Lock()
	oldBridge := router.entries[oldRoom].registry.(*fakeRoomBridge)
	router.mu.Unlock()
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
	oldBridge.mu.Lock()
	closed := oldBridge.closed
	oldBridge.mu.Unlock()
	if !closed {
		t.Fatal("closing room did not close its bridge registry")
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
	if _, err = tickets.IssueForRoomGeneration(ctx, user, "room", old); !errors.Is(err, sfu.ErrRoomNotFound) {
		t.Fatalf("ticket issued for stale room selection: %v", err)
	}
	if _, err = tickets.IssueForRoomGeneration(ctx, user, "room", fresh.Generation); err != nil {
		t.Fatalf("ticket for current selection: %v", err)
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
	admin.newBridge = func(context.Context, *sfu.Room) (nvrRoomBridge, error) {
		return nil, errors.New("bridge setup failed")
	}
	if _, err = admin.CreateRoom(ctx, principal, "setup-fails"); err == nil {
		t.Fatal("room created after bridge setup failure")
	}
	if _, ok := manager.Get(sfu.Key{Namespace: nvrSFURoom.Namespace, ID: "setup-fails"}); ok {
		t.Fatal("failed room was not rolled back")
	}
	if err = admin.closeAll(); err != nil {
		t.Fatal(err)
	}
	if _, _, ok = admin.resolve("room"); ok {
		t.Fatal("room survived shutdown")
	}
}

func TestNVRSFURoomIdleCleanupPreservesDefaultAndActive(t *testing.T) {
	ctx := context.Background()
	manager := sfu.NewManager()
	defaultRoom, err := manager.CreateWithLimits(nvrSFURoom, nvrSFURoomLimits)
	if err != nil {
		t.Fatal(err)
	}
	admin := newNVRSFURoomAdmin(manager, defaultRoom, nil)
	router := newNVRSFUBridgeRouter(func(context.Context, string) error { return nil })
	admin.bridges = router
	for _, id := range []string{"default", "idle", "active"} {
		var room *sfu.Room
		if id == "default" {
			room = defaultRoom
		} else {
			room, err = manager.CreateWithLimits(sfu.Key{Namespace: "nvr", ID: id}, nvrSFURoomLimits)
			if err != nil {
				t.Fatal(err)
			}
			admin.entries[id] = room
		}
		if err = router.add(room, &fakeRoomBridge{}); err != nil {
			t.Fatal(err)
		}
	}
	active := admin.entries["active"]
	if err = active.Join(sfu.Participant{ID: "member"}); err != nil {
		t.Fatal(err)
	}
	old := admin.entries["idle"]
	router.mu.Lock()
	oldRegistry := router.entries[old].registry.(*fakeRoomBridge)
	router.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	if err = admin.collectIdle(ctx, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := admin.resolve("idle"); ok {
		t.Fatal("idle room survived")
	}
	if _, _, ok := admin.resolve("default"); !ok {
		t.Fatal("default room collected")
	}
	if _, _, ok := admin.resolve("active"); !ok {
		t.Fatal("active room collected")
	}
	oldRegistry.mu.Lock()
	closed := oldRegistry.closed
	oldRegistry.mu.Unlock()
	if !closed {
		t.Fatal("idle bridge registry survived")
	}
	if err = admin.closeAll(); err != nil {
		t.Fatal(err)
	}
	if err = router.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestNVRSFURoomAllowsFourSourcesAndEnforcesLimit(t *testing.T) {
	room, err := sfu.NewManager().CreateWithLimits(sfu.Key{Namespace: "test", ID: "screen-av"}, nvrSFURoomLimits)
	if err != nil {
		t.Fatal(err)
	}
	if err = room.Join(sfu.Participant{ID: "publisher"}); err != nil {
		t.Fatal(err)
	}
	sources := []struct{ kind, source, codec string }{
		{"audio", "microphone", "audio/opus"}, {"video", "camera", "video/H264"},
		{"video", "screen", "video/H264"}, {"audio", "screen", "audio/opus"},
	}
	var screenAudio string
	for _, source := range sources {
		publication, err := room.Publish("publisher", source.kind, source.source, source.codec)
		if err != nil {
			t.Fatalf("publish %s/%s: %v", source.kind, source.source, err)
		}
		if source.kind == "audio" && source.source == "screen" {
			screenAudio = publication.ID
		}
	}
	if _, err = room.Publish("publisher", "audio", "screen", "audio/opus"); !errors.Is(err, sfu.ErrPublicationLimit) {
		t.Fatalf("fifth publication: %v", err)
	}
	if err = room.Unpublish("publisher", screenAudio); err != nil {
		t.Fatal(err)
	}
	if _, err = room.Publish("publisher", "audio", "screen", "audio/opus"); err != nil {
		t.Fatalf("slot not released: %v", err)
	}
}
