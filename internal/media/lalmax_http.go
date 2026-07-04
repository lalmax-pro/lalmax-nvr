package media

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
)

// lal ErrorCodeGroupNotFound 鈥?group not in lalmax yet (e.g. GB28181 RTP port open, no packets).
const lalErrorCodeGroupNotFound = 1001

type LalmaxHTTPConfig struct {
	BaseURL    string
	PublicURL  string
	HTTPClient *http.Client
	// Lal protocol ports (for lal-backed protocols like RTMP, RTSP, HTTP-FLV)
	RTMPPort int
	RTSPPort int
	HTTPPort int // HTTP-FLV port
}

type LalmaxHTTP struct {
	baseURL   *url.URL
	publicURL *url.URL
	client    *http.Client
	// Lal protocol ports
	rtmpPort int
	rtspPort int
	httpPort int
}

func NewLalmaxHTTP(cfg LalmaxHTTPConfig) (*LalmaxHTTP, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("lalmax base URL is required")
	}
	baseURL, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse lalmax base URL: %w", err)
	}
	publicRaw := cfg.PublicURL
	if publicRaw == "" {
		publicRaw = cfg.BaseURL
	}
	publicURL, err := url.Parse(strings.TrimRight(publicRaw, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse lalmax public URL: %w", err)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	// Set default lal ports if not specified
	rtmpPort := cfg.RTMPPort
	if rtmpPort == 0 {
		rtmpPort = config.DefaultLalRTMPPort
	}
	rtspPort := cfg.RTSPPort
	if rtspPort == 0 {
		rtspPort = config.DefaultLalRTSPPort
	}
	httpPort := cfg.HTTPPort
	if httpPort == 0 {
		httpPort = config.DefaultLalHTTPPort
	}
	return &LalmaxHTTP{
		baseURL:   baseURL,
		publicURL: publicURL,
		client:    client,
		rtmpPort:  rtmpPort,
		rtspPort:  rtspPort,
		httpPort:  httpPort,
	}, nil
}

func (e *LalmaxHTTP) Start(ctx context.Context) error    { return e.Ready(ctx) }
func (e *LalmaxHTTP) Shutdown(ctx context.Context) error { return nil }
func (e *LalmaxHTTP) Ready(ctx context.Context) error {
	var resp lalResp[json.RawMessage]
	return e.getJSON(ctx, "/api/stat/lal_info", nil, &resp)
}
func (e *LalmaxHTTP) StopPull(ctx context.Context, streamID string) error {
	if streamID == "" {
		return errors.New("stream ID is required")
	}
	q := url.Values{"stream_name": []string{streamID}}
	var resp lalResp[json.RawMessage]
	return e.postJSON(ctx, "/api/ctrl/stop_relay_pull", q, nil, &resp)
}

func (e *LalmaxHTTP) StartPull(ctx context.Context, req StartPullRequest) (*StreamSession, error) {
	if req.StreamID == "" {
		return nil, errors.New("stream ID is required")
	}
	if req.SourceURL == "" {
		return nil, errors.New("source URL is required")
	}
	body := map[string]any{"url": req.SourceURL, "stream_name": req.StreamID}
	if req.PullTimeout > 0 {
		body["pull_timeout_ms"] = int(req.PullTimeout / time.Millisecond)
	}
	// PullRetryNum takes precedence over RetryForever
	if req.PullRetryNum != 0 {
		body["pull_retry_num"] = req.PullRetryNum
	} else if req.RetryForever {
		body["pull_retry_num"] = -1
	}
	if req.AutoStopNoView > 0 {
		body["auto_stop_pull_after_no_out_ms"] = int(req.AutoStopNoView / time.Millisecond)
	}
	if strings.EqualFold(req.Transport, "tcp") || req.Transport == "" {
		body["rtsp_mode"] = 0
	}
	var resp lalResp[sessionPayload]
	if err := e.postJSON(ctx, "/api/ctrl/start_relay_pull", nil, body, &resp); err != nil {
		return nil, err
	}
	return &StreamSession{SessionID: firstNonEmpty(resp.Data.SessionID, resp.Data.ID), StreamID: req.StreamID, AppName: req.AppName, Protocol: "relay_pull", StartedAt: time.Now()}, nil
}

func (e *LalmaxHTTP) StartRTPReceive(ctx context.Context, req StartRTPReceiveRequest) (*StreamSession, error) {
	if req.StreamID == "" {
		return nil, errors.New("stream ID is required")
	}
	body := map[string]any{"stream_name": req.StreamID}
	if req.Port > 0 {
		body["port"] = req.Port
	}
	if req.Timeout > 0 {
		body["timeout_ms"] = int(req.Timeout / time.Millisecond)
	}
	if strings.Contains(strings.ToLower(req.Protocol), "tcp") {
		body["is_tcp_flag"] = 1
	}
	var resp lalResp[rtpPubPayload]
	if err := e.postJSON(ctx, "/api/ctrl/start_rtp_pub", nil, body, &resp); err != nil {
		return nil, err
	}
	return &StreamSession{SessionID: firstNonEmpty(resp.Data.SessionID, resp.Data.ID), StreamID: req.StreamID, AppName: req.AppName, Protocol: "rtp", Port: resp.Data.Port, StartedAt: time.Now()}, nil
}

func (e *LalmaxHTTP) StopRTPReceive(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.New("session ID is required")
	}
	q := url.Values{"session_id": []string{sessionID}}
	var resp lalResp[json.RawMessage]
	return e.postJSON(ctx, "/api/ctrl/stop_rtp_pub", q, nil, &resp)
}

