package sfubridge

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/q191201771/lalmax/sfu/access"
)

func TestPersistentOutputRestoredAsDeniedAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nvr.db")
	db, err := storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Init(ctx); err != nil {
		t.Fatal(err)
	}
	original, policy, publisher, _, publication := setup(t)
	grants := auth.NewMediaAuthorizer()
	service, err := NewPersistent(ctx, original.room, policy, publisher, grants, original.validate, 1, db)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := service.Start(ctx, "operator", publication)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-publisher.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not started")
	}
	if err = grants.AuthorizePlayback("rtsp", handle.StreamID, handle.PlaybackToken); err != nil {
		t.Fatal(err)
	}
	handle.Stop()
	select {
	case <-handle.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not stopped")
	}
	if err = service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Init(ctx); err != nil {
		t.Fatal(err)
	}
	restored := auth.NewMediaAuthorizer()
	reopened, err := NewPersistent(ctx, original.room, policy, publisher, restored, original.validate, 1, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(ctx)
	if !restored.InternalPlaybackRequired(handle.StreamID) {
		t.Fatal("retired identity not restored")
	}
	if err = restored.AuthorizePlayback("rtsp", handle.StreamID, handle.PlaybackToken); err == nil {
		t.Fatal("old playback token accepted")
	}
	if err = restored.AuthorizePublish("rtmp", handle.StreamID, ""); err == nil {
		t.Fatal("anonymous publication allowed")
	}
}

type failingStore struct{ listErr, registerErr error }

func (s failingStore) ListSFUBridgeOutputIDs(context.Context) ([]string, error) {
	return nil, s.listErr
}
func (s failingStore) RegisterSFUBridgeOutput(context.Context, string) error { return s.registerErr }
func TestPersistentStorageFailureFailsClosed(t *testing.T) {
	original, policy, publisher, grants, publication := setup(t)
	want := errors.New("storage unavailable")
	if _, err := NewPersistent(context.Background(), original.room, policy, publisher, grants, original.validate, 1, failingStore{listErr: want}); !errors.Is(err, want) {
		t.Fatalf("list: %v", err)
	}
	service, err := NewPersistent(context.Background(), original.room, policy, publisher, grants, original.validate, 1, failingStore{registerErr: want})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	if _, err = service.Start(context.Background(), "operator", publication); !errors.Is(err, want) {
		t.Fatalf("register: %v", err)
	}
	select {
	case <-publisher.opened:
		t.Fatal("publisher started without identity persistence")
	default:
	}
}

type countingStore struct{ calls int }

func (s *countingStore) ListSFUBridgeOutputIDs(context.Context) ([]string, error) { return nil, nil }
func (s *countingStore) RegisterSFUBridgeOutput(context.Context, string) error    { s.calls++; return nil }
func TestDeniedSourceDoesNotPersistAnOutput(t *testing.T) {
	original, policy, publisher, grants, publication := setup(t)
	store := &countingStore{}
	svc, err := NewPersistent(context.Background(), original.room, policy, publisher, grants, original.validate, 1, store)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close(context.Background())
	policy.mu.Lock()
	policy.deny = access.Subscribe
	policy.mu.Unlock()
	if _, err = svc.Start(context.Background(), "operator", publication); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("denied subscription: %v", err)
	}
	if store.calls != 0 {
		t.Fatalf("unauthorized request persisted %d identities", store.calls)
	}
}
