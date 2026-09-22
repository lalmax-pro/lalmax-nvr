package gb28181

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// BroadcastSession represents a voice broadcast/intercom session.
type BroadcastSession struct {
	DeviceID     string
	ChannelID    string
	Session      *sipgo.DialogClientSession
	RTPConn      *net.UDPConn
	TCPConn      net.Conn
	TCPListener  net.Listener
	RTPPort      int
	TCPActive    bool
	RTPPeerIP    string
	RTPPeerPort  int
	SSRC         string
	RTPSSRC      uint32
	CallID       string
	PayloadType  uint8
	IsTCP        bool
	TCPPassive   bool
	AudioChan    chan []byte
	ReadyCh      chan struct{}
	TCPReadyCh   chan struct{}
	TCPReadyOnce sync.Once
	ReadyOnce    sync.Once
	StopOnce     sync.Once
	IdleTimer    *time.Timer
	SeqNum       uint16
	Timestamp    uint32
	AudioBuffer  []byte
	client       *sipgo.Client
	cfg          *Config
	stopped      bool
	mu           sync.Mutex
}

// BroadcastManager manages broadcast sessions.
type BroadcastManager struct {
	mu          sync.RWMutex
	sessions    map[string]*BroadcastSession // key: deviceID_channelID
	client      *sipgo.Client
	cfg         *Config
	deviceStore *DeviceStore
}

// NewBroadcastManager creates a new broadcast manager.
func NewBroadcastManager(client *sipgo.Client, cfg *Config, store *DeviceStore) *BroadcastManager {
	return &BroadcastManager{
		sessions:    make(map[string]*BroadcastSession),
		client:      client,
		cfg:         cfg,
		deviceStore: store,
	}
}

func broadcastKey(deviceID, channelID string) string {
	return deviceID + "_" + channelID
}

// StartBroadcast starts a voice broadcast session to a device channel.
func (bm *BroadcastManager) StartBroadcast(deviceID, channelID string, store *DeviceStore) (*BroadcastSession, error) {
	key := broadcastKey(deviceID, channelID)

	bm.mu.Lock()
	if existing, ok := bm.sessions[key]; ok {
		bm.mu.Unlock()
		return existing, nil
	}
	bm.mu.Unlock()

	dev, ok := store.Load(deviceID)
	if !ok || !dev.IsOnline {
		return nil, ErrDeviceOffline
	}

	_, ok = dev.GetChannel(channelID)
	if !ok {
		return nil, ErrChannelNotExist
	}

	bs := &BroadcastSession{
		DeviceID:    deviceID,
		ChannelID:   channelID,
		AudioChan:   make(chan []byte, 100),
		ReadyCh:     make(chan struct{}),
		TCPReadyCh:  make(chan struct{}),
		PayloadType: 8, // PCMA
		client:      bm.client,
		cfg:         bm.cfg,
	}

	// Generate one stable RTP SSRC for the whole talk session.
	bs.RTPSSRC = uint32(randInt(1000000000, 0x7FFFFFFF))
	bs.SSRC = fmt.Sprintf("%010d", bs.RTPSSRC)

	// Prepare UDP listener
	if err := bs.prepareRTPConn(); err != nil {
		return nil, fmt.Errorf("prepare RTP failed: %w", err)
	}

	bm.mu.Lock()
	bm.sessions[key] = bs
	bm.mu.Unlock()

	// Send broadcast notify to device
	if err := bs.sendBroadcastNotify(store); err != nil {
		bm.RemoveSession(key)
		_ = bs.Stop()
		return nil, fmt.Errorf("send broadcast notify failed: %w", err)
	}

	slog.Info("[Broadcast] session started", "device_id", deviceID, "channel_id", channelID, "port", bs.RTPPort)
	return bs, nil
}

// StopBroadcast stops a broadcast session.
func (bm *BroadcastManager) StopBroadcast(deviceID, channelID string) error {
	key := broadcastKey(deviceID, channelID)
	bm.mu.Lock()
	bs, ok := bm.sessions[key]
	if !ok {
		bm.mu.Unlock()
		return nil
	}
	delete(bm.sessions, key)
	bm.mu.Unlock()

	return bs.Stop()
}

