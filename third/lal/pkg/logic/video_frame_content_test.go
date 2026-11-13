package logic

import (
	"testing"

	"github.com/q191201771/lal/pkg/base"
)

func TestOnlyCodedVideoPacketsAdvanceLastFrame(t *testing.T) {
	cases := []struct {
		name string
		msg  base.RtmpMsg
		want bool
	}{
		{"no-data", base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}}, false},
		{"audio", base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdAudio}, Payload: []byte{0xaf, 1, 0}}, false},
		{"avc-seq", base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: []byte{0x17, 0, 0, 0, 0, 1}}, false},
		{"avc-end", base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: []byte{0x17, 2, 0, 0, 0}}, false},
		{"avc-inter-end", base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: []byte{0x27, 2, 0, 0, 0}}, false},
		{"avc-coded", base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: []byte{0x27, 1, 0, 0, 0, 0x65}}, true},
		{"hevc-legacy-end", base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: []byte{0x1c, 2, 0, 0, 0}}, false},
		{"hevc-legacy-coded", base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: []byte{0x1c, 1, 0, 0, 0, 0x26}}, true},
		{"hevc-enhanced-seq", base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: []byte{0x80, 'h', 'v', 'c', '1', 0x01}}, false},
		{"hevc-enhanced-end", base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: []byte{0x82, 'h', 'v', 'c', '1'}}, false},
		{"hevc-enhanced-frame", base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: []byte{0x83, 'h', 'v', 'c', '1', 0x26}}, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEncodedVideoFrame(tt.msg); got != tt.want {
				t.Fatalf("real frame=%t want=%t", got, tt.want)
			}
		})
	}
}

func TestGroupDiscardsTruncatedHEVC(t *testing.T) {
	cfg := Config{}
	cfg.HttpflvConfig.Enable = true
	group := NewGroup("live", "truncated-hevc", &cfg, GroupOption{}, deadlockTestGroupObserver{})
	t.Cleanup(group.Dispose)

	truncatedRecord := make([]byte, 28)
	copy(truncatedRecord, []byte{0x80, 'h', 'v', 'c', '1'})
	truncatedRecord[27] = 3
	for _, payload := range [][]byte{
		{0x1c},
		{0x80},
		{0x91, 'h', 'v', 'c', '1', 0},
		truncatedRecord,
	} {
		group.OnReadRtmpAvMsg(base.RtmpMsg{
			Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo, MsgLen: uint32(len(payload))},
			Payload: payload,
		})
	}
	stat := group.GetStat(0)
	if stat.VideoCodec != "" || stat.LastVideoFrameUnixSec != 0 || group.httpflvGopCache.VideoSeqHeader != nil {
		t.Fatal("truncated HEVC established video state or entered the GOP cache")
	}
}
