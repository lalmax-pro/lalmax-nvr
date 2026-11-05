package bridge

import (
	"bytes"
	"encoding/hex"
	"errors"
	"github.com/pion/rtp"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/remux"
	"testing"
)

func packet(seq uint16, ts uint32, marker bool, payload ...byte) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: seq, Timestamp: ts, Marker: marker}, Payload: payload}
}
func mustPush(t *testing.T, h *H264, p *rtp.Packet) *base.AvPacket {
	t.Helper()
	frame, err := h.Push(p)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}
func stap(nalus ...[]byte) []byte {
	out := []byte{24}
	for _, n := range nalus {
		out = append(out, byte(len(n)>>8), byte(len(n)))
		out = append(out, n...)
	}
	return out
}
func TestH264StartsOnKeyframeAndFeedsLalRemuxer(t *testing.T) {
	sps, _ := hex.DecodeString("67640032ad84010c20086100430802184010c200843b5014005ad370101014000003000400000300ca100002")
	pps, _ := hex.DecodeString("68ee3cb0")
	h := NewH264()
	if got := mustPush(t, h, packet(1, 100, true, 0x41, 1)); got != nil {
		t.Fatal("non-keyframe emitted before SPS/PPS")
	}
	in := packet(2, 200, true, stap(sps, pps, []byte{0x65, 0x88})...)
	frame := mustPush(t, h, in)
	if frame == nil || frame.PayloadType != base.AvPacketPtAvc || frame.Timestamp != 0 || frame.Pts != 0 {
		t.Fatalf("unexpected frame %+v", frame)
	}
	before := bytes.Clone(frame.Payload)
	for i := range in.Payload {
		in.Payload[i] = 0
	}
	if !bytes.Equal(frame.Payload, before) {
		t.Fatal("frame aliases incoming RTP")
	}
	var video []base.RtmpMsg
	mux := remux.NewAvPacket2RtmpRemuxer().WithOnRtmpMsg(func(m base.RtmpMsg) {
		if m.Header.MsgTypeId == base.RtmpTypeIdVideo {
			video = append(video, m)
		}
	})
	mux.WithOption(func(o *base.AvPacketStreamOption) { o.VideoFormat = base.AvPacketStreamVideoFormatAnnexb })
	mux.FeedAvPacket(*frame)
	if len(video) != 2 || video[0].Payload[1] != base.RtmpAvcPacketTypeSeqHeader || video[1].Payload[1] != base.RtmpAvcPacketTypeNalu {
		t.Fatalf("expected AVC sequence header and frame, got %+v", video)
	}
	out := mustPush(t, h, packet(3, 9200, true, 0x41, 0x22))
	if out == nil || out.Timestamp != 100 || out.Pts != 100 {
		t.Fatalf("bad time: %+v", out)
	}
}
func TestH264FragmentLossAndRecovery(t *testing.T) {
	h := NewH264()
	mustPush(t, h, packet(65533, 100, true, stap([]byte{0x67, 1}, []byte{0x68, 2}, []byte{0x65, 3})...))
	if got := mustPush(t, h, packet(65534, 400, false, 0x7c, 0x85, 0xaa)); got != nil {
		t.Fatal("partial fragment emitted")
	}
	if got := mustPush(t, h, packet(0, 400, true, 0x7c, 0x45, 0xcc)); got != nil {
		t.Fatal("damaged fragment emitted")
	}
	if got := mustPush(t, h, packet(1, 500, true, 0x41, 4)); got != nil {
		t.Fatal("inter frame emitted after loss")
	}
	out := mustPush(t, h, packet(2, 600, true, 0x65, 5))
	if out == nil || !bytes.Contains(out.Payload, []byte{0, 0, 0, 1, 0x67, 1}) {
		t.Fatalf("failed to recover %+v", out)
	}
}
func TestH264WrapRegressionAndSource(t *testing.T) {
	h := NewH264()
	mustPush(t, h, packet(1, 0xfffffff0, true, stap([]byte{0x67, 1}, []byte{0x68, 2}, []byte{0x65, 3})...))
	out := mustPush(t, h, packet(2, 0x00002318, true, 0x41, 1))
	if out == nil || out.Timestamp != 100 {
		t.Fatalf("wrap: %+v", out)
	}
	if _, err := h.Push(packet(3, 0x1000, true, 0x41, 1)); !errors.Is(err, ErrTimestampRegression) {
		t.Fatalf("regression: %v", err)
	}
	bad := packet(4, 0x3000, true, 0x65, 1)
	bad.SSRC = 99
	if _, err := h.Push(bad); !errors.Is(err, ErrSSRCChanged) {
		t.Fatalf("SSRC: %v", err)
	}
}
func TestH264ParameterChangeWaitsForIDR(t *testing.T) {
	h := NewH264()
	mustPush(t, h, packet(1, 1, true, stap([]byte{0x67, 1}, []byte{0x68, 2}, []byte{0x65, 3})...))
	if out := mustPush(t, h, packet(2, 2, true, stap([]byte{0x67, 9}, []byte{0x41, 1})...)); out != nil {
		t.Fatal("emitted frame before new IDR")
	}
	out := mustPush(t, h, packet(3, 3, true, 0x65, 1))
	if out == nil || !bytes.Contains(out.Payload, []byte{0, 0, 0, 1, 0x67, 9}) {
		t.Fatal("new SPS not applied")
	}
}

