package livekitsfu

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// A canceled HTTP setup must not leave a bridge connection attempt running.
func TestOpenTracksHonorsSetupCancellation(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); <-stop }()
		}
	}()
	setup, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = OpenTracks(setup, context.Background(), "http://"+listener.Addr().String(), "invalid-token", TrackRequest{RoomSID: "RM_test", AudioSID: "TR_test"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected error: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("connection attempt did not honor its deadline")
	}
}
