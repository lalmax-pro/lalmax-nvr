package srt

import (
	"context"
	"sync"

	maxlogic "github.com/q191201771/lalmax/logic"

	"github.com/asticode/go-astits"
	srt "github.com/datarhei/gosrt"
	"github.com/gofrs/uuid"
	"github.com/q191201771/lal/pkg/aac"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/naza/pkg/nazalog"
)

const (
	srtVideoPID = 0x100
	srtAudioPID = 0x101
)

type Subscriber struct {
	ctx               context.Context
	conn              srt.Conn
	streamName        string
	subscriberId      string
	maxSendPacketSize int
	group             *maxlogic.Group
	mux               *astits.Muxer
	out               *tsBatch
	closeOnce         sync.Once

	videoPID   uint16
	audioPID   uint16
	videoCodec uint8
	paramSets  []byte
	asc        *aac.AscContext
}

func NewSubscriber(ctx context.Context, conn srt.Conn, streamName string, maxSendPacketSize int) *Subscriber {
	u, _ := uuid.NewV4()
	sub := &Subscriber{
		ctx:               ctx,
		conn:              conn,
		streamName:        streamName,
		subscriberId:      u.String(),
		maxSendPacketSize: maxSendPacketSize,
	}
	nazalog.Infof("create srt subscriber, streamName:%s, subscriberId:%s", streamName, sub.subscriberId)
	return sub
}

func (s *Subscriber) Run() {
	ok, group := maxlogic.GetGroupManagerInstance().GetGroupByStreamName(s.streamName)
	if !ok {
		nazalog.Warnf("not found stream group, streamName:%s", s.streamName)
		s.closeConn()
		return
	}
	s.group = group
	s.out = newTSBatch(s.ctx, s.conn, s.maxSendPacketSize, func() {
		s.closeConn()
		group.RemoveSubscriber(s.subscriberId)
	})
	s.mux = astits.NewMuxer(s.ctx, s.out)
	group.AddSubscriber(maxlogic.SubscriberInfo{
		SubscriberID: s.subscriberId,
		Protocol:     maxlogic.SubscriberProtocolSRT,
	}, s)
}

func (s *Subscriber) OnMsg(msg base.RtmpMsg) {
	if s.mux == nil {
		return
	}
	switch msg.Header.MsgTypeId {
	case base.RtmpTypeIdVideo:
		if len(msg.Payload) >= 5 && msg.IsVideoKeySeqHeader() {
			s.cacheParamSets(msg)
		}
	case base.RtmpTypeIdAudio:
		if len(msg.Payload) >= 2 && msg.IsAacSeqHeader() {
			s.cacheASC(msg)
		}
	default:
		return
	}
	s.ensureStreams()

	switch msg.Header.MsgTypeId {
	case base.RtmpTypeIdVideo:
		if msg.IsVideoKeySeqHeader() || s.videoPID == 0 {
			return
		}
		au := s.videoAnnexB(msg)
		if len(au) == 0 {
			return
		}
		dts := uint64(msg.Dts())
		s.writePES(s.videoPID, au, dts+uint64(msg.Cts()), dts, msg.IsVideoKeyNalu())
	case base.RtmpTypeIdAudio:
		adts := s.audioADTS(msg)
		if len(adts) == 0 || s.audioPID == 0 {
			return
		}
		dts := uint64(msg.Dts())
		s.writePES(s.audioPID, adts, dts, dts, false)
	}
}

func (s *Subscriber) OnStop() {
	nazalog.Info("srt subscriber onStop")
	s.closeConn()
}

func (s *Subscriber) GetSubscriberStat() maxlogic.SubscriberStat {
	if s == nil || s.conn == nil {
		return maxlogic.SubscriberStat{}
	}

	var stats srt.Statistics
	s.conn.Stats(&stats)

	stat := maxlogic.SubscriberStat{
		ReadBytesSum:  stats.Accumulated.ByteRecv,
		WroteBytesSum: stats.Accumulated.ByteSent,
	}
	if remoteAddr := s.conn.RemoteAddr(); remoteAddr != nil {
		stat.RemoteAddr = remoteAddr.String()
	}
	return stat
}

func (s *Subscriber) closeConn() {
	s.closeOnce.Do(func() {
		if s.conn != nil {
			s.conn.Close()
		}
	})
}

