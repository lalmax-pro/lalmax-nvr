package base

import (
	"net/http"
	"sync"
	"testing"
)

func TestHttpServerManagerDisposeBeforeAndDuringListen(t *testing.T) {
	for i := 0; i < 30; i++ {
		manager := NewHttpServerManager()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = manager.AddListen(LocalAddrCtx{Addr: "127.0.0.1:0"}, "/", func(http.ResponseWriter, *http.Request) {})
		}()
		go func() { defer wg.Done(); _ = manager.Dispose() }()
		wg.Wait()
		if err := manager.RunLoop(); err != http.ErrServerClosed {
			t.Fatalf("disposed manager entered Serve: %v", err)
		}
		if err := manager.AddListen(LocalAddrCtx{Addr: "127.0.0.1:0"}, "/", nil); err != http.ErrServerClosed {
			t.Fatal("late listener started after disposal")
		}
	}
}
