package voip

import (
	"fmt"
	"github.com/pion/rtp"
	"sync"
	"time"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/rtprtcp"
	"github.com/q191201771/lalmax/voip/media"
	"github.com/q191201771/lalmax/voip/sdp"
	"github.com/q191201771/naza/pkg/nazalog"
)

type PubSession struct {
	closeOnce      sync.Once
	closed         bool
	negotiated     sdp.Negotiated
	feedMu         sync.Mutex
	streamName     string
	callID         string
	fromUser       string
	toUser         string
	audioSession   *media.RtpSession
	videoSession   *media.RtpSession
	bundle         bool
	onAvPacket     rtprtcp.OnAvPacket
	onDTMFEvent    media.DTMFEventHandler
	onTimeout      func()
	portAllocator  *media.PortAllocator
	allocatedPorts []int
	sessionStat    base.BasicSessionStat
	mutex          sync.Mutex
	log            nazalog.Logger
	audioSrtpCtx   *media.SrtpContext // Audio SRTP上下文
	videoSrtpCtx   *media.SrtpContext // Video SRTP上下文
	audioDTLSSRTP  *media.DTLSSRTPConfig
	videoDTLSSRTP  *media.DTLSSRTPConfig
}

type PubSessionConfig struct {
	StreamName            string
	CallID                string
	FromUser              string
	ToUser                string
	MediaIP               string
	Negotiated            sdp.Negotiated
	PortAllocator         *media.PortAllocator
	AudioPort             int // Pre-allocated audio port (optional, will allocate if 0)
	VideoPort             int // Pre-allocated video port (optional, will allocate if 0)
	AudioRTCPPort         int
	VideoRTCPPort         int
	OnAvPacket            rtprtcp.OnAvPacket
	OnAudioRTP            func(*rtp.Packet)
	RemoteAudioRTPAddress string
	RemoteSourceIP        string
	OnDTMFEvent           media.DTMFEventHandler
	OnTimeout             func()
	TimeoutMs             int
	Log                   nazalog.Logger
	AudioSrtpCtx          *media.SrtpContext // Audio SRTP上下文（如果使用SRTP）
	VideoSrtpCtx          *media.SrtpContext // Video SRTP上下文（如果使用SRTP）
	AudioDTLSSRTP         *media.DTLSSRTPConfig
	VideoDTLSSRTP         *media.DTLSSRTPConfig
}