func (s *Subscriber) ensureStreams() {
	if s.mux == nil {
		return
	}
	if s.videoCodec == 0 && s.group != nil {
		if h := s.group.GetVideoSeqHeaderMsg(); h != nil {
			s.cacheParamSets(*h)
		}
	}
	if s.asc == nil && s.group != nil {
		if h := s.group.GetAudioSeqHeaderMsg(); h != nil {
			s.cacheASC(*h)
		}
	}
	if s.videoPID == 0 && s.videoCodec != 0 {
		st := astits.StreamTypeH264Video
		if s.videoCodec == base.RtmpCodecIdHevc {
			st = astits.StreamTypeH265Video
		}
		if err := s.mux.AddElementaryStream(astits.PMTElementaryStream{
			ElementaryPID: srtVideoPID,
			StreamType:    st,
		}); err != nil {
			nazalog.Errorf("srt add video stream failed, streamName:%s, err:%v", s.streamName, err)
			return
		}
		s.mux.SetPCRPID(srtVideoPID)
		s.videoPID = srtVideoPID
	}
	if s.audioPID == 0 && s.asc != nil {
		if err := s.mux.AddElementaryStream(astits.PMTElementaryStream{
			ElementaryPID: srtAudioPID,
			StreamType:    astits.StreamTypeAACAudio,
		}); err != nil {
			nazalog.Errorf("srt add audio stream failed, streamName:%s, err:%v", s.streamName, err)
			return
		}
		if s.videoPID == 0 {
			s.mux.SetPCRPID(srtAudioPID)
		}
		s.audioPID = srtAudioPID
	}
}

func (s *Subscriber) writePES(pid uint16, data []byte, ptsMs, dtsMs uint64, randomAccess bool) {
	if s.mux == nil || pid == 0 || len(data) == 0 {
		return
	}
	pts := astits.ClockReference{Base: int64(ptsMs) * 90}
	dts := astits.ClockReference{Base: int64(dtsMs) * 90}
	af := &astits.PacketAdaptationField{RandomAccessIndicator: randomAccess}
	if pid == s.pcrPID() {
		af.HasPCR = true
		pcr := astits.ClockReference{Base: dts.Base}
		af.PCR = &pcr
	}
	_, err := s.mux.WriteData(&astits.MuxerData{
		PID:             pid,
		AdaptationField: af,
		PES: &astits.PESData{
			Data: data,
			Header: &astits.PESHeader{
				OptionalHeader: &astits.PESOptionalHeader{
					PTS:                    &pts,
					DTS:                    &dts,
					PTSDTSIndicator:        astits.PTSDTSIndicatorBothPresent,
					DataAlignmentIndicator: true,
				},
			},
		},
	})
	if err != nil {
		nazalog.Errorf("srt ts mux write failed, streamName:%s, err:%v", s.streamName, err)
	}
}

func (s *Subscriber) pcrPID() uint16 {
	if s.videoPID != 0 {
		return s.videoPID
	}
	return s.audioPID
}

type tsBatch struct {
	ctx    context.Context
	conn   srt.Conn
	buf    []byte
	limit  int
	onFail func()
	err    error
}

func newTSBatch(ctx context.Context, conn srt.Conn, maxSendPacketSize int, onFail func()) *tsBatch {
	if maxSendPacketSize <= 0 {
		maxSendPacketSize = 4
	}
	return &tsBatch{
		ctx:    ctx,
		conn:   conn,
		limit:  maxSendPacketSize * astits.MpegTsPacketSize,
		onFail: onFail,
	}
}

func (w *tsBatch) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	select {
	case <-w.ctx.Done():
		w.fail(w.ctx.Err())
		return 0, w.err
	default:
	}
	w.buf = append(w.buf, p...)
	if len(w.buf) >= w.limit {
		if err := w.flush(); err != nil {
			w.fail(err)
			return 0, err
		}
	}
	return len(p), nil
}

func (w *tsBatch) flush() error {
	if len(w.buf) == 0 {
		return nil
	}
	if _, err := w.conn.Write(w.buf); err != nil {
		return err
	}
	w.buf = w.buf[:0]
	return nil
}

func (w *tsBatch) fail(err error) {
	if w.err != nil {
		return
	}
	w.err = err
	if w.onFail != nil {
		w.onFail()
	}
}
