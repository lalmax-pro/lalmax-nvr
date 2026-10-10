package gb28181

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// Server is the GB28181 SIP signaling server.
type Server struct {
	ua        *sipgo.UserAgent
	srv       *sipgo.Server
	client    *sipgo.Client
	gb        *GB28181API
	cfg       *Config
	db        storage.GB28181Repository
	store     *DeviceStore
	cancel    context.CancelFunc
	platforms *PlatformManager
	broadcast *BroadcastManager
	talk      *TalkManager
	alarm     *AlarmManager
	download  *DownloadManager
	hub       *WSHub
}

// NewServer creates and starts a GB28181 SIP server.
func NewServer(cfg *Config, mediaEngine media.Engine, db storage.GB28181Repository) (*Server, func()) {
	hub := NewWSHub()
	store := NewDeviceStore(db, hub)

	// Load devices from database
	if err := store.LoadFromDB(); err != nil {
		slog.Error("failed to load GB28181 devices from database", "error", err)
	}

	sipHost := cfg.Host
	if sipHost == "" {
		sipHost = cfg.MediaIP
	}

	ua, err := sipgo.NewUA(
		sipgo.WithUserAgent("lalmax-nvr"),
		sipgo.WithUserAgentHostname(sipHost),
	)
	if err != nil {
		slog.Error("failed to create SIP UA", "error", err)
		return nil, func() {}
	}

	srv, err := sipgo.NewServer(ua)
	if err != nil {
		slog.Error("failed to create SIP server", "error", err)
		ua.Close()
		return nil, func() {}
	}

	client, err := sipgo.NewClient(ua, sipgo.WithClientHostname(sipHost))
	if err != nil {
		slog.Error("failed to create SIP client", "error", err)
		srv.Close()
		ua.Close()
		return nil, func() {}
	}

	s := &Server{
		ua:     ua,
		srv:    srv,
		client: client,
		cfg:    cfg,
		db:     db,
		store:  store,
		hub:    hub,
	}

	api := NewGB28181API(cfg, store, client, mediaEngine)
	api.svr = s
	s.gb = api

	// Initialize managers
	s.platforms = NewPlatformManager(client, cfg.Host, cfg.MediaIP, cfg.ID, cfg.Password, db)
	s.broadcast = NewBroadcastManager(client, cfg, store)
	s.talk = NewTalkManager(client, cfg, store)
	s.alarm = NewAlarmManager(client, cfg, db)
	s.download = NewDownloadManager(client, cfg, mediaEngine, db, "")

	// Register SIP handlers
	srv.OnRegister(api.handlerRegister)
	srv.OnMessage(api.handlerMessage)
	srv.OnNotify(api.handlerNotify)
	srv.OnBye(api.handlerBye)
	srv.OnInvite(s.handlerInvite)
	srv.OnAck(s.handlerAck)

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	go func() {
		addr := fmt.Sprintf("0.0.0.0:%d", cfg.Port)
		slog.Info("GB28181 SIP UDP server starting", "addr", addr)
		if err := srv.ListenAndServe(ctx, "udp", addr); err != nil {
			slog.Error("SIP UDP listener error", "error", err)
		}
	}()
	go func() {
		addr := fmt.Sprintf("0.0.0.0:%d", cfg.Port)
		slog.Info("GB28181 SIP TCP server starting", "addr", addr)
		if err := srv.ListenAndServe(ctx, "tcp", addr); err != nil {
			slog.Error("SIP TCP listener error", "error", err)
		} else {
			slog.Info("GB28181 SIP TCP server stopped")
		}
	}()
	go s.startTickerCheck()
	go s.hub.Run()
	go s.startChannelMissingScan()

	// Load upstream platforms
	go func() {
		if err := s.platforms.LoadPlatforms(); err != nil {
			slog.Error("failed to load GB28181 platforms", "error", err)
		}
	}()

	return s, s.Stop
}

// startTickerCheck periodically checks for offline devices via heartbeat timeout.
func (s *Server) startTickerCheck() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		s.store.RangeDevices(func(key string, dev *Device) bool {
			if !dev.IsOnline {
				return true
			}
			if len(key) < 18 {
				return true
			}

			interval := dev.keepaliveInterval
			if interval == 0 {
				interval = 60
			}
			timeoutCount := dev.keepaliveTimeout
			if timeoutCount == 0 {
				timeoutCount = 3
			}
			timeout := time.Duration(interval) * time.Duration(timeoutCount) * time.Second

			if dev.LastKeepaliveAt.IsZero() {
				if !dev.LastRegisterAt.IsZero() && now.Sub(dev.LastRegisterAt) >= timeout {
					s.gb.logout(key)
				}
				return true
			}

			if sub := now.Sub(dev.LastKeepaliveAt); sub >= timeout {
				slog.Info("device offline detected", "device_id", key, "elapsed", sub)
				s.gb.logout(key)
			}
			return true
		})
	}
}