// RemoveSession removes a session from the manager.
func (bm *BroadcastManager) RemoveSession(key string) {
	bm.mu.Lock()
	delete(bm.sessions, key)
	bm.mu.Unlock()
}

// GetSession returns a broadcast session by key.
func (bm *BroadcastManager) GetSession(deviceID, channelID string) (*BroadcastSession, bool) {
	key := broadcastKey(deviceID, channelID)
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	bs, ok := bm.sessions[key]
	return bs, ok
}

func (bs *BroadcastSession) prepareRTPConn() error {
	localAddr, err := net.ResolveUDPAddr("udp", ":0")
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		return err
	}

	bs.RTPConn = conn
	bs.RTPPort = conn.LocalAddr().(*net.UDPAddr).Port
	return nil
}

func (bs *BroadcastSession) sendBroadcastNotify(store *DeviceStore) error {
	dev, ok := store.Load(bs.DeviceID)
	if !ok {
		return ErrDeviceNotExist
	}

	uri := sip.Uri{
		Scheme: "sip",
		User:   bs.ChannelID,
		Host:   getHost(dev.Address),
		Port:   getPort(dev.Address),
	}

	req := sip.NewRequest(sip.MESSAGE, uri)
	req.AppendHeader(sip.NewHeader("Content-Type", "Application/MANSCDP+xml"))

	sourceID := bs.cfg.ID
	if sourceID == "" {
		sourceID = bs.DeviceID
	}

	sn := randInt(100000, 999999)
	body := fmt.Sprintf(`<?xml version="1.0" encoding="GB2312"?>
<Notify>
<CmdType>Broadcast</CmdType>
<SN>%d</SN>
<SourceID>%s</SourceID>
<TargetID>%s</TargetID>
</Notify>`, sn, sourceID, bs.ChannelID)
	req.SetBody([]byte(body))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := bs.client.Do(ctx, req)
	return err
}

// SendAudioData sends audio data to the broadcast session.
func (bs *BroadcastSession) SendAudioData(data []byte) error {
	bs.mu.Lock()
	if bs.stopped {
		bs.mu.Unlock()
		return fmt.Errorf("broadcast session stopped")
	}
	bs.mu.Unlock()

	select {
	case bs.AudioChan <- data:
		return nil
	default:
		return fmt.Errorf("audio channel full")
	}
}

// Stop stops the broadcast session.
func (bs *BroadcastSession) Stop() error {
	var err error
	bs.StopOnce.Do(func() {
		bs.mu.Lock()
		bs.stopped = true
		bs.mu.Unlock()

		if bs.IdleTimer != nil {
			bs.IdleTimer.Stop()
		}

		close(bs.AudioChan)

		if bs.Session != nil {
			_ = bs.Session.Bye(context.Background())
		}

		if bs.RTPConn != nil {
			bs.RTPConn.Close()
		}
		if bs.TCPConn != nil {
			bs.TCPConn.Close()
		}
		if bs.TCPListener != nil {
			bs.TCPListener.Close()
		}

		slog.Info("[Broadcast] session stopped", "device_id", bs.DeviceID, "channel_id", bs.ChannelID)
	})
	return err
}