func NewPubSession(cfg PubSessionConfig) (*PubSession, error) {
	if cfg.Log == nil {
		cfg.Log = nazalog.GetGlobalLogger()
	}
	if cfg.OnAvPacket == nil {
		return nil, fmt.Errorf("OnAvPacket callback is required")
	}
	if cfg.PortAllocator == nil {
		return nil, fmt.Errorf("PortAllocator is required")
	}
	if cfg.MediaIP == "" {
		cfg.MediaIP = "0.0.0.0"
	}
	if cfg.TimeoutMs == 0 {
		cfg.TimeoutMs = 15000
	}

	session := &PubSession{
		streamName:    cfg.StreamName,
		negotiated:    cfg.Negotiated,
		callID:        cfg.CallID,
		fromUser:      cfg.FromUser,
		toUser:        cfg.ToUser,
		onAvPacket:    cfg.OnAvPacket,
		onDTMFEvent:   cfg.OnDTMFEvent,
		onTimeout:     cfg.OnTimeout,
		portAllocator: cfg.PortAllocator,
		sessionStat:   base.NewBasicSessionStat(base.SessionTypeCustomizePub, ""),
		log:           cfg.Log,
		bundle:        cfg.Negotiated.Bundle,
		audioSrtpCtx:  cfg.AudioSrtpCtx,
		videoSrtpCtx:  cfg.VideoSrtpCtx,
		audioDTLSSRTP: cfg.AudioDTLSSRTP,
		videoDTLSSRTP: cfg.VideoDTLSSRTP,
	}

	// Preallocated ports are transferred to the publisher even if construction fails.
	if cfg.AudioPort > 0 {
		session.allocatedPorts = append(session.allocatedPorts, cfg.AudioPort)
	}
	if cfg.VideoPort > 0 && cfg.VideoPort != cfg.AudioPort {
		session.allocatedPorts = append(session.allocatedPorts, cfg.VideoPort)
	}
	for _, port := range []int{cfg.AudioRTCPPort, cfg.VideoRTCPPort} {
		if port > 0 && port != cfg.AudioPort && port != cfg.VideoPort {
			session.allocatedPorts = append(session.allocatedPorts, port)
		}
	}

	onPacket := func(pkt base.AvPacket) {
		if (pkt.IsAudio() && cfg.Negotiated.AudioHeld) || (pkt.IsVideo() && cfg.Negotiated.VideoHeld) {
			return
		}
		session.mutex.Lock()
		cb := session.onAvPacket
		session.mutex.Unlock()
		if cb != nil {
			session.feedMu.Lock()
			defer session.feedMu.Unlock()
			cb(pkt)
		}
	}

	if cfg.Negotiated.Bundle {
		if cfg.Negotiated.Audio == nil || cfg.Negotiated.Video == nil {
			session.dispose()
			return nil, fmt.Errorf("bundle session requires both audio and video")
		}

		var audioPort int
		var err error
		if cfg.AudioPort > 0 {
			audioPort = cfg.AudioPort
		} else {
			audioPort, err = cfg.PortAllocator.Alloc()
			if err != nil {
				session.dispose()
				return nil, fmt.Errorf("allocate audio port failed: %w", err)
			}
		}
		if cfg.AudioPort == 0 {
			session.allocatedPorts = append(session.allocatedPorts, audioPort)
		}

		audioSession, err := media.NewRtpSession(media.RtpSessionConfig{
			MediaIP:      cfg.MediaIP,
			Port:         audioPort,
			Payload:      cfg.Negotiated.Audio,
			VideoPayload: cfg.Negotiated.Video,
			DTMFPayload:  cfg.Negotiated.DTMF,
			OnAvPacket:   onPacket,
			OnDTMFEvent:  cfg.OnDTMFEvent,
			OnTimeout:    cfg.OnTimeout,
			TimeoutMs:    cfg.TimeoutMs,
			Log:          cfg.Log,
			SrtpContext:  cfg.AudioSrtpCtx, // 传递SRTP上下文
			DTLSSRTP:     cfg.AudioDTLSSRTP,
			RTCPMux:      true,
			VideoPLI:     cfg.Negotiated.VideoPLI,
		})
		if err != nil {
			session.dispose()
			return nil, fmt.Errorf("create bundled rtp session failed: %w", err)
		}
		session.audioSession = audioSession
	} else {
		if cfg.Negotiated.Audio != nil {
			var audioPort int
			var err error

			// Use pre-allocated port if provided, otherwise allocate new one
			if cfg.AudioPort > 0 {
				audioPort = cfg.AudioPort
			} else {
				audioPort, cfg.AudioRTCPPort, err = cfg.PortAllocator.AllocMedia(cfg.Negotiated.AudioRTCPMux)
				if err != nil {
					session.dispose()
					return nil, fmt.Errorf("allocate audio port failed: %w", err)
				}
			}
			// Add to allocated ports list so it will be freed on dispose
			if cfg.AudioPort == 0 {
				session.allocatedPorts = append(session.allocatedPorts, audioPort)
				if cfg.AudioRTCPPort != audioPort {
					session.allocatedPorts = append(session.allocatedPorts, cfg.AudioRTCPPort)
				}
			}

			audioSession, err := media.NewRtpSession(media.RtpSessionConfig{
				MediaIP:           cfg.MediaIP,
				Port:              audioPort,
				Payload:           cfg.Negotiated.Audio,
				OnRTP:             cfg.OnAudioRTP,
				RemoteRTPAddress:  cfg.RemoteAudioRTPAddress,
				RemoteSourceIP:    cfg.RemoteSourceIP,
				DTMFPayload:       cfg.Negotiated.DTMF,
				OnAvPacket:        onPacket,
				OnDTMFEvent:       cfg.OnDTMFEvent,
				OnTimeout:         cfg.OnTimeout,
				TimeoutMs:         cfg.TimeoutMs,
				Log:               cfg.Log,
				SrtpContext:       cfg.AudioSrtpCtx, // 传递SRTP上下文
				DTLSSRTP:          cfg.AudioDTLSSRTP,
				RTCPMux:           cfg.Negotiated.AudioRTCPMux,
				RTCPPort:          cfg.AudioRTCPPort,
				RemoteRTCPAddress: cfg.Negotiated.AudioRTCPAddress,
			})
			if err != nil {
				session.dispose()
				return nil, fmt.Errorf("create audio rtp session failed: %w", err)
			}
			session.audioSession = audioSession
		}

		if cfg.Negotiated.Video != nil {
			var videoPort int
			var err error

			// Use pre-allocated port if provided, otherwise allocate new one
			if cfg.VideoPort > 0 {
				videoPort = cfg.VideoPort
			} else {
				videoPort, cfg.VideoRTCPPort, err = cfg.PortAllocator.AllocMedia(cfg.Negotiated.VideoRTCPMux)
				if err != nil {
					session.dispose()
					return nil, fmt.Errorf("allocate video port failed: %w", err)
				}
			}
			// Add to allocated ports list so it will be freed on dispose
			if cfg.VideoPort == 0 {
				session.allocatedPorts = append(session.allocatedPorts, videoPort)
				if cfg.VideoRTCPPort != videoPort {
					session.allocatedPorts = append(session.allocatedPorts, cfg.VideoRTCPPort)
				}
			}

			videoSession, err := media.NewRtpSession(media.RtpSessionConfig{
				MediaIP:           cfg.MediaIP,
				Port:              videoPort,
				Payload:           cfg.Negotiated.Video,
				OnAvPacket:        onPacket,
				OnTimeout:         cfg.OnTimeout,
				TimeoutMs:         cfg.TimeoutMs,
				Log:               cfg.Log,
				SrtpContext:       cfg.VideoSrtpCtx, // 传递SRTP上下文
				DTLSSRTP:          cfg.VideoDTLSSRTP,
				RTCPMux:           cfg.Negotiated.VideoRTCPMux,
				RTCPPort:          cfg.VideoRTCPPort,
				RemoteRTCPAddress: cfg.Negotiated.VideoRTCPAddress,
				VideoPLI:          cfg.Negotiated.VideoPLI,
			})
			if err != nil {
				session.dispose()
				return nil, fmt.Errorf("create video rtp session failed: %w", err)
			}
			session.videoSession = videoSession
		}
	}

	if session.audioSession == nil && session.videoSession == nil {
		session.dispose()
		return nil, fmt.Errorf("no audio or video session created")
	}

	cfg.Log.Infof("voip pub session created: stream=%s, callID=%s, from=%s, to=%s",
		cfg.StreamName, cfg.CallID, cfg.FromUser, cfg.ToUser)

	return session, nil
}

