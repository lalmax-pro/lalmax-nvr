package livekitsfu

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRuntime struct {
	started, ready, stop chan struct{}
	running              atomic.Bool
}

func (f *fakeRuntime) Start() error {
	close(f.started)
	<-f.ready
	f.running.Store(true)
	<-f.stop
	f.running.Store(false)
	return nil
}
func (f *fakeRuntime) Stop(bool)       { close(f.stop) }
func (f *fakeRuntime) IsRunning() bool { return f.running.Load() }

func TestRunStopsAfterCancelledStartup(t *testing.T) {
	f := &fakeRuntime{started: make(chan struct{}), ready: make(chan struct{}), stop: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, f) }()
	<-f.started
	cancel()
	select {
	case <-f.stop:
		t.Fatal("stopped before the server was ready")
	default:
	}
	close(f.ready)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestRunDoesNotStartCancelledContext(t *testing.T) {
	f := &fakeRuntime{started: make(chan struct{}), ready: make(chan struct{}), stop: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, f); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-f.started:
		t.Fatal("started after cancellation")
	default:
	}
}