// OnInvite handles a SIP INVITE from a device for broadcast/intercom.
func (bm *BroadcastManager) OnInvite(req *sip.Request, tx sip.ServerTransaction) {
	from := req.From()
	if from == nil || from.Address.User == "" {
		slog.Error("[Broadcast] OnInvite: invalid from header")
		return
	}

	deviceID := from.Address.User
	sdpBody := string(req.Body())

	slog.Info("[Broadcast] OnInvite received", "device_id", deviceID, "sdp_len", len(sdpBody))

	// Parse SDP to get peer info
	offer := parseBroadcastSDP(sdpBody)
	offer.IP = normalizeMediaIP(offer.IP, req.Source())

	// Find matching session
	// 1. Try to find by channelID from SDP o= line
	// 2. Try to find by deviceID (fromUser)
	// 3. Try to find by looking up device in store (fromUser might be channelID)
	bm.mu.RLock()
	var bs *BroadcastSession
	if offer.ChannelID != "" {
		bs, _ = bm.sessions[broadcastKey(deviceID, offer.ChannelID)]
	}
	if bs == nil {
		for _, s := range bm.sessions {
			if s.DeviceID == deviceID {
				bs = s
				break
			}
		}
	}
	// Fallback: fromUser might be channelID, find device by channelID
	if bs == nil && bm.deviceStore != nil {
		bm.deviceStore.RangeDevices(func(devID string, dev *Device) bool {
			if ch, ok := dev.GetChannel(deviceID); ok {
				// Found device with this channelID, try to find session by deviceID
				for _, s := range bm.sessions {
					if s.DeviceID == devID {
						bs = s
						slog.Info("[Broadcast] OnInvite: found session by channelID lookup",
							"device_id", devID, "channel_id", ch.ChannelID)
						return false
					}
				}
			}
			return true
		})
	}
	bm.mu.RUnlock()

	if bs == nil {
		slog.Warn("[Broadcast] OnInvite: no session found", "device_id", deviceID)
		tx.Respond(sip.NewResponseFromRequest(req, 404, "Not Found", nil))
		return
	}

	bs.RTPPeerIP = offer.IP
	bs.RTPPeerPort = offer.Port
	bs.IsTCP = offer.IsTCP
	bs.TCPActive = !offer.TCPActive
	if offer.PayloadType >= 0 {
		bs.PayloadType = uint8(offer.PayloadType)
	}
	if offer.SSRC != 0 {
		bs.RTPSSRC = offer.SSRC
		bs.SSRC = fmt.Sprintf("%010d", offer.SSRC)
	}

	// Build 200 OK response with SDP
	sdpIP := advertisedMediaIP(bs.cfg)

	mediaPort := bs.RTPPort
	protocol := "RTP/AVP"
	if offer.IsTCP {
		if bs.RTPConn != nil {
			_ = bs.RTPConn.Close()
			bs.RTPConn = nil
		}
		if bs.TCPActive {
			if err := bs.prepareTCPActivePort(); err != nil {
				slog.Error("[Broadcast] OnInvite: prepare TCP active port failed", "error", err)
				tx.Respond(sip.NewResponseFromRequest(req, 500, "Internal Server Error", nil))
				return
			}
		} else {
			if err := bs.prepareTCPListener(); err != nil {
				slog.Error("[Broadcast] OnInvite: prepare TCP listener failed", "error", err)
				tx.Respond(sip.NewResponseFromRequest(req, 500, "Internal Server Error", nil))
				return
			}
		}
		mediaPort = bs.RTPPort
		protocol = "TCP/RTP/AVP"
	}

	responseSDP := fmt.Sprintf("v=0\r\n"+
		"o=%s 0 0 IN IP4 %s\r\n"+
		"s=Play\r\n"+
		"c=IN IP4 %s\r\n"+
		"t=0 0\r\n"+
		"m=audio %d %s %d\r\n"+
		"a=rtpmap:%d PCMA/8000\r\n"+
		"a=sendonly\r\n",
		bs.cfg.ID, sdpIP, sdpIP, mediaPort, protocol, bs.PayloadType, bs.PayloadType)
	if offer.IsTCP {
		setup := "passive"
		if bs.TCPActive {
			setup = "active"
		}
		responseSDP += fmt.Sprintf("a=setup:%s\r\n", setup)
		responseSDP += "a=connection:new\r\n"
	}
	responseSDP += fmt.Sprintf("y=%s\r\n", bs.SSRC)
	responseSDP += "f=v/////a/1/8/1\r\n"

	okResp := sip.NewResponseFromRequest(req, 200, "OK", nil)
	okResp.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	okResp.SetBody([]byte(responseSDP))

	if err := tx.Respond(okResp); err != nil {
		slog.Error("[Broadcast] OnInvite: send 200 OK failed", "error", err)
		return
	}

	if bs.IsTCP {
		if bs.TCPActive {
			go bs.dialTCPConn()
		} else {
			go bs.acceptTCPConn()
		}
	} else {
		bs.markReady()
	}

	// Start audio sender
	go bs.startAudioSender()

	slog.Info("[Broadcast] OnInvite: 200 OK sent", "device_id", deviceID, "port", mediaPort)
}

