package gb28181

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/emiago/sipgo"
)

const (
	PayloadTypePCMA   uint8  = 8
	AudioChanBuffer   int    = 100
	TCPDialTimeout           = 10 * time.Second
	RTPSamplesPer20ms uint32 = 160
	RTPVersion2       byte   = 0x80
)

// TalkSession 表示一个对讲会话
type TalkSession struct {
	DeviceID      string
	ChannelID     string
	TransportMode TransportMode
	SSRC          string
	CallID        string
	PayloadType   uint8

	// 网络连接
	RTPConn     *net.UDPConn // UDP 模式
	TCPConn     net.Conn     // TCP 主动模式
	TCPListener net.Listener // TCP 被动模式
	RTPPort     int
	RTPPeerIP   string
	RTPPeerPort int

	// 音频通道
	AudioChan chan []byte
	ReadyCh   chan struct{}
	ReadyOnce sync.Once
	StopOnce  sync.Once

	// RTP 状态
	SeqNum    uint16
	Timestamp uint32

	// 控制
	client  *sipgo.Client
	cfg     *Config
	stopped bool
	mu      sync.Mutex
}

// NewTalkSession 创建新的 TalkSession
func NewTalkSession(deviceID, channelID string, mode TransportMode, cfg *Config, client *sipgo.Client) *TalkSession {
	return &TalkSession{
		DeviceID:      deviceID,
		ChannelID:     channelID,
		TransportMode: mode,
		PayloadType:   PayloadTypePCMA,
		AudioChan:     make(chan []byte, AudioChanBuffer),
		ReadyCh:       make(chan struct{}),
		client:        client,
		cfg:           cfg,
	}
}

// prepareUDPConn 准备 UDP 连接
func (ts *TalkSession) prepareUDPConn() error {
	localAddr, err := net.ResolveUDPAddr("udp", ":0")
	if err != nil {
		return fmt.Errorf("resolve UDP addr: %w", err)
	}

	conn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		return fmt.Errorf("listen UDP: %w", err)
	}

	ts.RTPConn = conn
	ts.RTPPort = conn.LocalAddr().(*net.UDPAddr).Port
	return nil
}

// prepareTCPListener 准备 TCP 被动监听
func (ts *TalkSession) prepareTCPListener() error {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return fmt.Errorf("listen TCP: %w", err)
	}

	ts.TCPListener = listener
	ts.RTPPort = listener.Addr().(*net.TCPAddr).Port
	return nil
}

// acceptTCPConn 接受 TCP 连接（TCP 被动模式）
func (ts *TalkSession) acceptTCPConn() {
	conn, err := ts.TCPListener.Accept()
	if err != nil {
		if !ts.stopped {
			slog.Error("[Talk] accept TCP failed", "error", err)
		}
		return
	}

	ts.mu.Lock()
	ts.TCPConn = conn
	ts.mu.Unlock()

	slog.Info("[Talk] TCP connection accepted", "device_id", ts.DeviceID, "remote", conn.RemoteAddr())

	// 通知就绪
	ts.ReadyOnce.Do(func() {
		close(ts.ReadyCh)
	})
}

// connectTCP 主动连接设备（TCP 主动模式）
func (ts *TalkSession) connectTCP(peerIP string, peerPort int) {
	addr := net.JoinHostPort(peerIP, fmt.Sprintf("%d", peerPort))
	conn, err := net.DialTimeout("tcp", addr, TCPDialTimeout)
	if err != nil {
		slog.Error("[Talk] connect TCP failed", "error", err, "addr", addr)
		return
	}

	ts.mu.Lock()
	ts.TCPConn = conn
	ts.RTPPeerIP = peerIP
	ts.RTPPeerPort = peerPort
	ts.mu.Unlock()

	slog.Info("[Talk] TCP connected", "device_id", ts.DeviceID, "remote", addr)

	// 通知就绪
	ts.ReadyOnce.Do(func() {
		close(ts.ReadyCh)
	})
}

// SendAudioData 发送音频数据到通道
func (ts *TalkSession) SendAudioData(data []byte) error {
	ts.mu.Lock()
	if ts.stopped {
		ts.mu.Unlock()
		return fmt.Errorf("session stopped")
	}
	ts.mu.Unlock()

	select {
	case ts.AudioChan <- data:
		return nil
	default:
		return fmt.Errorf("audio channel full")
	}
}

