package gb28181

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
)

// PlayInput contains parameters for starting a GB28181 play session.
type PlayInput struct {
	DeviceID     string
	ChannelID    string
	StreamMode   int8   // 0=UDP, 1=TCP passive, 2=TCP active
	InternalID   string // internal stream ID for lalmax
	StreamNumber int    // 码流编号: 0=主码流, 1=子码流 (GB28181-2022)
}

// PlaybackInput contains parameters for starting a GB28181 playback (historical) session.
type PlaybackInput struct {
	DeviceID   string
	ChannelID  string
	StreamMode int8   // 0=UDP, 1=TCP passive, 2=TCP active
	InternalID string // internal stream ID for lalmax
	StartTime  time.Time
	EndTime    time.Time
}

// InviteResult contains the result of a SIP INVITE transaction.
type InviteResult struct {
	SSRC      string
	CallID    string
	FromTag   string
	ToTag     string
	RemoteURI string
}

// PlaySpeedInput contains parameters for changing playback speed.
type PlaySpeedInput struct {
	DeviceID  string
	ChannelID string
	Speed     float32 // 0.5, 1, 2, 4
}

// PlaySeekInput contains parameters for seeking in playback.
type PlaySeekInput struct {
	DeviceID  string
	ChannelID string
	SeekTime  int64 // seconds from start
}

// PlayPauseInput contains parameters for pausing playback.
type PlayPauseInput struct {
	DeviceID  string
	ChannelID string
}

// StopPlayInput contains parameters for stopping a play session.
type StopPlayInput struct {
	DeviceID  string
	ChannelID string
}

// handlerBye handles incoming BYE requests from devices.
func (g *GB28181API) handlerBye(req *sip.Request, tx sip.ServerTransaction) {
	deviceID := req.From().Address.User
	callID := string(*req.CallID())
	source := req.Source()

	slog.Info("[SIP] BYE received",
		"device_id", deviceID,
		"call_id", callID,
		"source", source,
	)

	// Find and remove the stream by matching CallID
	g.streams.streams.Range(func(key, value any) bool {
		stream := value.(*Streams)
		if stream.DeviceID == deviceID {
			slog.Info("[SIP] BYE - stopping stream", "device_id", deviceID, "stream_key", key)
			g.streams.deleteStream(key.(string))
			if stream.SessionID != "" && g.mediaEngine != nil {
				_ = g.mediaEngine.StopRTPReceive(context.Background(), stream.SessionID)
			}
			return false
		}
		return true
	})

	tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	slog.Info("[SIP] BYE response sent", "device_id", deviceID)
}