func (bs *BroadcastSession) markReady() {
	bs.ReadyOnce.Do(func() {
		close(bs.ReadyCh)
	})
}

func (bs *BroadcastSession) markTCPReady() {
	bs.TCPReadyOnce.Do(func() {
		close(bs.TCPReadyCh)
	})
	bs.markReady()
}

func (bs *BroadcastSession) prepareTCPListener() error {
	if bs.TCPListener != nil {
		return nil
	}
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return err
	}
	bs.TCPListener = listener
	bs.RTPPort = listener.Addr().(*net.TCPAddr).Port
	return nil
}

func (bs *BroadcastSession) prepareTCPActivePort() error {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return err
	}
	bs.RTPPort = listener.Addr().(*net.TCPAddr).Port
	return listener.Close()
}

func (bs *BroadcastSession) dialTCPConn() {
	if bs.RTPPeerIP == "" || bs.RTPPeerPort == 0 {
		slog.Error("[Broadcast] TCP dial skipped: peer address not ready", "device_id", bs.DeviceID, "channel_id", bs.ChannelID)
		return
	}

	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{Port: bs.RTPPort}, Timeout: 3 * time.Second}
	remoteAddr := net.JoinHostPort(bs.RTPPeerIP, strconv.Itoa(bs.RTPPeerPort))

	var conn net.Conn
	var err error
	for i := 0; i < 3; i++ {
		conn, err = dialer.Dial("tcp", remoteAddr)
		if err == nil {
			break
		}
		slog.Warn("[Broadcast] TCP dial failed", "device_id", bs.DeviceID, "channel_id", bs.ChannelID, "remote", remoteAddr, "attempt", i+1, "error", err)
		time.Sleep(time.Second)
	}
	if err != nil {
		slog.Error("[Broadcast] TCP dial failed", "device_id", bs.DeviceID, "channel_id", bs.ChannelID, "remote", remoteAddr, "error", err)
		return
	}

	bs.setTCPConn(conn)
	slog.Info("[Broadcast] TCP connected", "device_id", bs.DeviceID, "channel_id", bs.ChannelID, "remote", conn.RemoteAddr().String())
	bs.markTCPReady()
	bs.readTCPConn(conn)
}

func (bs *BroadcastSession) acceptTCPConn() {
	if bs.TCPListener == nil {
		return
	}
	conn, err := bs.TCPListener.Accept()
	if err != nil {
		bs.mu.Lock()
		stopped := bs.stopped
		bs.mu.Unlock()
		if !stopped {
			slog.Error("[Broadcast] TCP accept failed", "device_id", bs.DeviceID, "channel_id", bs.ChannelID, "error", err)
		}
		return
	}

	bs.setTCPConn(conn)
	slog.Info("[Broadcast] TCP connected", "device_id", bs.DeviceID, "channel_id", bs.ChannelID, "remote", conn.RemoteAddr().String())
	bs.markTCPReady()
	bs.readTCPConn(conn)
}

func (bs *BroadcastSession) setTCPConn(conn net.Conn) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if bs.TCPConn != nil && bs.TCPConn != conn {
		_ = bs.TCPConn.Close()
	}
	bs.TCPConn = conn
}