func (s *PubSession) AppName() string {
	return ""
}

func (s *PubSession) StreamName() string {
	return s.streamName
}

func (s *PubSession) CallID() string {
	return s.callID
}

func (s *PubSession) AudioPort() int {
	if s.audioSession != nil {
		return s.audioSession.Port()
	}
	return 0
}

func (s *PubSession) VideoPort() int {
	if s.videoSession != nil {
		return s.videoSession.Port()
	}
	if s.bundle && s.audioSession != nil {
		return s.audioSession.Port()
	}
	return 0
}

func (s *PubSession) UniqueKey() string {
	return s.streamName
}

// Negotiated returns the SDP selected when this session was created.
func (s *PubSession) Negotiated() sdp.Negotiated { return s.negotiated }

// StartMediaTimeout starts the established call's inactivity window after ACK.
func (s *PubSession) StartMediaTimeout(timeoutMs int) {
	if s.audioSession != nil {
		if s.negotiated.AudioHeld && (!s.bundle || s.negotiated.VideoHeld) {
			s.audioSession.SetTimeout(0)
		} else {
			s.audioSession.SetTimeout(timeoutMs)
		}
	}
	if s.videoSession != nil {
		if s.negotiated.VideoHeld {
			s.videoSession.SetTimeout(0)
		} else {
			s.videoSession.SetTimeout(timeoutMs)
		}
	}
}

func (s *PubSession) Dispose() error { return s.dispose() }
func (s *PubSession) dispose() error {
	s.closeOnce.Do(func() {
		s.mutex.Lock()
		s.closed = true
		s.onAvPacket = nil
		audio, video := s.audioSession, s.videoSession
		s.mutex.Unlock()
		// Close waits for callbacks, so it must run outside the callback mutex.
		if audio != nil {
			_ = audio.Close()
		}
		if video != nil {
			_ = video.Close()
		}
		if s.audioSrtpCtx != nil && (s.audioDTLSSRTP == nil || audio == nil) {
			s.audioSrtpCtx.Close()
		}
		if s.videoSrtpCtx != nil && (s.videoDTLSSRTP == nil || video == nil) {
			s.videoSrtpCtx.Close()
		}
		for _, port := range s.allocatedPorts {
			s.portAllocator.Free(port)
		}
		s.log.Infof("voip pub session disposed: stream=%s, callID=%s", s.streamName, s.callID)
	})
	return nil
}

func (s *PubSession) GetStat() base.StatSession {
	return s.sessionStat.GetStat()
}

func (s *PubSession) UpdateStat(intervalSec uint32) {
	s.sessionStat.UpdateStat(intervalSec)
}

func (s *PubSession) IsAlive() (readAlive, writeAlive bool) {
	return true, false
}

func (s *PubSession) SetOnAvPacket(cb rtprtcp.OnAvPacket) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if !s.closed {
		s.onAvPacket = cb
	}
}

func (s *PubSession) FromUser() string { return s.fromUser }

// RetainDTLSContext keeps the existing SRTP rollover and replay state when the
// remote fingerprint and bundle layout have not changed.
func (s *PubSession) RetainDTLSContext(video bool) *media.SrtpContext {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.closed {
		return nil
	}
	track := s.audioSession
	if video {
		track = s.videoSession
	}
	if track == nil {
		return nil
	}
	return track.RetainDTLSContext()
}

func (s *PubSession) WriteAudioRTP(p *rtp.Packet) error {
	if s.audioSession == nil {
		return fmt.Errorf("audio unavailable")
	}
	return s.audioSession.WriteRTP(p)
}

func (s *PubSession) WriteDTMF(key string, duration time.Duration) error {
	if s.audioSession == nil {
		return fmt.Errorf("audio unavailable")
	}
	return s.audioSession.WriteDTMF(key, duration)
}

func (s *PubSession) AudioPacketCounts() (received, sent uint32) {
	if s.audioSession == nil {
		return 0, 0
	}
	return s.audioSession.PacketCounts()
}