func (e *LalmaxHTTP) KickSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.New("session ID is required")
	}
	streams, err := e.ListStreams(ctx)
	if err != nil {
		return err
	}
	streamID := ""
	for _, stream := range streams {
		if stream.Publisher != nil && stream.Publisher.SessionID == sessionID {
			streamID = stream.StreamID
			break
		}
		for _, sub := range stream.Subscribers {
			if sub.SessionID == sessionID {
				streamID = stream.StreamID
				break
			}
		}
		if streamID != "" {
			break
		}
	}
	if streamID == "" {
		return fmt.Errorf("session %q not found", sessionID)
	}
	var resp lalResp[json.RawMessage]
	return e.postJSON(ctx, "/api/ctrl/kick_session", nil, map[string]any{"session_id": sessionID, "stream_name": streamID}, &resp)
}

func (e *LalmaxHTTP) GetStream(ctx context.Context, streamID string) (*StreamInfo, error) {
	if streamID == "" {
		return nil, errors.New("stream ID is required")
	}
	q := url.Values{"stream_name": []string{streamID}}
	var resp lalResp[groupPayload]
	if err := e.getJSON(ctx, "/api/stat/group", q, &resp); err != nil {
		if isLalGroupNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	info := groupToStreamInfo(resp.Data)
	return &info, nil
}

func isLalGroupNotFound(err error) bool {
	if err == nil {
		return false
	}
	var code int
	if _, scanErr := fmt.Sscanf(err.Error(), "lalmax error %d:", &code); scanErr == nil {
		return code == lalErrorCodeGroupNotFound
	}
	return strings.Contains(err.Error(), "group not found")
}

func (e *LalmaxHTTP) ListStreams(ctx context.Context) ([]StreamInfo, error) {
	var resp lalResp[struct {
		Groups []groupPayload `json:"groups"`
	}]
	if err := e.getJSON(ctx, "/api/stat/all_group", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]StreamInfo, 0, len(resp.Data.Groups))
	for _, group := range resp.Data.Groups {
		out = append(out, groupToStreamInfo(group))
	}
	return out, nil
}

