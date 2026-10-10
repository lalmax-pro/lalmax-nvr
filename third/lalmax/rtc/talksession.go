package rtc

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/voip/sdp"
)

// TalkSession is a full-duplex browser audio leg. The SIP leg is independent of
// browser ICE, DTLS, payload numbers and RTP header extensions.
type TalkSession struct {
	pc    *webrtc.PeerConnection
	track *webrtc.TrackLocalStaticRTP
	once  sync.Once
}

func NewTalkSession(ctx context.Context, offer string, codec sdp.Payload, onRTP func(*rtp.Packet), onDisconnected func()) (*TalkSession, string, error) {
	return newTalkSession(ctx, offer, codec, onRTP, onDisconnected, nil, nil, nil)
}
func (s *RtcServer) NewTalkSession(ctx context.Context, offer string, codec sdp.Payload, onRTP func(*rtp.Packet), onDisconnected func()) (*TalkSession, string, error) {
	return newTalkSession(ctx, offer, codec, onRTP, onDisconnected, s.config.ICEHostNATToIPs, s.udpMux, s.tcpMux)
}
func newTalkSession(ctx context.Context, offer string, codec sdp.Payload, onRTP func(*rtp.Packet), onDisconnected func(), ips []string, udpMux ice.UDPMux, tcpMux ice.TCPMux) (*TalkSession, string, error) {
	if err := sdp.ValidateBrowserTalk(offer, codec); err != nil {
		return nil, "", err
	}
	capability := webrtc.RTPCodecCapability{MimeType: "audio/" + codec.CodecName, ClockRate: uint32(codec.ClockRate)}
	if strings.EqualFold(codec.CodecName, "opus") {
		capability.Channels = 2
		capability.SDPFmtpLine = "minptime=10;useinbandfec=1"
	}
	engine := &webrtc.MediaEngine{}
	if err := engine.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: capability, PayloadType: webrtc.PayloadType(codec.PayloadType)}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, "", err
	}
	registry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(engine, registry); err != nil {
		return nil, "", err
	}
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	if len(ips) > 0 {
		settings.SetNAT1To1IPs(ips, webrtc.ICECandidateTypeHost)
	}
	if udpMux != nil {
		settings.SetICEUDPMux(udpMux)
	}
	if tcpMux != nil {
		settings.SetICETCPMux(tcpMux)
		settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeTCP4})
	}
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(engine), webrtc.WithInterceptorRegistry(registry), webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", err
	}
	session := &TalkSession{pc: pc}
	success := false
	defer func() {
		if !success {
			session.Close()
		}
	}()
	track, err := webrtc.NewTrackLocalStaticRTP(capability, "audio", "voip-talk")
	if err != nil {
		return nil, "", err
	}
	session.track = track
	sender, err := pc.AddTrack(track)
	if err != nil {
		return nil, "", err
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if track.Kind() != webrtc.RTPCodecTypeAudio || !strings.EqualFold(track.Codec().MimeType, capability.MimeType) {
			return
		}
		for {
			p, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			if onRTP != nil {
				onRTP(p)
			}
		}
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed && onDisconnected != nil {
			go onDisconnected()
		}
	})
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		return nil, "", err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return nil, "", err
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(answer); err != nil {
		return nil, "", err
	}
	select {
	case <-gathered:
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	if pc.LocalDescription() == nil {
		return nil, "", fmt.Errorf("WebRTC answer unavailable")
	}
	success = true
	return session, pc.LocalDescription().SDP, nil
}

func (s *TalkSession) WriteRTP(p *rtp.Packet) error {
	clean := rtp.Packet{Header: rtp.Header{Version: 2, Marker: p.Marker, SequenceNumber: p.SequenceNumber, Timestamp: p.Timestamp, SSRC: p.SSRC}, Payload: p.Payload}
	return s.track.WriteRTP(&clean)
}
func (s *TalkSession) Close() { s.once.Do(func() { _ = s.pc.Close() }) }
