package jtsim

import (
	"bytes"
	"testing"
)

func TestCodecRoundTripEscape(t *testing.T) {
	for _, version := range []byte{0, 1} {
		original := message{id: msgPlay, sim: "1003", seq: 42, version: version, body: []byte{0x7d, 0x7e, 1}}
		encoded, err := encodeMessage(original)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(encoded, []byte{0x7d, 0x01}) || !bytes.Contains(encoded, []byte{0x7d, 0x02}) {
			t.Fatalf("escapes missing: %x", encoded)
		}
		decoded, err := decodeMessage(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if decoded.id != msgPlay || decoded.seq != 42 || decoded.sim != "1003" || decoded.version != version || !bytes.Equal(decoded.body, original.body) {
			t.Fatalf("decoded=%+v", decoded)
		}
	}
}
