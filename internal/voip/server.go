package voip

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/rtp"
	"github.com/q191201771/lal/pkg/base"
	maxrtc "github.com/q191201771/lalmax/rtc"
	"github.com/q191201771/lalmax/voip"
	"github.com/q191201771/lalmax/voip/media"
	"github.com/q191201771/lalmax/voip/sdp"
	"github.com/q191201771/naza/pkg/nazalog"
)

type Server struct {
	requestMu          sync.Mutex
	closeOnce          sync.Once
	ready              chan struct{}
	transactions       map[string]*serverTransaction
	clientTransactions map[string]*clientTransaction
	outbound           map[string]*outboundCall
	newTalkSession     func(context.Context, string, sdp.Payload, func(*rtp.Packet), func()) (*maxrtc.TalkSession, string, error)
	config             Config
	listenAddr         string
	conn               *net.UDPConn
	udpTransport       *UDPTransport
	tcpTransport       *TCPTransport
	tlsTransport       *TCPTransport
	pbxTCPTransport    *TCPTransport
	pbxTLSTransport    *TCPTransport
	dtlsTransport      *DTLSTransport
	wsTransport        *WebSocketTransport
	wssTransport       *WebSocketTransport
	registrar          *Registrar
	upstream           *upstreamRegistration
	dialogManager      *DialogManager
	nonceStore         *digestNonceStore
	portAllocator      *media.PortAllocator
	onPubSession       func(*voip.PubSession) error
	onDelSession       func(*voip.PubSession) error
	onReplaceSession   func(*voip.PubSession, *voip.PubSession) error
	stopChan           chan struct{}
	wg                 sync.WaitGroup
	log                nazalog.Logger
	dtlsCert           tls.Certificate
	dtlsFingerprint    string
	onCallEnded        func(CallRecord)
}

type ServerConfig struct {
	NewTalkSession   func(context.Context, string, sdp.Payload, func(*rtp.Packet), func()) (*maxrtc.TalkSession, string, error)
	Config           Config
	OnPubSession     func(*voip.PubSession) error
	OnDelSession     func(*voip.PubSession) error
	OnReplaceSession func(*voip.PubSession, *voip.PubSession) error
	OnCallEnded      func(CallRecord)
	Log              nazalog.Logger
}