func (e *LalmaxHTTP) BuildPlayURL(ctx context.Context, req PlayURLRequest) (*PlayURL, error) {
	if req.StreamID == "" {
		return nil, errors.New("stream ID is required")
	}
	proto := strings.ToLower(req.Protocol)
	if proto == "" {
		proto = "hls"
	}
	app := req.AppName
	if app == "" {
		app = "live"
	}

	// Determine the base URL based on protocol
	// lalmax protocols (ll-hls, webrtc, fmp4) use the lalmax HTTP port.
	// lal protocols (rtmp, rtsp, flv) use their respective ports
	u := e.getBaseURLForProtocol(proto)
	switch proto {
	case "webrtc", "whep":
		u.Path = "/webrtc/whep"
		q := u.Query()
		q.Set("streamid", req.StreamID)
		if req.AppName != "" {
			q.Set("app_name", req.AppName)
		}
		u.RawQuery = q.Encode()
		proto = "whep"
	case "fmp4", "http-fmp4":
		u.Path = "/live/m4s/" + pathEscape(req.StreamID) + ".mp4"
		q := u.Query()
		if req.AppName != "" {
			q.Set("app_name", req.AppName)
		}
		u.RawQuery = q.Encode()
		proto = "fmp4"
	case "flv", "http-flv":
		// HTTP-FLV uses lal's HTTP port
		u.Path = "/" + pathEscape(app) + "/" + pathEscape(req.StreamID) + ".flv"
		proto = "flv"
	case "ws-flv", "websocket-flv":
		// WS-FLV uses lalmax port
		if u.Scheme == "https" {
			u.Scheme = "wss"
		} else {
			u.Scheme = "ws"
		}
		u.Path = "/" + pathEscape(app) + "/" + pathEscape(req.StreamID) + ".flv"
		proto = "ws-flv"
	case "hls", "hls-ts":
		// HLS (TS) uses lal's HTTP port with lal's URL pattern
		u.Host = fmt.Sprintf("%s:%d", u.Hostname(), e.httpPort)
		u.Path = "/hls/" + pathEscape(req.StreamID) + ".m3u8"
		proto = "hls"
	case "ll-hls", "llhls", "hls-fmp4":
		// LL-HLS / HLS (fMP4) uses lalmax port
		u.Path = "/live/hls/" + pathEscape(req.StreamID) + "/index.m3u8"
		q := u.Query()
		q.Set("ll-hls", "1")
		if req.AppName != "" {
			q.Set("app_name", req.AppName)
		}
		u.RawQuery = q.Encode()
		if proto == "llhls" || proto == "hls-fmp4" {
			proto = "ll-hls"
		}
	case "rtmp":
		// RTMP uses lal's RTMP port
		u.Scheme = "rtmp"
		u.Host = fmt.Sprintf("%s:%d", u.Hostname(), e.rtmpPort)
		u.Path = "/" + pathEscape(app) + "/" + pathEscape(req.StreamID)
	case "rtsp":
		// RTSP uses lal's RTSP port
		u.Scheme = "rtsp"
		u.Host = fmt.Sprintf("%s:%d", u.Hostname(), e.rtspPort)
		u.Path = "/" + pathEscape(app) + "/" + pathEscape(req.StreamID)
	default:
		return nil, fmt.Errorf("unsupported play protocol %q", req.Protocol)
	}
	if req.Token != "" {
		q := u.Query()
		q.Set("token", req.Token)
		u.RawQuery = q.Encode()
	}
	expires := time.Time{}
	if req.TTL > 0 {
		expires = time.Now().Add(req.TTL)
	}
	return &PlayURL{URL: u.String(), Protocol: proto, ExpiresAt: expires}, nil
}

// getBaseURLForProtocol returns the appropriate base URL for the given protocol.
// lal protocols (flv, ws-flv, hls) use the lal HTTP port (default 18080).
// lalmax protocols (ll-hls, webrtc, fmp4) use the lalmax HTTP port (default 12090).
func (e *LalmaxHTTP) getBaseURLForProtocol(proto string) url.URL {
	switch proto {
	case "flv", "http-flv", "ws-flv", "websocket-flv", "hls", "hls-ts":
		// HTTP-FLV, WS-FLV and HLS use lal's HTTP port
		u := *e.publicURL
		u.Host = fmt.Sprintf("%s:%d", u.Hostname(), e.httpPort)
		return u
	default:
		// All other protocols use lalmax port
		return *e.publicURL
	}
}

