package media

import (
	"encoding/binary"
	"testing"

	"github.com/q191201771/lal/pkg/base"
	"github.com/stretchr/testify/require"
)

func TestRtmpMsgToFrames_AVCNALU(t *testing.T) {
	nal := []byte{0x65, 0x88, 0x84, 0x00, 0x10}
	avcc := make([]byte, 4+len(nal))
	binary.BigEndian.PutUint32(avcc, uint32(len(nal)))
	copy(avcc[4:], nal)
	payload := append([]byte{0x17, 0x01, 0x00, 0x00, 0x00}, avcc...)
	msg := base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo, TimestampAbs: 1000},
		Payload: payload,
	}
	frames := rtmpMsgToFrames(msg)
	require.Len(t, frames, 1)
	require.Equal(t, FrameVideo, frames[0].Kind)
	require.Equal(t, "h264", frames[0].Codec)
	require.True(t, frames[0].IsKey)
	require.Equal(t, append([]byte{0, 0, 0, 1}, nal...), frames[0].Data)
}

func TestRtmpMsgToFrames_AVCSeqHeader(t *testing.T) {
	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xf8, 0x41, 0xa2}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	payload := []byte{0x17, 0x00, 0x00, 0x00, 0x00, 0x01, 0x42, 0x00, 0x0a, 0xff, 0xe1}
	payload = append(payload, byte(len(sps)>>8), byte(len(sps)))
	payload = append(payload, sps...)
	payload = append(payload, 0x01)
	payload = append(payload, byte(len(pps)>>8), byte(len(pps)))
	payload = append(payload, pps...)

	frames := rtmpMsgToFrames(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo},
		Payload: payload,
	})
	require.Len(t, frames, 2)
	require.True(t, frames[0].IsSeqHeader)
	require.Equal(t, append([]byte{0, 0, 0, 1}, sps...), frames[0].Data)
	require.Equal(t, append([]byte{0, 0, 0, 1}, pps...), frames[1].Data)
}

func TestRtmpMsgToFrames_AAC(t *testing.T) {
	seq := rtmpMsgToFrames(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdAudio, TimestampAbs: 0},
		Payload: []byte{0xa0, 0x00, 0x12, 0x10},
	})
	require.Len(t, seq, 1)
	require.Equal(t, FrameAudio, seq[0].Kind)
	require.Equal(t, "aac", seq[0].Codec)
	require.True(t, seq[0].IsSeqHeader)
	require.Equal(t, []byte{0x12, 0x10}, seq[0].Data)

	raw := rtmpMsgToFrames(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdAudio, TimestampAbs: 23},
		Payload: []byte{0xa0, 0x01, 0x21, 0x22, 0x23},
	})
	require.Len(t, raw, 1)
	require.False(t, raw[0].IsSeqHeader)
	require.Equal(t, []byte{0x21, 0x22, 0x23}, raw[0].Data)
}

func TestLalmaxHTTP_SubscribeFramesUnsupported(t *testing.T) {
	e := &LalmaxHTTP{}
	_, err := e.SubscribeFrames(t.Context(), SubscribeFramesRequest{StreamID: "cam1"})
	require.ErrorIs(t, err, ErrFramesNotSupported)
}
