package logic

import "github.com/q191201771/lal/pkg/base"

// isEncodedVideoFrame excludes AVC/HEVC sequence headers, end-of-sequence
// control packets, and Enhanced-RTMP metadata. Only actual coded video
// packets are evidence for the non-consuming last-video-frame timestamp.
func isEncodedVideoFrame(msg base.RtmpMsg) bool {
	if msg.Header.MsgTypeId != base.RtmpTypeIdVideo || len(msg.Payload) == 0 {
		return false
	}
	p := msg.Payload
	if p[0]&0x80 != 0 {
		if len(p) <= 5 {
			return false
		}
		packetType := p[0] & 0x0f
		if packetType == base.RtmpExPacketTypeCodedFrames {
			return len(p) > 8
		}
		return packetType == base.RtmpExPacketTypeCodedFramesX
	}
	codec := p[0] & 0x0f
	if codec == base.RtmpCodecIdAvc || codec == base.RtmpCodecIdHevc {
		return len(p) > 5 && p[1] == base.RtmpAvcPacketTypeNalu
	}
	// Older non-AVC/HEVC codecs have no AVC packet-type framing.
	return len(p) > 1
}
