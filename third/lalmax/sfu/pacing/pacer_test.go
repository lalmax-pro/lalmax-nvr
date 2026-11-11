package pacing

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

func testWriter() interceptor.RTPWriter {
	return interceptor.RTPWriterFunc(func(_ *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) { return len(payload), nil })
}
func testPacer(t *testing.T, rate int, delay time.Duration, pending int) *Pacer {
	t.Helper()
	p, err := New(rate, delay, pending)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.AddStream(1, testWriter())
	return p
}
func waitPending(t *testing.T, p *Pacer, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		n := p.pending
		p.mu.Unlock()
		if n == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("pending did not reach %d", want)
}
func TestPacerSchedulesByBitrate(t *testing.T) {
	p := testPacer(t, 80000, time.Second, 4)
	h := &rtp.Header{Version: 2, SSRC: 1}
	payload := make([]byte, 100)
	start := time.Now()
	for i := 0; i < 4; i++ {
		n, err := p.Write(h, payload, nil)
		if err != nil || n != len(payload) {
			t.Fatalf("write: %d %v", n, err)
		}
	}
	if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
		t.Fatalf("four packets sent too quickly: %s", elapsed)
	}
}
func TestPacerBoundsAndClosesWaitingWrites(t *testing.T) {
	p := testPacer(t, 8000, time.Second, 1)
	h := &rtp.Header{Version: 2, SSRC: 1}
	payload := make([]byte, 100)
	if _, err := p.Write(h, payload, nil); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := p.Write(h, payload, nil); result <- err }()
	waitPending(t, p, 1)
	if _, err := p.Write(h, payload, nil); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("pending limit: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("close returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock sender")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write(h, payload, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
}
func TestPacerRejectsExcessDelayAndStaleStream(t *testing.T) {
	p := testPacer(t, 8000, 150*time.Millisecond, 4)
	h := &rtp.Header{Version: 2, SSRC: 1}
	payload := make([]byte, 100)
	p.mu.Lock()
	p.next = time.Now().Add(time.Hour)
	p.mu.Unlock()
	if _, err := p.Write(h, payload, nil); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("delay limit: %v", err)
	}
	p = testPacer(t, 8000, time.Second, 4)
	if _, err := p.Write(h, payload, nil); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := p.Write(h, payload, nil); result <- err }()
	waitPending(t, p, 1)
	p.RemoveStream(1)
	p.AddStream(1, testWriter())
	select {
	case err := <-result:
		if !errors.Is(err, ErrUnknownStream) {
			t.Fatalf("stale write: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stale write did not finish")
	}
}
func TestPacerConcurrentClose(t *testing.T) {
	p := testPacer(t, 1000000, 50*time.Millisecond, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				p.AddStream(1, testWriter())
				_, _ = p.Write(&rtp.Header{Version: 2, SSRC: 1}, []byte{1}, nil)
				p.RemoveStream(1)
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Close() }()
	wg.Wait()
}

func TestPacerRejectsPacketThatCannotFitDelayBudget(t *testing.T) {
	p := testPacer(t, 1000, 100*time.Millisecond, 2)
	h := &rtp.Header{Version: 2, SSRC: 1}
	if _, err := p.Write(h, make([]byte, 100), nil); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("oversized reservation: %v", err)
	}
	p.SetTargetBitrate(1000000)
	if _, err := p.Write(h, make([]byte, 100), nil); err != nil {
		t.Fatalf("rate recovery: %v", err)
	}
}

func TestPacerUnbindUnblocksWaitingWrite(t *testing.T) {
	p := testPacer(t, 100, 10*time.Second, 4)
	h := &rtp.Header{Version: 2, SSRC: 1}
	payload := make([]byte, 100)
	if _, err := p.Write(h, payload, nil); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := p.Write(h, payload, nil); result <- err }()
	waitPending(t, p, 1)
	p.RemoveStream(1)
	select {
	case err := <-result:
		if !errors.Is(err, ErrUnknownStream) {
			t.Fatalf("unbind returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unbind did not release waiting write")
	}
}

func TestPacerRejectsInvalidInput(t *testing.T) {
	for _, cfg := range []struct {
		rate    int
		delay   time.Duration
		pending int
	}{
		{0, time.Second, 1}, {1, 0, 1}, {1, time.Second, 0},
	} {
		if _, err := New(cfg.rate, cfg.delay, cfg.pending); err == nil {
			t.Fatalf("invalid config accepted: %+v", cfg)
		}
	}
	p := testPacer(t, 1_000_000, time.Second, 1)
	if _, err := p.Write(nil, nil, nil); !errors.Is(err, ErrInvalidPacket) {
		t.Fatalf("nil header: %v", err)
	}
	if _, err := p.Write(&rtp.Header{Version: 2, SSRC: 1}, make([]byte, 65535), nil); !errors.Is(err, ErrInvalidPacket) {
		t.Fatalf("oversized packet: %v", err)
	}
}

func TestPacerCountsRTPPadding(t *testing.T) {
	p := testPacer(t, 8000, 100*time.Millisecond, 1)
	h := &rtp.Header{Version: 2, SSRC: 1, Padding: true, PaddingSize: 200}
	if _, err := p.Write(h, nil, nil); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("padding must count toward the bitrate: %v", err)
	}
}

func TestPacerSharesBudgetAcrossStreams(t *testing.T) {
	p := testPacer(t, 80000, time.Second, 4)
	p.AddStream(2, testWriter())
	payload := make([]byte, 100)
	start := time.Now()
	for _, ssrc := range []uint32{1, 2, 1, 2} {
		if _, err := p.Write(&rtp.Header{Version: 2, SSRC: ssrc}, payload, nil); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
		t.Fatalf("streams bypassed shared budget: %s", elapsed)
	}
}