// startAudioSender 启动音频发送器
func (ts *TalkSession) startAudioSender() {
	slog.Info("[Talk] audio sender started", "device_id", ts.DeviceID)

	// 等待就绪
	<-ts.ReadyCh

	for audioData := range ts.AudioChan {
		if err := ts.sendAudioData(audioData); err != nil {
			slog.Error("[Talk] send audio failed", "error", err)
		}
	}

	slog.Info("[Talk] audio sender stopped", "device_id", ts.DeviceID)
}

// sendAudioData 发送音频数据
func (ts *TalkSession) sendAudioData(data []byte) error {
	ts.mu.Lock()
	if ts.stopped {
		ts.mu.Unlock()
		return fmt.Errorf("session stopped")
	}

	// 构建 RTP 包（需要锁保护 SeqNum/Timestamp）
	packet, err := ts.buildRTPPacket(data)
	if err != nil {
		ts.mu.Unlock()
		return fmt.Errorf("build RTP packet: %w", err)
	}

	// 复制连接引用，避免在 I/O 期间持锁
	mode := ts.TransportMode
	udpConn := ts.RTPConn
	udpIP := ts.RTPPeerIP
	udpPort := ts.RTPPeerPort
	tcpConn := ts.TCPConn
	ts.mu.Unlock()

	// 根据传输模式发送（不持锁）
	switch mode {
	case TransportUDP:
		if udpConn == nil || udpIP == "" || udpPort == 0 {
			return fmt.Errorf("UDP connection not ready")
		}
		remoteAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(udpIP, fmt.Sprintf("%d", udpPort)))
		if err != nil {
			return fmt.Errorf("resolve remote addr: %w", err)
		}
		_, err = udpConn.WriteToUDP(packet, remoteAddr)
		return err

	case TransportTCPPassive, TransportTCPActive:
		if tcpConn == nil {
			return fmt.Errorf("TCP connection not ready")
		}
		// GB28181 TCP传输格式：2字节长度前缀 + RTP包
		length := make([]byte, 2)
		binary.BigEndian.PutUint16(length, uint16(len(packet)))
		if _, err := tcpConn.Write(length); err != nil {
			return err
		}
		_, err := tcpConn.Write(packet)
		return err

	default:
		return fmt.Errorf("unsupported transport mode: %d", mode)
	}
}

// buildRTPPacket 构建 RTP 包
func (ts *TalkSession) buildRTPPacket(data []byte) ([]byte, error) {
	ts.SeqNum++
	ts.Timestamp += RTPSamplesPer20ms

	packet := make([]byte, 12+len(data))
	packet[0] = RTPVersion2
	packet[1] = ts.PayloadType
	binary.BigEndian.PutUint16(packet[2:4], ts.SeqNum)
	binary.BigEndian.PutUint32(packet[4:8], ts.Timestamp)

	ssrc, err := parseSSRC(ts.SSRC)
	if err != nil {
		return nil, fmt.Errorf("parse SSRC %q: %w", ts.SSRC, err)
	}
	binary.BigEndian.PutUint32(packet[8:12], ssrc)

	copy(packet[12:], data)
	return packet, nil
}

// parseSSRC 解析 SSRC 字符串为 uint32
func parseSSRC(s string) (uint32, error) {
	var ssrc uint32
	n, err := fmt.Sscanf(s, "%d", &ssrc)
	if err != nil {
		return 0, fmt.Errorf("scan SSRC %q: %w", s, err)
	}
	if n != 1 {
		return 0, fmt.Errorf("scan SSRC %q: parsed %d values, want 1", s, n)
	}
	return ssrc, nil
}

// Stop 停止对讲会话
func (ts *TalkSession) Stop() error {
	var err error
	ts.StopOnce.Do(func() {
		ts.mu.Lock()
		ts.stopped = true
		close(ts.AudioChan)
		ts.AudioChan = nil
		ts.mu.Unlock()

		if ts.RTPConn != nil {
			err = errors.Join(err, ts.RTPConn.Close())
		}
		if ts.TCPConn != nil {
			err = errors.Join(err, ts.TCPConn.Close())
		}
		if ts.TCPListener != nil {
			err = errors.Join(err, ts.TCPListener.Close())
		}

		slog.Info("[Talk] session stopped", "device_id", ts.DeviceID, "channel_id", ts.ChannelID)
	})
	return err
}