func NewServer(cfg ServerConfig) (*Server, error) {
	cfg.Config.Normalize()
	if err := cfg.Config.Validate(); err != nil {
		return nil, err
	}
	if cfg.Log == nil {
		cfg.Log = nazalog.GetGlobalLogger()
	}
	s := &Server{
		config:             cfg.Config,
		newTalkSession:     cfg.NewTalkSession,
		ready:              make(chan struct{}),
		transactions:       make(map[string]*serverTransaction),
		clientTransactions: make(map[string]*clientTransaction),
		outbound:           make(map[string]*outboundCall),
		listenAddr:         cfg.Config.SipListenAddr,
		registrar:          NewRegistrar(),
		dialogManager:      NewDialogManager(),
		nonceStore:         newDigestNonceStore(),
		portAllocator:      media.NewPortAllocator(cfg.Config.MediaPortMin, cfg.Config.MediaPortMax),
		onPubSession:       cfg.OnPubSession,
		onDelSession:       cfg.OnDelSession,
		onReplaceSession:   cfg.OnReplaceSession,
		onCallEnded:        cfg.OnCallEnded,
		stopChan:           make(chan struct{}),
		log:                cfg.Log,
	}
	if s.newTalkSession == nil {
		s.newTalkSession = maxrtc.NewTalkSession
	}
	initialized := false
	defer func() {
		if !initialized {
			_ = s.Close()
		}
	}()
	if cfg.Config.SrtpEnable {
		cert, fingerprint, err := media.GenerateDTLSCertificate()
		if err != nil {
			return nil, err
		}
		s.dtlsCert = cert
		s.dtlsFingerprint = fingerprint
	}

	// Start UDP transport
	udpAddr, err := net.ResolveUDPAddr("udp", s.listenAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve sip listen addr failed: %w", err)
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("listen sip udp failed: %w", err)
	}

	s.conn = conn
	s.udpTransport = NewUDPTransport(conn, s.log)
	s.log.Infof("sip server listening on UDP %s", s.listenAddr)

	// Start TCP transport if configured
	if cfg.Config.SipTcpListenAddr != "" {
		tcpAddr, err := net.ResolveTCPAddr("tcp", cfg.Config.SipTcpListenAddr)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("resolve sip tcp listen addr failed: %w", err)
		}

		listener, err := net.ListenTCP("tcp", tcpAddr)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("listen sip tcp failed: %w", err)
		}

		s.tcpTransport = NewTCPTransport(listener, s.handleMessage, s.log)
		s.log.Infof("sip server listening on TCP %s", cfg.Config.SipTcpListenAddr)
	}

	// Start TLS transport if configured
	if cfg.Config.SipTlsListenAddr != "" {
		if cfg.Config.SipTlsCertFile == "" || cfg.Config.SipTlsKeyFile == "" {
			s.Close()
			return nil, fmt.Errorf("sip tls cert/key file required when sip tls listen addr is configured")
		}

		cert, err := tls.LoadX509KeyPair(cfg.Config.SipTlsCertFile, cfg.Config.SipTlsKeyFile)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("load sip tls certificate failed: %w", err)
		}

		tlsAddr, err := net.ResolveTCPAddr("tcp", cfg.Config.SipTlsListenAddr)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("resolve sip tls listen addr failed: %w", err)
		}

		listener, err := net.ListenTCP("tcp", tlsAddr)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("listen sip tls failed: %w", err)
		}

		s.tlsTransport = NewTLSTransport(listener, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}, s.handleMessage, s.log)
		s.log.Infof("sip server listening on TLS %s", cfg.Config.SipTlsListenAddr)
	}

	if cfg.Config.SipDtlsListenAddr != "" {
		if cfg.Config.SipTlsCertFile == "" || cfg.Config.SipTlsKeyFile == "" {
			s.Close()
			return nil, fmt.Errorf("sip tls cert/key file required when sip dtls listen addr is configured")
		}

		cert, err := tls.LoadX509KeyPair(cfg.Config.SipTlsCertFile, cfg.Config.SipTlsKeyFile)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("load sip dtls certificate failed: %w", err)
		}

		dtlsAddr, err := net.ResolveUDPAddr("udp", cfg.Config.SipDtlsListenAddr)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("resolve sip dtls listen addr failed: %w", err)
		}

		s.dtlsTransport, err = NewDTLSTransport(dtlsAddr, &dtls.Config{
			Certificates: []tls.Certificate{cert},
			ClientAuth:   dtls.NoClientCert,
		}, s.handleMessage, s.log)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("listen sip dtls failed: %w", err)
		}
		s.log.Infof("sip server listening on DTLS-UDP %s", cfg.Config.SipDtlsListenAddr)
	}

	if cfg.Config.SipWsListenAddr != "" {
		listener, err := net.Listen("tcp", cfg.Config.SipWsListenAddr)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("listen sip websocket failed: %w", err)
		}

		s.wsTransport = NewWebSocketTransport(listener, TransportWS, s.handleMessage, s.log)
		s.log.Infof("sip server listening on WS %s", cfg.Config.SipWsListenAddr)
	}

	if cfg.Config.SipWssListenAddr != "" {
		if cfg.Config.SipTlsCertFile == "" || cfg.Config.SipTlsKeyFile == "" {
			s.Close()
			return nil, fmt.Errorf("sip tls cert/key file required when sip wss listen addr is configured")
		}

		cert, err := tls.LoadX509KeyPair(cfg.Config.SipTlsCertFile, cfg.Config.SipTlsKeyFile)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("load sip wss certificate failed: %w", err)
		}

		tcpListener, err := net.Listen("tcp", cfg.Config.SipWssListenAddr)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("listen sip websocket tls failed: %w", err)
		}
		listener := tls.NewListener(tcpListener, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		})

		s.wssTransport = NewWebSocketTransport(listener, TransportWSS, s.handleMessage, s.log)
		s.log.Infof("sip server listening on WSS %s", cfg.Config.SipWssListenAddr)
	}

	// Advertise the bound ports, including OS-assigned ports when configured as 0.
	// Transport callbacks wait for ready before reading this configuration.
	s.listenAddr = s.conn.LocalAddr().String()
	if s.tcpTransport != nil {
		s.config.SipTcpListenAddr = s.tcpTransport.listener.Addr().String()
	}
	if s.tlsTransport != nil {
		s.config.SipTlsListenAddr = s.tlsTransport.listener.Addr().String()
	}
	if s.dtlsTransport != nil {
		s.config.SipDtlsListenAddr = s.dtlsTransport.listener.Addr().String()
	}
	if s.wsTransport != nil {
		s.config.SipWsListenAddr = s.wsTransport.listener.Addr().String()
	}
	if s.wssTransport != nil {
		s.config.SipWssListenAddr = s.wssTransport.listener.Addr().String()
	}
	if err := s.initUpstreamTransport(); err != nil {
		return nil, err
	}
	if err := s.initUpstreamRegistration(); err != nil {
		return nil, err
	}
	s.wg.Add(2)
	close(s.ready)
	go s.runReceive()
	go s.runDialogTimeouts()
	initialized = true
	return s, nil
}

func (s *Server) Dispose() {
	_ = s.Close()
}

func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopChan)
		s.requestMu.Lock()
		for _, call := range s.outbound {
			s.cancelOutbound(call, "server stopped")
		}
		if s.upstream != nil && s.upstream.state == "registered" {
			s.sendUpstreamRegister(s.upstream, 0, time.Now())
		}
		for _, dialog := range s.dialogManager.List() {
			s.notifyBye(dialog)
		}
		s.requestMu.Unlock()
		if s.udpTransport != nil {
			s.udpTransport.Close()
		}
		if s.tcpTransport != nil {
			s.tcpTransport.Close()
		}
		if s.tlsTransport != nil {
			s.tlsTransport.Close()
		}
		if s.pbxTCPTransport != nil {
			s.pbxTCPTransport.Close()
		}
		if s.pbxTLSTransport != nil {
			s.pbxTLSTransport.Close()
		}
		if s.dtlsTransport != nil {
			s.dtlsTransport.Close()
		}
		if s.wsTransport != nil {
			s.wsTransport.Close()
		}
		if s.wssTransport != nil {
			s.wssTransport.Close()
		}
		if s.conn != nil {
			s.conn.Close()
		}
		s.wg.Wait()
		if s.registrar != nil {
			s.registrar.Close()
		}
		s.requestMu.Lock()
		defer s.requestMu.Unlock()
		for _, c := range s.outbound {
			s.releaseOutbound(c)
		}
		for _, dialog := range s.dialogManager.List() {
			s.terminate(dialog.CallID, "failed", "server stopped")
		}
	})
	return nil
}

