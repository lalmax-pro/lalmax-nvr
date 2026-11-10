package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/sfubridge"
	"github.com/q191201771/lalmax/sfu"
)

type fakeRoomBridge struct {
	mu      sync.Mutex
	owner   string
	id      string
	closed  bool
	start   chan struct{}
	release chan struct{}
}

func (f *fakeRoomBridge) Create(_ context.Context, p, pub string) (sfubridge.Status, error) {
	if f.start != nil {
		close(f.start)
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return sfubridge.Status{}, sfubridge.ErrClosed
	}
	f.owner = p
	return sfubridge.Status{ID: f.id, PublicationID: pub}, nil
}
func (f *fakeRoomBridge) CreateAV(ctx context.Context, p, v, a string) (sfubridge.Status, error) {
	return f.Create(ctx, p, v)
}
func (f *fakeRoomBridge) List(_ context.Context, p string) ([]sfubridge.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owner != p || f.id == "" {
		return []sfubridge.Status{}, nil
	}
	return []sfubridge.Status{{ID: f.id}}, nil
}
func (f *fakeRoomBridge) Get(_ context.Context, p, id string) (sfubridge.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != f.id || p != f.owner {
		return sfubridge.Status{}, sfubridge.ErrNotFound
	}
	return sfubridge.Status{ID: id}, nil
}
func (f *fakeRoomBridge) Playback(ctx context.Context, p, id string) (string, string, error) {
	if _, err := f.Get(ctx, p, id); err != nil {
		return "", "", err
	}
	return id, "token", nil
}
func (f *fakeRoomBridge) Stop(ctx context.Context, p, id string) error {
	_, err := f.Get(ctx, p, id)
	return err
}
func (f *fakeRoomBridge) Remove(ctx context.Context, p, id string) error { return f.Stop(ctx, p, id) }
func (f *fakeRoomBridge) Close(context.Context) error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}
func (f *fakeRoomBridge) PruneFinished(time.Time) {}
func (f *fakeRoomBridge) RevalidateOwners(context.Context, func(context.Context, string) error) error {
	return nil
}
func TestNVRSFUBridgeRouterRoomIsolationAndCloseRace(t *testing.T) {
	ctx := context.Background()
	manager := sfu.NewManager()
	r1, _ := manager.Create(sfu.Key{Namespace: "nvr", ID: "one"}, 4)
	r2, _ := manager.Create(sfu.Key{Namespace: "nvr", ID: "two"}, 4)
	if err := r1.Join(sfu.Participant{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := r2.Join(sfu.Participant{ID: "b"}); err != nil {
		t.Fatal(err)
	}
	p1, err := r1.Publish("a", "video", "camera", "video/H264")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := r2.Publish("b", "video", "camera", "video/H264")
	if err != nil {
		t.Fatal(err)
	}
	router := newNVRSFUBridgeRouter(func(_ context.Context, p string) error {
		if p != "admin" {
			return errors.New("denied")
		}
		return nil
	})
	one := &fakeRoomBridge{id: "one"}
	two := &fakeRoomBridge{id: "two", start: make(chan struct{}), release: make(chan struct{})}
	if err = router.add(r1, one); err != nil {
		t.Fatal(err)
	}
	if err = router.add(r2, two); err != nil {
		t.Fatal(err)
	}
	if _, err = router.Create(ctx, "bad", p1.ID); !errors.Is(err, sfubridge.ErrDenied) {
		t.Fatalf("unauthorized create: %v", err)
	}
	status, err := router.Create(ctx, "admin", p1.ID)
	if err != nil || status.ID != "one" {
		t.Fatalf("route: %+v %v", status, err)
	}
	if _, err = router.Get(ctx, "other", "one"); !errors.Is(err, sfubridge.ErrNotFound) {
		t.Fatalf("wrong owner: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := router.Create(ctx, "admin", p2.ID); done <- err }()
	<-two.start
	if err = router.remove(ctx, r2); err != nil {
		t.Fatal(err)
	}
	close(two.release)
	if err = <-done; !errors.Is(err, sfubridge.ErrClosed) {
		t.Fatalf("concurrent close: %v", err)
	}
	if _, err = router.Get(ctx, "admin", "one"); err != nil {
		t.Fatal(err)
	}
	if err = router.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestNVRSFUBridgeRouterListsOnlyAuthorizedOwnerAcrossRooms(t *testing.T) {
	ctx := context.Background()
	manager := sfu.NewManager()
	a, _ := manager.Create(sfu.Key{Namespace: "nvr", ID: "a"}, 4)
	b, _ := manager.Create(sfu.Key{Namespace: "nvr", ID: "b"}, 4)
	c, _ := manager.Create(sfu.Key{Namespace: "nvr", ID: "c"}, 4)
	router := newNVRSFUBridgeRouter(func(_ context.Context, p string) error {
		if p == "revoked" {
			return errors.New("revoked")
		}
		return nil
	})
	for _, item := range []struct {
		room  *sfu.Room
		entry *fakeRoomBridge
	}{{a, &fakeRoomBridge{owner: "alice", id: "z"}}, {b, &fakeRoomBridge{owner: "alice", id: "a"}}, {c, &fakeRoomBridge{owner: "bob", id: "b"}}} {
		if err := router.add(item.room, item.entry); err != nil {
			t.Fatal(err)
		}
	}
	items, err := router.List(ctx, "alice")
	if err != nil || len(items) != 2 || items[0].ID != "a" || items[1].ID != "z" {
		t.Fatalf("alice's rooms: %+v, %v", items, err)
	}
	items, err = router.List(ctx, "bob")
	if err != nil || len(items) != 1 || items[0].ID != "b" {
		t.Fatalf("bob's rooms: %+v, %v", items, err)
	}
	if _, err := router.List(ctx, "revoked"); !errors.Is(err, sfubridge.ErrDenied) {
		t.Fatalf("revoked user: %v", err)
	}
	if err := router.remove(ctx, a); err != nil {
		t.Fatal(err)
	}
	items, err = router.List(ctx, "alice")
	if err != nil || len(items) != 1 || items[0].ID != "a" {
		t.Fatalf("removed room retained: %+v, %v", items, err)
	}
	if err = router.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = router.List(ctx, "alice"); !errors.Is(err, sfubridge.ErrClosed) {
		t.Fatalf("closed router: %v", err)
	}
}

type blockingListBridge struct {
	*fakeRoomBridge
	entered, release chan struct{}
}

func (f *blockingListBridge) List(ctx context.Context, p string) ([]sfubridge.Status, error) {
	close(f.entered)
	select {
	case <-f.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return f.fakeRoomBridge.List(ctx, p)
}

func TestNVRSFUBridgeRouterOmitsRoomRemovedDuringList(t *testing.T) {
	room, _ := sfu.NewManager().Create(sfu.Key{Namespace: "nvr", ID: "room"}, 4)
	bridge := &blockingListBridge{fakeRoomBridge: &fakeRoomBridge{owner: "alice", id: "bridge"}, entered: make(chan struct{}), release: make(chan struct{})}
	router := newNVRSFUBridgeRouter(func(context.Context, string) error { return nil })
	if err := router.add(room, bridge); err != nil {
		t.Fatal(err)
	}
	type listResult struct {
		items []sfubridge.Status
		err   error
	}
	result := make(chan listResult, 1)
	go func() { items, err := router.List(context.Background(), "alice"); result <- listResult{items, err} }()
	select {
	case <-bridge.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("list did not reach room registry")
	}
	if err := router.remove(context.Background(), room); err != nil {
		t.Fatal(err)
	}
	close(bridge.release)
	select {
	case got := <-result:
		if got.err != nil || len(got.items) != 0 {
			t.Fatalf("removed room leaked: %+v, %v", got.items, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("list blocked after room removal")
	}
}

type delayedRoomBridge struct {
	*fakeRoomBridge
	release <-chan struct{}
}

func (f *delayedRoomBridge) Close(ctx context.Context) error {
	select {
	case <-f.release:
		return f.fakeRoomBridge.Close(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestNVRSFUBridgeRouterRetriesTimedOutCleanup(t *testing.T) {
	room, err := sfu.NewManager().Create(sfu.Key{Namespace: "nvr", ID: "delayed"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	registry := &delayedRoomBridge{fakeRoomBridge: &fakeRoomBridge{}, release: release}
	router := newNVRSFUBridgeRouter(func(context.Context, string) error { return nil })
	if err = router.add(room, registry); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = router.remove(ctx, room); !errors.Is(err, context.Canceled) {
		t.Fatalf("close: %v", err)
	}
	router.mu.Lock()
	pending := len(router.retiring)
	router.mu.Unlock()
	if pending != 1 {
		t.Fatalf("lost pending cleanup: %d", pending)
	}
	close(release)
	if err = router.RevalidateOwners(context.Background(), func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	router.mu.Lock()
	pending = len(router.retiring)
	router.mu.Unlock()
	if pending != 0 {
		t.Fatalf("cleanup not released: %d", pending)
	}
	if err = router.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