func (e *LalmaxHTTP) SubscribeEvents(ctx context.Context, filter EventFilter) (<-chan Event, error) {
	q := url.Values{}
	if filter.StreamID != "" {
		q.Set("stream_name", filter.StreamID)
	}
	if filter.AppName != "" {
		q.Set("app_name", filter.AppName)
	}
	if len(filter.Types) > 0 {
		events := make([]string, 0, len(filter.Types))
		for _, typ := range filter.Types {
			events = append(events, mediaEventToHook(typ))
		}
		q.Set("events", strings.Join(events, ","))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.endpoint("/api/hook/stream", q), nil)
	if err != nil {
		return nil, err
	}
	// Use a client with no timeout for SSE connections (long-lived)
	sseClient := &http.Client{Timeout: 0}
	resp, err := sseClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("lalmax hook stream returned HTTP %d", resp.StatusCode)
	}
	out := make(chan Event, 64)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		scanSSE(ctx, resp.Body, out)
	}()
	return out, nil
}

func (e *LalmaxHTTP) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.endpoint(path, q), nil)
	if err != nil {
		return err
	}
	return e.doJSON(req, out)
}

func (e *LalmaxHTTP) postJSON(ctx context.Context, path string, q url.Values, body any, out any) error {
	var r io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint(path, q), r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return e.doJSON(req, out)
}

func (e *LalmaxHTTP) doJSON(req *http.Request, out any) error {
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("lalmax returned HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode lalmax response: %w", err)
	}
	if basic, ok := responseBasic(out); ok && basic.ErrorCode != 0 {
		return fmt.Errorf("lalmax error %d: %s", basic.ErrorCode, basic.Description)
	}
	return nil
}

func (e *LalmaxHTTP) endpoint(path string, q url.Values) string {
	u := *e.baseURL
	u.Path = path
	u.RawQuery = q.Encode()
	return u.String()
}

type lalResp[T any] struct {
	ErrorCode   int    `json:"error_code"`
	Description string `json:"desp"`
	Data        T      `json:"data"`
}

type basicResp struct {
	ErrorCode   int
	Description string
}

func responseBasic(v any) (basicResp, bool) {
	switch resp := v.(type) {
	case *lalResp[json.RawMessage]:
		return basicResp{ErrorCode: resp.ErrorCode, Description: resp.Description}, true
	case *lalResp[sessionPayload]:
		return basicResp{ErrorCode: resp.ErrorCode, Description: resp.Description}, true
	case *lalResp[rtpPubPayload]:
		return basicResp{ErrorCode: resp.ErrorCode, Description: resp.Description}, true
	case *lalResp[groupPayload]:
		return basicResp{ErrorCode: resp.ErrorCode, Description: resp.Description}, true
	case *lalResp[struct {
		Groups []groupPayload `json:"groups"`
	}]:
		return basicResp{ErrorCode: resp.ErrorCode, Description: resp.Description}, true
	default:
		return basicResp{}, false
	}
}

type sessionPayload struct {
	ID                string `json:"id"`
	SessionID         string `json:"session_id"`
	Protocol          string `json:"protocol"`
	Remote            string `json:"remote_addr"`
	BitrateKbits      int    `json:"bitrate_kbits"`
	ReadBitrateKbits  int    `json:"read_bitrate_kbits"`
	WriteBitrateKbits int    `json:"write_bitrate_kbits"`
	ReadBytesSum      uint64 `json:"read_bytes_sum"`
	WroteBytesSum     uint64 `json:"wrote_bytes_sum"`
}

// rtpPubPayload is used for the start_rtp_pub API response.
type rtpPubPayload struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Port      int    `json:"port"`
}

