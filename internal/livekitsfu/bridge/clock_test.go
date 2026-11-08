package bridge

import (
	"errors"
	"testing"
	"time"
)

func TestClockOffset(t *testing.T) {
	a := ClockReference{NTP: 200 << 32, RTP: 0xfffffff0, Rate: 48000}
	b := ClockReference{NTP: 200<<32 | 1<<31, RTP: 1234, Rate: 90000}
	got, err := ClockOffset(a, 47984, b, 46234)
	if err != nil || got != 0 {
		t.Fatalf("offset: %s %v", got, err)
	}
	got, err = ClockOffset(b, 46234, a, 47984)
	if err != nil || got != 0 {
		t.Fatalf("reverse: %s %v", got, err)
	}
	got, err = ClockOffset(a, 47984, b, 55234)
	if err != nil || got != 100*time.Millisecond {
		t.Fatalf("skew: %s %v", got, err)
	}
	if _, err = ClockOffset(a, 0, ClockReference{}, 0); !errors.Is(err, ErrInvalidClock) {
		t.Fatal(err)
	}
}

func TestClockOffsetNTPEraRollover(t *testing.T) {
	a := ClockReference{NTP: 0xffffffff<<32 | 1<<31, RTP: 1, Rate: 48000}
	b := ClockReference{NTP: 1 << 31, RTP: 1, Rate: 48000}
	got, err := ClockOffset(a, 1, b, 1)
	if err != nil || got != time.Second {
		t.Fatalf("rollover: %s %v", got, err)
	}
}