// startChannelMissingScan starts a goroutine that periodically scans for missing channels.
func (s *Server) startChannelMissingScan() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.scanMissingChannels()
	}
}

// scanMissingChannels checks for channels with high missing_count and marks them offline.
func (s *Server) scanMissingChannels() {
	ctx := context.Background()

	// 查询 missing_count >= 3 的通道
	channels, err := s.db.ListMissingChannels(ctx, 3)
	if err != nil {
		slog.Error("failed to list missing channels", "error", err)
		return
	}

	for _, ch := range channels {
		// 标记为离线
		if err := s.db.UpdateChannelStatus(ctx,
			ch.DeviceID, ch.ChannelID, "offline"); err != nil {
			slog.Error("failed to update channel status",
				"device_id", ch.DeviceID,
				"channel_id", ch.ChannelID,
				"error", err)
			continue
		}

		// 从内存中删除
		if dev, ok := s.store.Load(ch.DeviceID); ok {
			dev.Channels.Delete(ch.ChannelID)
		}

		// 广播事件
		s.hub.Broadcast(Event{
			Type: EventChannelOffline,
			Data: map[string]interface{}{
				"device_id":  ch.DeviceID,
				"channel_id": ch.ChannelID,
			},
		})

		slog.Info("channel marked offline due to missing",
			"device_id", ch.DeviceID,
			"channel_id", ch.ChannelID,
			"missing_count", ch.MissingCount)
	}
}

// QueryCatalog queries the catalog of a device.
func (s *Server) QueryCatalog(deviceID string) error {
	return s.gb.QueryCatalog(deviceID)
}

// Play starts a GB28181 play session.
func (s *Server) Play(in *PlayInput) (string, error) {
	return s.gb.Play(in)
}

// StopPlay stops a GB28181 play session.
func (s *Server) StopPlay(in *StopPlayInput) error {
	return s.gb.StopPlay(in)
}

// PTZControl sends a PTZ control command.
func (s *Server) PTZControl(deviceID, channelID, ptzCmd string) error {
	return s.gb.PTZControl(deviceID, channelID, ptzCmd)
}

// PTZPositionControl sends a precise position control command.
// horizontalAngle: 0-360 degrees, verticalAngle: 0-90 degrees, zoomLevel: 1-100x
func (s *Server) PTZPositionControl(deviceID, channelID string, horizontalAngle, verticalAngle, zoomLevel float64) error {
	return s.gb.PTZPositionControl(deviceID, channelID, horizontalAngle, verticalAngle, zoomLevel)
}

// QueryPTZPosition queries the current PTZ position.
func (s *Server) QueryPTZPosition(deviceID, channelID string) error {
	return s.gb.QueryPTZPosition(deviceID, channelID)
}

// SetPreset sets a preset position.
func (s *Server) SetPreset(deviceID, channelID string, presetID int) error {
	return s.gb.SetPreset(deviceID, channelID, presetID)
}

// CallPreset calls a preset position.
func (s *Server) CallPreset(deviceID, channelID string, presetID int) error {
	return s.gb.CallPreset(deviceID, channelID, presetID)
}

// DeletePreset deletes a preset position.
func (s *Server) DeletePreset(deviceID, channelID string, presetID int) error {
	return s.gb.DeletePreset(deviceID, channelID, presetID)
}

// CruiseAddPoint adds a point to a cruise.
func (s *Server) CruiseAddPoint(deviceID, channelID string, cruiseID, presetID int) error {
	return s.gb.CruiseAddPoint(deviceID, channelID, cruiseID, presetID)
}

// CruiseDeletePoint deletes a point from a cruise.
func (s *Server) CruiseDeletePoint(deviceID, channelID string, cruiseID, presetID int) error {
	return s.gb.CruiseDeletePoint(deviceID, channelID, cruiseID, presetID)
}

// CruiseSetSpeed sets the cruise speed.
func (s *Server) CruiseSetSpeed(deviceID, channelID string, cruiseID, speed int) error {
	return s.gb.CruiseSetSpeed(deviceID, channelID, cruiseID, speed)
}

