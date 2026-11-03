package rtsp

import (
	"errors"
	"net"
	"testing"
)

// ServerManager shutdown can race the RTSP listener's startup. A server
// disposed during Listen must never publish a live socket after shutdown,
// and a concurrently closing listener must not race the RunLoop reader.
func TestServerDisposeDuringListen(t *testing.T) {
	for i := 0; i < 25; i++ {
		server := NewServer("127.0.0.1:0", nil, ServerAuthConfig{})
		started := make(chan error, 1)
		go func() { started <- server.Listen() }()
		server.Dispose()
		if err := <-started; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
		// A closed server can never accept or rebind a listener.
		if err := server.RunLoop(); err == nil {
			t.Fatal("disposed RTSP listener still accepts connections")
		}
		if err := server.Listen(); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("disposed server allowed new listener: %v", err)
		}
		server.Dispose()
	}
}

func TestServerListenRunLoopAndDispose(t *testing.T) {
	server := NewServer("127.0.0.1:0", nil, ServerAuthConfig{})
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	running := make(chan error, 1)
	go func() { running <- server.RunLoop() }()
	server.Dispose()
	if err := <-running; err == nil {
		t.Fatal("closed RTSP listener did not stop the accept loop")
	}
}
