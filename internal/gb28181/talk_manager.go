package gb28181

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

const BroadcastNotifyTimeout = 10 * time.Second

// TalkManager 管理所有对讲会话
type TalkManager struct {
	mu         sync.RWMutex
	sessions   map[string]*TalkSession // key: deviceID_channelID
	client     *sipgo.Client
	cfg        *Config
	deviceStore *DeviceStore
}

// NewTalkManager 创建新的 TalkManager
func NewTalkManager(client *sipgo.Client, cfg *Config, store *DeviceStore) *TalkManager {
	return &TalkManager{
		sessions:   make(map[string]*TalkSession),
		client:     client,
		cfg:        cfg,
		deviceStore: store,
	}
}

// talkKey 生成会话 key
func talkKey(deviceID, channelID string) string {
	return deviceID + "_" + channelID
}

// StartTalk 启动对讲会话
func (tm *TalkManager) StartTalk(deviceID, channelID string, mode TransportMode, store *DeviceStore) (*TalkSession, error) {
	key := talkKey(deviceID, channelID)

	// 检查设备是否在线
	dev, ok := store.Load(deviceID)
	if !ok || !dev.IsOnline {
		return nil, ErrDeviceOffline
	}

	_, ok = dev.GetChannel(channelID)
	if !ok {
		return nil, ErrChannelNotExist
	}

	// 创建会话
	session := NewTalkSession(deviceID, channelID, mode, tm.cfg, tm.client)

	// 根据传输模式准备网络连接
	switch mode {
	case TransportUDP:
		if err := session.prepareUDPConn(); err != nil {
			return nil, fmt.Errorf("prepare UDP: %w", err)
		}
	case TransportTCPPassive:
		if err := session.prepareTCPListener(); err != nil {
			return nil, fmt.Errorf("prepare TCP listener: %w", err)
		}
	case TransportTCPActive:
		// TCP 主动模式在收到 INVITE 后才连接
		session.RTPPort = 0 // 端口在连接时分配
	}

	// 生成 SSRC
	session.SSRC = fmt.Sprintf("%010d", randInt64(1000000000, 9999999999))

	// 存储会话（原子性检查并插入，防止竞态条件）
	tm.mu.Lock()
	if existing, ok := tm.sessions[key]; ok {
		tm.mu.Unlock()
		return existing, nil
	}
	tm.sessions[key] = session
	tm.mu.Unlock()

	// 发送 Broadcast Notify
	if err := tm.sendBroadcastNotify(session, store); err != nil {
		tm.RemoveSession(key)
		return nil, fmt.Errorf("send broadcast notify: %w", err)
	}

	slog.Info("[Talk] session started", "device_id", deviceID, "channel_id", channelID, "mode", mode, "port", session.RTPPort)
	return session, nil
}

// StopTalk 停止对讲会话
func (tm *TalkManager) StopTalk(deviceID, channelID string) error {
	key := talkKey(deviceID, channelID)
	tm.mu.Lock()
	session, ok := tm.sessions[key]
	if !ok {
		tm.mu.Unlock()
		return nil
	}
	delete(tm.sessions, key)
	tm.mu.Unlock()

	return session.Stop()
}

// RemoveSession 移除会话
func (tm *TalkManager) RemoveSession(key string) {
	tm.mu.Lock()
	delete(tm.sessions, key)
	tm.mu.Unlock()
}

// GetSession 获取会话
func (tm *TalkManager) GetSession(deviceID, channelID string) (*TalkSession, bool) {
	key := talkKey(deviceID, channelID)
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	session, ok := tm.sessions[key]
	return session, ok
}