type groupPayload struct {
	StreamName string           `json:"stream_name"`
	AppName    string           `json:"app_name"`
	AudioCodec string           `json:"audio_codec"`
	VideoCodec string           `json:"video_codec"`
	Pub        sessionPayload   `json:"pub"`
	Subs       []sessionPayload `json:"subs"`
	Pull       sessionPayload   `json:"pull"`
	FPS        []struct {
		UnixSec int64   `json:"unix_sec"`
		V       float64 `json:"v"`
		Value   float64 `json:"value"`
		Num     float64 `json:"num"`
		FPS     float64 `json:"fps"`
	} `json:"in_frame_per_sec"`
}

func groupToStreamInfo(group groupPayload) StreamInfo {
	isPubActive := group.Pub.SessionID != "" || group.Pub.ID != ""
	isPullActive := group.Pull.SessionID != "" || group.Pull.ID != ""
	info := StreamInfo{StreamID: group.StreamName, AppName: group.AppName, Active: isPubActive || isPullActive, AudioCodec: group.AudioCodec, VideoCodec: group.VideoCodec}
	if isPubActive {
		info.Publisher = sessionInfoFromPayload(group.Pub, "pub")
	} else if isPullActive {
		info.Publisher = sessionInfoFromPayload(group.Pull, "pull")
	}
	for _, sub := range group.Subs {
		info.Subscribers = append(info.Subscribers, *sessionInfoFromPayload(sub, "sub"))
	}
	if len(group.FPS) > 0 {
		var totalFPS float64
		var samples int
		var latestUnixSec int64
		for _, record := range group.FPS {
			fps := firstNonZero(record.FPS, record.Value, record.Num, record.V)
			if fps > 0 {
				totalFPS += fps
				samples++
			}
			if record.UnixSec > latestUnixSec {
				latestUnixSec = record.UnixSec
			}
		}
		if samples > 0 {
			info.InFPS = totalFPS / float64(samples)
		}
		if latestUnixSec > 0 {
			info.LastFrameTime = time.Unix(latestUnixSec, 0)
		}
	}
	return info
}

func sessionInfoFromPayload(payload sessionPayload, fallbackProtocol string) *SessionInfo {
	return &SessionInfo{
		SessionID:         firstNonEmpty(payload.SessionID, payload.ID),
		Protocol:          firstNonEmpty(payload.Protocol, fallbackProtocol),
		Remote:            payload.Remote,
		BitrateKbits:      payload.BitrateKbits,
		ReadBitrateKbits:  payload.ReadBitrateKbits,
		WriteBitrateKbits: payload.WriteBitrateKbits,
		ReadBytesSum:      payload.ReadBytesSum,
		WroteBytesSum:     payload.WroteBytesSum,
	}
}

func scanSSE(ctx context.Context, r io.Reader, out chan<- Event) {
	scanner := bufio.NewScanner(r)
	var id int64
	var name string
	var data []byte
	flush := func() bool {
		if name == "" && len(data) == 0 {
			return true
		}
		ev := hookToMediaEvent(id, name, data)
		select {
		case out <- ev:
			id = 0
			name = ""
			data = nil
			return true
		case <-ctx.Done():
			return false
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if !flush() {
				return
			}
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "id":
			id, _ = strconv.ParseInt(val, 10, 64)
		case "event":
			name = val
		case "data":
			data = append(data, val...)
		}
	}
	_ = flush()
}

func hookToMediaEvent(id int64, hookName string, raw []byte) Event {
	var payload struct {
		AppName    string `json:"app_name"`
		StreamName string `json:"stream_name"`
		SessionID  string `json:"session_id"`
		Protocol   string `json:"protocol"`
	}
	_ = json.Unmarshal(raw, &payload)
	return Event{ID: id, Type: hookToMediaEventType(hookName), StreamID: payload.StreamName, AppName: payload.AppName, SessionID: payload.SessionID, Protocol: payload.Protocol, At: time.Now(), Raw: append([]byte(nil), raw...)}
}

