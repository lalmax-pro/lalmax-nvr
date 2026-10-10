package media

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"net"
	"sync"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/rtprtcp"
	"github.com/q191201771/lalmax/voip/sdp"
	"github.com/q191201771/naza/pkg/nazalog"
)

type RtpSession struct {
	keyCloseOnce                               sync.Once
	closeOnce                                  sync.Once
	mediaIP                                    string
	port                                       int
	payload                                    *sdp.Payload
	dtmfPayloadType                            int
	dtmfClockRate                              int
	conn                                       *net.UDPConn
	rtcpConn                                   *net.UDPConn
	rtcpRemote                                 *net.UDPAddr
	rtcpMux                                    bool
	videoPLI                                   bool
	receiverSSRC                               uint32
	reports                                    map[uint32]*receptionStats
	lastPLI                                    time.Time
	remoteAddr                                 *net.UDPAddr // Remote address for sending RTCP
	unpacker                                   rtprtcp.IRtpUnpacker
	videoPayload                               *sdp.Payload
	videoUnpacker                              rtprtcp.IRtpUnpacker
	dtmfParser                                 *DTMFParser
	onAvPacket                                 rtprtcp.OnAvPacket
	onRTP                                      func(*rtp.Packet)
	sendMu                                     sync.Mutex
	sendSeq                                    uint16
	sendSSRC                                   uint32
	sentPackets, sentOctets, lastSentTimestamp uint32
	lastSentAt                                 time.Time
	remotePinned                               bool
	allowedRemoteIP                            net.IP
	onDTMFEvent                                DTMFEventHandler
	onTimeout                                  func()
	timeoutMs                                  int
	lastActivity                               time.Time
	stopChan                                   chan struct{}
	wg                                         sync.WaitGroup
	mutex                                      sync.Mutex
	log                                        nazalog.Logger
	srtpContext                                *SrtpContext // SRTP加密上下文
	srtpEnabled                                bool         // 是否启用SRTP
	dtlsConfig                                 *DTLSSRTPConfig
	dtlsPacketConn                             *dtlsPacketConn
	dtlsStarted                                bool
	// RTCP statistics
	packetsReceived uint32
	ssrc            uint32
}

type RtpSessionConfig struct {
	MediaIP           string
	Port              int
	Payload           *sdp.Payload
	VideoPayload      *sdp.Payload
	DTMFPayload       *sdp.Payload
	OnAvPacket        rtprtcp.OnAvPacket
	OnRTP             func(*rtp.Packet)
	RemoteRTPAddress  string
	RemoteSourceIP    string
	OnDTMFEvent       DTMFEventHandler
	OnTimeout         func()
	TimeoutMs         int
	Log               nazalog.Logger
	SrtpContext       *SrtpContext // SRTP上下文（如果启用SRTP）
	DTLSSRTP          *DTLSSRTPConfig
	RTCPPort          int
	RTCPMux           bool
	RemoteRTCPAddress string
	VideoPLI          bool
}

