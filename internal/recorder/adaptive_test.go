package recorder

import (
	"testing"
	"time"
)

func TestAdaptiveGateSparseThenActive(t *testing.T) {
	g := NewAdaptiveGate(30 * time.Second)
	now := time.Now()
	if !g.ShouldWrite(true, now) {
		t.Fatal("first IDR should write")
	}
	if g.ShouldWrite(true, now.Add(time.Second)) {
		t.Fatal("second IDR within interval should be skipped")
	}
	if g.ShouldWrite(false, now.Add(2*time.Second)) {
		t.Fatal("P-frame should be skipped while calm")
	}
	g.Trigger(time.Minute)
	if !g.ShouldWrite(false, time.Now()) {
		t.Fatal("P-frame should write while active")
	}
}

func TestAdaptiveGatePFrameSpike(t *testing.T) {
	g := NewAdaptiveGate(30 * time.Second)
	for i := 0; i < 10; i++ {
		g.ObservePFrame(100)
	}
	g.ObservePFrame(5000)
	if !g.ShouldWrite(false, time.Now()) {
		t.Fatal("spike should promote to active")
	}
}
