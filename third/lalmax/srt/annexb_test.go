package srt

import (
	"bytes"
	"testing"

	"github.com/q191201771/lal/pkg/avc"
	"github.com/q191201771/lal/pkg/base"
)

func TestVideoAnnexBKeyFramePrependsParameterSets(t *testing.T) {
	seq := base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: []byte{
		0x17, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x42, 0x00, 0x0a, 0xff,
		0xe1, 0x00, 0x04, 0x67, 0x42, 0x00, 0x0a,
		0x01, 0x00, 0x04, 0x68, 0xce, 0x38, 0x80,
	}}
	idr := base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: []byte{
		0x17, 0x01, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x02, 0x65, 0x88,
	}}

	sub := &Subscriber{}
	sub.cacheParamSets(seq)
	if sub.videoCodec != base.RtmpCodecIdAvc {
		t.Fatalf("video codec = %d, want avc", sub.videoCodec)
	}
	if len(sub.paramSets) == 0 {
		t.Fatal("parameter sets were not cached")
	}

	au := sub.videoAnnexB(idr)
	if !bytes.HasPrefix(au, avc.AudNalu) {
		t.Fatalf("access unit = %x, want AUD prefix", au)
	}
	if !bytes.Contains(au, []byte{0x67, 0x42}) || !bytes.Contains(au, []byte{0x65, 0x88}) {
		t.Fatalf("access unit = %x, want sps and idr", au)
	}
}