// CruiseStart starts a cruise.
func (s *Server) CruiseStart(deviceID, channelID string, cruiseID int) error {
	return s.gb.CruiseStart(deviceID, channelID, cruiseID)
}

// CruiseStop stops a cruise.
func (s *Server) CruiseStop(deviceID, channelID string, cruiseID int) error {
	return s.gb.CruiseStop(deviceID, channelID, cruiseID)
}

// ScanSetLeft sets the left boundary of a scan.
func (s *Server) ScanSetLeft(deviceID, channelID string, scanID int) error {
	return s.gb.ScanSetLeft(deviceID, channelID, scanID)
}

// ScanSetRight sets the right boundary of a scan.
func (s *Server) ScanSetRight(deviceID, channelID string, scanID int) error {
	return s.gb.ScanSetRight(deviceID, channelID, scanID)
}

// ScanSetSpeed sets the scan speed.
func (s *Server) ScanSetSpeed(deviceID, channelID string, scanID, speed int) error {
	return s.gb.ScanSetSpeed(deviceID, channelID, scanID, speed)
}

// ScanStart starts a scan.
func (s *Server) ScanStart(deviceID, channelID string, scanID int) error {
	return s.gb.ScanStart(deviceID, channelID, scanID)
}

// ScanStop stops a scan.
func (s *Server) ScanStop(deviceID, channelID string, scanID int) error {
	return s.gb.ScanStop(deviceID, channelID, scanID)
}

// QueryRecordInfo queries a device for its recording list.
func (s *Server) QueryRecordInfo(deviceID, channelID string, startTime, endTime time.Time) (*Records, error) {
	return s.gb.QueryRecordInfo(deviceID, channelID, startTime, endTime)
}

// Playback starts a historical playback session.
func (s *Server) Playback(in *PlaybackInput) (string, error) {
	return s.gb.Playback(in)
}

// PlaySpeed changes the playback speed.
func (s *Server) PlaySpeed(in *PlaySpeedInput) error {
	return s.gb.PlaySpeed(in)
}

// PlaySeek seeks to a position in playback.
func (s *Server) PlaySeek(in *PlaySeekInput) error {
	return s.gb.PlaySeek(in)
}

// PlayPause pauses playback.
func (s *Server) PlayPause(in *PlayPauseInput) error {
	return s.gb.PlayPause(in)
}

// PlayResume resumes playback.
func (s *Server) PlayResume(in *PlayPauseInput) error {
	return s.gb.PlayResume(in)
}

// IsStreamPlaying reports whether the given stream ID has an active GB28181 play session.
func (s *Server) IsStreamPlaying(streamID string) bool {
	return globalStreams.IsStreamPlaying(streamID)
}

// GetPlatforms returns the platform manager.
func (s *Server) GetPlatforms() *PlatformManager {
	return s.platforms
}

// GetBroadcastManager returns the broadcast manager.
func (s *Server) GetBroadcastManager() *BroadcastManager {
	return s.broadcast
}

// GetTalkManager returns the talk manager.
func (s *Server) GetTalkManager() *TalkManager {
	return s.talk
}

// GetAlarmManager returns the alarm manager.
func (s *Server) GetAlarmManager() *AlarmManager {
	return s.alarm
}

// Snapshot sends a snapshot command to the device.
func (s *Server) Snapshot(deviceID, channelID string) error {
	return s.gb.Snapshot(deviceID, channelID)
}

// DeviceReset sends a device reset command.
func (s *Server) DeviceReset(deviceID, channelID string) error {
	return s.gb.DeviceReset(deviceID, channelID)
}

// RecordControl sends a record control command.
func (s *Server) RecordControl(deviceID, channelID, recordCmd string) error {
	return s.gb.RecordControl(deviceID, channelID, recordCmd)
}

// SetHomePosition sets the home position for a device.
func (s *Server) SetHomePosition(deviceID, channelID string, enabled, resetTime, presetIndex int) error {
	return s.gb.SetHomePosition(deviceID, channelID, enabled, resetTime, presetIndex)
}

// DeviceConfigQuery queries device configuration.
func (s *Server) DeviceConfigQuery(deviceID, channelID string) error {
	return s.gb.DeviceConfigQuery(deviceID, channelID)
}

// DeviceStatusQuery queries device status.
func (s *Server) DeviceStatusQuery(deviceID, channelID string) error {
	return s.gb.DeviceStatusQuery(deviceID, channelID)
}

