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
	for _, ms := range []int64{100, 0, 200} {
		if err := q.push(base.AvPacket{Timestamp: ms}, now); err != nil {
			t.Fatal(err)
		}
	}
	var got []int64
	sink := func(p base.AvPacket) error { got = append(got, p.Timestamp); return nil }
	if err := q.drain(now, false, sink); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("early drain: %v", got)
	}
	if err := q.drain(now.Add(avReorderWindow), false, sink); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1] != 100 || got[2] != 200 {
		t.Fatalf("ordered drain: %v", got)
	}
	_ = q.push(base.AvPacket{Timestamp: 50}, now)
	if len(q.frames) != 0 {
		t.Fatal("late packet retained")
	}
	for i := 0; i < avQueueLimit; i++ {
		if err := q.push(base.AvPacket{Timestamp: 300}, now); err != nil {
			t.Fatal(err)
		}
	}
	if !errors.Is(q.push(base.AvPacket{Timestamp: 300}, now), errAVQueueFull) {
		t.Fatal("unbounded queue")
	}
}