// OnInvite 处理设备 INVITE 请求
func (tm *TalkManager) OnInvite(req *sip.Request, tx sip.ServerTransaction) {
	from := req.From()
	if from == nil || from.Address.User == "" {
		slog.Error("[Talk] OnInvite: invalid from header")
		return
	}

	to := req.To()
	if to == nil || to.Address.User == "" {
		slog.Error("[Talk] OnInvite: invalid to header")
		return
	}

	deviceID := from.Address.User
	channelID := to.Address.User
	sdpBody := string(req.Body())

	slog.Info("[Talk] OnInvite received", "device_id", deviceID, "channel_id", channelID, "sdp_len", len(sdpBody))
	slog.Info("[Talk] OnInvite device SDP", "sdp", sdpBody)

	// 解析设备 SDP
	peerIP, peerPort, isTCP, setupActive, deviceSSRC, payloadType, err := parseTalkSDP(sdpBody)
	if err != nil {
		slog.Error("[Talk] OnInvite: parse SDP failed", "error", err)
		tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
		return
	}

	// 查找匹配的会话（同时匹配 deviceID 和 channelID）
	// 1. 先用 deviceID + channelID 精确匹配
	// 2. 如果找不到，只用 deviceID 匹配
	// 3. 如果还是找不到，用 channelID 查找设备，再用设备ID查找session
	tm.mu.RLock()
	var session *TalkSession
	for _, s := range tm.sessions {
		if s.DeviceID == deviceID && s.ChannelID == channelID {
			session = s
			break
		}
	}
	if session == nil {
		// Fallback: 只匹配 deviceID
		for _, s := range tm.sessions {
			if s.DeviceID == deviceID {
				session = s
				slog.Info("[Talk] OnInvite: matched by deviceID only",
					"session_channel_id", s.ChannelID,
					"request_channel_id", channelID)
				break
			}
		}
	}
	// Fallback: fromUser might be channelID, find device by channelID
	if session == nil && tm.deviceStore != nil {
		tm.deviceStore.RangeDevices(func(devID string, dev *Device) bool {
			if ch, ok := dev.GetChannel(deviceID); ok {
				// Found device with this channelID, try to find session by deviceID
				for _, s := range tm.sessions {
					if s.DeviceID == devID {
						session = s
						slog.Info("[Talk] OnInvite: found session by channelID lookup",
							"device_id", devID, "channel_id", ch.ChannelID)
						return false
					}
				}
			}
			return true
		})
	}
	tm.mu.RUnlock()

	if session == nil {
		slog.Warn("[Talk] OnInvite: no session found", "device_id", deviceID, "channel_id", channelID)
		tx.Respond(sip.NewResponseFromRequest(req, 404, "Not Found", nil))
		return
	}

	// 更新会话信息（持锁防止数据竞争）
	session.mu.Lock()
	session.RTPPeerIP = peerIP
	session.RTPPeerPort = peerPort
	if deviceSSRC != "" {
		session.SSRC = deviceSSRC
	}
	if payloadType >= 0 {
		session.PayloadType = uint8(payloadType)
	}

	// 根据传输模式准备连接
	// 注意：a=setup:active 表示设备是主动方，NVR应该被动等待连接
	//       a=setup:passive 表示设备是被动方，NVR应该主动连接设备
	transportMode := TransportUDP
	if isTCP {
		if setupActive {
			// 设备主动连接NVR -> NVR使用TCP被动模式（监听等待连接）
			transportMode = TransportTCPPassive
			// 如果session之前是UDP模式，需要启动TCP监听
			if session.TCPListener == nil {
				if err := session.prepareTCPListener(); err != nil {
					slog.Error("[Talk] OnInvite: prepare TCP listener failed", "error", err)
					tx.Respond(sip.NewResponseFromRequest(req, 500, "Internal Server Error", nil))
					return
				}
			}
		} else {
			// 设备被动等待NVR连接 -> NVR使用TCP主动模式（连接设备）
			transportMode = TransportTCPActive
			go session.connectTCP(peerIP, peerPort)
		}
	}
	session.TransportMode = transportMode
	session.mu.Unlock()

	// 构建 200 OK 响应
	sdpIP := tm.cfg.MediaIP
	if sdpIP == "" {
		sdpIP = tm.cfg.Host
	}

	sdp := buildTalkSDP(tm.cfg.ID, sdpIP, session.RTPPort, transportMode, session.SSRC)
	slog.Info("[Talk] OnInvite: response SDP", "sdp", string(sdp))
	okResp := sip.NewResponseFromRequest(req, 200, "OK", nil)
	okResp.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	okResp.AppendHeader(sip.NewHeader("Contact", fmt.Sprintf("sip:%s@%s:%d", tm.cfg.ID, sdpIP, tm.cfg.Port)))
	okResp.SetBody(sdp)

	if err := tx.Respond(okResp); err != nil {
		slog.Error("[Talk] OnInvite: send 200 OK failed", "error", err)
		return
	}

	// 启动音频发送器
	go session.startAudioSender()

	// 根据传输模式启动连接
	if transportMode == TransportTCPPassive {
		// TCP被动模式：启动accept等待设备连接
		go session.acceptTCPConn()
	} else if transportMode == TransportUDP || transportMode == TransportTCPActive {
		// UDP或TCP主动模式：立即通知就绪
		session.ReadyOnce.Do(func() {
			close(session.ReadyCh)
		})
	}

	slog.Info("[Talk] OnInvite: 200 OK sent", "device_id", deviceID, "port", session.RTPPort, "mode", transportMode)
}

