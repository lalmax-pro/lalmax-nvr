package transcode

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeManagedProcess struct {
	done chan struct{}
	once sync.Once
}

func (p *fakeManagedProcess) Wait() error { <-p.done; return nil }
func (p *fakeManagedProcess) Stop(context.Context) error {
	p.once.Do(func() { close(p.done) })
	return nil
}

type fakeExecutor struct {
	starts atomic.Int32
	ready  atomic.Bool
}

func (e *fakeExecutor) Start(context.Context, SourceRef, Profile, string) (Process, error) {
	e.starts.Add(1)
	return &fakeManagedProcess{done: make(chan struct{})}, nil
}
func (e *fakeExecutor) Ready(context.Context, string) bool { return e.ready.Load() }
func managerJob(t *testing.T, id string, p Profile) Job {
	t.Helper()
	return Job{ID: id, Source: SourceRef{EngineID: "embedded", AppName: "live", StreamID: "camera"}, ProfileID: p.ID, ProfileRevision: p.Revision, Enabled: true, RunPolicy: Always, Generation: 1}
}

type failingExecutor struct{}

func (failingExecutor) Start(context.Context, SourceRef, Profile, string) (Process, error) {
	return nil, invalid("test failure")
}
func (failingExecutor) Ready(context.Context, string) bool { return false }

type errorCodeExecutor struct{ err error }

