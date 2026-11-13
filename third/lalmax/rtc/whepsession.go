package rtc

import (
	"bytes"
	"sync"
	"sync/atomic"
	"time"

	maxlogic "github.com/q191201771/lalmax/logic"

	"github.com/gofrs/uuid"
	"github.com/pion/rtp"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lal/pkg/avc"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/naza/pkg/nazalog"
)

const whepMaxReplayPaceDelay = 5 * time.Millisecond

type whepSession struct {
	group          *maxlogic.Group
	pc             *peerConnection
	subscriberId   string
	lalServer      logic.ILalServer
	videoTrack     *webrtc.TrackLocalStaticRTP
	audioTrack     *webrtc.TrackLocalStaticRTP
	videoSender    *webrtc.RTPSender
	audioSender    *webrtc.RTPSender
	videopacker    *Packer
	audiopacker    *Packer
	msgChan        chan base.RtmpMsg
	streamID       string
	onRunEnd       func()
	closeOnce      sync.Once
	closing        atomic.Bool
	replayPending  atomic.Bool // true while the viewer plays a private GOP snapshot
	awaitKeyframe  atomic.Bool // drop intervening live deltas until a fresh keyframe
	done           chan struct{} // allow close to interrupt snapshot playback
	closeChan      chan bool
	connectedChan  chan struct{}
	connectedOnce  sync.Once
	paceBaseDts    uint32
	paceBaseAt     time.Time
	paceStarted    bool
	wroteBytes     atomic.Uint64
	videoPackets   atomic.Uint64
	videoWriteErrs atomic.Uint64
	pliRequests    atomic.Uint64
	nackRequests   atomic.Uint64
	remoteAddr     atomic.Value
}

// WebRTC viewers are unreliable network consumers; media queue memory must
// never grow without bound. Disconnect slow consumers instead of stalling
// publisher dispatch or silently accumulating a multi-minute backlog.
func boundedWHEPQueueSize(requested int) int {
	if requested <= 0 { return 256 }
	if requested > 2048 { return 2048 }
	return requested
}

func (conn *whepSession) requestClose() {
	if conn == nil { return }
	conn.closeOnce.Do(func() {
		conn.closing.Store(true)
		if conn.done != nil { close(conn.done) }
		select {
		case conn.closeChan <- true:
		default:
		}
	})
}

// enqueue must never block the publisher's OnMsg path while Group holds a
// subscriber's writeMux. Cached GOPs bypass this channel entirely.
func (conn *whepSession) enqueue(msg base.RtmpMsg) {
	if conn == nil || conn.closing.Load() { return }
	select {
	case conn.msgChan <- msg:
	default:
		nazalog.Warnf("WHEP subscriber too slow; disconnecting. id=%s stream=%s", conn.subscriberId, conn.streamID)
		conn.requestClose()
	}
}

func NewWhepSession(appName, streamid string, writeChanSize int, pc *peerConnection, lalServer logic.ILalServer) *whepSession {
	ok, group := maxlogic.GetGroupManagerInstance().GetGroup(maxlogic.NewStreamKey(appName, streamid))
	if !ok {
		nazalog.Errorf("not found stream, appName:%s, streamid:%s", appName, streamid)
		return nil
	}

	u, _ := uuid.NewV4()
	return &whepSession{
		group:         group,
		pc:            pc,
		lalServer:     lalServer,
		subscriberId:  u.String(),
		msgChan:       make(chan base.RtmpMsg, boundedWHEPQueueSize(writeChanSize)),
		streamID:      streamid,
		closeChan:     make(chan bool, 2),
		done:          make(chan struct{}),
		connectedChan: make(chan struct{}, 1),
	}
}

