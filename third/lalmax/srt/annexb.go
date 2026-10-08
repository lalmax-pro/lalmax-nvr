package srt

import (
	"github.com/q191201771/lal/pkg/aac"
	"github.com/q191201771/lal/pkg/avc"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/hevc"
	"github.com/q191201771/naza/pkg/nazalog"
)

func (s *Subscriber) cacheParamSets(msg base.RtmpMsg) {
	if len(msg.Payload) < 5 {
		return
	}
	var sets []byte
	var err error
	switch {
	case msg.IsAvcKeySeqHeader():
		sets, err = avc.SpsPpsSeqHeader2Annexb(msg.Payload)
		s.videoCodec = base.RtmpCodecIdAvc
	case msg.IsHevcKeySeqHeader():
		if msg.IsEnhanced() {
			sets, err = hevc.VpsSpsPpsEnhancedSeqHeader2Annexb(msg.Payload)
		} else {
			sets, err = hevc.VpsSpsPpsSeqHeader2Annexb(msg.Payload)
		}
		s.videoCodec = base.RtmpCodecIdHevc
	default:
		return
	}
	if err != nil {
		nazalog.Errorf("srt cache parameter sets failed, streamName:%s, err:%v", s.streamName, err)
		return
	}
	s.paramSets = sets
}

func (s *Subscriber) cacheASC(msg base.RtmpMsg) {
	if !msg.IsAacSeqHeader() || len(msg.Payload) < 3 {
		return
	}
	asc, err := aac.NewAscContext(msg.Payload[2:])
	if err != nil {
		nazalog.Errorf("srt cache aac config failed, streamName:%s, err:%v", s.streamName, err)
		return
	}
	s.asc = asc
}

func (s *Subscriber) videoAnnexB(msg base.RtmpMsg) []byte {
	if len(msg.Payload) < 5 {
		return nil
	}
	codec := msg.VideoCodecId()
	if codec != base.RtmpCodecIdAvc && codec != base.RtmpCodecIdHevc {
		return nil
	}
	var (
		nals [][]byte
		err  error
	)
	if codec == base.RtmpCodecIdHevc && msg.IsEnchanedHevcNalu() {
		index := msg.GetEnchanedHevcNaluIndex()
		if index < 0 || index > len(msg.Payload) {
			return nil
		}
		nals, err = avc.SplitNaluAvcc(msg.Payload[index:])
	} else {
		nals, err = avc.SplitNaluAvcc(msg.Payload[5:])
	}
	if err != nil {
		nazalog.Errorf("srt split nalu failed, streamName:%s, err:%v", s.streamName, err)
		return nil
	}

	var vps, sps, pps []byte
	media := make([][]byte, 0, len(nals))
	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		if codec == base.RtmpCodecIdAvc {
			switch avc.ParseNaluType(nal[0]) {
			case avc.NaluTypeAud:
				continue
			case avc.NaluTypeSps:
				sps = nal
				continue
			case avc.NaluTypePps:
				pps = nal
				continue
			}
		} else {
			switch hevc.ParseNaluType(nal[0]) {
			case hevc.NaluTypeAud:
				continue
			case hevc.NaluTypeVps:
				vps = nal
				continue
			case hevc.NaluTypeSps:
				sps = nal
				continue
			case hevc.NaluTypePps:
				pps = nal
				continue
			}
		}
		media = append(media, nal)
	}
	if codec == base.RtmpCodecIdAvc && len(sps) > 0 && len(pps) > 0 {
		s.paramSets = annexbNals(sps, pps)
	} else if codec == base.RtmpCodecIdHevc && len(vps) > 0 && len(sps) > 0 && len(pps) > 0 {
		s.paramSets = annexbNals(vps, sps, pps)
	}
	if len(media) == 0 {
		return nil
	}

	out := make([]byte, 0, len(s.paramSets)+len(media)*8)
	if codec == base.RtmpCodecIdAvc {
		out = append(out, avc.AudNalu...)
	} else {
		out = append(out, hevc.AudNalu...)
	}
	if msg.IsVideoKeyNalu() && len(s.paramSets) > 0 {
		out = append(out, s.paramSets...)
	}
	for _, nal := range media {
		out = append(out, avc.NaluStartCode4...)
		out = append(out, nal...)
	}
	return out
}

func (s *Subscriber) audioADTS(msg base.RtmpMsg) []byte {
	if s.asc == nil || len(msg.Payload) < 2 || msg.AudioCodecId() != base.RtmpSoundFormatAac {
		return nil
	}
	if msg.Payload[1] != base.RtmpAacPacketTypeRaw {
		return nil
	}
	raw := msg.Payload[2:]
	if len(raw) == 0 {
		return nil
	}
	header := s.asc.PackAdtsHeader(len(raw))
	out := make([]byte, 0, len(header)+len(raw))
	out = append(out, header...)
	out = append(out, raw...)
	return out
}

func annexbNals(nals ...[]byte) []byte {
	var out []byte
	for _, nal := range nals {
		out = append(out, avc.NaluStartCode4...)
		out = append(out, nal...)
	}
	return out
}
