package recorder

import (
	"context"
	"testing"
	"time"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/hevc"
	"github.com/stretchr/testify/require"
)

func TestCovAdaptiveGate_DefaultsAndWindow(t *testing.T) {
	var nilGate *AdaptiveGate
	require.True(t, nilGate.ShouldWrite(false, time.Now()))
	nilGate.Trigger(time.Second)
	nilGate.ObservePFrame(10)

	// An interval below the minimum falls back to the calm default.
	gate := NewAdaptiveGate(time.Second)
	require.Equal(t, 30*time.Second, gate.interval)
	require.Equal(t, float64(5), gate.spikeFactor)
	require.Equal(t, time.Minute, NewAdaptiveGate(time.Minute).interval)

	// Trigger with a non-positive hold uses the default 30s window.
	gate.Trigger(0)
	require.True(t, gate.ShouldWrite(false, time.Now()))

	// Non-positive sizes are ignored.
	gate.ObservePFrame(0)
	gate.ObservePFrame(-5)
	require.Empty(t, gate.sizes)

	// The size window is capped and a spike above the median promotes the gate.
	for i := 0; i < adaptiveWindow+4; i++ {
		gate.ObservePFrame(100)
	}
	require.Len(t, gate.sizes, adaptiveWindow)
	gate.activeUntil = time.Time{}
	gate.ObservePFrame(1000)
	require.True(t, gate.ShouldWrite(false, time.Now()))

	// Calm gating: only the periodic IDR is written.
	calm := NewAdaptiveGate(time.Hour)
	now := time.Now()
	require.True(t, calm.ShouldWrite(true, now))
	require.False(t, calm.ShouldWrite(true, now.Add(time.Second)))
	require.False(t, calm.ShouldWrite(false, now))
	require.True(t, calm.ShouldWrite(true, now.Add(2*time.Hour)))

	require.Zero(t, medianInt(nil))
	require.Equal(t, 2, medianInt([]int{3, 1, 2}))
}

func TestCovPrerollRing_NilAndTrimming(t *testing.T) {
	var nilRing *prerollRing
	nilRing.Push([]byte{1}, true)
	require.Nil(t, nilRing.Drain())

	ring := newPrerollRing(0)
	require.Equal(t, 2, ring.maxGOPs)
	ring.Push(nil, true) // empty payload ignored
	ring.Push([]byte{}, false)
	require.Empty(t, ring.frames)

	// Two GOPs are retained; the third drops the oldest one.
	ring.Push([]byte{1}, true)
	ring.Push([]byte{2}, false)
	ring.Push([]byte{3}, true)
	ring.Push([]byte{4}, false)
	ring.Push([]byte{5}, true)
	out := ring.Drain()
	require.NotEmpty(t, out)
	require.Equal(t, []byte{5}, out[len(out)-1])
	require.Zero(t, ring.gops)
	require.Empty(t, ring.frames)

	// A single-GOP ring clamps the counter when every buffered frame is pruned.
	tight := newPrerollRing(1)
	tight.Push([]byte{1}, true)
	tight.Push([]byte{2}, true)
	tight.Push([]byte{3}, true)
	require.Equal(t, 1, tight.gops)
	require.NotEmpty(t, tight.Drain())
}

func TestCovFrameWatchdog_DefaultTimeoutAndStop(t *testing.T) {
	stop := make(chan struct{})
	close(stop)
	done := make(chan struct{})
	runFrameWatchdog(context.Background(), 0, make(chan struct{}), stop, done, func() {
		t.Error("watchdog must not fire when stopped")
	})
	<-done
}

func TestCovGroupRTMP_HevcSequenceHeaders(t *testing.T) {
	require.NotEmpty(t, testVPS265)
	seq, err := hevc.BuildSeqHeaderFromVpsSpsPps(testVPS265, testSPS265, testPPS265)
	require.NoError(t, err)

	codec, sps, pps, vps, ok := parseVideoSeq(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo, MsgLen: uint32(len(seq))},
		Payload: seq,
	})
	require.True(t, ok)
	require.Equal(t, "h265", codec)
	require.Equal(t, testSPS265, sps)
	require.Equal(t, testPPS265, pps)
	require.Equal(t, testVPS265, vps)

	// Enhanced RTMP ("hvc1" fourcc) sequence start.
	enhanced := append([]byte{0x80 | base.RtmpExPacketTypeSequenceStart, 'h', 'v', 'c', '1'}, seq[5:]...)
	codec, _, _, vps, ok = parseVideoSeq(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo, MsgLen: uint32(len(enhanced))},
		Payload: enhanced,
	})
	require.True(t, ok)
	require.Equal(t, "h265", codec)
	require.Equal(t, testVPS265, vps)

	// An enhanced sequence header with an unexpected packet type is rejected.
	badType := append([]byte{0x80 | base.RtmpExPacketTypeCodedFramesX, 'h', 'v', 'c', '1'}, seq[5:]...)
	_, _, _, _, ok = parseVideoSeq(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo, MsgLen: uint32(len(badType))},
		Payload: badType,
	})
	require.False(t, ok)
}

func TestCovGroupRTMP_EnhancedHevcNALUs(t *testing.T) {
	nals := naluAVCC([]byte{0x26, 0x01, 0xaf}) // IDR_W_RADL

	// CodedFramesX: body starts right after the 5-byte enhanced header.
	codedX := append([]byte{0x80 | base.RtmpExPacketTypeCodedFramesX, 'h', 'v', 'c', '1'}, nals...)
	got, ok := videoNALUs(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo, MsgLen: uint32(len(codedX))},
		Payload: codedX,
	})
	require.True(t, ok)
	require.Len(t, got, 1)
	require.Equal(t, []byte{0x26, 0x01, 0xaf}, got[0])

	// CodedFrames: three composition-time bytes sit between header and NALUs.
	coded := append([]byte{0x80 | base.RtmpExPacketTypeCodedFrames, 'h', 'v', 'c', '1', 0, 0, 0}, nals...)
	got, ok = videoNALUs(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo, MsgLen: uint32(len(coded))},
		Payload: coded,
	})
	require.True(t, ok)
	require.Len(t, got, 1)

	// An enhanced message whose NALU index is unusable is rejected.
	odd := []byte{0x80 | 2, 'h', 'v', 'c', '1', 0}
	_, ok = videoNALUs(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo, MsgLen: uint32(len(odd))},
		Payload: odd,
	})
	require.False(t, ok)

	// A truncated enhanced payload has no room for a body.
	_, ok = videoNALUs(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo},
		Payload: []byte{0x80 | base.RtmpExPacketTypeCodedFramesX, 'h', 'v', 'c', '1'},
	})
	require.False(t, ok)
}
