package udpts

import (
	"github.com/asticode/go-astits"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/lalmax/mpegts"
)

// tsFrameFeeder 把 MPEG-TS PES 喂给 lal CustomizePub。
type tsFrameFeeder struct {
	ss    logic.ICustomizePubSessionContext
	audio mpegts.Audio
}

func (f *tsFrameFeeder) OnFrame(cid astits.StreamType, frame []byte, pts uint64, dts uint64) {
	mpegts.Feed(f.ss, &f.audio, cid, frame, pts, dts)
}