func NewRtpSession(cfg RtpSessionConfig) (*RtpSession, error) {
	if cfg.Log == nil {
		cfg.Log = nazalog.GetGlobalLogger()
	}
	if cfg.TimeoutMs == 0 {
		cfg.TimeoutMs = 15000
	}

	s := &RtpSession{
		mediaIP:         cfg.MediaIP,
		port:            cfg.Port,
		payload:         cfg.Payload,
		videoPayload:    cfg.VideoPayload,
		dtmfPayloadType: -1,
		onAvPacket:      cfg.OnAvPacket,
		onRTP:           cfg.OnRTP,
		onDTMFEvent:     cfg.OnDTMFEvent,
		onTimeout:       cfg.OnTimeout,
		timeoutMs:       cfg.TimeoutMs,
		lastActivity:    time.Now(),
		stopChan:        make(chan struct{}),
		log:             cfg.Log,
		srtpContext:     cfg.SrtpContext,
		srtpEnabled:     cfg.SrtpContext != nil || cfg.DTLSSRTP != nil,
		dtlsConfig:      cfg.DTLSSRTP,
		rtcpMux:         cfg.RTCPMux, videoPLI: cfg.VideoPLI, reports: make(map[uint32]*receptionStats),
	}

	if cfg.DTMFPayload != nil {
		s.dtmfPayloadType = cfg.DTMFPayload.PayloadType
		s.dtmfClockRate = cfg.DTMFPayload.ClockRate
		if cfg.OnDTMFEvent != nil {
			s.dtmfParser = NewDTMFParser(cfg.DTMFPayload.ClockRate, cfg.OnDTMFEvent)
		}
	}

	if cfg.Payload != nil {
		s.unpacker = rtprtcp.DefaultRtpUnpackerFactory(
			cfg.Payload.Codec,
			cfg.Payload.ClockRate,
			128,
			func(pkt base.AvPacket) {
				s.mutex.Lock()
				cb := s.onAvPacket
				s.mutex.Unlock()
				if cb != nil {
					cb(pkt)
				}
			},
		)
	}
	if cfg.VideoPayload != nil {
		s.videoUnpacker = rtprtcp.DefaultRtpUnpackerFactory(
			cfg.VideoPayload.Codec,
			cfg.VideoPayload.ClockRate,
			128,
			func(pkt base.AvPacket) {
				s.mutex.Lock()
				cb := s.onAvPacket
				s.mutex.Unlock()
				if cb != nil {
					cb(pkt)
				}
			},
		)
	}

	addr := fmt.Sprintf("%s:%d", cfg.MediaIP, cfg.Port)
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("resolve udp addr failed: %w", err)
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("listen udp failed: %w", err)
	}

	s.conn = conn
	s.port = conn.LocalAddr().(*net.UDPAddr).Port
	var id [4]byte
	if _, err := rand.Read(id[:]); err != nil {
		conn.Close()
		return nil, err
	}
	s.receiverSSRC = binary.BigEndian.Uint32(id[:])
	s.sendSSRC = s.receiverSSRC
	if cfg.RemoteRTPAddress != "" {
		s.allowedRemoteIP = net.ParseIP(cfg.RemoteSourceIP)
		s.remoteAddr, err = net.ResolveUDPAddr("udp4", cfg.RemoteRTPAddress)
		if err != nil {
			conn.Close()
			return nil, err
		}
	}
	if cfg.RemoteRTCPAddress != "" {
		s.rtcpRemote, err = net.ResolveUDPAddr("udp4", cfg.RemoteRTCPAddress)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("resolve RTCP address: %w", err)
		}
	}
	if !cfg.RTCPMux {
		port := cfg.RTCPPort
		if port == 0 {
			port = s.port + 1
		}
		s.rtcpConn, err = net.ListenUDP("udp4", &net.UDPAddr{IP: udpAddr.IP, Port: port})
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("listen RTCP: %w", err)
		}
	}
	codecName := ""
	if cfg.Payload != nil {
		codecName = cfg.Payload.CodecName
	}
	if cfg.VideoPayload != nil {
		if codecName != "" {
			codecName += "+"
		}
		codecName += cfg.VideoPayload.CodecName
	}
	if s.srtpEnabled {
		if s.dtlsConfig != nil {
			s.log.Infof("dtls-srtp session listening on %s for %s (encrypted)", addr, codecName)
		} else {
			s.log.Infof("srtp session listening on %s for %s (encrypted)", addr, codecName)
		}
	} else {
		s.log.Infof("rtp session listening on %s for %s", addr, codecName)
	}

	count := 3
	if s.rtcpConn != nil {
		count++
	}
	s.wg.Add(count)
	go s.runReceive()
	if cfg.DTLSSRTP != nil && cfg.DTLSSRTP.Client && s.remoteAddr != nil {
		s.mutex.Lock()
		s.dtlsPacketConn = newDTLSPacketConn(s.conn, s.conn.LocalAddr(), s.remoteAddr)
		s.dtlsStarted = true
		s.wg.Add(1)
		go s.runDTLSServer(s.dtlsPacketConn, s.remoteAddr)
		s.mutex.Unlock()
	}
	go s.runTimeout()
	go s.runRTCP()
	if s.rtcpConn != nil {
		go s.runReceiveRTCP()
	}

	return s, nil
}

