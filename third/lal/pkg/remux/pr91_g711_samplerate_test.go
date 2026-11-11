package remux_test

import (
    "strings"
    "testing"

    "github.com/q191201771/lal/pkg/base"
    "github.com/q191201771/lal/pkg/remux"
    "github.com/q191201771/lal/pkg/rtmp"
    "github.com/q191201771/lal/pkg/rtprtcp"
    "github.com/q191201771/lal/pkg/sdp"
)

// GStreamer flvmux provides audiocodecid without audiosamplerate for G.711.
// The resulting SDP must use the standard 8 kHz PCMA/PCMU clock.
func TestG711MetadataDefaultsTo8000(t *testing.T) {
    for _, tc := range []struct{name string; codec uint8; initial byte; want string}{
        {"PCMA", base.RtmpSoundFormatG711A, 0x72, "PCMA/8000"},
        {"PCMU", base.RtmpSoundFormatG711U, 0x82, "PCMU/8000"},
    } {
        t.Run(tc.name, func(t *testing.T) {
            metadata, err := rtmp.BuildMetadata(0, 0, int(tc.codec), 0)
            if err != nil { t.Fatal(err) }
            var raw string
            r := remux.NewRtmp2RtspRemuxer(
                func(ctx sdp.LogicContext) { raw = string(ctx.RawSdp) },
                func(rtprtcp.RtpPacket) {},
            )
            r.FeedRtmpMsg(base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdMetadata}, Payload: metadata})
            payload := make([]byte, 161)
            payload[0] = tc.initial
            for i := 0; i < 32; i++ {
                r.FeedRtmpMsg(base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdAudio, TimestampAbs: uint32(i * 20)}, Payload: payload})
            }
            if !strings.Contains(raw, tc.want) {
                t.Fatalf("G711 metadata without sample rate should yield %s; SDP=%s", tc.want, raw)
            }
        })
    }
}
