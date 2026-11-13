package rtmp

import (
	"sync"
	"testing"
)

// TestServerDisposeDuringListen ensures a listener that finishes binding after
// shutdown is closed instead of escaping through an unsynchronized ln field.
func TestServerDisposeDuringListen(t *testing.T) {
	for i := 0; i < 100; i++ {
		server := NewServer("127.0.0.1:0", nil)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = server.Listen()
		}()
		go func() {
			defer wg.Done()
			server.Dispose()
		}()
		wg.Wait()
		server.Dispose()
	}
}