func hookToMediaEventType(name string) EventType {
	switch name {
	case "on_group_start":
		return EventStreamStarted
	case "on_stream_active":
		return EventStreamActive
	case "on_group_exit":
		return EventStreamStopped
	case "on_pub_start":
		return EventPublisherStarted
	case "on_pub_stop":
		return EventPublisherStopped
	case "on_sub_start":
		return EventSubscriberStarted
	case "on_sub_stop":
		return EventSubscriberStopped
	case "on_relay_pull_start":
		return EventRelayPullStarted
	case "on_relay_pull_stop":
		return EventRelayPullStopped
	default:
		return EventType("media.hook." + name)
	}
}

func mediaEventToHook(typ EventType) string {
	switch typ {
	case EventStreamStarted:
		return "on_group_start"
	case EventStreamActive:
		return "on_stream_active"
	case EventStreamStopped:
		return "on_group_exit"
	case EventPublisherStarted:
		return "on_pub_start"
	case EventPublisherStopped:
		return "on_pub_stop"
	case EventSubscriberStarted:
		return "on_sub_start"
	case EventSubscriberStopped:
		return "on_sub_stop"
	case EventRelayPullStarted:
		return "on_relay_pull_start"
	case EventRelayPullStopped:
		return "on_relay_pull_stop"
	default:
		return string(typ)
	}
}

func firstNonZero(values ...float64) float64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

func pathEscape(v string) string {
	return strings.ReplaceAll(url.PathEscape(v), "+", "%20")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (e *LalmaxHTTP) SubscribeRTMPEvents(ctx context.Context) (<-chan RTMPEvent, error) {
	events, err := e.SubscribeEvents(ctx, EventFilter{
		Types: []EventType{
			EventPublisherStarted,
			EventPublisherStopped,
			EventStreamActive,
			EventStreamStopped,
		},
	})
	if err != nil {
		return nil, err
	}

	out := make(chan RTMPEvent, 64)
	go func() {
		defer close(out)
		for ev := range events {
			var typ string
			switch ev.Type {
			case EventPublisherStarted:
				typ = "pub_start"
			case EventPublisherStopped:
				typ = "pub_stop"
			case EventStreamActive:
				typ = "stream_active"
			case EventStreamStopped:
				typ = "stream_stopped"
			default:
				continue
			}
			out <- RTMPEvent{
				StreamID: ev.StreamID,
				AppName:  ev.AppName,
				Protocol: ev.Protocol,
				Type:     typ,
			}
		}
	}()
	return out, nil
}

func (e *LalmaxHTTP) SubscribeSRTEvents(ctx context.Context) (<-chan SRTEvent, error) {
	events, err := e.SubscribeEvents(ctx, EventFilter{
		Types: []EventType{
			EventPublisherStarted,
			EventPublisherStopped,
			EventStreamActive,
			EventStreamStopped,
		},
	})
	if err != nil {
		return nil, err
	}

	out := make(chan SRTEvent, 64)
	go func() {
		defer close(out)
		for ev := range events {
			var typ string
			switch ev.Type {
			case EventPublisherStarted:
				typ = "pub_start"
			case EventPublisherStopped:
				typ = "pub_stop"
			case EventStreamActive:
				typ = "stream_active"
			case EventStreamStopped:
				typ = "stream_stopped"
			default:
				continue
			}
			out <- SRTEvent{
				StreamID: ev.StreamID,
				AppName:  ev.AppName,
				Protocol: ev.Protocol,
				Type:     typ,
			}
		}
	}()
	return out, nil
}

// AddCustomizePubSession is not supported for HTTP-based lalmax engine.
func (e *LalmaxHTTP) AddCustomizePubSession(_ context.Context, _ string) (CustomizePubSession, error) {
	return nil, fmt.Errorf("AddCustomizePubSession not supported for HTTP-based lalmax engine")
}

// DelCustomizePubSession is not supported for HTTP-based lalmax engine.
func (e *LalmaxHTTP) DelCustomizePubSession(_ context.Context, _ CustomizePubSession) error {
	return fmt.Errorf("DelCustomizePubSession not supported for HTTP-based lalmax engine")
}

