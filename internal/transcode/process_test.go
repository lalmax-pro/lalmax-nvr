package transcode

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestBackendHelper(t *testing.T) {
	if os.Getenv("TRANSCODE_HELPER") != "1" {
		return
	}
	// Invoked only in a child process; finite wait avoids leaking on test failure.
	time.Sleep(30 * time.Second)
	os.Exit(0)
}
func TestCommandProcessStopAndWait(t *testing.T) {
	t.Setenv("TRANSCODE_HELPER", "1")
	p, err := startCommand(context.Background(), os.Args[0], []string{"-test.run=^TestBackendHelper$"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err = p.Wait(); err == nil {
		t.Fatal("terminated process should report failure")
	}
	if err = p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestCommandProcessCancellation(t *testing.T) {
	t.Setenv("TRANSCODE_HELPER", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := startCommand(ctx, os.Args[0], []string{"-test.run=^TestBackendHelper$"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit")
	}
}
func TestCommandProcessSafeError(t *testing.T) {
	_, err := startCommand(context.Background(), "/nonexistent/backend-password", nil)
	if err == nil {
		t.Fatal("expected failure")
	}
	if err.Error() != "process_start_failed: could not start media backend; check executable and permissions" {
		t.Fatalf("unexpected error %v", err)
	}
}
