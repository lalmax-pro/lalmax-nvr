package sfubridge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/q191201771/lal/pkg/logic"
)

type managementPolicy struct {
	mu     sync.Mutex
	denied Action
}

func TestRegistryPruneFinishedKeepsActiveAndRecentEntries(t *testing.T) {
	now := time.Now()
	r := &Registry{entries: map[string]*registryEntry{
		"old":    {status: Status{State: StateStopped}, finishedAt: now.Add(-6 * time.Minute)},
		"recent": {status: Status{State: StateFailed}, finishedAt: now.Add(-time.Minute)},
		"active": {status: Status{State: StatePending}},
	}}
	r.PruneFinished(now.Add(-5 * time.Minute))
	if _, ok := r.entries["old"]; ok {
		t.Fatal("old terminal entry was retained")
	}
	if len(r.entries) != 2 || r.entries["recent"] == nil || r.entries["active"] == nil {
		t.Fatalf("unexpected remaining entries: %+v", r.entries)
	}
}

func (p *managementPolicy) allow(_ context.Context, req ManagementRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if req.Principal != "operator" || req.Owner != req.Principal || req.Action == p.denied {
		return errors.New("no")
	}
	return nil
}
func (p *managementPolicy) deny(action Action) { p.mu.Lock(); p.denied = action; p.mu.Unlock() }
func TestRegistryOwnershipAuthorizationAndLifecycle(t *testing.T) {
	service, _, publisher, grants, pub := setup(t)
	policy := &managementPolicy{}
	registry, err := NewRegistry(service, policy.allow, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	status, err := registry.Create(ctx, "operator", pub)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StatePending {
		t.Fatalf("unexpected readiness: %s", status.State)
	}
	select {
	case <-publisher.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not opened")
	}
	if _, err = registry.Get(ctx, "other", status.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user access: %v", err)
	}
	if _, _, err = registry.Playback(ctx, "other", status.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user token: %v", err)
	}
	if err = registry.Stop(ctx, "other", status.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user stop: %v", err)
	}
	policy.deny(ActionPlayback)
	if _, _, err = registry.Playback(ctx, "operator", status.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("playback policy: %v", err)
	}
	policy.deny("")
	stream, token, err := registry.Playback(ctx, "operator", status.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stream != status.StreamID || grants.AuthorizePlayback("rtsp", stream, token) != nil {
		t.Fatal("invalid credential")
	}
	if _, err = registry.Create(ctx, "operator", pub); !errors.Is(err, ErrLimit) {
		t.Fatalf("registry cap: %v", err)
	}
	if err = registry.Remove(ctx, "operator", status.ID); !errors.Is(err, ErrNotFinished) {
		t.Fatalf("removed active bridge: %v", err)
	}
	policy.deny(ActionStop)
	if err = registry.Stop(ctx, "operator", status.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("stop policy: %v", err)
	}
	policy.deny("")
	if err = registry.Stop(ctx, "operator", status.ID); err != nil {
		t.Fatal(err)
	}
	if err = registry.Stop(ctx, "operator", status.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		state, err := registry.Get(ctx, "operator", status.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state.State == StateStopped {
			break
		}
		select {
		case <-deadline:
			t.Fatal("bridge did not stop")
		case <-time.After(time.Millisecond):
		}
	}
	if _, _, err = registry.Playback(ctx, "operator", status.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stopped credential: %v", err)
	}
	if err = grants.AuthorizePlayback("rtsp", stream, token); err == nil {
		t.Fatal("credential not revoked")
	}
	if err = registry.Remove(ctx, "operator", status.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Get(ctx, "operator", status.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestRegistryShutdownRejectsCreate(t *testing.T) {
	service, _, publisher, _, pub := setup(t)
	registry, err := NewRegistry(service, (&managementPolicy{}).allow, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Create(context.Background(), "operator", pub); err != nil {
		t.Fatal(err)
	}
	select {
	case <-publisher.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not opened")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = registry.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Create(ctx, "operator", pub); !errors.Is(err, ErrClosed) {
		t.Fatalf("create after close: %v", err)
	}
}

func TestSlowAuthorizationDoesNotBlockStop(t *testing.T) {
	service, _, publisher, _, pub := setup(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	policy := func(ctx context.Context, req ManagementRequest) error {
		if req.Action == ActionView {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	registry, err := NewRegistry(service, policy, 1)
	if err != nil {
		t.Fatal(err)
	}
	status, err := registry.Create(context.Background(), "operator", pub)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-publisher.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not opened")
	}
	done := make(chan error, 1)
	go func() { _, err := registry.Get(context.Background(), "operator", status.ID); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("policy not called")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- registry.Stop(context.Background(), "operator", status.ID) }()
	select {
	case err = <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow policy blocked stop")
	}
	close(release)
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("view stuck")
	}
}

type failingPublisher struct{}

func (failingPublisher) AddCustomizePubSession(string) (logic.ICustomizePubSessionContext, error) {
	return nil, errors.New("occupied input")
}
func (failingPublisher) DelCustomizePubSession(logic.ICustomizePubSessionContext) {
	panic("must not delete unowned session")
}
func TestRegistryReportsAsynchronousFailure(t *testing.T) {
	service, _, _, grants, pub := setup(t)
	service.publisher = failingPublisher{}
	registry, err := NewRegistry(service, (&managementPolicy{}).allow, 1)
	if err != nil {
		t.Fatal(err)
	}
	status, err := registry.Create(context.Background(), "operator", pub)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		current, err := registry.Get(context.Background(), "operator", status.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == StateFailed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("asynchronous failure not recorded")
		case <-time.After(time.Millisecond):
		}
	}
	if _, _, err = registry.Playback(context.Background(), "operator", status.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if !grants.InternalPlaybackRequired(status.StreamID) {
		t.Fatal("output identity not revoked")
	}
	if err = registry.Remove(context.Background(), "operator", status.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRevalidateOwnersStopsRevokedBridge(t *testing.T) {
	service, _, publisher, grants, pub := setup(t)
	registry, err := NewRegistry(service, (&managementPolicy{}).allow, 2)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	status, err := registry.Create(ctx, "operator", pub)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-publisher.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not opened")
	}
	stream, token, err := registry.Playback(ctx, "operator", status.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = registry.RevalidateOwners(ctx, func(context.Context, string) error { return errors.New("revoked") }); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		current, err := registry.Get(ctx, "operator", status.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == StateStopped {
			break
		}
		select {
		case <-deadline:
			t.Fatal("bridge not stopped")
		case <-time.After(time.Millisecond):
		}
	}
	if err = grants.AuthorizePlayback("rtsp", stream, token); err == nil {
		t.Fatal("revoked credential accepted")
	}
}