// Play sends a GB28181 INVITE so the device can push PS/RTP to the platform.
func (g *GB28181API) Play(in *PlayInput) (string, error) {
	log := slog.With("device_id", in.DeviceID, "channel_id", in.ChannelID)
	log.Info("starting play")

	ch, ok := g.store.GetChannel(in.DeviceID, in.ChannelID)
	if !ok {
		return "", ErrChannelNotExist
	}

	ch.device.playMutex.Lock()
	defer ch.device.playMutex.Unlock()

	if !ch.device.IsOnline {
		return "", ErrDeviceOffline
	}

	// Stop existing stream if any
	key := "play:" + in.DeviceID + ":" + in.ChannelID
	if existing, ok := g.streams.loadStream(key); ok {
		log.Debug("stopping existing stream before re-play")
		g.stopPlay(existing)
	}

	// Step 1: Open RTP server on lalmax
	log.Debug("opening RTP server on lalmax")
	protocol := "udp"
	if in.StreamMode == 1 || in.StreamMode == 2 {
		protocol = "tcp"
	}

	rtpReq := media.StartRTPReceiveRequest{
		StreamID: in.InternalID,
		AppName:  "rtp",
		Protocol: protocol,
		Timeout:  10 * time.Second,
	}
	// Single port mode: use configured port
	if g.cfg.MediaPort > 0 {
		rtpReq.Port = g.cfg.MediaPort
	}

	rtpSession, err := g.mediaEngine.StartRTPReceive(context.Background(), rtpReq)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "-300") {
			log.Info("RTP stream already exists, retrying")
			time.Sleep(500 * time.Millisecond)
			rtpSession, err = g.mediaEngine.StartRTPReceive(context.Background(), rtpReq)
			if err != nil {
				return "", fmt.Errorf("open RTP server failed: %w", err)
			}
		} else {
			return "", fmt.Errorf("open RTP server failed: %w", err)
		}
	}

	// Step 2: Send SIP INVITE
	log.Debug("sending SIP INVITE", "port", rtpSession.Port)
	inviteResult, err := g.sipPlayInvite(ch, in, rtpSession.Port)
	if err != nil {
		log.Error("INVITE failed", "err", err)
		_ = g.mediaEngine.StopRTPReceive(context.Background(), rtpSession.SessionID)
		return "", err
	}

	// Store stream info with dialog
	stream := &Streams{
		DeviceID:  in.DeviceID,
		ChannelID: in.ChannelID,
		SSRC:      inviteResult.SSRC,
		SessionID: rtpSession.SessionID,
		CallID:    inviteResult.CallID,
		FromTag:   inviteResult.FromTag,
		ToTag:     inviteResult.ToTag,
		RemoteURI: inviteResult.RemoteURI,
	}
	g.streams.storeStream(key, stream)

	log.Info("play started", "ssrc", inviteResult.SSRC, "port", rtpSession.Port)
	return inviteResult.SSRC, nil
}

// Playback initiates a GB28181 INVITE for historical video playback.
func (g *GB28181API) Playback(in *PlaybackInput) (string, error) {
	log := slog.With("device_id", in.DeviceID, "channel_id", in.ChannelID,
		"start", in.StartTime.Format(time.RFC3339), "end", in.EndTime.Format(time.RFC3339))
	log.Info("starting playback")

	ch, ok := g.store.GetChannel(in.DeviceID, in.ChannelID)
	if !ok {
		return "", ErrChannelNotExist
	}

	ch.device.playMutex.Lock()
	defer ch.device.playMutex.Unlock()

	if !ch.device.IsOnline {
		return "", ErrDeviceOffline
	}

	// Stop existing stream if any
	key := "play:" + in.DeviceID + ":" + in.ChannelID
	if existing, ok := g.streams.loadStream(key); ok {
		log.Debug("stopping existing stream before playback")
		g.stopPlay(existing)
	}

	// Step 1: Open RTP server on lalmax
	protocol := "udp"
	if in.StreamMode == 1 || in.StreamMode == 2 {
		protocol = "tcp"
	}

	playbackReq := media.StartRTPReceiveRequest{
		StreamID: in.InternalID,
		AppName:  "rtp",
		Protocol: protocol,
		Timeout:  10 * time.Second,
	}
	// Single port mode: use configured port
	if g.cfg.MediaPort > 0 {
		playbackReq.Port = g.cfg.MediaPort
	}

	rtpSession, err := g.mediaEngine.StartRTPReceive(context.Background(), playbackReq)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "-300") {
			time.Sleep(500 * time.Millisecond)
			rtpSession, err = g.mediaEngine.StartRTPReceive(context.Background(), playbackReq)
			if err != nil {
				return "", fmt.Errorf("open RTP server failed: %w", err)
			}
		} else {
			return "", fmt.Errorf("open RTP server failed: %w", err)
		}
	}

	// Step 2: Send SIP INVITE with Playback mode
	inviteResult, err := g.sipPlaybackInvite(ch, in, rtpSession.Port)
	if err != nil {
		log.Error("Playback INVITE failed", "err", err)
		_ = g.mediaEngine.StopRTPReceive(context.Background(), rtpSession.SessionID)
		return "", err
	}

	stream := &Streams{
		DeviceID:  in.DeviceID,
		ChannelID: in.ChannelID,
		SSRC:      inviteResult.SSRC,
		SessionID: rtpSession.SessionID,
		CallID:    inviteResult.CallID,
		FromTag:   inviteResult.FromTag,
		ToTag:     inviteResult.ToTag,
		RemoteURI: inviteResult.RemoteURI,
	}
	g.streams.storeStream(key, stream)

	log.Info("playback started", "ssrc", inviteResult.SSRC, "port", rtpSession.Port)
	return inviteResult.SSRC, nil
}