func (s *RtpSession) Port() int {
	return s.port
}

func (s *RtpSession) SetTimeout(timeoutMs int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.timeoutMs = timeoutMs
	s.lastActivity = time.Now()
}

func (s *RtpSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopChan)
		if s.conn != nil {
			_ = s.conn.Close()
		}
		if s.rtcpConn != nil {
			_ = s.rtcpConn.Close()
		}
		s.mutex.Lock()
		if s.dtlsPacketConn != nil {
			_ = s.dtlsPacketConn.Close()
		}
		s.mutex.Unlock()
	})
	s.wg.Wait()
	// DTLS owns its derived keys; SDES keys belong to the publisher.
	s.keyCloseOnce.Do(func() {
		if s.dtlsConfig != nil {
			if ctx, _ := s.getSrtpContext(); ctx != nil {
				ctx.Close()
			}
		}
	})
	return nil
}

func (s *RtpSession) runReceive() {
	defer s.wg.Done()

	buf := make([]byte, 2048)
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
				s.log.Warnf("read udp failed: %v", err)
				continue
			}
		}

		packet := buf[:n]
		if isSTUNPacket(packet) {
			continue
		}

		if isDTLSPacket(packet) && s.dtlsConfig != nil {
			s.handleDTLSPacket(packet, remoteAddr)
			continue
		}

		if s.rtcpMux && isRTCPMuxPacket(packet) {
			s.handleRTCPPacket(packet)
		} else {
			s.handleRtpPacket(packet, remoteAddr)
		}
	}
}

// isRTCPMuxPacket implements the RTP/RTCP mux packet-type range check from
// RFC 5761. With mux enabled, RTP payload types 64-95 are reserved to avoid
// ambiguity with RTCP packet types 192-223 after the RTP marker bit is set.
func isRTCPMuxPacket(packet []byte) bool {
	return len(packet) >= 4 && packet[0]>>6 == 2 && packet[1] >= 192 && packet[1] <= 223
}

func (s *RtpSession) handleDTLSPacket(data []byte, remoteAddr *net.UDPAddr) {
	s.mutex.Lock()
	if s.dtlsPacketConn == nil {
		s.dtlsPacketConn = newDTLSPacketConn(s.conn, s.conn.LocalAddr(), remoteAddr)
	}
	packetConn := s.dtlsPacketConn
	if !s.dtlsStarted {
		s.dtlsStarted = true
		s.wg.Add(1)
		go s.runDTLSServer(packetConn, remoteAddr)
	}
	s.mutex.Unlock()

	packetConn.enqueue(data, remoteAddr)
}

func (s *RtpSession) runDTLSServer(packetConn *dtlsPacketConn, remoteAddr *net.UDPAddr) {
	defer s.wg.Done()

	cfg := &dtls.Config{
		Certificates: []tls.Certificate{s.dtlsConfig.Certificate},
		ClientAuth:   dtls.RequireAnyClientCert,
		SRTPProtectionProfiles: []dtls.SRTPProtectionProfile{
			dtls.SRTP_AES128_CM_HMAC_SHA1_80,
			dtls.SRTP_AES128_CM_HMAC_SHA1_32,
		},
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("dtls peer certificate missing")
			}
			return verifyFingerprint(rawCerts[0], s.dtlsConfig.RemoteFingerprint)
		},
	}

	cfg.InsecureSkipVerify = true // The SDP fingerprint is verified by VerifyPeerCertificate.
	var conn *dtls.Conn
	var err error
	if s.dtlsConfig.Client {
		conn, err = dtls.Client(packetConn, remoteAddr, cfg)
	} else {
		conn, err = dtls.Server(packetConn, remoteAddr, cfg)
	}
	if err != nil {
		s.log.Warnf("dtls-srtp handshake failed from %s: %v", remoteAddr, err)
		return
	}
	ctxHandshake, cancelHandshake := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelHandshake()
	if err := conn.HandshakeContext(ctxHandshake); err != nil {
		conn.Close()
		s.log.Warnf("dtls-srtp handshake failed from %s: %v", remoteAddr, err)
		return
	}

	profile, ok := conn.SelectedSRTPProtectionProfile()
	if !ok {
		conn.Close()
		s.log.Warnf("dtls-srtp handshake completed without srtp profile from %s", remoteAddr)
		return
	}
	ctx, err := createSrtpContextFromDTLSRole(conn, profile, s.dtlsConfig.Client)
	if err != nil {
		conn.Close()
		s.log.Warnf("create dtls-srtp context failed from %s: %v", remoteAddr, err)
		return
	}

	s.mutex.Lock()
	old := s.srtpContext
	s.srtpContext = ctx
	s.remoteAddr = remoteAddr
	s.srtpEnabled = true
	s.mutex.Unlock()
	if old != nil {
		old.Close()
	}
	s.log.Infof("dtls-srtp handshake completed from %s profile=%v", remoteAddr, profile)

	<-s.stopChan
	conn.Close()
}