func (conn *whepSession) GetAnswerSDP(offer string) (sdp string) {
	var err error

	videoHeader := conn.group.GetVideoSeqHeaderMsg()
	if videoHeader != nil {
		if videoHeader.IsAvcKeySeqHeader() {
			conn.videoTrack, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", "lalmax")
			if err != nil {
				nazalog.Error(err)
				return
			}

			conn.videoSender, err = conn.pc.AddTrack(conn.videoTrack)
			if err != nil {
				nazalog.Error(err)
				return
			}
			go conn.observeVideoRTCP(conn.videoSender)

			conn.videopacker = NewPacker(PacketH264, videoHeader.Payload)
		} else if videoHeader.IsHevcKeySeqHeader() {
			conn.videoTrack, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH265}, "video", "lalmax")
			if err != nil {
				nazalog.Error(err)
				return
			}

			conn.videoSender, err = conn.pc.AddTrack(conn.videoTrack)
			if err != nil {
				nazalog.Error(err)
				return
			}
			go conn.observeVideoRTCP(conn.videoSender)

			conn.videopacker = NewPacker(PacketHEVC, videoHeader.Payload)
		}
	}

	audioHeader := conn.group.GetAudioSeqHeaderMsg()
	if audioHeader != nil {
		var mimeType string
		audioId := audioHeader.AudioCodecId()
		switch audioId {
		case base.RtmpSoundFormatG711A:
			conn.audioTrack, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMA}, "audio", "lalmax")
			if err != nil {
				nazalog.Error(err)
				return
			}

			mimeType = PacketPCMA
		case base.RtmpSoundFormatG711U:
			conn.audioTrack, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU}, "audio", "lalmax")
			if err != nil {
				nazalog.Error(err)
				return
			}

			mimeType = PacketPCMU
		case base.RtmpSoundFormatOpus:
			conn.audioTrack, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "lalmax")
			if err != nil {
				nazalog.Error(err)
				return
			}

			mimeType = PacketOPUS
		default:
			nazalog.Error("unsupport audio codeid:", audioId)
		}

		if conn.audioTrack != nil {
			conn.audioSender, err = conn.pc.AddTrack(conn.audioTrack)
			if err != nil {
				nazalog.Error(err)
				return
			}

			conn.audiopacker = NewPacker(mimeType, nil)
		}
	}

	gatherComplete := webrtc.GatheringCompletePromise(conn.pc.PeerConnection)

	conn.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(offer),
	})

	answer, err := conn.pc.CreateAnswer(nil)
	if err != nil {
		nazalog.Error(err)
		return
	}

	err = conn.pc.SetLocalDescription(answer)
	if err != nil {
		nazalog.Error(err)
		return
	}

	<-gatherComplete

	sdp = conn.pc.LocalDescription().SDP
	return
}

func (conn *whepSession) Run() {
	defer conn.Close()
	if conn.onRunEnd != nil { defer conn.onRunEnd() }
	conn.pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		nazalog.Info("peer connection state: ", state.String())

		switch state {
		case webrtc.PeerConnectionStateConnected:
			conn.signalConnected()
		case webrtc.PeerConnectionStateDisconnected:
			fallthrough
		case webrtc.PeerConnectionStateFailed:
			fallthrough
		case webrtc.PeerConnectionStateClosed:
			conn.requestClose()
		}
	})

	if conn.pc.ConnectionState() == webrtc.PeerConnectionStateConnected {
		conn.signalConnected()
	}

	for {
		select {
		case <-conn.connectedChan:
			// Register with an atomic GOP snapshot; never replay while
			// holding Group's per-subscriber delivery lock.
			conn.replayPending.Store(true)
			snapshot, ok := conn.group.AddSubscriberWithSnapshot(maxlogic.SubscriberInfo{
				SubscriberID: conn.subscriberId,
				Protocol:     maxlogic.SubscriberProtocolWHEP,
			}, conn)
			if !ok { return }
			// Live frames arriving during replay are skipped, not queued
			// or blocked. Resume only on a fresh video keyframe.
			conn.playCachedSnapshot(snapshot)
			conn.awaitKeyframe.Store(conn.videoTrack != nil)
			conn.replayPending.Store(false)
			goto connected
		case <-conn.closeChan:
			nazalog.Info("RemoveConsumer, connid:", conn.subscriberId)
			conn.group.RemoveSubscriber(conn.subscriberId)
			return
		}
	}

connected:
	statsTicker := time.NewTicker(15 * time.Second)
	defer statsTicker.Stop()
	for {
		select {
		case msg := <-conn.msgChan:
			conn.sendMediaMessage(msg, false)
		case <-statsTicker.C:
			nazalog.Infof("WHEP video stats subscriber=%s rtp_packets=%d write_errors=%d pli=%d nack=%d", conn.subscriberId, conn.videoPackets.Load(), conn.videoWriteErrs.Load(), conn.pliRequests.Load(), conn.nackRequests.Load())
		case <-conn.closeChan:
			nazalog.Info("RemoveConsumer, connid:", conn.subscriberId)
			conn.group.RemoveSubscriber(conn.subscriberId)
			return
		}
	}
}