func (s *Server) runReceive() {
	defer s.wg.Done()

	buf := make([]byte, 65535)
	for {
		select {
		case <-s.stopChan:
			return
		default:
		}

		s.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, remoteAddr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			select {
			case <-s.stopChan:
				return
			default:
				s.log.Warnf("read sip udp failed: %v", err)
				continue
			}
		}

		rawMsg := string(buf[:n])
		addr := TransportAddr{
			Type: TransportUDP,
			Addr: remoteAddr.String(),
		}
		s.handleMessage(rawMsg, addr)
	}
}

func (s *Server) handleMessage(rawMsg string, remoteAddr TransportAddr) {
	select {
	case <-s.ready:
	case <-s.stopChan:
		return
	}
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	select {
	case <-s.stopChan:
		return
	default:
	}
	if isSIPKeepalive(rawMsg) {
		return
	}

	s.log.Debugf("received raw SIP message from %s: len=%d raw=%q", remoteAddr, len(rawMsg), rawMsg)

	msg, err := ParseMessage(rawMsg)
	if err != nil {
		s.log.Warnf("parse sip message from %s failed: %v, len=%d raw=%q", remoteAddr, err, len(rawMsg), rawMsg)
		return
	}

	if !msg.IsRequest {
		if s.handleUpstreamResponse(msg, remoteAddr, time.Now()) {
			return
		}
		if s.handleOutboundResponse(msg, remoteAddr) {
			return
		}
		s.handleClientResponse(msg, remoteAddr)
		return
	}
	if !validRequestHeaders(msg) {
		if msg.Method != MethodAck {
			s.sendResponse(BuildResponse(msg, 400, "Bad Request", ""), remoteAddr)
		}
		return
	}
	if msg.Method != MethodAck && msg.Method != MethodCancel && msg.GetHeader("Require") != "" {
		response := BuildResponse(msg, 420, "Bad Extension", "")
		response = strings.Replace(response, "\r\n\r\n", "\r\nUnsupported: "+msg.GetHeader("Require")+"\r\n\r\n", 1)
		s.sendResponse(response, remoteAddr)
		return
	}
	if msg.Method != MethodAck {
		if tx := s.transactions[transactionKey(msg, msg.Method)]; tx != nil && time.Now().Before(tx.expires) {
			if !sameTransactionRequest(msg, tx.request) {
				s.sendResponse(BuildResponse(msg, 400, "Changed Transaction Request", ""), remoteAddr)
				return
			}
			s.sendResponse(tx.response, remoteAddr)
			return
		}
	}

	if s.handleOutboundRequest(msg, remoteAddr) {
		return
	}
	s.log.Debugf("received SIP %s from %s", msg.Method, remoteAddr)

	var response string
	switch msg.Method {
	case MethodRegister:
		response = s.handleRegister(msg, remoteAddr)
	case MethodOptions:
		response = s.handleOptions(msg, remoteAddr)
	case MethodInvite:
		response = s.handleInvite(msg, remoteAddr)
	case MethodUpdate:
		response = s.handleRenegotiation(msg, remoteAddr)
	case MethodAck:
		s.handleAck(msg, remoteAddr)
		return // ACK has no response
	case MethodBye:
		response = s.handleBye(msg, remoteAddr)
	case MethodCancel:
		response = s.handleCancel(msg, remoteAddr)
	default:
		s.log.Warnf("unsupported SIP method: %s", msg.Method)
		response = BuildResponse(msg, 501, "Not Implemented", "")
	}

	if response != "" {
		s.rememberResponse(msg, response, remoteAddr)
		s.sendResponse(response, remoteAddr)
	}
}

func isSIPKeepalive(rawMsg string) bool {
	return strings.TrimSpace(rawMsg) == ""
}

func (s *Server) sendResponse(response string, remoteAddr TransportAddr) error {
	s.log.Debugf("sending SIP response to %s: len=%d", remoteAddr, len(response))

	err := fmt.Errorf("signaling transport unavailable")
	switch remoteAddr.Type {
	case TransportUDP:
		if s.udpTransport != nil {
			err = s.udpTransport.Send([]byte(response), remoteAddr)
		}
	case TransportTCP:
		if s.upstream != nil && remoteAddr == s.upstream.peer && s.pbxTCPTransport != nil {
			err = s.pbxTCPTransport.Send([]byte(response), remoteAddr)
		} else if s.tcpTransport != nil {
			err = s.tcpTransport.Send([]byte(response), remoteAddr)
		} else if s.pbxTCPTransport != nil {
			err = s.pbxTCPTransport.Send([]byte(response), remoteAddr)
		}
	case TransportTLS:
		if s.upstream != nil && remoteAddr == s.upstream.peer && s.pbxTLSTransport != nil {
			err = s.pbxTLSTransport.Send([]byte(response), remoteAddr)
		} else if s.tlsTransport != nil {
			err = s.tlsTransport.Send([]byte(response), remoteAddr)
		} else if s.pbxTLSTransport != nil {
			err = s.pbxTLSTransport.Send([]byte(response), remoteAddr)
		}
	case TransportDTLS:
		if s.dtlsTransport != nil {
			err = s.dtlsTransport.Send([]byte(response), remoteAddr)
		}
	case TransportWS:
		if s.wsTransport != nil {
			err = s.wsTransport.Send([]byte(response), remoteAddr)
		}
	case TransportWSS:
		if s.wssTransport != nil {
			err = s.wssTransport.Send([]byte(response), remoteAddr)
		}
	default:
		s.log.Warnf("unsupported transport type: %s", remoteAddr.Type)
		return err
	}

	if err != nil {
		s.log.Warnf("send sip response failed: %v", err)
	} else {
		s.log.Debugf("SIP response sent successfully to %s", remoteAddr)
	}
	return err
}

