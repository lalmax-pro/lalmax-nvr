package bridge

import (
	"errors"
	"time"
)

var ErrAVClockRegression = errors.New("bridge: synchronized timestamp regressed")

// avClock anchors the output to the first decodable video frame. Its caller
// validates the SSRCs, report freshness, shared sender clock and publication
// lifetime before supplying each report. It is used from one goroutine only.
type avClock struct {
	anchor    ClockReference
	anchorRTP uint32
	lastVideo time.Duration
	lastAudio time.Duration
	audioSet  bool
}

func newAVClock(video ClockReference, rtp uint32) *avClock {
	return &avClock{anchor: video, anchorRTP: rtp}
}

func (c *avClock) video(ref ClockReference, rtp uint32) (time.Duration, error) {
	delta, err := ClockOffset(c.anchor, c.anchorRTP, ref, rtp)
	if err != nil {
		return 0, err
	}
	if delta < c.lastVideo {
		return 0, ErrAVClockRegression
	}
	c.lastVideo = delta
	return delta, nil
}

func (c *avClock) audio(ref ClockReference, rtp uint32) (time.Duration, error) {
	delta, err := ClockOffset(c.anchor, c.anchorRTP, ref, rtp)
	if err != nil {
		return 0, err
	}
	if c.audioSet && delta < c.lastAudio {
		return 0, ErrAVClockRegression
	}
	c.audioSet = true
	c.lastAudio = delta
	return delta, nil
}
