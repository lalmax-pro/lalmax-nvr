package udpts

import (
	"github.com/asticode/go-astits"
	"github.com/q191201771/lal/pkg/aac"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	codec "github.com/yapingcat/gomedia/go-codec"
)

// tsFrameFeeder 把 MPEG-TS PES 喂给 lal CustomizePub，音频处理与 srt.Publisher 一致。
type tsFrameFeeder struct {
	ss              logic.ICustomizePubSessionContext
	audioSampleRate uint32
	foundAudio      bool
}

func (f *tsFrameFeeder) OnFrame(cid astits.StreamType, frame []byte, pts uint64, dts uint64) {
	if f.ss == nil {
		return
	}
	switch cid {
	case astits.StreamTypeAACAudio:
		f.feedAAC(frame, dts)
	case astits.StreamTypeH264Video:
		_ = f.ss.FeedAvPacket(base.AvPacket{
			Payload:     frame,
			PayloadType: base.AvPacketPtAvc,
			Pts:         int64(pts),
			Timestamp:   int64(dts),
		})
	case astits.StreamTypeH265Video:
		_ = f.ss.FeedAvPacket(base.AvPacket{
			Payload:     frame,
			PayloadType: base.AvPacketPtHevc,
			Pts:         int64(pts),
			Timestamp:   int64(dts),
		})
	}
}

func (f *tsFrameFeeder) feedAAC(frame []byte, dts uint64) {
	if !f.foundAudio {
		asc, err := codec.ConvertADTSToASC(frame)
		if err != nil {
			return
		}
		_ = f.ss.FeedAudioSpecificConfig(asc.Encode())
		f.audioSampleRate = uint32(codec.AACSampleIdxToSample(int(asc.Sample_freq_index)))
		f.foundAudio = true
	}

	var preAudioDts uint64
	ctx := aac.AdtsHeaderContext{}
	for len(frame) > aac.AdtsHeaderLength {
		ctx.Unpack(frame[:])
		if preAudioDts == 0 {
			preAudioDts = dts
		} else if f.audioSampleRate > 0 {
			preAudioDts += uint64(1024 * 1000 / f.audioSampleRate)
		}

		if len(frame) < int(ctx.AdtsLength) {
			return
		}
		payload := frame[aac.AdtsHeaderLength:ctx.AdtsLength]
		if len(frame) > int(ctx.AdtsLength) {
			frame = frame[ctx.AdtsLength:]
		} else {
			frame = frame[0:0]
		}
		_ = f.ss.FeedAvPacket(base.AvPacket{
			Timestamp:   int64(preAudioDts),
			PayloadType: base.AvPacketPtAac,
			Pts:         int64(preAudioDts),
			Payload:     payload,
		})
	}
}