func (s *Server) handleRegister(msg *Message, remoteAddr TransportAddr) string {
	fromUser := ExtractUser(msg.From())
	if fromUser == "" {
		s.log.Warnf("register missing from user")
		return BuildResponse(msg, 400, "Bad Request", "")
	}

	if ok, response := s.authenticateRequest(msg, fromUser); !ok {
		return response
	}

	contact := msg.Contact()
	userAgent := msg.GetHeader("User-Agent")
	expiresStr := msg.GetHeader("Expires")
	expires := 3600
	if expiresStr != "" {
		fmt.Sscanf(expiresStr, "%d", &expires)
	}

	if expires == 0 {
		s.registrar.Unregister(fromUser)
		s.log.Infof("user unregistered: %s", fromUser)
	} else {
		s.registrar.Register(fromUser, contact, userAgent, expires)
		s.registrar.SetPeer(fromUser, remoteAddr)
		s.log.Infof("user registered: %s, contact=%s, expires=%d", fromUser, contact, expires)
	}

	return BuildResponse(msg, 200, "OK", "")
}

func (s *Server) handleOptions(msg *Message, remoteAddr TransportAddr) string {
	if ExtractTag(msg.To()) != "" {
		d := s.dialogManager.Get(msg.CallID())
		if !matchingDialog(msg, d) {
			return BuildResponse(msg, 481, "Call/Transaction Does Not Exist", "")
		}
		seq := ExtractCSeqNumber(msg.CSeq())
		if seq <= d.RemoteCSeq {
			return BuildResponse(msg, 500, "CSeq Out of Order", "")
		}
		s.dialogManager.SetRemoteCSeq(d.CallID, seq)
	}
	response := BuildResponse(msg, 200, "OK", "")
	return response
}

func (s *Server) handleInvite(msg *Message, remoteAddr TransportAddr) string {
	callID := msg.CallID()
	fromUser := ExtractUser(msg.From())
	toUser := ExtractUser(msg.To())
	fromTag := ExtractTag(msg.From())

	s.log.Debugf("handleInvite: callID=%s, from=%s, to=%s, fromTag=%s", callID, fromUser, toUser, fromTag)

	if callID == "" || fromUser == "" || fromTag == "" {
		s.log.Warnf("invite missing required headers")
		return BuildResponse(msg, 400, "Bad Request", "")
	}
	if ok, response := s.authenticateRequest(msg, fromUser); !ok {
		return response
	}

	if s.dialogManager.Get(callID) != nil {
		return s.handleRenegotiation(msg, remoteAddr)
	}
	if ExtractTag(msg.To()) != "" {
		return BuildResponse(msg, 481, "Call/Transaction Does Not Exist", "")
	}
	if msg.Body == "" {
		return BuildResponse(msg, 488, "SDP Offer Required", "")
	}
	toTag := generateTag()
	dialog := s.dialogManager.Create(callID, fromTag, toTag, fromUser, toUser, remoteAddr.String())
	s.dialogManager.SetInvite(callID, msg, remoteAddr)
	if s.config.ManualAnswer {
		return BuildResponseWithContact(msg, 180, "Ringing", "", toTag, "")
	}
	identity := *dialog
	identity.talk = newInboundTalk()
	s.dialogManager.SetTalk(callID, identity.talk)
	identity.MediaCreatedAt = dialog.CreatedAt
	answer, pubSession, code := s.prepareMedia(msg, &identity)
	if code != 200 {
		s.terminate(callID, "failed", "media negotiation failed")
		return BuildResponse(msg, code, "Media Negotiation Failed", "")
	}
	s.dialogManager.SetPubSession(callID, pubSession)
	s.dialogManager.SetMediaState(callID, identity.MediaCreatedAt, pubSession.Negotiated().Held())
	if s.onPubSession != nil {
		if err := s.onPubSession(pubSession); err != nil {
			s.terminate(callID, "failed", "media publish failed")
			return BuildResponse(msg, 500, "Publish Failed", "")
		}
	}
	s.dialogManager.UpdateState(callID, DialogStateConfirmed)
	return BuildResponseWithContact(msg, 200, "OK", answer, toTag, s.buildContactURI(remoteAddr))
}

