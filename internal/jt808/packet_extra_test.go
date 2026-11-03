package jt808

import (
	"strings"
	"testing"
)

// rawFrame assembles an on-wire frame from an unescaped header/body buffer so
// malformed frames can be built without going through encodePacket.
func rawFrame(buf []byte) []byte {
	var check byte
	for _, b := range buf {
		check ^= b
	}
	buf = append(buf, check)
	out := make([]byte, 0, len(buf)+2)
	out = append(out, 0x7e)
	for _, b := range buf {
		switch b {
		case 0x7e:
			out = append(out, 0x7d, 2)
		case 0x7d:
			out = append(out, 0x7d, 1)
		default:
			out = append(out, b)
		}
	}
	return append(out, 0x7e)
}

func TestDecodePacketRejectsMalformedFrames(t *testing.T) {
	cases := []struct {
		name  string
		frame []byte
		want  string
	}{
		{"empty", nil, "missing JT808 delimiters"},
		{"single delimiter", []byte{0x7e}, "missing JT808 delimiters"},
		{"truncated escape", []byte{0x7e, 0x11, 0x12, 0x7d, 0x7e}, "truncated escape"},
		{"invalid escape", []byte{0x7e, 0x11, 0x12, 0x7d, 0x05, 0x7e}, "invalid escape"},
		{"too short", rawFrame([]byte{0x01, 0x02}), "JT808 frame too short"},
		{"checksum", []byte{0x7e, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x7e}, "checksum mismatch"},
		{"encrypted", rawFrame([]byte{0x00, 0x01, 0x04, 0x00, 0, 0, 0, 0, 0, 1, 0, 1}), "encrypted JT808 frame"},
		{"short 2019 header", rawFrame([]byte{0x00, 0x01, 0x40, 0x00, 0, 0, 0, 0, 0, 0, 0, 0}), "header too short"},
		{"invalid SIM", rawFrame([]byte{0x00, 0x01, 0x00, 0x00, 0x1a, 0, 0, 0, 0, 1, 0, 1, 0xff}), "invalid SIM BCD"},
		{"body length", rawFrame([]byte{0x00, 0x01, 0x00, 0x05, 0, 0, 0, 0, 0, 1, 0, 1, 0xff, 0xff, 0xff}), "body length mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodePacket(tc.frame); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDecodePacketAllZeroSIM(t *testing.T) {
	frame := rawFrame([]byte{0x00, 0x01, 0x00, 0x00, 0, 0, 0, 0, 0, 0, 0, 1})
	p, err := decodePacket(frame)
	if err != nil {
		t.Fatal(err)
	}
	if p.sim != "0" || p.id != cmdGeneralReply || p.seq != 1 {
		t.Fatalf("packet = %+v", p)
	}
}

func TestEncodePacketRejectsInvalidInput(t *testing.T) {
	if _, err := encodePacket(packet{sim: "1003", body: make([]byte, maxBody+1)}); err == nil || !strings.Contains(err.Error(), "body too long") {
		t.Fatalf("length error = %v", err)
	}
	if _, err := encodePacket(packet{sim: "not-a-sim"}); err == nil || !strings.Contains(err.Error(), "invalid JT808 SIM") {
		t.Fatalf("sim error = %v", err)
	}
}