// PlaySpeed changes the playback speed via SIP INFO.
func (g *GB28181API) PlaySpeed(in *PlaySpeedInput) error {
	key := "play:" + in.DeviceID + ":" + in.ChannelID
	stream, ok := g.streams.loadStream(key)
	if !ok {
		return fmt.Errorf("no active playback for %s:%s", in.DeviceID, in.ChannelID)
	}

	if stream.CallID == "" || stream.RemoteURI == "" {
		return fmt.Errorf("playback session missing dialog info")
	}

	// wvp format: PLAY RTSP/1.0 with Application/MANSRTSP content type
	infoBody := fmt.Sprintf("PLAY RTSP/1.0\r\nCSeq: %d\r\nScale: %.6f\r\n",
		time.Now().Unix()%100000000, in.Speed)

	return g.sendSIPInfo(stream, infoBody, "Application", "MANSRTSP")
}

// PlaySeek seeks to a position in playback via SIP INFO.
func (g *GB28181API) PlaySeek(in *PlaySeekInput) error {
	key := "play:" + in.DeviceID + ":" + in.ChannelID
	stream, ok := g.streams.loadStream(key)
	if !ok {
		return fmt.Errorf("no active playback for %s:%s", in.DeviceID, in.ChannelID)
	}

	if stream.CallID == "" || stream.RemoteURI == "" {
		return fmt.Errorf("playback session missing dialog info")
	}

	// wvp format for seek
	infoBody := fmt.Sprintf("PLAY RTSP/1.0\r\nCSeq: %d\r\nRange: npt=%d.000-\r\n",
		time.Now().Unix()%100000000, in.SeekTime)

	return g.sendSIPInfo(stream, infoBody, "Application", "MANSRTSP")
}

// PlayPause pauses playback via SIP INFO.
func (g *GB28181API) PlayPause(in *PlayPauseInput) error {
	key := "play:" + in.DeviceID + ":" + in.ChannelID
	stream, ok := g.streams.loadStream(key)
	if !ok {
		return fmt.Errorf("no active playback for %s:%s", in.DeviceID, in.ChannelID)
	}

	if stream.CallID == "" || stream.RemoteURI == "" {
		return fmt.Errorf("playback session missing dialog info")
	}

	infoBody := fmt.Sprintf("PAUSE RTSP/1.0\r\nCSeq: %d\r\nPauseTime: now\r\n",
		time.Now().Unix()%100000000)

	return g.sendSIPInfo(stream, infoBody, "Application", "MANSRTSP")
}

// PlayResume resumes playback via SIP INFO.
func (g *GB28181API) PlayResume(in *PlayPauseInput) error {
	key := "play:" + in.DeviceID + ":" + in.ChannelID
	stream, ok := g.streams.loadStream(key)
	if !ok {
		return fmt.Errorf("no active playback for %s:%s", in.DeviceID, in.ChannelID)
	}

	if stream.CallID == "" || stream.RemoteURI == "" {
		return fmt.Errorf("playback session missing dialog info")
	}

	infoBody := fmt.Sprintf("PLAY RTSP/1.0\r\nCSeq: %d\r\nRange: npt=now-\r\n",
		time.Now().Unix()%100000000)

	return g.sendSIPInfo(stream, infoBody, "Application", "MANSRTSP")
}