func (s *Server) prepareMedia(msg *Message, dialog *Dialog) (string, *voip.PubSession, int) {
	callID, fromUser, toUser := dialog.CallID, dialog.FromUser, dialog.ToUser
	mediaIP := s.config.MediaIP
	if mediaIP == "" {
		// Use local address from connection
		mediaIP = s.config.SipIP
		if mediaIP == "" {
			mediaIP = s.getLocalIP()
		}
	}

	answer, negotiated, err := sdp.NegotiateOffer(msg.Body, sdp.AnswerOptions{
		MediaIP:   mediaIP,
		SessionID: uint64(dialog.CreatedAt.UnixNano()), SessionVersion: uint64(ExtractCSeqNumber(msg.CSeq())),
		AudioPort:       0, // Will be set below
		VideoPort:       0,
		SrtpEnable:      s.config.SrtpEnable,
		BundleEnable:    s.config.BundleEnable,
		DTLSFingerprint: s.dtlsFingerprint,
	})
	if err != nil {
		s.log.Warnf("sdp negotiation failed: %v", err)
		if err == sdp.ErrUnsupportedZRTP {
			s.log.Warnf("ZRTP not implemented: callID=%s", callID)
			return "", nil, 488
		}
		if err == sdp.ErrUnsupportedSDP {
			return "", nil, 488
		}
		return "", nil, 488
	}

	if s.config.SrtpMandatory && ((negotiated.Audio != nil && !negotiated.AudioSecure) || (negotiated.Video != nil && !negotiated.VideoSecure)) {
		return "", nil, 488
	}

	// Create VoIP pub session
	streamName := sdp.StreamName(fromUser, callID)

	// Allocate ports and create answer SDP
	var audioPort, videoPort, audioRTCPPort, videoRTCPPort int
	transferred := false
	defer func() {
		if !transferred {
			for _, p := range []int{audioPort, videoPort, audioRTCPPort, videoRTCPPort} {
				if p > 0 {
					s.portAllocator.Free(p)
				}
			}
		}
	}()
	if negotiated.Audio != nil {
		s.log.Debugf("allocating audio port for callID=%s", callID)
		port, rtcpPort, err := s.portAllocator.AllocMedia(negotiated.AudioRTCPMux)
		if err != nil {
			s.log.Errorf("allocate audio port failed for callID=%s: %v", callID, err)
			return "", nil, 503
		}
		audioPort, audioRTCPPort = port, rtcpPort
		s.log.Debugf("allocated audio port %d for callID=%s", audioPort, callID)
	}

	if negotiated.Video != nil && negotiated.Bundle {
		videoPort, videoRTCPPort = audioPort, audioRTCPPort
	} else if negotiated.Video != nil {
		port, rtcpPort, err := s.portAllocator.AllocMedia(negotiated.VideoRTCPMux)
		if err != nil {
			s.log.Warnf("allocate video port failed: %v", err)
			return "", nil, 503
		}
		videoPort, videoRTCPPort = port, rtcpPort
	}

	// Generate SRTP keys if needed
	var audioCrypto, videoCrypto string
	var audioSrtpCtx, videoSrtpCtx *media.SrtpContext

	if s.config.SrtpEnable && negotiated.AudioSecure && !negotiated.AudioDTLS {
		// 生成audio的SRTP密钥
		keyMaterial, err := media.GenerateSrtpKey()
		if err != nil {
			s.log.Warnf("generate audio srtp key failed: %v", err)
			return "", nil, 500
		}
		audioCrypto = media.BuildCryptoAttribute(negotiated.AudioCryptoTag, negotiated.AudioCryptoSuite, keyMaterial)
		s.log.Debugf("selected audio srtp crypto for callID=%s: tag=%d suite=%s",
			callID, negotiated.AudioCryptoTag, negotiated.AudioCryptoSuite)

		remoteKeyMaterial, err := extractSrtpKeyMaterial(negotiated.AudioCrypto)
		if err != nil {
			s.log.Warnf("extract audio remote srtp key failed: %v", err)
			return "", nil, 500
		}

		// 创建SRTP上下文
		audioSrtpCtx, err = media.CreateSrtpContextPair(keyMaterial, remoteKeyMaterial, negotiated.AudioCryptoSuite)
		if err != nil {
			s.log.Warnf("create audio srtp context failed: %v", err)
			return "", nil, 500
		}
	}

	if s.config.SrtpEnable && negotiated.VideoSecure && !negotiated.VideoDTLS {
		// 生成video的SRTP密钥
		keyMaterial, err := media.GenerateSrtpKey()
		if err != nil {
			if audioSrtpCtx != nil {
				audioSrtpCtx.Close()
			}
			s.log.Warnf("generate video srtp key failed: %v", err)
			return "", nil, 500
		}
		videoCrypto = media.BuildCryptoAttribute(negotiated.VideoCryptoTag, negotiated.VideoCryptoSuite, keyMaterial)
		s.log.Debugf("selected video srtp crypto for callID=%s: tag=%d suite=%s",
			callID, negotiated.VideoCryptoTag, negotiated.VideoCryptoSuite)

		remoteKeyMaterial, err := extractSrtpKeyMaterial(negotiated.VideoCrypto)
		if err != nil {
			if audioSrtpCtx != nil {
				audioSrtpCtx.Close()
			}
			s.log.Warnf("extract video remote srtp key failed: %v", err)
			return "", nil, 500
		}

		// 创建SRTP上下文
		videoSrtpCtx, err = media.CreateSrtpContextPair(keyMaterial, remoteKeyMaterial, negotiated.VideoCryptoSuite)
		if err != nil {
			if audioSrtpCtx != nil {
				audioSrtpCtx.Close()
			}
			s.log.Warnf("create video srtp context failed: %v", err)
			return "", nil, 500
		}
	}

	// Regenerate answer with allocated ports and SRTP crypto
	answer, negotiated, err = sdp.NegotiateOffer(msg.Body, sdp.AnswerOptions{
		MediaIP:   mediaIP,
		SessionID: uint64(dialog.CreatedAt.UnixNano()), SessionVersion: uint64(ExtractCSeqNumber(msg.CSeq())),
		AudioPort:       audioPort,
		AudioRTCPPort:   audioRTCPPort,
		VideoRTCPPort:   videoRTCPPort,
		VideoPort:       videoPort,
		SrtpEnable:      s.config.SrtpEnable,
		BundleEnable:    s.config.BundleEnable,
		DTLSFingerprint: s.dtlsFingerprint,
		AudioCrypto:     audioCrypto,
		VideoCrypto:     videoCrypto,
	})
	if err != nil {
		if audioSrtpCtx != nil {
			audioSrtpCtx.Close()
		}
		if videoSrtpCtx != nil {
			videoSrtpCtx.Close()
		}
		s.log.Warnf("sdp negotiation failed: %v", err)
		if err == sdp.ErrUnsupportedZRTP {
			s.log.Warnf("ZRTP not implemented: callID=%s", callID)
			return "", nil, 488
		}
		return "", nil, 500
	}

	var audioDTLSSRTP, videoDTLSSRTP *media.DTLSSRTPConfig
	if negotiated.AudioDTLS {
		audioDTLSSRTP = &media.DTLSSRTPConfig{
			Certificate:       s.dtlsCert,
			RemoteFingerprint: negotiated.AudioFingerprint,
		}
	}
	if negotiated.VideoDTLS {
		videoDTLSSRTP = &media.DTLSSRTPConfig{
			Certificate:       s.dtlsCert,
			RemoteFingerprint: negotiated.VideoFingerprint,
		}
	}
	if negotiated.Bundle {
		videoDTLSSRTP = nil
	}

	// A peer may retain its DTLS association when adding a track or resuming.
	// Preserve verified keys and replay counters; a new handshake can still rekey.
	if previous := dialog.PubSession; previous != nil {
		old := previous.Negotiated()
		if old.Bundle == negotiated.Bundle {
			if negotiated.AudioDTLS && old.AudioDTLS && strings.EqualFold(old.AudioFingerprint, negotiated.AudioFingerprint) {
				audioSrtpCtx = previous.RetainDTLSContext(false)
			}
			if negotiated.VideoDTLS && old.VideoDTLS && !negotiated.Bundle && strings.EqualFold(old.VideoFingerprint, negotiated.VideoFingerprint) {
				videoSrtpCtx = previous.RetainDTLSContext(true)
			}
		}
	}

	createdAt := dialog.MediaCreatedAt
	mediaTimeout := max(s.config.RtpTimeoutMs, s.config.AckTimeoutMs)
	transferred = true // The publisher owns all preallocated ports, including on failure.
	pubSession, err := voip.NewPubSession(voip.PubSessionConfig{
		StreamName:    streamName,
		CallID:        callID,
		FromUser:      fromUser,
		ToUser:        toUser,
		MediaIP:       "0.0.0.0",
		Negotiated:    negotiated,
		PortAllocator: s.portAllocator,
		AudioPort:     audioPort, // Use pre-allocated audio port
		VideoPort:     videoPort, // Use pre-allocated video port
		AudioRTCPPort: audioRTCPPort,
		VideoRTCPPort: videoRTCPPort,
		OnAvPacket: func(pkt base.AvPacket) {
			// Will be set by Group.AddVoipPubSession
		},
		OnAudioRTP: func(packet *rtp.Packet) {
			if dialog.talk != nil {
				dialog.talk.writeRemoteRTP(packet)
			}
		},
		OnDTMFEvent: func(event media.DTMFEvent) {
			s.log.Infof("DTMF event: stream=%s, key=%s, duration=%d", streamName, event.Key, event.Duration)
		},
		OnTimeout: func() {
			s.log.Warnf("RTP timeout for stream: %s", streamName)
			s.handleRtpTimeout(callID, createdAt)
		},
		TimeoutMs:     mediaTimeout,
		Log:           s.log,
		AudioSrtpCtx:  audioSrtpCtx, // 传递audio SRTP上下文
		VideoSrtpCtx:  videoSrtpCtx, // 传递video SRTP上下文
		AudioDTLSSRTP: audioDTLSSRTP,
		VideoDTLSSRTP: videoDTLSSRTP,
	})

	if err != nil {
		s.log.Warnf("create pub session failed: %v", err)
		return "", nil, 500
	}

	return answer, pubSession, 200
}

