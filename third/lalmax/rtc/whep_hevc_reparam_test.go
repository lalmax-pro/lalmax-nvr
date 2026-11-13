package rtc

import (
	"bytes"
	"testing"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/hevc"
)

// A WHEP session must update the existing encoder after an actual legacy
// sequence-header change; initialization-only tests do not catch this bug.
func TestWHEPLegacyHEVCInSessionPPSUpdate(t *testing.T) {
	original, err := hevc.BuildSeqHeaderFromVpsSpsPps(fixtureVPS, fixtureSPS, fixturePPS)
	if err != nil {
		t.Fatal(err)
	}
	changedPPS := append([]byte(nil), fixturePPS...)
	changedPPS[len(changedPPS)-1] ^= 0x01
	updated, err := hevc.BuildSeqHeaderFromVpsSpsPps(fixtureVPS, fixtureSPS, changedPPS)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name            string
		initial, change []byte
	}{
		{"legacy", original, updated},
		{"enhanced", append([]byte{0x80 | base.RtmpExPacketTypeSequenceStart, 'h', 'v', 'c', '1'}, original[5:]...), append([]byte{0x80 | base.RtmpExPacketTypeSequenceStart, 'h', 'v', 'c', '1'}, updated[5:]...)},
		{"legacy-to-enhanced", original, append([]byte{0x80 | base.RtmpExPacketTypeSequenceStart, 'h', 'v', 'c', '1'}, updated[5:]...)},
		{"enhanced-to-legacy", append([]byte{0x80 | base.RtmpExPacketTypeSequenceStart, 'h', 'v', 'c', '1'}, original[5:]...), updated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := &whepSession{videopacker: NewPacker(PacketHEVC, tc.initial)}
			encoder, ok := conn.videopacker.enc.(*HevcRtpEncoder)
			if !ok || encoder == nil {
				t.Fatal("HEVC encoder missing")
			}
			if !bytes.Equal(encoder.pps, fixturePPS) {
				t.Fatal("wrong initial PPS")
			}
			conn.updateVideoCodec(base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: tc.change})
			if !bytes.Equal(encoder.pps, changedPPS) {
				t.Fatalf("in-session HEVC PPS was not updated in %s", tc.name)
			}
			conn.updateVideoCodec(base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: tc.change})
			if !bytes.Equal(encoder.pps, changedPPS) {
				t.Fatal("repeat changed parameter sets")
			}

			// Exercise the live receive and playback paths with truncated
			// framing, then the parser with a complete header but short VPS.
			header := base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}
			short := base.RtmpMsg{Header: header, Payload: []byte{0x80}}
			conn.OnMsg(short)
			conn.sendMediaMessage(short, true)
			truncated := append([]byte{0x80, 'h', 'v', 'c', '1'}, updated[5:28]...)
			conn.sendMediaMessage(base.RtmpMsg{Header: header, Payload: truncated}, false)
			if !bytes.Equal(encoder.pps, changedPPS) {
				t.Fatal("truncated input replaced the valid PPS")
			}
		})
	}
}