func (bs *BroadcastSession) readTCPConn(conn net.Conn) {
	// Keep reading and discarding peer TCP packets so connection closure is detected.
	lengthBuf := make([]byte, 2)
	for {
		if _, err := io.ReadFull(conn, lengthBuf); err != nil {
			break
		}
		n := int(binary.BigEndian.Uint16(lengthBuf))
		if n <= 0 {
			continue
		}
		if _, err := io.CopyN(io.Discard, conn, int64(n)); err != nil {
			break
		}
	}

	bs.mu.Lock()
	if bs.TCPConn == conn {
		bs.TCPConn = nil
	}
	bs.mu.Unlock()
	_ = conn.Close()
}

// OnAck handles a SIP ACK for broadcast.
func (bm *BroadcastManager) OnAck(req *sip.Request, tx sip.ServerTransaction) {
	from := req.From()
	if from == nil {
		return
	}
	deviceID := from.Address.User
	slog.Debug("[Broadcast] OnAck received", "device_id", deviceID)
}

// OnBye handles a SIP BYE for broadcast.
func (bm *BroadcastManager) OnBye(req *sip.Request, tx sip.ServerTransaction) {
	from := req.From()
	if from == nil {
		return
	}
	deviceID := from.Address.User

	bm.mu.Lock()
	for key, bs := range bm.sessions {
		if bs.DeviceID == deviceID {
			delete(bm.sessions, key)
			go bs.Stop()
			break
		}
	}
	bm.mu.Unlock()

	tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	slog.Info("[Broadcast] OnBye: session stopped", "device_id", deviceID)
}

func (bs *BroadcastSession) startAudioSender() {
	slog.Info("[Broadcast] audio sender started", "device_id", bs.DeviceID)

	<-bs.ReadyCh

	for audioData := range bs.AudioChan {
		if err := bs.sendAudioData(audioData); err != nil {
			slog.Error("[Broadcast] send audio failed", "error", err)
		}
	}

	slog.Info("[Broadcast] audio sender stopped", "device_id", bs.DeviceID)
}