func (s *Server) handleAck(msg *Message, remoteAddr TransportAddr) {
	s.acknowledgeTransaction(msg)
}

func (s *Server) handleBye(msg *Message, remoteAddr TransportAddr) string {
	callID := msg.CallID()
	s.log.Debugf("handleBye called: callID=%s, remoteAddr=%s", callID, remoteAddr)

	dialog := s.dialogManager.Get(callID)
	if !matchingDialog(msg, dialog) {
		s.log.Warnf("BYE for unknown dialog: %s", callID)
		return BuildResponse(msg, 481, "Call/Transaction Does Not Exist", "")
	}
	if ExtractCSeqNumber(msg.CSeq()) <= dialog.RemoteCSeq {
		return BuildResponse(msg, 500, "CSeq Out of Order", "")
	}

	s.log.Debugf("found dialog for BYE: callID=%s, state=%v", callID, dialog.State)

	s.terminate(callID, "completed", "ended by terminal")

	s.log.Infof("call terminated by BYE: %s", callID)

	response := BuildResponse(msg, 200, "OK", "")
	s.log.Debugf("BYE response built: len=%d", len(response))
	return response
}

func (s *Server) handleCancel(msg *Message, remoteAddr TransportAddr) string {
	callID := msg.CallID()
	dialog := s.dialogManager.Get(callID)
	if dialog == nil || ExtractTag(msg.From()) != dialog.FromTag || ExtractCSeqNumber(msg.CSeq()) != dialog.InviteCSeq || msg.GetHeader("Via") != dialog.InviteBranch || msg.RequestURI != dialog.InviteURI {
		return BuildResponse(msg, 481, "Call/Transaction Does Not Exist", "")
	}
	// A final INVITE response has already been sent. CANCEL acknowledges the
	// transaction but cannot end a confirmed call; the client must send BYE.
	if dialog.State == DialogStateEarly {
		if dialog.PubSession == nil && dialog.Invite != nil {
			response := BuildResponseWithContact(dialog.Invite, 487, "Request Terminated", "", dialog.ToTag, "")
			s.updateInviteResponse(dialog, response)
			_ = s.sendResponse(response, dialog.Peer)
		}
		s.terminate(callID, "cancelled", "cancelled before answer")
	}
	return BuildResponse(msg, 200, "OK", "")
}