func (s *RtpSession) getSrtpContext() (*SrtpContext, bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.srtpContext, s.srtpEnabled
}

func isSTUNPacket(data []byte) bool {
	if len(data) < 20 {
		return false
	}
	// STUN packets have the two most significant bits cleared and carry the
	// magic cookie 0x2112A442 at bytes 4..7.
	return data[0]&0xC0 == 0 &&
		data[4] == 0x21 &&
		data[5] == 0x12 &&
		data[6] == 0xA4 &&
		data[7] == 0x42
}

func (s *RtpSession) handleRtpPacket(data []byte, remoteAddr *net.UDPAddr) {
	s.mutex.Lock()
	expected, pinned, allowed := s.remoteAddr, s.remotePinned, s.allowedRemoteIP
	s.mutex.Unlock()
	if expected != nil {
		if pinned && expected.String() != remoteAddr.String() {
			return
		}
		if !pinned && !expected.IP.Equal(remoteAddr.IP) && (allowed == nil || !allowed.Equal(remoteAddr.IP)) {
			return
		}
	}
	var rtpData []byte
	var err error

	// 如果启用SRTP，先解密
	if srtpContext, srtpEnabled := s.getSrtpContext(); srtpEnabled {
		if srtpContext == nil {
			s.log.Debugf("dropping srtp packet before crypto context is ready")
			return
		}
		rtpData, err = srtpContext.DecryptRTP(data)
		if err != nil {
			s.log.Warnf("decrypt srtp packet failed: %v", err)
			return
		}
	} else {
		rtpData = data
	}

	pkt, err := rtprtcp.ParseRtpPacket(rtpData)
	if err != nil {
		s.log.Warnf("parse rtp packet failed: %v", err)
		return
	}

	// Only authenticated and parsed RTP contributes to receiver statistics.
	clock := 0
	if s.payload != nil && int(pkt.Header.PacketType) == s.payload.PayloadType {
		clock = s.payload.ClockRate
	}
	video := s.videoPayload != nil && int(pkt.Header.PacketType) == s.videoPayload.PayloadType
	if video {
		clock = s.videoPayload.ClockRate
	}
	if s.dtmfParser != nil && int(pkt.Header.PacketType) == s.dtmfPayloadType {
		clock = s.dtmfParser.clockRate
	}
	if clock == 0 {
		return
	}
	if s.payload != nil && (s.payload.Codec == base.AvPacketPtAvc || s.payload.Codec == base.AvPacketPtHevc) {
		video = true
	}
	s.mutex.Lock()
	s.lastActivity = time.Now()
	if !s.remotePinned {
		s.remoteAddr = remoteAddr
		s.remotePinned = true
	}
	stats := s.reports[pkt.Header.Ssrc]
	if stats == nil {
		if len(s.reports) >= 32 {
			s.mutex.Unlock()
			return
		}
		stats = &receptionStats{}
		s.reports[pkt.Header.Ssrc] = stats
	}
	gap, fresh := stats.receive(pkt.Header.Seq, pkt.Header.Timestamp, clock, time.Now())
	first := stats.received == 1
	s.packetsReceived++
	if s.ssrc == 0 {
		s.ssrc = pkt.Header.Ssrc
	}
	s.mutex.Unlock()
	if !fresh {
		return
	}
	if video && (gap || first) {
		s.sendPLI(pkt.Header.Ssrc)
	}

	if s.dtmfParser != nil && int(pkt.Header.PacketType) == s.dtmfPayloadType {
		s.dtmfParser.Feed(pkt)
		return
	}

	if s.onRTP != nil && s.payload != nil && int(pkt.Header.PacketType) == s.payload.PayloadType {
		var raw rtp.Packet
		if raw.Unmarshal(rtpData) == nil {
			s.onRTP(&raw)
		}
	}
	payloadType := int(pkt.Header.PacketType)
	if s.unpacker != nil && s.payload != nil && payloadType == s.payload.PayloadType {
		s.unpacker.Feed(pkt)
		return
	}
	if s.videoUnpacker != nil && s.videoPayload != nil && payloadType == s.videoPayload.PayloadType {
		s.videoUnpacker.Feed(pkt)
	}
}

