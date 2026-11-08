package bridge

import (
	"errors"
	"time"
)

var ErrInvalidClock = errors.New("bridge: invalid RTP clock reference")

// ClockReference is an RTCP Sender Report's NTP/RTP pair for one source.
// Callers must verify the SSRC, publication lifetime, clock rate and that
// reports for tracks being compared share a sender clock before using it.
type ClockReference struct {
	NTP  uint64
	RTP  uint32
	Rate uint32
}

// ClockOffset returns media time b minus media time a. NTP era rollover and
// RTP timestamp rollover are handled modulo their wire representations; both
// references and each packet must be within half their respective wrap period.
// This does not estimate clock drift or synchronize unrelated senders.
func ClockOffset(a ClockReference, aTimestamp uint32, b ClockReference, bTimestamp uint32) (time.Duration, error) {
	if a.NTP == 0 || b.NTP == 0 || a.Rate == 0 || b.Rate == 0 {
		return 0, ErrInvalidClock
	}
	ntpDelta := int64(b.NTP - a.NTP)
	seconds := ntpDelta >> 32
	fraction := uint64(uint32(ntpDelta))
	// Limit the delta so duration arithmetic cannot overflow. Sender reports
	// from tracks in the same session should be separated by far less time.
	if seconds < -86400 || seconds > 86400 {
		return 0, ErrInvalidClock
	}
	ntp := time.Duration(seconds)*time.Second + time.Duration((fraction*uint64(time.Second))>>32)
	aTicks := int64(int32(aTimestamp - a.RTP))
	bTicks := int64(int32(bTimestamp - b.RTP))
	aTime := time.Duration(aTicks * int64(time.Second) / int64(a.Rate))
	bTime := time.Duration(bTicks * int64(time.Second) / int64(b.Rate))
	return ntp + bTime - aTime, nil
}
