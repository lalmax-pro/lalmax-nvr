package voip

import (
	"sync"
	"testing"
	"time"
)

func TestRegistrarConcurrentCloseStopsExpiry(t *testing.T) {
	for i := 0; i < 20; i++ {
		r := NewRegistrar()
		var wg sync.WaitGroup
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func() { defer wg.Done(); r.Close() }()
		}
		wg.Wait()
		select {
		case <-r.done:
		case <-time.After(time.Second):
			t.Fatal("registration expiry worker leaked")
		}
	}
}

func TestServerCloseStopsRegistrar(t *testing.T) {
	s, _, _, _ := newTestServer(t, 5000, 15000)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.registrar.done:
	default:
		t.Fatal("server did not stop registration expiry")
	}
}
