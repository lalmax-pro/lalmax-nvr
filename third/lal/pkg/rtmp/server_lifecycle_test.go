package rtmp

import (
	"errors"
	"net"
	"sync"
	"testing"
)

func TestServerListenDisposeRace(t *testing.T) {
	for _, tc := range []struct {
		name   string
		listen func(*Server) error
	}{
		{"tcp", func(s *Server) error { return s.Listen() }},
		{"tls", func(s *Server) error { return s.ListenWithTLS("../../conf/cert.pem", "../../conf/key.pem") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < 100; i++ {
				server := NewServer("127.0.0.1:0", nil)
				var wg sync.WaitGroup
				wg.Add(2)
				start := make(chan struct{})
				var listenErr error
				go func() { defer wg.Done(); <-start; listenErr = tc.listen(server) }()
				go func() { defer wg.Done(); <-start; server.Dispose() }()
				close(start)
				wg.Wait()
				if listenErr != nil && !errors.Is(listenErr, net.ErrClosed) {
					t.Fatalf("listen: %v", listenErr)
				}
				if err := server.RunLoop(); !errors.Is(err, net.ErrClosed) {
					t.Fatalf("closed run loop: %v", err)
				}
				if err := tc.listen(server); !errors.Is(err, net.ErrClosed) {
					t.Fatalf("listen after dispose: %v", err)
				}
				server.Dispose()
			}
		})
	}
}