// AnswerIncomingCall accepts a manually screened inbound SIP call.
func (s *Server) AnswerIncomingCall(callID string) error {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	select {
	case <-s.stopChan:
		return fmt.Errorf("VoIP is stopped")
	default:
	}
	d := s.dialogManager.Get(callID)
	if !s.config.ManualAnswer || d == nil || d.State != DialogStateEarly || d.PubSession != nil || d.Invite == nil {
		return fmt.Errorf("incoming call is not waiting for an answer")
	}
	answer, pubSession, code := s.prepareMedia(d.Invite, d)
	if code != 200 {
		return fmt.Errorf("could not prepare call media (SIP %d)", code)
	}
	if s.onPubSession != nil {
		if err := s.onPubSession(pubSession); err != nil {
			_ = pubSession.Dispose()
			return fmt.Errorf("publish call media: %w", err)
		}
	}
	s.dialogManager.SetPubSession(callID, pubSession)
	s.dialogManager.SetMediaState(callID, d.CreatedAt, pubSession.Negotiated().Held())
	response := BuildResponseWithContact(d.Invite, 200, "OK", answer, d.ToTag, s.buildContactURI(d.Peer))
	s.updateInviteResponse(d, response)
	s.dialogManager.UpdateState(callID, DialogStateConfirmed)
	if err := s.sendResponse(response, d.Peer); err != nil {
		return fmt.Errorf("send SIP answer: %w", err)
	}
	return nil
}

// RejectIncomingCall declines an inbound SIP call that is still ringing.
func (s *Server) RejectIncomingCall(callID string) error {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	d := s.dialogManager.Get(callID)
	if !s.config.ManualAnswer || d == nil || d.State != DialogStateEarly || d.PubSession != nil || d.Invite == nil {
		return fmt.Errorf("incoming call is not waiting for an answer")
	}
	response := BuildResponseWithContact(d.Invite, 603, "Decline", "", d.ToTag, "")
	s.updateInviteResponse(d, response)
	s.terminate(callID, "rejected", "rejected by operator")
	if err := s.sendResponse(response, d.Peer); err != nil {
		return fmt.Errorf("send SIP rejection: %w", err)
	}
	return nil
}

func (s *Server) terminate(callID, outcome, reason string) {
	dialog := s.dialogManager.Take(callID)
	if dialog == nil {
		return
	}
	if s.onCallEnded != nil {
		s.onCallEnded(callRecordFromDialog(dialog, outcome, reason, time.Now()))
	}
	for _, tx := range s.transactions {
		if tx.accepted && tx.request.CallID() == callID {
			tx.acked = true
		}
	}
	if dialog.PubSession != nil {
		// Stop receiving before detaching the lal publisher.
		_ = dialog.PubSession.Dispose()
		if s.onDelSession != nil {
			_ = s.onDelSession(dialog.PubSession)
		}
	}
	if dialog.talk != nil {
		s.resetInboundTalk(dialog.talk)
	}
	if dialog.OnTerminate != nil {
		dialog.OnTerminate()
	}
}

func (s *Server) handleRtpTimeout(callID string, createdAt time.Time) {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	d := s.dialogManager.Get(callID)
	if d == nil || !d.MediaCreatedAt.Equal(createdAt) {
		return
	}
	s.notifyBye(d)
	s.terminate(callID, "failed", "media timeout")
}

