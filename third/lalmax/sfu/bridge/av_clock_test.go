package bridge

import (
	"errors"
	"testing"
	"time"
)

func TestAVClockTracksSenderReports(t *testing.T) {
	v := ClockReference{NTP: 100 << 32, RTP: 90000, Rate: 90000}
	a := ClockReference{NTP: 100 << 32, RTP: 48000, Rate: 48000}
	c := newAVClock(v, 90000)
	if got, err := c.video(v, 99000); err != nil || got != 100*time.Millisecond {
		t.Fatalf("video: %s %v", got, err)
	}
	if got, err := c.audio(a, 55200); err != nil || got != 150*time.Millisecond {
		t.Fatalf("audio: %s %v", got, err)
	}
	updated := ClockReference{NTP: 101 << 32, RTP: 96000, Rate: 48000}
	if got, err := c.audio(updated, 100800); err != nil || got != 1100*time.Millisecond {
		t.Fatalf("updated: %s %v", got, err)
	}
	if _, err := c.audio(updated, 100000); !errors.Is(err, ErrAVClockRegression) {
		t.Fatalf("regression: %v", err)
	}
}
