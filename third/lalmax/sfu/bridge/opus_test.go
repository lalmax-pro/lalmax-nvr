package bridge

import (
	"errors"
	"testing"

	"github.com/pion/rtp"
	"github.com/q191201771/lal/pkg/base"
)

func TestOpusRTPConversion(t *testing.T) {
	o := &Opus{}
	p := &rtp.Packet{Header: rtp.Header{SSRC: 42, SequenceNumber: 65535, Timestamp: 0xfffffe20}, Payload: []byte{0xf8, 1}}
	frame, err := o.Push(p)
	if err != nil || frame == nil || frame.PayloadType != base.AvPacketPtOpus || frame.Timestamp != 0 {
		t.Fatalf("first: %+v %v", frame, err)
	}
	p.Payload[1] = 2
	if frame.Payload[1] != 1 {
		t.Fatal("payload aliases RTP input")
	}
	p.SequenceNumber = 0
	p.Timestamp += 960
	frame, err = o.Push(p)
	if err != nil || frame == nil || frame.Timestamp != 20 || frame.Pts != 20 {
		t.Fatalf("wrapped timestamp: %+v %v", frame, err)
	}
	if frame, err = o.Push(p); err != nil || frame != nil {
		t.Fatalf("duplicate: %+v %v", frame, err)
	}
	p.SequenceNumber = 1
	p.Padding = true
	p.PaddingSize = 8
	p.Payload = nil
	p.Timestamp += 9000
	if frame, err = o.Push(p); err != nil || frame != nil {
		t.Fatalf("padding: %+v %v", frame, err)
	}
	p.SequenceNumber = 2
	p.Padding = false
	p.PaddingSize = 0
	p.Payload = []byte{0xf8}
	p.Timestamp = 0x5a0
	frame, err = o.Push(p)
	if err != nil || frame == nil || frame.Timestamp != 40 {
		t.Fatalf("after padding: %+v %v", frame, err)
	}
	p.SequenceNumber = 3
	p.Timestamp--
	if _, err = o.Push(p); !errors.Is(err, ErrOpusTimestampRegression) {
		t.Fatalf("regression: %v", err)
	}
}

func TestOpusInvalidAndChangedSource(t *testing.T) {
	var o Opus
	if _, err := o.Push(nil); !errors.Is(err, ErrInvalidRTP) {
		t.Fatal(err)
	}
	p := &rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: 1}, Payload: []byte{1}}
	if _, err := o.Push(p); err != nil {
		t.Fatal(err)
	}
	p.SSRC = 2
	p.SequenceNumber++
	if _, err := o.Push(p); !errors.Is(err, ErrOpusSSRCChanged) {
		t.Fatal(err)
	}
}