func (s *RtpSession) handleRTCPPacket(data []byte) bool {
	rtcpData := data
	srtpContext, srtpEnabled := s.getSrtpContext()
	if srtpEnabled {
		if srtpContext == nil {
			s.log.Debugf("dropping srtcp packet before crypto context is ready")
			return false
		}
		decrypted, err := srtpContext.DecryptRTCP(data)
		if err != nil {
			s.log.Warnf("decrypt srtcp packet failed: %v", err)
			return false
		}
		rtcpData = decrypted
	}

	packets, err := rtcp.Unmarshal(rtcpData)
	if err != nil {
		s.log.Debugf("invalid RTCP packet: %v", err)
		return false
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	for _, packet := range packets {
		if sr, ok := packet.(*rtcp.SenderReport); ok {
			stats := s.reports[sr.SSRC]
			if stats == nil {
				if len(s.reports) >= 32 {
					continue
				}
				stats = &receptionStats{}
				s.reports[sr.SSRC] = stats
			}
			stats.lastSR = uint32(sr.NTPTime >> 16)
			stats.lastSRAt = time.Now()
		}
	}

	return true
}

func (s *RtpSession) runRTCP() {
	defer s.wg.Done()

	// Send RTCP RR (Receiver Report) every 5 seconds
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.sendRTCP_RR()
		}
	}
}

func (s *RtpSession) sendRTCP_RR() {
	s.sendRTCP(nil)
}

func (s *RtpSession) runTimeout() {
	defer s.wg.Done()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.mutex.Lock()
			elapsed := time.Since(s.lastActivity)
			timeout := s.timeoutMs
			s.mutex.Unlock()

			if timeout > 0 && elapsed > time.Duration(timeout)*time.Millisecond {
				s.log.Warnf("rtp session timeout after %v", elapsed)
				if s.onTimeout != nil {
					go s.onTimeout()
				}
				return
			}
		}
	}
}

// RetainDTLSContext only exports keys verified against this peer certificate.
func (s *RtpSession) RetainDTLSContext() *SrtpContext {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	select {
	case <-s.stopChan:
		return nil
	default:
	}
	if s.dtlsConfig == nil {
		return nil
	}
	return s.srtpContext.Retain()
}

// WriteRTP uses the same bound socket and cryptographic context as reception.
// Each leg owns its SSRC and sequence space; browser-only extensions are omitted.
func (s *RtpSession) WriteRTP(packet *rtp.Packet) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.payload == nil {
		return fmt.Errorf("RTP audio payload unavailable")
	}
	return s.writeRTPWithLock(packet, uint8(s.payload.PayloadType))
}

