package media

import (
	"time"

	"github.com/q191201771/lal/pkg/avc"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/hevc"
)

var annexBStartCode = []byte{0x00, 0x00, 0x00, 0x01}

func rtmpMsgToFrames(msg base.RtmpMsg) []MediaFrame {
	if len(msg.Payload) < 2 {
		return nil
	}
	switch msg.Header.MsgTypeId {
	case base.RtmpTypeIdVideo:
		return rtmpVideoToFrames(msg)
	case base.RtmpTypeIdAudio:
		return rtmpAudioToFrames(msg)
	default:
		return nil
	}
}

func rtmpVideoToFrames(msg base.RtmpMsg) []MediaFrame {
	pts := time.Duration(msg.Pts()) * time.Millisecond
	if msg.IsVideoKeySeqHeader() {
		return videoSeqHeaderFrames(msg, pts)
	}
	isH264 := msg.VideoCodecId() == base.RtmpCodecIdAvc
	codec := "h264"
	if !isH264 {
		codec = "h265"
	}
	index := 5
	if msg.IsEnchanedHevcNalu() {
		index = msg.GetEnchanedHevcNaluIndex()
	}
	if index <= 0 || index >= len(msg.Payload) {
		return nil
	}
	nals, err := avc.SplitNaluAvcc(msg.Payload[index:])
	if err != nil || len(nals) == 0 {
		return nil
	}
	out := make([]MediaFrame, 0, len(nals))
	isKey := msg.IsVideoKeyNalu()
	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		out = append(out, MediaFrame{
			Kind:  FrameVideo,
			Codec: codec,
			IsKey: isKey && isVideoKeyframeNALU(nal, !isH264),
			PTS:   pts,
			Data:  withAnnexB(nal),
		})
	}
	return out
}

func videoSeqHeaderFrames(msg base.RtmpMsg, pts time.Duration) []MediaFrame {
	if msg.IsAvcKeySeqHeader() {
		sps, pps, err := avc.ParseSpsPpsFromSeqHeader(msg.Payload)
		if err != nil {
			return nil
		}
		return []MediaFrame{
			{Kind: FrameVideo, Codec: "h264", IsSeqHeader: true, PTS: pts, Data: withAnnexB(sps)},
			{Kind: FrameVideo, Codec: "h264", IsSeqHeader: true, PTS: pts, Data: withAnnexB(pps)},
		}
	}
	var vps, sps, pps []byte
	var err error
	if msg.IsEnhanced() {
		vps, sps, pps, err = hevc.ParseVpsSpsPpsFromEnhancedSeqHeader(msg.Payload)
	} else {
		vps, sps, pps, err = hevc.ParseVpsSpsPpsFromSeqHeader(msg.Payload)
	}
	if err != nil {
		return nil
	}
	out := make([]MediaFrame, 0, 3)
	if len(vps) > 0 {
		out = append(out, MediaFrame{Kind: FrameVideo, Codec: "h265", IsSeqHeader: true, PTS: pts, Data: withAnnexB(vps)})
	}
	if len(sps) > 0 {
		out = append(out, MediaFrame{Kind: FrameVideo, Codec: "h265", IsSeqHeader: true, PTS: pts, Data: withAnnexB(sps)})
	}
	if len(pps) > 0 {
		out = append(out, MediaFrame{Kind: FrameVideo, Codec: "h265", IsSeqHeader: true, PTS: pts, Data: withAnnexB(pps)})
	}
	return out
}

func rtmpAudioToFrames(msg base.RtmpMsg) []MediaFrame {
	pts := time.Duration(msg.Header.TimestampAbs) * time.Millisecond
	codecID := msg.AudioCodecId()
	switch codecID {
	case base.RtmpSoundFormatAac:
		if len(msg.Payload) < 3 {
			return nil
		}
		if msg.IsAacSeqHeader() {
			return []MediaFrame{{
				Kind:        FrameAudio,
				Codec:       "aac",
				IsSeqHeader: true,
				PTS:         pts,
				Data:        append([]byte(nil), msg.Payload[2:]...),
			}}
		}
		return []MediaFrame{{
			Kind:  FrameAudio,
			Codec: "aac",
			PTS:   pts,
			Data:  append([]byte(nil), msg.Payload[2:]...),
		}}
	case base.RtmpSoundFormatG711A, base.RtmpSoundFormatG711U:
		if len(msg.Payload) < 2 {
			return nil
		}
		codec := "g711a"
		if codecID == base.RtmpSoundFormatG711U {
			codec = "g711u"
		}
		return []MediaFrame{{
			Kind:  FrameAudio,
			Codec: codec,
			PTS:   pts,
			Data:  append([]byte(nil), msg.Payload[1:]...),
		}}
	default:
		return nil
	}
}

func withAnnexB(nal []byte) []byte {
	out := make([]byte, 4+len(nal))
	copy(out, annexBStartCode)
	copy(out[4:], nal)
	return out
}

func isVideoKeyframeNALU(nal []byte, isH265 bool) bool {
	if len(nal) == 0 {
		return false
	}
	if isH265 {
		t := (nal[0] >> 1) & 0x3F
		return t == 19 || t == 20
	}
	return nal[0]&0x1F == 5
}
