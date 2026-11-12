package rtc

import (
	"bytes"
	"testing"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/hevc"
)

// Fixture VPS/SPS/PPS copied from the pinned lal HEVC parser tests.
var fixtureVPS = []byte{
	0x40, 0x01,
	0x0c, 0x01, 0xff, 0xff,
	0x01,
	0x60, 0x00, 0x00, 0x03, 0x00,
	0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00,
	0x3f,
	0xba, 0x02, 0x40,
}
var fixtureSPS = []byte{
	0x42, 0x01,
	0x01,
	0x01,
	0x60, 0x00, 0x00, 0x03, 0x00,
	0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00,
	0x3f,
	0xa0, 0x05, 0x02, 0x01, 0x71, 0xf2, 0xe5, 0xba, 0x4a, 0x4c, 0x2f, 0x01, 0x01, 0x00, 0x00, 0x03, 0x00, 0x01, 0x00, 0x00, 0x03, 0x00, 0x0f, 0x08,
}
var fixturePPS = []byte{
	0x44, 0x01, 0xc0, 0x73, 0xc1, 0x89,
}

func TestHevcRtpEncoderAcceptsLegacyAndEnhancedSequenceHeaders(t *testing.T) {
	legacy, err := hevc.BuildSeqHeaderFromVpsSpsPps(fixtureVPS, fixtureSPS, fixturePPS)
	if err != nil {
		t.Fatalf("build legacy sequence header: %v", err)
	}
	if len(legacy) < 6 || legacy[0] != 0x1c {
		t.Fatalf("unexpected legacy HEVC header %x", legacy)
	}
	enhanced := append([]byte{0x80 | base.RtmpExPacketTypeSequenceStart, 'h', 'v', 'c', '1'}, legacy[5:]...)
	for _, tc := range []struct {
		name string
		header []byte
	}{
		{"legacy-0x1c", legacy},
		{"enhanced-hvc1", enhanced},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoder := NewHevcRtpEncoder(tc.header)
			if encoder == nil {
				t.Fatal("valid HEVC stream has no RTP encoder")
			}
			if !bytes.Equal(encoder.vps, fixtureVPS) ||
				!bytes.Equal(encoder.sps, fixtureSPS) ||
				!bytes.Equal(encoder.pps, fixturePPS) {
				t.Fatal("HEVC parameter sets differ from sequence header")
			}
			if NewPacker(PacketHEVC, tc.header).enc == nil {
				t.Fatal("HEVC WHEP packer has no encoder")
			}
		})
	}
}