// DeviceInfoQuery queries device information.
func (s *Server) DeviceInfoQuery(deviceID, channelID string) error {
	return s.gb.DeviceInfoQuery(deviceID, channelID)
}
func (s *Server) GetDownloadManager() *DownloadManager {
	return s.download
}

// GetDeviceStore returns the device store.
func (s *Server) GetDeviceStore() *DeviceStore {
	return s.store
}

// GetConfig returns the GB28181 configuration.
func (s *Server) GetConfig() *Config {
	return s.cfg
}

// handlerInvite handles incoming SIP INVITE requests (from devices for broadcast or from upstream platforms for playback).
func (s *Server) handlerInvite(req *sip.Request, tx sip.ServerTransaction) {
	from := req.From()
	if from == nil || from.Address.User == "" {
		slog.Error("[SIP] INVITE: invalid from header")
		tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
		return
	}

	fromUser := from.Address.User
	sdpBody := string(req.Body())

	slog.Info("[SIP] INVITE received", "from", fromUser, "sdp_len", len(sdpBody))

	// Check if this is from an upstream platform (by matching ServerGBID)
	isUpstream := false
	s.platforms.mu.RLock()
	for _, p := range s.platforms.platforms {
		if p.Config.ServerGBID == fromUser || p.Config.DeviceGBID == fromUser {
			isUpstream = true
			break
		}
	}
	s.platforms.mu.RUnlock()

	if isUpstream {
		// This is from upstream platform - handle as platform INVITE
		slog.Info("[SIP] INVITE from upstream platform", "from", fromUser)
		// Forward to platform handler
		tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
		return
	}

	// Check for pending talk sessions first
	if from != nil {
		to := req.To()
		if to != nil {
			if _, ok := s.talk.GetSession(from.Address.User, to.Address.User); ok {
				s.talk.OnInvite(req, tx)
				return
			}
		}
	}

	// Check for pending broadcast sessions
	if _, ok := s.broadcast.GetSession(fromUser, ""); ok {
		s.broadcast.OnInvite(req, tx)
		return
	}

	// Device-initiated INVITE (e.g., for talk/broadcast response)
	// Parse SDP to get channel ID from o= line
	channelID := parseChannelIDFromSDP(sdpBody)
	if channelID == "" {
		channelID = fromUser // Fallback to using fromUser as channelID
	}

	slog.Info("[SIP] INVITE device-initiated", "from", fromUser, "channel_id", channelID)

	// Find device and channel from registered devices
	var foundDevice *Device
	var foundChannel *Channel
	s.store.RangeDevices(func(deviceID string, dev *Device) bool {
		// Method 1: Find by deviceID match (fromUser is deviceID)
		if deviceID == fromUser {
			// Try to find the specific channel
			if ch, ok := dev.GetChannel(channelID); ok {
				foundDevice = dev
				foundChannel = ch
				return false
			}
			// Fallback: use first channel
			dev.Channels.Range(func(k, v any) bool {
				foundDevice = dev
				foundChannel = v.(*Channel)
				return false
			})
			return false
		}
		// Method 2: fromUser might be a channelID
		if ch, ok := dev.GetChannel(fromUser); ok {
			foundDevice = dev
			foundChannel = ch
			return false
		}
		return true
	})

	if foundDevice == nil || foundChannel == nil {
		slog.Warn("[SIP] INVITE: device/channel not found",
			"from", fromUser,
			"channel_id", channelID,
			"tip", "Check if device registered and catalog queried")
		tx.Respond(sip.NewResponseFromRequest(req, 404, "Not Found", nil))
		return
	}

	slog.Info("[SIP] INVITE: found device and channel",
		"device_id", foundDevice.DeviceID,
		"channel_id", foundChannel.ChannelID,
		"is_online", foundDevice.IsOnline)

	// Create a talk session for device-initiated INVITE
	_, err := s.talk.StartTalk(foundDevice.DeviceID, foundChannel.ChannelID, TransportUDP, s.store)
	if err != nil {
		slog.Error("[SIP] INVITE: failed to start talk session", "error", err)
		tx.Respond(sip.NewResponseFromRequest(req, 500, "Internal Server Error", nil))
		return
	}

	// Handle the INVITE with the newly created session
	s.talk.OnInvite(req, tx)
}

