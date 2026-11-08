package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/sfubridge"
	"github.com/q191201771/lalmax/sfu"
)

func TestBridgeIdempotencyConcurrentRetryAndAuthorization(t *testing.T) {
	room, err := sfu.NewManager().Create(sfu.Key{Namespace: "nvr", ID: "room"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err = room.Join(sfu.Participant{ID: "source"}); err != nil {
		t.Fatal(err)
	}
	pub, err := room.Publish("source", "video", "camera", "video/H264")
	if err != nil {
		t.Fatal(err)
	}
	var revoked atomic.Bool
	router := newNVRSFUBridgeRouter(func(_ context.Context, _ string) error {
		if revoked.Load() {
			return errors.New("revoked")
		}
		return nil
	})
	bridge := &fakeRoomBridge{id: "bridge", start: make(chan struct{}), release: make(chan struct{})}
	if err = router.add(room, bridge); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = router.Close(context.Background()) })

	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := router.CreateIdempotent(ctx, "alice", pub.ID, "", "request-1"); first <- err }()
	<-bridge.start
	if _, err = router.ResolveIdempotent(context.Background(), "alice", "request-1"); !errors.Is(err, sfubridge.ErrRequestPending) {
		t.Fatalf("pending lookup: %v", err)
	}
	if _, err = router.ResolveIdempotent(context.Background(), "bob", "request-1"); !errors.Is(err, sfubridge.ErrNotFound) {
		t.Fatalf("other principal: %v", err)
	}
	if _, err = router.ResolveIdempotent(context.Background(), "alice", "missing"); !errors.Is(err, sfubridge.ErrNotFound) {
		t.Fatalf("missing lookup: %v", err)
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v", err)
	}
	if _, err = router.CreateIdempotent(context.Background(), "alice", "different", "", "request-1"); !errors.Is(err, sfubridge.ErrIdempotencyConflict) {
		t.Fatalf("different payload: %v", err)
	}
	const retries = 20
	results := make(chan error, retries)
	for i := 0; i < retries; i++ {
		go func() {
			status, err := router.CreateIdempotent(context.Background(), "alice", pub.ID, "", "request-1")
			if err == nil && status.ID != "bridge" {
				err = errors.New("incorrect bridge")
			}
			results <- err
		}()
	}
	close(bridge.release)
	for i := 0; i < retries; i++ {
		if err = <-results; err != nil {
			t.Fatalf("retry: %v", err)
		}
	}
	status, err := router.ResolveIdempotent(context.Background(), "alice", "request-1")
	if err != nil || status.ID != "bridge" {
		t.Fatalf("resolved lookup: %+v %v", status, err)
	}
	revoked.Store(true)
	if _, err = router.ResolveIdempotent(context.Background(), "alice", "request-1"); !errors.Is(err, sfubridge.ErrDenied) {
		t.Fatalf("revoked lookup: %v", err)
	}
	if _, err = router.CreateIdempotent(context.Background(), "alice", pub.ID, "", "request-1"); !errors.Is(err, sfubridge.ErrDenied) {
		t.Fatalf("revoked retry: %v", err)
	}
	revoked.Store(false)
	if err = router.remove(context.Background(), room); err != nil {
		t.Fatal(err)
	}
	if _, err = router.CreateIdempotent(context.Background(), "alice", pub.ID, "", "request-1"); !errors.Is(err, sfubridge.ErrNotFound) {
		t.Fatalf("removed room: %v", err)
	}
	if _, err = router.ResolveIdempotent(context.Background(), "alice", "request-1"); !errors.Is(err, sfubridge.ErrNotFound) {
		t.Fatalf("lookup after room removal: %v", err)
	}
}

func TestBridgeIdempotencyRetentionAndCapacity(t *testing.T) {
	router := newNVRSFUBridgeRouter(func(context.Context, string) error { return nil })
	for i := 0; i < maxBridgeRequests; i++ {
		router.requests[bridgeRequestKey{principal: "a", key: string(rune(i))}] = &bridgeRequest{done: make(chan struct{})}
	}
	if _, err := router.CreateIdempotent(context.Background(), "alice", "pub", "", "new"); !errors.Is(err, sfubridge.ErrLimit) {
		t.Fatalf("capacity: %v", err)
	}
	for _, request := range router.requests {
		request.completed = time.Now().Add(-bridgeRequestRetention - time.Second)
		break
	}
	// The expired record is removed, making room; the missing publication then fails.
	if _, err := router.CreateIdempotent(context.Background(), "alice", "pub", "", "new"); !errors.Is(err, sfu.ErrPublicationNotFound) {
		t.Fatalf("expired slot: %v", err)
	}
}

func TestBridgeIdempotencyKeysAreScopedToPrincipal(t *testing.T) {
	router := newNVRSFUBridgeRouter(func(context.Context, string) error { return nil })
	ctx := context.Background()
	if _, err := router.CreateIdempotent(ctx, "alice", "first", "", "same-key"); !errors.Is(err, sfu.ErrPublicationNotFound) {
		t.Fatalf("alice: %v", err)
	}
	if _, err := router.CreateIdempotent(ctx, "bob", "second", "", "same-key"); !errors.Is(err, sfu.ErrPublicationNotFound) {
		t.Fatalf("bob's independent key: %v", err)
	}
	if _, err := router.CreateIdempotent(ctx, "alice", "second", "", "same-key"); !errors.Is(err, sfubridge.ErrIdempotencyConflict) {
		t.Fatalf("alice's conflicting retry: %v", err)
	}
}

func TestBridgeRequestLookupDoesNotCreateAndExpires(t *testing.T) {
	router := newNVRSFUBridgeRouter(func(context.Context, string) error { return nil })
	ctx := context.Background()
	if _, err := router.ResolveIdempotent(ctx, "alice", "unknown"); !errors.Is(err, sfubridge.ErrNotFound) {
		t.Fatalf("unknown lookup: %v", err)
	}
	if len(router.requests) != 0 {
		t.Fatal("lookup created a request")
	}
	done := make(chan struct{})
	close(done)
	router.requests[bridgeRequestKey{"alice", "expired"}] = &bridgeRequest{done: done, completed: time.Now().Add(-bridgeRequestRetention - time.Second)}
	if _, err := router.ResolveIdempotent(ctx, "alice", "expired"); !errors.Is(err, sfubridge.ErrNotFound) {
		t.Fatalf("expired lookup: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := router.ResolveIdempotent(canceled, "alice", "expired"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup: %v", err)
	}
}
