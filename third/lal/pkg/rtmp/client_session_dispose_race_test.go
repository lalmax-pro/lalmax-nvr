package rtmp

import (
	"errors"
	"net"
	"testing"

	"github.com/q191201771/lal/pkg/base"
)

// Disposing while the background TCP dial is in flight must never race with
// connection publication or leave a connection alive after Dispose.
func TestClientSessionDisposeDuringConnect(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	for i := 0; i < 40; i++ {
		s := NewClientSession(base.SessionTypeRtmpPush)
		s.urlCtx = base.UrlContext{Scheme: "rtmp", HostWithPort: listener.Addr().String()}
		done := make(chan error, 1)
		go func() { done <- s.tcpConnect() }()
		_ = s.Dispose()
		if err := <-done; err != nil && !errors.Is(err, base.ErrSessionNotStarted) {
			t.Fatalf("connect %d: %v", i, err)
		}
		_ = s.Dispose()
	}
}