// WriteDTMF sends one RFC 4733 telephone-event using the negotiated event payload.
// The end packet is repeated to tolerate packet loss on the SIP media leg.
func (s *RtpSession) WriteDTMF(key string, duration time.Duration) error {
	event, ok := dtmfEventCode(key)
	if !ok {
		return fmt.Errorf("unsupported DTMF key %q", key)
	}
	if s.dtmfPayloadType < 0 || s.dtmfClockRate <= 0 || s.dtmfClockRate > 48000 {
		return fmt.Errorf("telephone-event was not negotiated")
	}
	if duration < 40*time.Millisecond || duration > time.Second {
		return fmt.Errorf("DTMF duration must be between 40ms and 1s")
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	step := s.dtmfClockRate / 50 // 20 ms packets
	if step < 1 {
		step = 1
	}
	total := int(duration * time.Duration(s.dtmfClockRate) / time.Second)
	if total < step {
		total = step
	}
	timestamp := uint32((time.Now().UnixNano() / int64(time.Millisecond)) * int64(s.dtmfClockRate) / 1000)
	first := true
	send := func(elapsed uint16, end bool) error {
		if !first {
			timer := time.NewTimer(20 * time.Millisecond)
			select {
			case <-timer.C:
			case <-s.stopChan:
				timer.Stop()
				return net.ErrClosed
			}
		}
		err := s.writeDTMFPayload(event, elapsed, timestamp, first, end)
		first = false
		return err
	}
	for elapsed := step; elapsed < total; elapsed += step {
		if err := send(uint16(elapsed), false); err != nil {
			return err
		}
	}
	for i := 0; i < 3; i++ {
		if err := send(uint16(total), true); err != nil {
			return err
		}
	}
	return nil
}

func (s *RtpSession) writeDTMFPayload(event uint8, duration uint16, timestamp uint32, marker, end bool) error {
	flags := byte(10) // RFC 4733 volume, 0-63
	if end {
		flags |= 0x80
	}
	payload := []byte{event, flags, byte(duration >> 8), byte(duration)}
	packet := &rtp.Packet{Header: rtp.Header{Version: 2, Marker: marker, Timestamp: timestamp}, Payload: payload}
	return s.writeRTPWithLock(packet, uint8(s.dtmfPayloadType))
}

// Caller holds sendMu so audio and telephone-event packets share one RTP sequence.
func (s *RtpSession) writeRTPWithLock(packet *rtp.Packet, payloadType uint8) error {
	s.mutex.Lock()
	target := s.remoteAddr
	crypto, secure := s.srtpContext, s.srtpEnabled
	s.mutex.Unlock()
	if target == nil || s.payload == nil {
		return fmt.Errorf("RTP target unavailable")
	}
	select {
	case <-s.stopChan:
		return net.ErrClosed
	default:
	}
	p := rtp.Packet{Header: rtp.Header{Version: 2, Marker: packet.Marker, PayloadType: payloadType, SequenceNumber: s.sendSeq, Timestamp: packet.Timestamp, SSRC: s.sendSSRC}, Payload: packet.Payload}
	s.sendSeq++
	raw, err := p.Marshal()
	if err != nil {
		return err
	}
	if secure {
		if crypto == nil {
			return fmt.Errorf("SRTP handshake pending")
		}
		raw, err = crypto.EncryptRTP(raw)
		if err != nil {
			return err
		}
	}
	_, err = s.conn.WriteToUDP(raw, target)
	if err == nil {
		s.mutex.Lock()
		s.sentPackets++
		s.sentOctets += uint32(len(p.Payload))
		s.lastSentTimestamp = p.Timestamp
		s.lastSentAt = time.Now()
		s.mutex.Unlock()
	}
	return err
}

func dtmfEventCode(key string) (uint8, bool) {
	switch key {
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		return key[0] - '0', true
	case "*":
		return 10, true
	case "#":
		return 11, true
	case "A", "B", "C", "D":
		return uint8(key[0]-'A') + 12, true
	default:
		return 0, false
	}
}

func (s *RtpSession) PacketCounts() (received, sent uint32) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.packetsReceived, s.sentPackets
}