func TestH264FragmentAssemblyAndDuplicate(t *testing.T) {
	h := NewH264()
	mustPush(t, h, packet(1, 100, true, stap([]byte{0x67, 1}, []byte{0x68, 2})...))
	if out := mustPush(t, h, packet(2, 200, false, 0x7c, 0x85, 0xaa)); out != nil {
		t.Fatal("partial frame emitted")
	}
	if out := mustPush(t, h, packet(2, 200, false, 0x7c, 0x85, 0xaa)); out != nil {
		t.Fatal("duplicate packet emitted")
	}
	out := mustPush(t, h, packet(3, 200, true, 0x7c, 0x45, 0xbb))
	if out == nil || !bytes.HasSuffix(out.Payload, []byte{0, 0, 0, 1, 0x65, 0xaa, 0xbb}) {
		t.Fatalf("invalid reassembly: %+v", out)
	}
}
func TestH264TimestampChangeDiscardsIncompleteAccessUnit(t *testing.T) {
	h := NewH264()
	mustPush(t, h, packet(1, 100, true, stap([]byte{0x67, 1}, []byte{0x68, 2}, []byte{0x65, 3})...))
	mustPush(t, h, packet(2, 200, false, 0x41, 0xaa))
	if out := mustPush(t, h, packet(3, 300, true, 0x41, 0xbb)); out != nil {
		t.Fatal("incomplete access unit emitted")
	}
	if out := mustPush(t, h, packet(4, 400, true, 0x65, 0xcc)); out == nil {
		t.Fatal("failed to recover at IDR")
	}
}

func TestH264GapDropsRemainderOfAccessUnit(t *testing.T) {
	h := NewH264()
	mustPush(t, h, packet(1, 100, true, stap([]byte{0x67, 1}, []byte{0x68, 2}, []byte{0x65, 3})...))
	if out := mustPush(t, h, packet(3, 200, true, 0x65, 0xaa)); out != nil {
		t.Fatal("possibly incomplete IDR emitted after packet gap")
	}
	if out := mustPush(t, h, packet(4, 300, true, 0x41, 0xbb)); out != nil {
		t.Fatal("inter frame emitted after gap")
	}
	if out := mustPush(t, h, packet(5, 400, true, 0x65, 0xcc)); out == nil {
		t.Fatal("failed to resume at later complete IDR")
	}
}