func (bs *BroadcastSession) sendAudioData(data []byte) error {
	if bs.IsTCP {
		if bs.TCPConn == nil {
			return fmt.Errorf("TCP connection not ready")
		}
	} else if bs.RTPConn == nil || bs.RTPPeerIP == "" || bs.RTPPeerPort == 0 {
		return fmt.Errorf("RTP connection not ready")
	}
	if len(data) == 0 {
		return nil
	}

	var remoteAddr *net.UDPAddr
	if !bs.IsTCP {
		var err error
		remoteAddr, err = net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", bs.RTPPeerIP, bs.RTPPeerPort))
		if err != nil {
			return err
		}
	}

	const g711FrameSamples = 160 // 20ms at 8kHz, 1 byte per PCMA sample.
	for len(data) > 0 {
		n := g711FrameSamples
		if len(data) < n {
			n = len(data)
		}
		if err := bs.sendRTPPayload(remoteAddr, data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func (bs *BroadcastSession) sendRTPPayload(remoteAddr *net.UDPAddr, payload []byte) error {
	bs.SeqNum++
	bs.Timestamp += uint32(len(payload))

	// RTP header (12 bytes) + payload
	rtpPacket := make([]byte, 12+len(payload))
	rtpPacket[0] = 0x80 // V=2
	rtpPacket[1] = bs.PayloadType
	rtpPacket[2] = byte(bs.SeqNum >> 8)
	rtpPacket[3] = byte(bs.SeqNum)
	rtpPacket[4] = byte(bs.Timestamp >> 24)
	rtpPacket[5] = byte(bs.Timestamp >> 16)
	rtpPacket[6] = byte(bs.Timestamp >> 8)
	rtpPacket[7] = byte(bs.Timestamp)
	// SSRC
	rtpPacket[8] = byte(bs.RTPSSRC >> 24)
	rtpPacket[9] = byte(bs.RTPSSRC >> 16)
	rtpPacket[10] = byte(bs.RTPSSRC >> 8)
	rtpPacket[11] = byte(bs.RTPSSRC)
	copy(rtpPacket[12:], payload)

	if bs.IsTCP {
		bs.mu.Lock()
		conn := bs.TCPConn
		bs.mu.Unlock()
		if conn == nil {
			return fmt.Errorf("TCP connection not ready")
		}
		length := make([]byte, 2)
		binary.BigEndian.PutUint16(length, uint16(len(rtpPacket)))
		if _, err := conn.Write(length); err != nil {
			return err
		}
		_, err := conn.Write(rtpPacket)
		return err
	}

	_, err := bs.RTPConn.WriteToUDP(rtpPacket, remoteAddr)
	return err
}

type broadcastSDPOffer struct {
	ChannelID   string
	IP          string
	Port        int
	IsTCP       bool
	TCPActive   bool
	PayloadType int
	SSRC        uint32
}

// parseBroadcastSDP extracts peer IP, port, transport, codec and GB metadata from SDP.
func parseBroadcastSDP(sdpBody string) broadcastSDPOffer {
	offer := broadcastSDPOffer{
		PayloadType: 8, // default PCMA
	}

	lines := splitLines(sdpBody)
	for _, line := range lines {
		switch {
		case len(line) > 2 && line[:2] == "o=":
			// o=<channel-id> 0 0 IN IP4 x.x.x.x
			parts := splitFields(line[2:])
			if len(parts) > 0 {
				offer.ChannelID = parts[0]
			}
		case len(line) > 2 && line[:2] == "c=":
			// c=IN IP4 x.x.x.x
			parts := splitFields(line[2:])
			for i, p := range parts {
				if p == "IP4" && i+1 < len(parts) {
					offer.IP = parts[i+1]
				}
			}
		case len(line) > 2 && line[:2] == "m=":
			// m=audio port RTP/AVP 8
			parts := splitFields(line[2:])
			if len(parts) >= 3 {
				fmt.Sscanf(parts[1], "%d", &offer.Port)
				if parts[2] == "TCP/RTP/AVP" || parts[2] == "RTP/AVP/TCP" {
					offer.IsTCP = true
				}
				if len(parts) >= 4 {
					fmt.Sscanf(parts[3], "%d", &offer.PayloadType)
				}
			}
		case len(line) > len("a=setup:") && line[:len("a=setup:")] == "a=setup:":
			offer.TCPActive = line[len("a=setup:"):] == "active"
		case len(line) > 2 && line[:2] == "y=":
			if ssrc, err := strconv.ParseUint(line[2:], 10, 32); err == nil {
				offer.SSRC = uint32(ssrc)
			}
		}
	}
	return offer
}

func normalizeMediaIP(mediaIP, source string) string {
	if mediaIP != "" && mediaIP != "0.0.0.0" {
		return mediaIP
	}
	if source == "" {
		return mediaIP
	}
	host, _, err := net.SplitHostPort(source)
	if err == nil && host != "" {
		return host
	}
	return source
}

func advertisedMediaIP(cfg *Config) string {
	if cfg != nil {
		if isAdvertisableMediaIP(cfg.MediaIP) {
			return cfg.MediaIP
		}
		if isAdvertisableMediaIP(cfg.Host) {
			return cfg.Host
		}
	}
	if ip := firstNonLoopbackIPv4(); ip != "" {
		return ip
	}
	return "127.0.0.1"
}

func isAdvertisableMediaIP(addr string) bool {
	if addr == "" {
		return false
	}
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.To4() != nil && !ip.IsUnspecified() && !ip.IsLoopback()
}

func firstNonLoopbackIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip4 := ip.To4(); ip4 != nil && !ip4.IsUnspecified() && !ip4.IsLoopback() {
				return ip4.String()
			}
		}
	}
	return ""
}

func splitLines(s string) []string {
	var lines []string
	current := ""
	for _, c := range s {
		if c == '\n' {
			lines = append(lines, current)
			current = ""
		} else if c != '\r' {
			current += string(c)
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func splitFields(s string) []string {
	var fields []string
	current := ""
	for _, c := range s {
		if c == ' ' || c == '\t' {
			if current != "" {
				fields = append(fields, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		fields = append(fields, current)
	}
	return fields
}
