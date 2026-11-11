package rtc

import (
	"bytes"
	"testing"

	"github.com/q191201771/lal/pkg/base"
)

func TestPackerInvalidHEVCHeaderReturnsErrorWithoutPanic(t *testing.T) {
	packer := NewPacker(PacketHEVC, []byte{0x80, 0x68, 0x76, 0x63, 0x31, 0x00})
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("invalid HEVC sequence header panicked: %v", recovered)
		}
	}()
	if _, err := packer.Encode(base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: []byte{0x1c, 0x01, 0x00, 0x00, 0x00}}); err == nil {
		t.Fatal("invalid HEVC sequence header unexpectedly produced RTP")
	}
}


func TestHEVCNALPayloadEnhancedCompositionOffset(t *testing.T) {
	nals := []byte{0, 0, 0, 3, 0x26, 0x01, 0xaf}
	coded := append([]byte{0x80 | base.RtmpExPacketTypeCodedFrames, 'h', 'v', 'c', '1', 0, 0, 0}, nals...)
	got, err := hevcNALPayload(base.RtmpMsg{Payload: coded})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, nals) {
		t.Fatalf("CodedFrames NAL payload = %x, want %x", got, nals)
	}

	codedX := append([]byte{0x80 | base.RtmpExPacketTypeCodedFramesX, 'h', 'v', 'c', '1'}, nals...)
	got, err = hevcNALPayload(base.RtmpMsg{Payload: codedX})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, nals) {
		t.Fatalf("CodedFramesX NAL payload = %x, want %x", got, nals)
	}
}

func TestHEVCNALPayloadRejectsShortPacket(t *testing.T) {
	if _, err := hevcNALPayload(base.RtmpMsg{Payload: []byte{0x81, 'h', 'v', 'c', '1'}}); err == nil {
		t.Fatal("short HEVC packet unexpectedly accepted")
	}
}
