package recorder

import (
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/q191201771/lal/pkg/avc"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/hevc"
)

func videoTimes(msg base.RtmpMsg) (dts, pts time.Duration, ok bool) {
	if msg.Header.MsgTypeId != base.RtmpTypeIdVideo || len(msg.Payload) < 5 {
		return 0, 0, false
	}
	dts = time.Duration(msg.Dts()) * time.Millisecond
	pts = dts + time.Duration(msg.Cts())*time.Millisecond
	return dts, pts, true
}

func parseVideoSeq(msg base.RtmpMsg) (codec string, sps, pps, vps []byte, ok bool) {
	if msg.Header.MsgTypeId != base.RtmpTypeIdVideo || len(msg.Payload) < 5 {
		return "", nil, nil, nil, false
	}
	if msg.IsAvcKeySeqHeader() {
		sps, pps, err := avc.ParseSpsPpsFromSeqHeader(msg.Payload)
		if err != nil || len(sps) < 4 || len(pps) == 0 {
			return "", nil, nil, nil, false
		}
		return string(model.FormatH264), append([]byte(nil), sps...), append([]byte(nil), pps...), nil, true
	}
	if !msg.IsHevcKeySeqHeader() {
		return "", nil, nil, nil, false
	}
	var err error
	if msg.IsEnhanced() {
		vps, sps, pps, err = hevc.ParseVpsSpsPpsFromEnhancedSeqHeader(msg.Payload)
	} else {
		vps, sps, pps, err = hevc.ParseVpsSpsPpsFromSeqHeader(msg.Payload)
	}
	if err != nil || len(vps) == 0 || len(sps) < 4 || len(pps) == 0 {
		return "", nil, nil, nil, false
	}
	return string(model.FormatH265), append([]byte(nil), sps...), append([]byte(nil), pps...), append([]byte(nil), vps...), true
}

func videoNALUs(msg base.RtmpMsg) ([][]byte, bool) {
	if msg.Header.MsgTypeId != base.RtmpTypeIdVideo || len(msg.Payload) < 5 {
		return nil, false
	}
	if msg.IsVideoKeySeqHeader() {
		return nil, false
	}
	var body []byte
	if msg.IsEnchanedHevcNalu() {
		idx := msg.GetEnchanedHevcNaluIndex()
		if idx <= 0 || idx >= len(msg.Payload) {
			return nil, false
		}
		body = msg.Payload[idx:]
	} else if msg.Payload[1] == base.RtmpAvcPacketTypeNalu {
		body = msg.Payload[5:]
	} else {
		return nil, false
	}
	nals, err := avc.SplitNaluAvcc(body)
	if err != nil || len(nals) == 0 {
		return nil, false
	}
	return nals, true
}

func isVideoKey(msg base.RtmpMsg) bool {
	if msg.Header.MsgTypeId != base.RtmpTypeIdVideo || len(msg.Payload) < 2 {
		return false
	}
	return msg.IsVideoKeyNalu()
}

func isVCL(codec string, nalu []byte) (key bool, ok bool) {
	if len(nalu) == 0 {
		return false, false
	}
	if codec == string(model.FormatH265) {
		t := (nalu[0] >> 1) & 0x3F
		if t >= 32 {
			return false, false
		}
		return t == 19 || t == 20, true
	}
	t := nalu[0] & 0x1F
	if t != 1 && t != 5 {
		return false, false
	}
	return t == 5, true
}

type audioFrame struct {
	codec    string
	config   []byte
	frame    []byte
	isConfig bool
}

func parseAudio(msg base.RtmpMsg) (audioFrame, bool) {
	if msg.Header.MsgTypeId != base.RtmpTypeIdAudio || len(msg.Payload) < 2 {
		return audioFrame{}, false
	}
	switch msg.Payload[0] >> 4 {
	case base.RtmpSoundFormatAac:
		if msg.Payload[1] == base.RtmpAacPacketTypeSeqHeader {
			return audioFrame{codec: "aac", config: append([]byte(nil), msg.Payload[2:]...), isConfig: true}, true
		}
		if msg.Payload[1] != base.RtmpAacPacketTypeRaw {
			return audioFrame{}, false
		}
		return audioFrame{codec: "aac", frame: msg.Payload[2:]}, len(msg.Payload) > 2
	case base.RtmpSoundFormatOpus:
		return audioFrame{codec: "opus", frame: msg.Payload[1:]}, len(msg.Payload) > 1
	case base.RtmpSoundFormatG711A:
		return audioFrame{codec: "g711", config: []byte{0, 0, 0, 0x1f, 0x40}, frame: msg.Payload[1:]}, len(msg.Payload) > 1
	case base.RtmpSoundFormatG711U:
		return audioFrame{codec: "g711", config: []byte{1, 0, 0, 0x1f, 0x40}, frame: msg.Payload[1:]}, len(msg.Payload) > 1
	default:
		return audioFrame{}, false
	}
}
