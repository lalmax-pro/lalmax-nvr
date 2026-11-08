package sfubridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// registryTestService models lifecycle completion independently of the SFU
// implementation; the embedded LiveKit tests exercise actual media and grants.
type registryTestService struct {
	mu      sync.Mutex
	closed  bool
	next    int
	cancels []context.CancelFunc
	wg      sync.WaitGroup
	fail    chan struct{}
}

func (s *registryTestService) Start(ctx context.Context, _, _ string) (*Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	s.next++
	id := fmt.Sprintf("stream-%d", s.next)
	life, cancel := context.WithCancel(context.Background())
	s.cancels = append(s.cancels, cancel)
	s.wg.Add(1)
	s.mu.Unlock()
	done := make(chan error, 1)
	handle, err := NewHandle(id, "secret", done, cancel)
	if err != nil {
		s.wg.Done()
		cancel()
		return nil, err
	}
	go func() {
		defer s.wg.Done()
		select {
		case <-life.Done():
			done <- life.Err()
		case <-s.fail:
			done <- errors.New("source failed")
		}
	}()
	return handle, nil
}
func (s *registryTestService) StartAV(ctx context.Context, p, v, _ string) (*Handle, error) {
	return s.Start(ctx, p, v)
}
func (s *registryTestService) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	for _, cancel := range s.cancels {
		cancel()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func waitRegistryState(t *testing.T, r *Registry, id string, want State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, err := r.Get(context.Background(), "owner", id)
		if err == nil && status.State == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("bridge %s did not reach %s", id, want)
}

func TestRegistryOwnershipLifecycleAndRevocation(t *testing.T) {
	service := &registryTestService{fail: make(chan struct{})}
	r, err := NewRegistry(service, func(_ context.Context, request ManagementRequest) error {
		if request.Principal != request.Owner {
			return errors.New("denied")
		}
		return nil
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	ctx := context.Background()
	status, err := r.CreateAV(ctx, "owner", "video", "audio")
	if err != nil || status.AudioPublicationID != "audio" {
		t.Fatalf("create: %+v %v", status, err)
	}
	if _, err = r.Get(ctx, "other", status.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user get: %v", err)
	}
	if _, _, err = r.Playback(ctx, "other", status.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user playback: %v", err)
	}
	if err = r.Stop(ctx, "other", status.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user stop: %v", err)
	}
	if _, err = r.Create(ctx, "owner", "other"); !errors.Is(err, ErrLimit) {
		t.Fatalf("limit: %v", err)
	}
	if err = r.Remove(ctx, "owner", status.ID); !errors.Is(err, ErrNotFinished) {
		t.Fatalf("remove active: %v", err)
	}
	if stream, token, err := r.Playback(ctx, "owner", status.ID); err != nil || stream != status.ID || token != "secret" {
		t.Fatalf("playback: %q %q %v", stream, token, err)
	}
	if err = r.RevalidateOwners(ctx, func(context.Context, string) error { return errors.New("revoked") }); err != nil {
		t.Fatal(err)
	}
	waitRegistryState(t, r, status.ID, StateStopped)
	if _, _, err = r.Playback(ctx, "owner", status.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stopped token: %v", err)
	}
	if err = r.Stop(ctx, "owner", status.ID); err != nil {
		t.Fatalf("repeat stop: %v", err)
	}
	if err = r.Remove(ctx, "owner", status.ID); err != nil {
		t.Fatal(err)
	}
	if !r.Empty() {
		t.Fatal("removed registry not empty")
	}
}

func TestRegistryReportsFailureAndPrunes(t *testing.T) {
	service := &registryTestService{fail: make(chan struct{})}
	r, err := NewRegistry(service, func(context.Context, ManagementRequest) error { return nil }, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	status, err := r.Create(context.Background(), "owner", "video")
	if err != nil {
		t.Fatal(err)
	}
	close(service.fail)
	waitRegistryState(t, r, status.ID, StateFailed)
	if _, _, err = r.Playback(context.Background(), "owner", status.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed token: %v", err)
	}
	if removed := r.PruneFinishedIDs(time.Now().Add(time.Second)); len(removed) != 1 || removed[0] != status.ID || !r.Empty() {
		t.Fatalf("prune: %v", removed)
	}
}

func TestSlowAuthorizationDoesNotBlockStop(t *testing.T) {
	service := &registryTestService{fail: make(chan struct{})}
	entered, release := make(chan struct{}), make(chan struct{})
	r, err := NewRegistry(service, func(ctx context.Context, request ManagementRequest) error {
		if request.Action == ActionView {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	status, err := r.Create(context.Background(), "owner", "video")
	if err != nil {
		t.Fatal(err)
	}
	viewDone := make(chan error, 1)
	go func() { _, err := r.Get(context.Background(), "owner", status.ID); viewDone <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("authorization not called")
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- r.Stop(context.Background(), "owner", status.ID) }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stop blocked on authorization")
	}
	close(release)
	select {
	case err := <-viewDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("view did not finish")
	}
}

func TestListOmitsEntryRemovedDuringAuthorization(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	r := &Registry{entries: map[string]*registryEntry{
		"bridge": {owner: "owner", status: Status{ID: "bridge", State: StateStopped}},
	}, authorize: func(ctx context.Context, request ManagementRequest) error {
		if request.Action == ActionView {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}}
	type result struct {
		items []Status
		err   error
	}
	done := make(chan result, 1)
	go func() { items, err := r.List(context.Background(), "owner"); done <- result{items, err} }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("view authorization not reached")
	}
	if err := r.Remove(context.Background(), "owner", "bridge"); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case got := <-done:
		if got.err != nil || len(got.items) != 0 {
			t.Fatalf("removed entry leaked: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("list did not finish")
	}
}

type delayedBridgeService struct {
	started, release, canceled chan struct{}
}

func (s *delayedBridgeService) Start(context.Context, string, string) (*Handle, error) {
	close(s.started)
	<-s.release
	return NewHandle("late", "secret", make(chan error, 1), func() { close(s.canceled) })
}
func (s *delayedBridgeService) StartAV(ctx context.Context, p, v, _ string) (*Handle, error) {
	return s.Start(ctx, p, v)
}
func (*delayedBridgeService) Close(context.Context) error { return nil }

func TestCreationRacingCloseReleasesLateHandle(t *testing.T) {
	service := &delayedBridgeService{started: make(chan struct{}), release: make(chan struct{}), canceled: make(chan struct{})}
	r, err := NewRegistry(service, func(context.Context, ManagementRequest) error { return nil }, 1)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := r.Create(context.Background(), "owner", "track"); result <- err }()
	select {
	case <-service.started:
	case <-time.After(3 * time.Second):
		t.Fatal("creation did not start")
	}
	if err := r.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(service.release)
	select {
	case err := <-result:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("late creation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("late creation did not finish")
	}
	select {
	case <-service.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("late handle not canceled")
	}
}