func (conn *whepSession) signalConnected() {
	conn.connectedOnce.Do(func() {
		conn.refreshRemoteAddr()
		conn.connectedChan <- struct{}{}
	})
}

// playCachedSnapshot processes a bounded-by-GOP-cache snapshot in the
// viewer's own Run goroutine. Its pacing can never hold Group.writeMux or
// block any publisher/recorder. Close interrupts the replay promptly.
func (conn *whepSession) playCachedSnapshot(snapshot []base.RtmpMsg) {
	conn.paceBaseAt = time.Time{}
	conn.paceBaseDts = 0
	conn.paceStarted = false
	for _, msg := range snapshot {
		if conn.closing.Load() { break }
		if conn.done != nil {
			select {
			case <-conn.done:
				return
			default:
			}
		}
		conn.sendMediaMessage(msg, true)
	}
	conn.paceBaseAt = time.Time{}
	conn.paceBaseDts = 0
	conn.paceStarted = false
}

// sendMediaMessage is owned by the viewer's Run goroutine, including codec
// changes and RTP packetization. It never runs from the publisher's OnMsg.
func (conn *whepSession) sendMediaMessage(msg base.RtmpMsg, cached bool) {
	if msg.Header.MsgTypeId == base.RtmpTypeIdVideo && !msg.HasCompleteVideoHeader() {
		return
	}
	if msg.IsVideoKeySeqHeader() {
		conn.updateVideoCodec(msg)
		return
	}
	if cached { conn.paceReplayMsg(msg) }
	if msg.Header.MsgTypeId == base.RtmpTypeIdAudio && conn.audioTrack != nil {
		conn.sendAudio(msg)
	} else if msg.Header.MsgTypeId == base.RtmpTypeIdVideo && conn.videoTrack != nil {
		conn.sendVideo(msg)
	}
}

func (conn *whepSession) paceReplayMsg(msg base.RtmpMsg) {
	if msg.Header.MsgTypeId != base.RtmpTypeIdAudio && msg.Header.MsgTypeId != base.RtmpTypeIdVideo {
		return
	}
	if msg.IsVideoKeySeqHeader() || msg.IsAacSeqHeader() {
		return
	}

	if !conn.paceStarted {
		conn.paceBaseDts = msg.Dts()
		conn.paceBaseAt = time.Now()
		conn.paceStarted = true
		return
	}

	dtsDelta := int64(msg.Dts()) - int64(conn.paceBaseDts)
	if dtsDelta <= 0 {
		return
	}

	mediaElapsed := time.Duration(dtsDelta) * time.Millisecond
	delay := time.Until(conn.paceBaseAt.Add(mediaElapsed))
	if delay <= 0 {
		return
	}
	if delay > whepMaxReplayPaceDelay {
		delay = whepMaxReplayPaceDelay
	}

	time.Sleep(delay)
}

func (conn *whepSession) OnMsg(msg base.RtmpMsg) {
	// OnMsg is invoked under Group's per-subscriber writeMux. It must
	// never wait for cached replay pacing or a full viewer queue.
	if conn.replayPending.Load() || conn.closing.Load() { return }
	switch msg.Header.MsgTypeId {
	case base.RtmpTypeIdMetadata:
		return
	case base.RtmpTypeIdAudio:
		if conn.audioTrack != nil && !conn.awaitKeyframe.Load() {
			conn.enqueue(msg)
		}
	case base.RtmpTypeIdVideo:
		if !msg.HasCompleteVideoHeader() {
			return
		}
		if msg.IsVideoKeySeqHeader() {
			conn.enqueue(msg)
			return
		}
		if conn.videoTrack == nil { return }
		if conn.awaitKeyframe.Load() {
			if !msg.IsVideoKeyNalu() { return }
			// Cached GOP replay may have spanned a codec parameter change.
			// Before the first fresh keyframe, forward the latest sequence
			// header so the decoder does not resume with stale SPS/PPS/VPS.
			if conn.group != nil {
				if header := conn.group.GetVideoSeqHeaderMsg(); header != nil {
					conn.enqueue(*header)
				}
			}
			conn.awaitKeyframe.Store(false)
		}
		conn.enqueue(msg)
	}
}

func (conn *whepSession) OnStop() {
	conn.requestClose()
}

