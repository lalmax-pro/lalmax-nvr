package mpegts

import (
	"github.com/asticode/go-astits"
	"github.com/q191201771/lal/pkg/aac"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
)

// Audio tracks AAC sample rate learned from the first ADTS frame.
type Audio struct {
	SampleRate uint32
	ready      bool
}

// TimeMs converts PES timestamps from the 90 kHz clock to milliseconds.
func TimeMs(pes *astits.PESData) (pts, dts uint64) {
	if pes == nil || pes.Header == nil || pes.Header.OptionalHeader == nil {
		return 0, 0
	}
	oh := pes.Header.OptionalHeader
	if oh.PTS != nil {
		pts = uint64(oh.PTS.Base / 90)
	}
	if oh.DTS != nil {
		dts = uint64(oh.DTS.Base / 90)
	} else {
		dts = pts
	}
	return pts, dts
}

// Feed sends one MPEG-TS access unit into a lal customize publish session.
func Feed(ss logic.ICustomizePubSessionContext, audio *Audio, st astits.StreamType, frame []byte, pts, dts uint64) {
	if ss == nil || len(frame) == 0 {
		return
	}
	switch st {
	case astits.StreamTypeAACAudio:
		feedAAC(ss, audio, frame, dts)
	case astits.StreamTypeH264Video:
		_ = ss.FeedAvPacket(base.AvPacket{
			Payload:     frame,
			PayloadType: base.AvPacketPtAvc,
			Pts:         int64(pts),
			Timestamp:   int64(dts),
		})
	case astits.StreamTypeH265Video:
		_ = ss.FeedAvPacket(base.AvPacket{
			Payload:     frame,
			PayloadType: base.AvPacketPtHevc,
			Pts:         int64(pts),
			Timestamp:   int64(dts),
		})
	}
}

func feedAAC(ss logic.ICustomizePubSessionContext, audio *Audio, frame []byte, dts uint64) {
	if audio == nil {
		audio = &Audio{}
	}
	if !audio.ready {
		asc, err := aac.MakeAscWithAdtsHeader(frame)
		if err != nil {
			return
		}
		_ = ss.FeedAudioSpecificConfig(asc)
		if ctx, err := aac.NewAscContext(asc); err == nil {
			if hz, err := ctx.GetSamplingFrequency(); err == nil && hz > 0 {
				audio.SampleRate = uint32(hz)
			}
		}
		audio.ready = true
	}

	var preAudioDts uint64
	ctx := aac.AdtsHeaderContext{}
	for len(frame) > aac.AdtsHeaderLength {
		if err := ctx.Unpack(frame); err != nil {
			return
		}
		if preAudioDts == 0 {
			preAudioDts = dts
		} else if audio.SampleRate > 0 {
			preAudioDts += uint64(1024 * 1000 / audio.SampleRate)
		}
		if len(frame) < int(ctx.AdtsLength) || int(ctx.AdtsLength) < aac.AdtsHeaderLength {
			return
		}
		payload := frame[aac.AdtsHeaderLength:ctx.AdtsLength]
		if len(frame) > int(ctx.AdtsLength) {
			frame = frame[ctx.AdtsLength:]
		} else {
			frame = frame[:0]
		}
		_ = ss.FeedAvPacket(base.AvPacket{
			Timestamp:   int64(preAudioDts),
			PayloadType: base.AvPacketPtAac,
			Pts:         int64(preAudioDts),
			Payload:     payload,
		})
	}
}