// sendSIPInfo sends a SIP INFO request to the device for playback control.
func (g *GB28181API) sendSIPInfo(stream *Streams, body string, contentType string, contentSubType string) error {
	dev, ok := g.store.Load(stream.DeviceID)
	if !ok {
		return fmt.Errorf("device not found: %s", stream.DeviceID)
	}

	// Use device address for SIP INFO
	recipient := sip.Uri{
		Scheme: "sip",
		User:   stream.ChannelID,
		Host:   getHost(dev.Address),
		Port:   getPort(dev.Address),
	}

	req := sip.NewRequest(sip.INFO, recipient)
	req.SetBody([]byte(body))
	req.AppendHeader(sip.NewHeader("Content-Type", contentType+"/"+contentSubType))
	
	// Set Call-ID from the INVITE dialog
	callID := sip.CallIDHeader(stream.CallID)
	req.AppendHeader(&callID)
	
	// Set From/To with tags from the INVITE dialog
	req.AppendHeader(&sip.FromHeader{
		Address: sip.Uri{Scheme: "sip", User: g.cfg.ID, Host: g.cfg.Host},
		Params:  sip.HeaderParams(sip.NewParams()),
	})
	if stream.FromTag != "" {
		req.From().Params.Add("tag", stream.FromTag)
	}
	
	req.AppendHeader(&sip.ToHeader{
		Address: sip.Uri{Scheme: "sip", User: stream.ChannelID, Host: getHost(dev.Address)},
		Params:  sip.HeaderParams(sip.NewParams()),
	})
	if stream.ToTag != "" {
		req.To().Params.Add("tag", stream.ToTag)
	}
	
	// Set CSeq
	cseq := sip.CSeqHeader{SeqNo: uint32(time.Now().Unix() % 100000000), MethodName: sip.INFO}
	req.AppendHeader(&cseq)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	slog.Info("[SIP] INFO sending", 
		"recipient", recipient.String(),
		"call_id", stream.CallID,
		"from_tag", stream.FromTag,
		"to_tag", stream.ToTag,
		"body", body)
	
	resp, err := g.client.Do(ctx, req)
	if err != nil {
		slog.Error("[SIP] INFO send failed", "error", err)
		return fmt.Errorf("SIP INFO failed: %w", err)
	}

	slog.Info("[SIP] INFO response", "status", resp.StatusCode, "reason", resp.Reason)
	if !resp.IsSuccess() {
		return fmt.Errorf("SIP INFO rejected: %d %s", resp.StatusCode, resp.Reason)
	}

	return nil
}

// StopPlay stops a GB28181 play session by sending BYE.
func (g *GB28181API) StopPlay(in *StopPlayInput) error {
	key := "play:" + in.DeviceID + ":" + in.ChannelID
	stream, ok := g.streams.deleteStream(key)
	if !ok {
		return nil
	}
	return g.stopPlay(stream)
}

func (g *GB28181API) stopPlay(stream *Streams) error {
	if stream.SessionID != "" && g.mediaEngine != nil {
		_ = g.mediaEngine.StopRTPReceive(context.Background(), stream.SessionID)
	}
	// Note: For sipgo, we'd need to track the INVITE request/response to send BYE.
	// For now, just stop the RTP session. A proper implementation would store
	// the dialog state and send BYE through the client.
	return nil
}

func (g *GB28181API) sipPlayInvite(ch *Channel, in *PlayInput, port int) (*InviteResult, error) {
	ipStr := g.cfg.MediaIP
	ssrc := g.streams.getSSRC(g.cfg.GetDomain())

	body := buildPlaySDP(in.DeviceID, ch.ChannelID, ipStr, port, in.StreamMode, ssrc, false, time.Time{}, time.Time{}, in.StreamNumber)

	dev := ch.device
	recipient := sip.Uri{
		Scheme: "sip",
		User:   ch.ChannelID,
		Host:   getHost(dev.Address),
		Port:   getPort(dev.Address),
	}

	slog.Info("[SIP] INVITE preparing",
		"device_id", in.DeviceID,
		"channel_id", ch.ChannelID,
		"recipient", recipient.String(),
		"media_ip", ipStr,
		"media_port", port,
		"ssrc", ssrc,
		"stream_mode", in.StreamMode,
	)

	req := sip.NewRequest(sip.INVITE, recipient)
	req.SetBody(body)
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.AppendHeader(sip.NewHeader("Subject",
		fmt.Sprintf("%s:%s,%s:%s", ch.ChannelID, ssrc, g.cfg.ID, "0")))

	return g.doInvite(req, ch)
}