func (e errorCodeExecutor) Start(context.Context, SourceRef, Profile, string) (Process, error) {
	return nil, e.err
}
func (errorCodeExecutor) Ready(context.Context, string) bool { return false }
func TestStartFailureExposesOnlySafeDomainCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"rtmp_disabled", &Error{Code: "rtmp_disabled", Message: "RTMP listener is disabled"}, "rtmp_disabled"},
		{"source_offline", &Error{Code: "source_offline", Message: "source is offline"}, "source_offline"},
		{"raw_error", errors.New("rtsp://user:secret@camera"), "backend_start_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(context.Background(), errorCodeExecutor{tc.err}, 1)
			defer m.Close(context.Background())
			p := testProfile(t)
			r, err := m.Acquire(managerJob(t, "a", p), p)
			if err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			done := m.work[r.Key].done
			m.mu.Unlock()
			<-done
			r, ok := m.Status("a")
			if !ok || r.State != Failed || r.ErrorCode != tc.want {
				t.Fatalf("unexpected runtime: %+v", r)
			}
		})
	}
}
func TestTerminalWorkDoesNotConsumeCapacity(t *testing.T) {
	m := NewManager(context.Background(), failingExecutor{}, 1)
	defer m.Close(context.Background())
	p := testProfile(t)
	first, err := m.Acquire(managerJob(t, "a", p), p)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	done := m.work[first.Key].done
	m.mu.Unlock()
	<-done
	other := p
	other.Video.BitrateKbps++
	if _, err = m.Acquire(managerJob(t, "b", other), other); err != nil {
		t.Fatalf("failed work held quota: %v", err)
	}
}
func TestTerminalSharedWorkRestartsWithOwners(t *testing.T) {
	e := &fakeExecutor{}
	m := NewManager(context.Background(), e, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	defer m.Close(ctx)
	p := testProfile(t)
	r, err := m.Acquire(managerJob(t, "a", p), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Acquire(managerJob(t, "b", p), p); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	old := m.work[r.Key]
	old.cancel()
	m.mu.Unlock()
	<-old.done
	restarted, err := m.Acquire(managerJob(t, "a", p), p)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.VariantID == r.VariantID {
		t.Fatal("restart reused physical stream identity")
	}
	if restarted.Key != r.Key {
		t.Fatal("restart lost effective-work sharing identity")
	}
	if err = m.Release(ctx, "a", 1); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Status("b"); !ok {
		t.Fatal("restart lost other owner")
	}
	m.mu.Lock()
	owners := len(m.work[r.Key].owners)
	m.mu.Unlock()
	if owners != 1 {
		t.Fatal("owner accounting failed")
	}
}

func TestManagerSharesAndReleasesWork(t *testing.T) {
	e := &fakeExecutor{}
	m := NewManager(context.Background(), e, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	defer m.Close(ctx)
	p := testProfile(t)
	a, err := m.Acquire(managerJob(t, "a", p), p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Acquire(managerJob(t, "b", p), p)
	if err != nil {
		t.Fatal(err)
	}
	if a.VariantID != b.VariantID || a.State != Starting {
		t.Fatal("shared work mismatch")
	}
	other := p
	other.Video.BitrateKbps++
	if _, err = m.Acquire(managerJob(t, "c", other), other); err == nil {
		t.Fatal("quota bypass")
	}
	if err = m.Release(ctx, "a", 1); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Status("b"); !ok {
		t.Fatal("other owner lost output")
	}
	e.ready.Store(true)
	deadline := time.After(2 * time.Second)
	for {
		r, _ := m.Status("b")
		if r.State == Running {
			break
		}
		select {
		case <-deadline:
			t.Fatal("never became ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if e.starts.Load() != 1 {
		t.Fatal("duplicate processes")
	}
	if err = m.Release(ctx, "b", 1); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Status("b"); ok {
		t.Fatal("released owner retained")
	}
}

type stalledExecutor struct{}

func (stalledExecutor) Start(ctx context.Context, _ SourceRef, _ Profile, _ string) (Process, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (stalledExecutor) Ready(context.Context, string) bool { return false }
func TestManagerBoundsStalledStart(t *testing.T) {
	m := NewManager(context.Background(), stalledExecutor{}, 1)
	m.startupTimeout = 20 * time.Millisecond
	p := testProfile(t)
	if _, err := m.Acquire(managerJob(t, "stalled", p), p); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		r, _ := m.Status("stalled")
		if r.State == Failed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("start remained stuck")
		case <-time.After(time.Millisecond):
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestManagerRejectsDelayedGenerationOperations(t *testing.T) {
	m := NewManager(context.Background(), &fakeExecutor{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	defer m.Close(ctx)
	p := testProfile(t)
	old := managerJob(t, "versioned", p)
	if _, err := m.Acquire(old, p); err != nil {
		t.Fatal(err)
	}
	if err := m.Release(ctx, old.ID, old.Generation); err != nil {
		t.Fatal(err)
	}
	next := old
	next.Generation = 2
	if _, err := m.Acquire(next, p); err != nil {
		t.Fatal(err)
	}
	if err := m.Release(ctx, old.ID, old.Generation); err == nil {
		t.Fatal("stale release accepted")
	}
	if _, err := m.Acquire(old, p); err == nil {
		t.Fatal("stale acquire accepted")
	}
	if _, ok := m.Status(next.ID); !ok {
		t.Fatal("new generation lost")
	}
}

func TestReleasedSharedOwnerCanBeRecreated(t *testing.T) {
	m := NewManager(context.Background(), &fakeExecutor{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	defer m.Close(ctx)
	p := testProfile(t)
	original := managerJob(t, "recreated", p)
	original.Generation = 5
	runtime, err := m.Acquire(original, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Acquire(managerJob(t, "keeper", p), p); err != nil {
		t.Fatal(err)
	}
	if err = m.Release(ctx, original.ID, original.Generation); err != nil {
		t.Fatal(err)
	}
	recreated := original
	recreated.Generation = 1
	joined, err := m.Acquire(recreated, p)
	if err != nil {
		t.Fatal("released shared owner retained stale generation:", err)
	}
	if joined.VariantID != runtime.VariantID {
		t.Fatal("recreated owner failed to share existing output")
	}
	if _, ok := m.Status("keeper"); !ok {
		t.Fatal("other owner lost its lease")
	}
}

func TestManagerDetectsMediaLossAfterRunning(t *testing.T) {
	e := &fakeExecutor{}
	e.ready.Store(true)
	m := NewManager(context.Background(), e, 1)
	m.healthTimeout = 30 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	defer m.Close(ctx)
	p := testProfile(t)
	if _, err := m.Acquire(managerJob(t, "loss", p), p); err != nil {
		t.Fatal(err)
	}
	await := func(state State) {
		t.Helper()
		deadline := time.NewTimer(time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			r, _ := m.Status("loss")
			if r.State == state {
				return
			}
			select {
			case <-deadline.C:
				t.Fatalf("state never reached %s: %+v", state, r)
			case <-tick.C:
			}
		}
	}
	await(Running)
	e.ready.Store(false)
	await(Failed)
	r, _ := m.Status("loss")
	if r.ErrorCode != "output_timeout" {
		t.Fatalf("unexpected loss reason: %s", r.ErrorCode)
	}
}

func TestManagerAllowsStopBeforeStartingNewGeneration(t *testing.T) {
	m := NewManager(context.Background(), &fakeExecutor{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer m.Close(ctx)
	p := testProfile(t)
	first := managerJob(t, "edit-before-start", p)
	if _, err := m.Acquire(first, p); err != nil {
		t.Fatal(err)
	}
	if err := m.Release(ctx, first.ID, first.Generation); err != nil {
		t.Fatal(err)
	}
	next := first
	next.Generation = 2
	if err := m.Release(ctx, next.ID, next.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire(next, p); err != nil {
		t.Fatal(err)
	}
}

func TestManagerClosePreventsAcquire(t *testing.T) {
	m := NewManager(context.Background(), &fakeExecutor{}, 2)
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := testProfile(t)
	if _, err := m.Acquire(managerJob(t, "x", p), p); err == nil {
		t.Fatal("closed service accepted job")
	}
}
