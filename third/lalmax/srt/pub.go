package srt

import (
	"bufio"
	"context"

	"github.com/asticode/go-astits"
	srt "github.com/datarhei/gosrt"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/lalmax/mpegts"
	"github.com/q191201771/naza/pkg/nazalog"
)

type Publisher struct {
	ctx        context.Context
	srv        *SrtServer
	ss         logic.ICustomizePubSessionContext
	streamName string
	conn       srt.Conn
}

func NewPublisher(ctx context.Context, conn srt.Conn, streamName string, srv *SrtServer) *Publisher {
	pub := &Publisher{
		ctx:        ctx,
		srv:        srv,
		streamName: streamName,
		conn:       conn,
	}
	nazalog.Infof("create srt publisher, streamName:%s", streamName)
	return pub
}

func (p *Publisher) SetSession(session logic.ICustomizePubSessionContext) {
	p.ss = session
}

func (p *Publisher) Run() {
	defer func() {
		p.conn.Close()
		p.srv.Remove(p.streamName, p.ss)
	}()
	if p.ss == nil {
		nazalog.Errorf("srt publisher has no session, streamName:%s", p.streamName)
		return
	}

	dmx := astits.NewDemuxer(p.ctx, bufio.NewReader(p.conn))
	esType := map[uint16]astits.StreamType{}
	var audio mpegts.Audio
	for {
		d, err := dmx.NextData()
		if err != nil {
			nazalog.Infof("stream [%s] disconnected", p.streamName)
			return
		}
		if d == nil {
			continue
		}
		if d.PMT != nil {
			for _, stream := range d.PMT.ElementaryStreams {
				if stream == nil {
					continue
				}
				esType[stream.ElementaryPID] = stream.StreamType
			}
			continue
		}
		if d.PES == nil || len(d.PES.Data) == 0 {
			continue
		}
		st, ok := esType[d.PID]
		if !ok {
			continue
		}
		pts, dts := mpegts.TimeMs(d.PES)
		mpegts.Feed(p.ss, &audio, st, append([]byte(nil), d.PES.Data...), pts, dts)
	}
}