// handlerAck handles incoming SIP ACK requests.
func (s *Server) handlerAck(req *sip.Request, tx sip.ServerTransaction) {
	from := req.From()
	if from == nil {
		return
	}

	fromUser := from.Address.User

	// Check if this is for a broadcast session
	s.broadcast.mu.RLock()
	for _, bs := range s.broadcast.sessions {
		if bs.DeviceID == fromUser {
			s.broadcast.mu.RUnlock()
			s.broadcast.OnAck(req, tx)
			return
		}
	}
	s.broadcast.mu.RUnlock()

	slog.Debug("[SIP] ACK received", "from", fromUser)
}

// ListDevices returns all registered devices with their channels.
func (s *Server) ListDevices() []map[string]interface{} {
	var result []map[string]interface{}
	s.store.RangeDevices(func(deviceID string, dev *Device) bool {
		channels := make([]map[string]interface{}, 0)
		dev.Channels.Range(func(k, v any) bool {
			ch := v.(*Channel)
			chInfo := map[string]interface{}{
				"channel_id": ch.ChannelID,
				"name":       ch.Name,
				"is_playing": globalStreams.isPlaying(deviceID, ch.ChannelID),
				"stream_id":  StreamID(deviceID, ch.ChannelID),

				// 2016标准字段
				"manufacturer": ch.Manufacturer,
				"model":        ch.Model,
				"status":       ch.Status,
				"ptz_type":     ch.PTZType,
				"longitude":    ch.Longitude,
				"latitude":     ch.Latitude,

				// 2022标准新增字段
				"stream_number_list": ch.StreamNumberList,
				"encode_type":        ch.EncodeType,
			}
			channels = append(channels, chInfo)
			return true
		})

		// Get device info from database
		deviceInfo := map[string]interface{}{
			"device_id":  deviceID,
			"is_online":  dev.IsOnline,
			"address":    dev.Address,
			"gb_version": string(dev.GBVersion),
			"channels":   channels,
		}

		// Try to get additional info from database
		if dbDev, err := s.db.GetGB28181Device(context.Background(), deviceID); err == nil && dbDev != nil {
			deviceInfo["name"] = dbDev.Name
			deviceInfo["manufacturer"] = dbDev.Manufacturer
			deviceInfo["model"] = dbDev.Model
			deviceInfo["firmware"] = dbDev.Firmware
			if dbDev.GBVersion != "" {
				deviceInfo["gb_version"] = dbDev.GBVersion
			}
			if dbDev.LastKeepaliveAt != nil {
				deviceInfo["last_keepalive_at"] = dbDev.LastKeepaliveAt
			}
			if dbDev.LastRegisterAt != nil {
				deviceInfo["last_register_at"] = dbDev.LastRegisterAt
			}
		}

		result = append(result, deviceInfo)
		return true
	})
	return result
}

// Stop stops the SIP server.
func (s *Server) Stop() {
	slog.Info("[SIP] stopping GB28181 server")
	if s.cancel != nil {
		s.cancel()
	}
	// Stop platform manager
	if s.platforms != nil {
		s.platforms.mu.Lock()
		for _, p := range s.platforms.platforms {
			p.Stop()
		}
		s.platforms.mu.Unlock()
	}
	// Stop talk manager
	if s.talk != nil {
		s.talk.StopAll()
	}
	// Small delay to allow listeners to close
	time.Sleep(100 * time.Millisecond)
	if s.client != nil {
		s.client.Close()
	}
	if s.srv != nil {
		s.srv.Close()
	}
	if s.ua != nil {
		s.ua.Close()
	}
	slog.Info("[SIP] GB28181 server stopped")
}

// resolveHost resolves a hostname to IP address.
func resolveHost(host string) string {
	if host == "" {
		return ""
	}
	if net.ParseIP(host) != nil {
		return host
	}
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		return host
	}
	return addrs[0]
}

// parseChannelIDFromSDP extracts channel ID from SDP o= line.
// Example: o=34020000002000000001 0 0 IN IP4 192.168.31.215
// Returns "34020000002000000001"
func parseChannelIDFromSDP(sdpBody string) string {
	lines := strings.Split(sdpBody, "\r\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "o=") {
			// Parse o=<channel-id> 0 0 IN IP4 x.x.x.x
			parts := strings.Fields(strings.TrimPrefix(line, "o="))
			if len(parts) > 0 {
				channelID := parts[0]
				// Validate it looks like a GB28181 ID (20 digits)
				if len(channelID) >= 18 && len(channelID) <= 20 {
					isDigit := true
					for _, c := range channelID {
						if c < '0' || c > '9' {
							isDigit = false
							break
						}
					}
					if isDigit {
						return channelID
					}
				}
			}
		}
	}
	return ""
}