func (g *GB28181API) sipPlaybackInvite(ch *Channel, in *PlaybackInput, port int) (*InviteResult, error) {
	ipStr := g.cfg.MediaIP
	ssrc := g.streams.getSSRC(g.cfg.GetDomain())

	// Playback doesn't support stream number, always use 0
	body := buildPlaySDP(in.DeviceID, ch.ChannelID, ipStr, port, in.StreamMode, ssrc, true, in.StartTime, in.EndTime, 0)

	dev := ch.device
	recipient := sip.Uri{
		Scheme: "sip",
		User:   ch.ChannelID,
		Host:   getHost(dev.Address),
		Port:   getPort(dev.Address),
	}

	slog.Info("[SIP] Playback INVITE preparing",
		"device_id", in.DeviceID,
		"channel_id", ch.ChannelID,
		"recipient", recipient.String(),
		"media_ip", ipStr,
		"media_port", port,
		"ssrc", ssrc,
		"start_time", in.StartTime,
		"start_time_unix", in.StartTime.Unix(),
		"end_time", in.EndTime,
		"end_time_unix", in.EndTime.Unix(),
	)
	slog.Info("[SIP] Playback SDP", "sdp", string(body))

	req := sip.NewRequest(sip.INVITE, recipient)
	req.SetBody(body)
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.AppendHeader(sip.NewHeader("Subject",
		fmt.Sprintf("%s:%s,%s:%s", ch.ChannelID, ssrc, g.cfg.ID, "0")))

	return g.doInvite(req, ch)
}

func (g *GB28181API) doInvite(req *sip.Request, ch *Channel) (*InviteResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slog.Info("[SIP] INVITE sending", "recipient", req.Recipient.String())
	tx, err := g.client.TransactionRequest(ctx, req)
	if err != nil {
		slog.Error("[SIP] INVITE send failed", "error", err)
		return nil, fmt.Errorf("INVITE send failed: %w", err)
	}
	defer tx.Terminate()

	for {
		select {
		case res := <-tx.Responses():
			slog.Info("[SIP] INVITE response received",
				"status_code", res.StatusCode,
				"reason", res.Reason,
			)
			if res.IsProvisional() {
				slog.Debug("[SIP] INVITE provisional response", "status", res.StatusCode)
				continue
			}
			if res.IsSuccess() {
				slog.Info("[SIP] INVITE success - preparing ACK")
				
				// Get Contact from 200 OK for ACK destination
				ackRecipient := req.Recipient
				remoteURI := req.Recipient.String()
				if contact := res.GetHeader("Contact"); contact != nil {
					var contactUri sip.Uri
					if err := sip.ParseUri(contact.Value(), &contactUri); err == nil {
						ackRecipient = contactUri
						remoteURI = contact.Value()
						slog.Info("[SIP] ACK using Contact from 200 OK", "contact", contact.Value())
					} else {
						slog.Warn("[SIP] Failed to parse Contact URI", "error", err)
					}
				}
				
				ack := sip.NewRequest(sip.ACK, ackRecipient)
				ack.AppendHeader(sip.HeaderClone(req.From()))
				ack.AppendHeader(sip.HeaderClone(res.To()))
				ack.AppendHeader(sip.HeaderClone(req.CallID()))
				cseq := *req.CSeq()
				cseq.MethodName = sip.ACK
				ack.AppendHeader(&cseq)
				
				slog.Info("[SIP] ACK sending", "to", ackRecipient.String())
				if err := g.client.WriteRequest(ack); err != nil {
					slog.Error("[SIP] ACK send failed", "error", err)
				} else {
					slog.Info("[SIP] ACK sent successfully")
				}

				ssrc := ""
				if subj := req.GetHeader("Subject"); subj != nil {
					// Extract SSRC from Subject: "channelID:ssrc,serverID:0"
					parts := strings.SplitN(subj.Value(), ":", 2)
					if len(parts) == 2 {
						ssrc = strings.SplitN(parts[1], ",", 2)[0]
					}
				}
				
				// Extract dialog info for later use (speed/seek)
				callID := ""
				if h := req.CallID(); h != nil {
					callID = h.Value()
				}
				fromTag := ""
				if h := req.GetHeader("From"); h != nil {
					if idx := strings.Index(h.Value(), ";tag="); idx >= 0 {
						fromTag = h.Value()[idx+5:]
					}
				}
				toTag := ""
				if h := res.GetHeader("To"); h != nil {
					if idx := strings.Index(h.Value(), ";tag="); idx >= 0 {
						toTag = h.Value()[idx+5:]
					}
				}
				
				// Parse SDP from response to get media info
				if len(res.Body()) > 0 {
					slog.Info("[SIP] INVITE response SDP", "body", string(res.Body()))
				}
				
				slog.Info("[SIP] INVITE completed", "ssrc", ssrc)
				return &InviteResult{
					SSRC:      ssrc,
					CallID:    callID,
					FromTag:   fromTag,
					ToTag:     toTag,
					RemoteURI: remoteURI,
				}, nil
			}
			slog.Warn("[SIP] INVITE rejected", "status", res.StatusCode, "reason", res.Reason)
			return nil, fmt.Errorf("INVITE rejected: %d %s", res.StatusCode, res.Reason)
		case <-ctx.Done():
			slog.Error("[SIP] INVITE timeout")
			return nil, fmt.Errorf("INVITE timeout")
		}
	}
}