func (conn *whepSession) sendAudio(msg base.RtmpMsg) {
	if conn.audiopacker != nil {
		pkts, err := conn.audiopacker.Encode(msg)
		if err != nil {
			nazalog.Error(err)
			return
		}

		for _, pkt := range pkts {
			if err := conn.audioTrack.WriteRTP(pkt); err != nil {
				continue
			}
			conn.recordSentRTP(pkt)
		}
	}
}

// Read RTCP feedback to diagnose browser keyframe/retransmission requests.
func (conn *whepSession) observeVideoRTCP(sender *webrtc.RTPSender) {
	for {
		packets, _, err := sender.ReadRTCP()
		if err != nil { return }
		for _, packet := range packets {
			switch feedback := packet.(type) {
			case *rtcp.PictureLossIndication:
				conn.pliRequests.Add(1)
			case *rtcp.TransportLayerNack:
				conn.nackRequests.Add(uint64(len(feedback.Nacks)))
			}
		}
	}
}

func (conn *whepSession) sendVideo(msg base.RtmpMsg) {
	if conn.videopacker != nil {

		pkts, err := conn.videopacker.Encode(msg)
		if err != nil {
			nazalog.Error(err)
			return
		}

		for _, pkt := range pkts {
			if err := conn.videoTrack.WriteRTP(pkt); err != nil {
				conn.videoWriteErrs.Add(1)
				continue
			}
			conn.videoPackets.Add(1)
			conn.recordSentRTP(pkt)
		}
	}
}

func (conn *whepSession) updateVideoCodec(msg base.RtmpMsg) {
	if conn.videopacker == nil {
		return
	}

	if msg.IsAvcKeySeqHeader() {
		sps, pps, err := avc.ParseSpsPpsFromSeqHeader(msg.Payload)
		if err != nil {
			nazalog.Error("ParseSpsPpsFromSeqHeader err:", err)
			return
		}

		if h264Encoder, ok := conn.videopacker.enc.(*H264RtpEncoder); ok {
			if bytes.Equal(h264Encoder.sps, sps) && bytes.Equal(h264Encoder.pps, pps) {
				return
			}
		}

		conn.videopacker.UpdateVideoCodec(nil, sps, pps)
		return
	}

	if msg.IsHevcKeySeqHeader() {
		vps, sps, pps, err := parseHEVCSequenceHeader(msg.Payload)
		if err != nil {
			nazalog.Error("ParseVpsSpsPpsFromSeqHeader err:", err)
			return
		}

		if hevcEncoder, ok := conn.videopacker.enc.(*HevcRtpEncoder); ok {
			if bytes.Equal(hevcEncoder.vps, vps) && bytes.Equal(hevcEncoder.sps, sps) && bytes.Equal(hevcEncoder.pps, pps) {
				return
			}
		}

		conn.videopacker.UpdateVideoCodec(vps, sps, pps)
	}
}

func (conn *whepSession) Close() {
	conn.requestClose()
	if conn.pc != nil {
		conn.pc.Close()
	}
}

func (conn *whepSession) GetSubscriberStat() maxlogic.SubscriberStat {
	conn.refreshRemoteAddr()
	return maxlogic.SubscriberStat{
		RemoteAddr:    conn.loadRemoteAddr(),
		WroteBytesSum: conn.wroteBytes.Load(),
	}
}

func (conn *whepSession) recordSentRTP(pkt *rtp.Packet) {
	if pkt == nil {
		return
	}
	conn.wroteBytes.Add(uint64(pkt.MarshalSize()))
}

func (conn *whepSession) refreshRemoteAddr() {
	if remoteAddr := conn.currentRemoteAddr(); remoteAddr != "" {
		conn.remoteAddr.Store(remoteAddr)
	}
}

func (conn *whepSession) currentRemoteAddr() string {
	if conn.videoSender != nil {
		if remoteAddr := remoteAddrFromDTLSTransport(conn.videoSender.Transport()); remoteAddr != "" {
			return remoteAddr
		}
	}
	if conn.audioSender != nil {
		if remoteAddr := remoteAddrFromDTLSTransport(conn.audioSender.Transport()); remoteAddr != "" {
			return remoteAddr
		}
	}
	if sctp := conn.pc.SCTP(); sctp != nil {
		return remoteAddrFromDTLSTransport(sctp.Transport())
	}
	return ""
}

func (conn *whepSession) loadRemoteAddr() string {
	v := conn.remoteAddr.Load()
	addr, _ := v.(string)
	return addr
}