// OnAck 处理设备 ACK 请求
func (tm *TalkManager) OnAck(req *sip.Request, tx sip.ServerTransaction) {
	from := req.From()
	if from == nil {
		return
	}
	deviceID := from.Address.User
	slog.Debug("[Talk] OnAck received", "device_id", deviceID)
}

// OnBye 处理设备 BYE 请求
func (tm *TalkManager) OnBye(req *sip.Request, tx sip.ServerTransaction) {
	from := req.From()
	if from == nil {
		return
	}
	deviceID := from.Address.User

	// 收集需要停止的会话，避免在持锁状态下调用 goroutine
	tm.mu.Lock()
	var stoppedSession *TalkSession
	for key, session := range tm.sessions {
		if session.DeviceID == deviceID {
			delete(tm.sessions, key)
			stoppedSession = session
			break
		}
	}
	tm.mu.Unlock()

	if stoppedSession != nil {
		stoppedSession.Stop()
	}

	tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	slog.Info("[Talk] OnBye: session stopped", "device_id", deviceID)
}

// sendBroadcastNotify 发送 Broadcast Notify
func (tm *TalkManager) sendBroadcastNotify(session *TalkSession, store *DeviceStore) error {
	dev, ok := store.Load(session.DeviceID)
	if !ok {
		return ErrDeviceNotExist
	}

	uri := sip.Uri{
		Scheme: "sip",
		User:   session.ChannelID,
		Host:   getHost(dev.Address),
		Port:   getPort(dev.Address),
	}

	req := sip.NewRequest(sip.MESSAGE, uri)
	req.AppendHeader(sip.NewHeader("Content-Type", "Application/MANSCDP+xml"))

	sourceID := tm.cfg.ID
	if sourceID == "" {
		sourceID = session.DeviceID
	}

	sn := randInt(100000, 999999)
	body := fmt.Sprintf(`<?xml version="1.0" encoding="GB2312"?>
<Notify>
<CmdType>Broadcast</CmdType>
<SN>%d</SN>
<SourceID>%s</SourceID>
<TargetID>%s</TargetID>
</Notify>`, sn, sourceID, session.ChannelID)
	req.SetBody([]byte(body))

	ctx, cancel := context.WithTimeout(context.Background(), BroadcastNotifyTimeout)
	defer cancel()

	_, err := tm.client.Do(ctx, req)
	return err
}

// StopAll 停止所有会话
func (tm *TalkManager) StopAll() {
	tm.mu.Lock()
	sessions := make([]*TalkSession, 0, len(tm.sessions))
	for _, s := range tm.sessions {
		sessions = append(sessions, s)
	}
	tm.sessions = make(map[string]*TalkSession)
	tm.mu.Unlock()

	for _, s := range sessions {
		s.Stop()
	}
}
