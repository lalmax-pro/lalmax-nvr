package remux_test

import (
	"encoding/hex"
	"testing"

	"github.com/q191201771/lal/pkg/avc"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/remux"
	"github.com/q191201771/lal/pkg/rtprtcp"
	"github.com/q191201771/lal/pkg/sdp"
)

func TestRtmp2RtspUsesPresentationTimestamp(t *testing.T) {
	sps, _ := hex.DecodeString("67640032ad84010c20086100430802184010c200843b5014005ad370101014000003000400000300ca100002")
	pps, _ := hex.DecodeString("68ee3cb0")
	sequenceHeader, err := avc.BuildSeqHeaderFromSpsPps(sps, pps)
	if err != nil {
		t.Fatal(err)
	}

	var packets []rtprtcp.RtpPacket
	r := remux.NewRtmp2RtspRemuxer(func(sdp.LogicContext) {}, func(pkt rtprtcp.RtpPacket) {
		packets = append(packets, pkt)
	})
	r.FeedRtmpMsg(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo},
		Payload: sequenceHeader,
	})
	for i := 0; i < 16; i++ {
		r.FeedRtmpMsg(base.RtmpMsg{
			Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo, TimestampAbs: 100},
			// AVC NALU packet with a 40ms PTS-DTS composition offset.
			Payload: []byte{0x17, 0x01, 0x00, 0x00, 0x28, 0x00, 0x00, 0x00, 0x02, 0x65, 0x88},
		})
	}

	if len(packets) == 0 {
		t.Fatal("expected at least one RTP packet")
	}
	if got, want := packets[0].Header.Timestamp, uint32(140*90); got != want {
		t.Fatalf("RTP timestamp = %d, want presentation timestamp %d", got, want)
	}
}
