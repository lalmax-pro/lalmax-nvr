package rtpforward

import (
	"errors"
	"sync"
	"testing"

	"github.com/pion/rtp"
)

func TestFanoutCopiesAndIsolatesSlowSubscriber(t *testing.T) {
	f, _ := New(2, 1)
	slow, _ := f.Subscribe("slow")
	fast, _ := f.Subscribe("fast")
	p := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 99}, Payload: []byte{1, 2, 3}}
	if err := f.WriteRTP(p); err != nil {
		t.Fatal(err)
	}
	a := <-fast.Packets()
	p.Payload[0] = 8
	a.Payload[1] = 9
	if err := f.WriteRTP(p); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(slow.Err(), ErrSlowSubscriber) {
		t.Fatalf("slow reason: %v", slow.Err())
	}
	b := <-slow.Packets()
	if b.Payload[0] != 1 || b.Payload[1] != 2 || b.SequenceNumber != 1 {
		t.Fatalf("packet not independent: %+v", b)
	}
	if _, ok := <-slow.Packets(); ok {
		t.Fatal("slow queue still open")
	}
	if (<-fast.Packets()).Payload[0] != 8 {
		t.Fatal("fast subscriber lost packet")
	}
}
func TestLimitsCloseAndStaleSubscriber(t *testing.T) {
	if _, err := New(0, 1); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	f, _ := New(1, 1)
	old, _ := f.Subscribe("a")
	if _, err := f.Subscribe("a"); !errors.Is(err, ErrDuplicateSubscriber) {
		t.Fatal(err)
	}
	if _, err := f.Subscribe("b"); !errors.Is(err, ErrSubscriberLimit) {
		t.Fatal(err)
	}
	old.Close()
	fresh, err := f.Subscribe("a")
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
	if err = f.WriteRTP(&rtp.Packet{Payload: []byte{7}}); err != nil {
		t.Fatal(err)
	}
	if (<-fresh.Packets()).Payload[0] != 7 {
		t.Fatal("stale close removed subscriber")
	}
	if err = f.WriteRTP(&rtp.Packet{Payload: make([]byte, 65536)}); !errors.Is(err, ErrInvalidPacket) {
		t.Fatal(err)
	}
	f.Close()
	f.Close()
	if !errors.Is(fresh.Err(), ErrClosed) {
		t.Fatal(fresh.Err())
	}
	if err = f.WriteRTP(&rtp.Packet{}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
func TestConcurrentWriteSubscribeAndClose(t *testing.T) {
	f, _ := New(2, 4)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				err := f.WriteRTP(&rtp.Packet{Payload: []byte{1}})
				if err != nil && !errors.Is(err, ErrClosed) {
					t.Error(err)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < 100; n++ {
			s, err := f.Subscribe("a")
			if err == nil {
				s.Close()
			} else if !errors.Is(err, ErrClosed) {
				t.Error(err)
			}
		}
	}()
	f.Close()
	wg.Wait()
}
