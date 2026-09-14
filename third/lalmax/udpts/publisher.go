package udpts

import (
	"context"
	"net"

	"github.com/asticode/go-astits"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/naza/pkg/nazalog"
)

type Publisher struct {
	ctx        context.Context
	ss         logic.ICustomizePubSessionContext
	streamName string
	conn       net.PacketConn
	feeder     *tsFrameFeeder
	readTOMs   int
	programID  uint16
}

func NewPublisher(ctx context.Context, conn net.PacketConn, streamName string, readTimeoutMs int, programID uint16) *Publisher {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Publisher{
		ctx:        ctx,
		streamName: streamName,
		conn:       conn,
		feeder:     &tsFrameFeeder{},
		readTOMs:   readTimeoutMs,
		programID:  programID,
	}
}

func (p *Publisher) SetSession(session logic.ICustomizePubSessionContext) {
	p.ss = session
	p.feeder.ss = session
}

func (p *Publisher) Run() error {
	defer func() {
		if p.conn != nil {
			_ = p.conn.Close()
		}
	}()

	sel := newProgramSelector(p.programID, p.streamName)
	dmx := astits.NewDemuxer(p.ctx, newUdpReader(p.conn, p.readTOMs),
		astits.DemuxerOptPacketSize(tsPacketSize),
		astits.DemuxerOptPacketSkipper(func(pkt *astits.Packet) bool {
			if pkt == nil {
				return false
			}
			return sel.skipPID(pkt.Header.PID)
		}),
	)

	for {
		d, err := dmx.NextData()
		if err != nil {
			nazalog.Infof("udp ts stream [%s] disconnected, err=%v", p.streamName, err)
			return err
		}
		if d == nil {
			continue
		}
		if d.PAT != nil {
			sel.onPAT(d.PAT)
			continue
		}
		if d.PMT != nil {
			sel.onPMT(d.PMT)
			continue
		}
		if d.PES == nil || len(d.PES.Data) == 0 {
			continue
		}
		st, ok := sel.streamType(d.PID)
		if !ok {
			continue
		}
		pts, dts := pesTimeMs(d.PES)
		p.feeder.OnFrame(st, append([]byte(nil), d.PES.Data...), pts, dts)
	}
}

func (p *Publisher) Close() {
	if p.conn != nil {
		_ = p.conn.Close()
	}
}