func buildPlaySDP(deviceID, channelID, ip string, port int, streamMode int8, ssrc string, playback bool, startTime, endTime time.Time, streamNumber int) []byte {
	protocol := "RTP/AVP"
	if streamMode == 1 || streamMode == 2 {
		protocol = "TCP/RTP/AVP"
	}

	name := "Play"
	if playback {
		name = "Playback"
	}

	// Build SDP following GB28181 standard format
	lines := []string{
		"v=0",
		fmt.Sprintf("o=%s 0 0 IN IP4 %s", deviceID, ip),
		fmt.Sprintf("s=%s", name),
		fmt.Sprintf("c=IN IP4 %s", ip),
	}

	// Timing line
	if playback {
		lines = append(lines, fmt.Sprintf("t=%d %d", startTime.Unix(), endTime.Unix()))
	} else {
		lines = append(lines, "t=0 0")
	}

	// Media line - only use payload type 96 (PS) for better compatibility
	lines = append(lines, fmt.Sprintf("m=video %d %s 96", port, protocol))
	lines = append(lines, "a=recvonly")
	lines = append(lines, "a=rtpmap:96 PS/90000")

	// TCP mode attributes
	if streamMode == 1 {
		lines = append(lines, "a=setup:passive")
		lines = append(lines, "a=connection:new")
	} else if streamMode == 2 {
		lines = append(lines, "a=setup:active")
		lines = append(lines, "a=connection:new")
	}

	// GB28181 specific: SSRC in y= line
	lines = append(lines, fmt.Sprintf("y=%s", ssrc))

	// f= line (media description with stream number for GB28181-2022)
	// Format: f= v/编码格式/分辨率/帧率/码率类型/码率大小 a/编码格式/码率大小/采样率
	// streamnumber: 0=主码流, 1=子码流
	if streamNumber > 0 {
		lines = append(lines, fmt.Sprintf("f= v/2/1///a/1///"))
	} else {
		lines = append(lines, "f=")
	}

	return []byte(strings.Join(lines, "\r\n") + "\r\n")
}

func getHost(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func getPort(addr string) int {
	if _, portStr, err := net.SplitHostPort(addr); err == nil {
		var p int
		fmt.Sscanf(portStr, "%d", &p)
		if p > 0 {
			return p
		}
	}
	return 5060
}
