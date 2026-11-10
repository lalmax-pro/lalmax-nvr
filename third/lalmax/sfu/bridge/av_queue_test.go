package bridge

import (
	"errors"
	"testing"
	"time"

	"github.com/q191201771/lal/pkg/base"
)

func TestAVQueueOrdersAndBounds(t *testing.T) {
	var q avQueue
	now := time.Now()
	for _, item := range []struct {
		ms    int64
		video bool
	}{{100, false}, {0, true}, {200, true}} {
		if err := q.push(base.AvPacket{Timestamp: item.ms}, now, item.video); err != nil {
			t.Fatal(err)
		}
	}
	var got []int64
	sink := func(p base.AvPacket) error { got = append(got, p.Timestamp); return nil }
	if err := q.drain(now, false, sink); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 0 || got[1] != 100 {
		t.Fatalf("early drain: %v", got)
	}
	if err := q.drain(now.Add(avReorderWindow), false, sink); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1] != 100 || got[2] != 200 {
		t.Fatalf("ordered drain: %v", got)
	}
	_ = q.push(base.AvPacket{Timestamp: 50}, now, true)
	if len(q.frames) != 0 {
		t.Fatal("late packet retained")
	}
	for i := 0; i < avQueueLimit; i++ {
		if err := q.push(base.AvPacket{Timestamp: 300}, now, true); err != nil {
			t.Fatal(err)
		}
	}
	if !errors.Is(q.push(base.AvPacket{Timestamp: 300}, now, true), errAVQueueFull) {
		t.Fatal("unbounded queue")
	}
}

func TestAVQueueWaitsForLaggingTrackButLimitsDelay(t *testing.T) {
	var q avQueue
	now := time.Now()
	var got []int64
	sink := func(p base.AvPacket) error { got = append(got, p.Timestamp); return nil }
	if err := q.push(base.AvPacket{Timestamp: 0}, now, true); err != nil {
		t.Fatal(err)
	}
	if err := q.push(base.AvPacket{Timestamp: 200}, now, false); err != nil {
		t.Fatal(err)
	}
	if err := q.drain(now, false, sink); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("leading audio emitted early: %v", got)
	}
	for _, ms := range []int64{40, 80, 120, 160, 200} {
		if err := q.push(base.AvPacket{Timestamp: ms}, now, true); err != nil {
			t.Fatal(err)
		}
		if err := q.drain(now, false, sink); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 7 || got[5] != 200 || got[6] != 200 {
		t.Fatalf("lost or reordered frames: %v", got)
	}
	if err := q.push(base.AvPacket{Timestamp: 240}, now, false); err != nil {
		t.Fatal(err)
	}
	if err := q.drain(now.Add(avReorderWindow-time.Millisecond), false, sink); err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatal("stalled track was not given its bounded wait")
	}
	if err := q.drain(now.Add(avReorderWindow), false, sink); err != nil {
		t.Fatal(err)
	}
	if len(got) != 8 || got[7] != 240 {
		t.Fatalf("stalled track blocked bridge: %v", got)
	}
}