func (s *Server) runDialogTimeouts() {
	defer s.wg.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.requestMu.Lock()
			s.runTransactions(time.Now())
			s.runOutbound(time.Now())
			s.runUpstreamRegistration(time.Now())
			s.runInboundTalk(time.Now())
			for _, d := range s.dialogManager.List() {
				if d.State == DialogStateEarly && d.PubSession == nil && time.Since(d.CreatedAt) > time.Duration(s.config.RingTimeoutMs)*time.Millisecond {
					if d.Invite != nil {
						response := BuildResponseWithContact(d.Invite, 480, "Temporarily Unavailable", "", d.ToTag, "")
						s.updateInviteResponse(d, response)
						_ = s.sendResponse(response, d.Peer)
					}
					s.terminate(d.CallID, "missed", "no answer")
					continue
				}
				if d.State == DialogStateConfirmed && time.Since(d.LastActivity) > min(time.Duration(s.config.AckTimeoutMs)*time.Millisecond, transactionLifetime) {
					s.notifyBye(d)
					s.terminate(d.CallID, "failed", "ACK timeout")
				}
			}
			s.requestMu.Unlock()
		}
	}
}

func (s *Server) getLocalIP() string {
	// Try to get local IP from connection
	if s.conn != nil {
		localAddr := s.conn.LocalAddr().String()
		parts := strings.Split(localAddr, ":")
		if len(parts) > 0 && parts[0] != "0.0.0.0" && parts[0] != "" {
			return parts[0]
		}
	}
	return "127.0.0.1"
}

func (s *Server) authenticateRequest(msg *Message, username string) (bool, string) {
	if !s.config.AuthEnable {
		return true, ""
	}

	user, ok := s.findUser(username)
	if !ok {
		s.log.Warnf("sip auth user not found: %s", username)
		return false, s.buildAuthChallenge(msg)
	}

	authHeader := msg.GetHeader("Authorization")
	if authHeader == "" {
		return false, s.buildAuthChallenge(msg)
	}

	params := parseDigestAuthorization(authHeader)
	if params == nil {
		return false, s.buildAuthChallenge(msg)
	}
	if params["username"] != username || params["realm"] != s.config.Realm || params["uri"] == "" || params["response"] == "" {
		return false, s.buildAuthChallenge(msg)
	}
	if !s.nonceStore.Valid(params["nonce"]) {
		return false, s.buildAuthChallenge(msg)
	}
	if expected := expectedDigestResponse(string(msg.Method), s.config.Realm, user.Password, params); !strings.EqualFold(expected, params["response"]) {
		s.log.Warnf("sip digest auth failed for user: %s", username)
		return false, s.buildAuthChallenge(msg)
	}
	return true, ""
}

func (s *Server) findUser(username string) (User, bool) {
	for _, u := range s.config.Users {
		if u.Username == username {
			return u, true
		}
	}
	return User{}, false
}

func (s *Server) buildAuthChallenge(msg *Message) string {
	resp := BuildResponse(msg, 401, "Unauthorized", "")
	nonce := s.nonceStore.New()
	challenge := buildDigestChallenge(s.config.Realm, nonce)
	return insertHeaderBeforeContentLength(resp, "WWW-Authenticate", challenge)
}

func insertHeaderBeforeContentLength(response, name, value string) string {
	header := fmt.Sprintf("%s: %s\r\n", name, value)
	idx := strings.Index(strings.ToLower(response), "content-length:")
	if idx < 0 {
		return response + header
	}
	return response[:idx] + header + response[idx:]
}

func extractSrtpKeyMaterial(cryptoLine string) ([]byte, error) {
	params, err := media.ParseCryptoAttribute(cryptoLine)
	if err != nil {
		return nil, err
	}
	return media.ExtractKeyMaterial(params)
}

func (s *Server) buildContactURI(remoteAddr TransportAddr) string {
	host := s.config.SipIP
	if host == "" {
		host = s.getLocalIP()
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}

	listenAddr := s.listenAddr
	scheme := "sip"
	transportParam := ""
	switch remoteAddr.Type {
	case TransportTCP:
		if s.config.SipTcpListenAddr != "" {
			listenAddr = s.config.SipTcpListenAddr
		}
		transportParam = ";transport=tcp"
	case TransportTLS:
		if s.config.SipTlsListenAddr != "" {
			listenAddr = s.config.SipTlsListenAddr
		}
		// SIPS Contact URIs are required for secure dialogs by some PBXs,
		// including Asterisk's PJSIP driver.
		scheme = "sips"
	case TransportDTLS:
		if s.config.SipDtlsListenAddr != "" {
			listenAddr = s.config.SipDtlsListenAddr
		}
		transportParam = ";transport=dtls-udp"
	case TransportWS:
		if s.config.SipWsListenAddr != "" {
			listenAddr = s.config.SipWsListenAddr
		}
		transportParam = ";transport=ws"
	case TransportWSS:
		if s.config.SipWssListenAddr != "" {
			listenAddr = s.config.SipWssListenAddr
		}
		transportParam = ";transport=wss"
	}

	port := "5060"
	if _, p, err := net.SplitHostPort(listenAddr); err == nil && p != "" {
		port = p
	}

	return fmt.Sprintf("<%s:%s:%s%s>", scheme, formatSIPHost(host), port, transportParam)
}

func formatSIPHost(host string) string {
	if ip := net.ParseIP(host); ip != nil && strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}
