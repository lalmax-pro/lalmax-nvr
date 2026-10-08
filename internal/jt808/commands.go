package jt808

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	cmdLogout        = 0x0003
	cmdAVQuery       = 0x9003
	cmdAVReport      = 0x1003
	cmdLoss          = 0x9105
	cmdPlayback      = 0x9201
	cmdPlaybackCtrl  = 0x9202
	cmdResourceQuery = 0x9205
	cmdResourceList  = 0x1205
	cmdFileUpload    = 0x9206
	cmdUploadCtrl    = 0x9207
	cmdUploadDone    = 0x1206
	cmdPTZRotate     = 0x9301
	cmdPTZFocus      = 0x9302
	cmdPTZIris       = 0x9303
	cmdPTZWiper      = 0x9304
	cmdPTZInfrared   = 0x9305
	cmdPTZZoom       = 0x9306

	grantLive = "live:"
	grantPB   = "pb:"
)

// ErrBadInput marks a caller error. API handlers map it to HTTP 400.
var ErrBadInput = errors.New("bad input")

// AVProperties is the terminal reply to 0x9003 (message 0x1003).
type AVProperties struct {
	AudioCodec      byte `json:"audio_codec"`
	AudioChannels   byte `json:"audio_channels"`
	AudioSampleRate byte `json:"audio_sample_rate"`
	AudioSampleBits byte `json:"audio_sample_bits"`
	AudioFrameLen   int  `json:"audio_frame_len"`
	AudioOutput     byte `json:"audio_output"`
	VideoCodec      byte `json:"video_codec"`
	MaxAudioCh      byte `json:"max_audio_channels"`
	MaxVideoCh      byte `json:"max_video_channels"`
}

// Resource is one item from 0x1205.
type Resource struct {
	Channel byte      `json:"channel"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Alarm   uint64    `json:"alarm"`
	AVType  byte      `json:"av_type"`
	Stream  byte      `json:"stream"`
	Storage byte      `json:"storage"`
	Size    uint32    `json:"size"`
}

// UploadTask tracks a 0x9206 request and the later 0x1206 result.
type UploadTask struct {
	Serial uint16    `json:"serial"`
	Done   bool      `json:"done"`
	Result byte      `json:"result"`
	At     time.Time `json:"at,omitempty"`
}

// LiveControlInput is 0x9102. Control: 0 close, 1 switch stream, 2 pause, 3 resume, 4 close talk.
// CloseAV: 0 audio+video, 1 audio, 2 video. Stream: 0 main, 1 sub.
type LiveControlInput struct {
	Key     string
	Channel byte
	Control byte
	CloseAV byte
	Stream  byte
}

// LossInput is 0x9105. Rate is a packet-loss percentage 0-100.
type LossInput struct {
	Key     string
	Channel byte
	Rate    byte
}

// PlaybackInput is 0x9201.
type PlaybackInput struct {
	Key        string
	Channel    byte
	Transport  string
	AVType     byte
	StreamType byte
	Storage    byte
	Mode       byte
	Speed      byte
	Start      time.Time
	End        time.Time
}

// PlaybackControlInput is 0x9202. Control: 0 start, 1 pause, 2 end, 3 fast, 4 rewind, 5 drag, 6 keyframe.
type PlaybackControlInput struct {
	Key     string
	Channel byte
	Control byte
	Speed   byte
	DragTo  time.Time
}

// ResourceQuery is 0x9205. Channel 0 lists every channel.
type ResourceQuery struct {
	Key        string
	Channel    byte
	Start      time.Time
	End        time.Time
	AVType     byte
	StreamType byte
	Storage    byte
}

// UploadInput is 0x9206. Empty host, port, user, or password fall back to server defaults.
type UploadInput struct {
	Key        string
	Channel    byte
	Start      time.Time
	End        time.Time
	Host       string
	Port       int
	User       string
	Password   string
	Path       string
	AVType     byte
	StreamType byte
	Storage    byte
	Condition  byte
}

// UploadControlInput is 0x9207. Control: 0 pause, 1 continue, 2 cancel.
type UploadControlInput struct {
	Key     string
	Serial  uint16
	Control byte
}

// PTZInput covers 0x9301–0x9306. Action is rotate, focus, iris, wiper, infrared, or zoom.
type PTZInput struct {
	Key     string
	Channel byte
	Action  string
	Direct  byte
	Speed   byte
}

func (s *Server) AllowStream(name string) bool {
	if s == nil || name == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sim, _, ok := strings.Cut(name, "_")
	t := s.terminals[sim]
	if !ok || s.stopped || t == nil || !t.Online || !t.Authenticated {
		return false
	}
	return s.grants[grantLive+name] > 0 || s.grants[grantPB+name] > 0
}

func (s *Server) grant(kind, name string) int {
	s.mu.Lock()
	if s.grants == nil {
		s.grants = map[string]int{}
	}
	previous := s.grants[kind+name]
	s.grants[kind+name] = 1
	s.mu.Unlock()
	return previous
}

func (s *Server) lockControl(key string) func() {
	value, _ := s.controls.LoadOrStore(key, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Server) restoreGrant(kind, name string, previous int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sim, _, _ := strings.Cut(name, "_")
	t := s.terminals[sim]
	if previous > 0 && !s.stopped && t != nil && t.Online && t.Authenticated {
		s.grants[kind+name] = previous
	} else {
		delete(s.grants, kind+name)
	}
}

func (s *Server) revoke(kind, name string) {
	s.mu.Lock()
	key := kind + name
	delete(s.grants, key)
	s.mu.Unlock()
}

// clearGrantsLocked is called when a terminal session loses authentication.
func (s *Server) clearGrantsLocked(sim string) {
	for name := range s.grants {
		if strings.HasPrefix(name, grantLive+sim+"_") || strings.HasPrefix(name, grantPB+sim+"_") {
			delete(s.grants, name)
		}
	}
}

func (s *Server) LiveControl(in LiveControlInput) error {
	unlock := s.lockControl(in.Key)
	defer unlock()
	if _, ok := validSIM(in.Key); !ok {
		return fmt.Errorf("%w: invalid SIM", ErrBadInput)
	}
	if in.Channel == 0 {
		in.Channel = 1
	}
	if in.Control > 4 || in.CloseAV > 2 || in.Stream > 1 {
		return fmt.Errorf("%w: invalid live control", ErrBadInput)
	}
	body := []byte{in.Channel, in.Control, in.CloseAV, in.Stream}
	if err := s.sender.send(in.Key, cmdStop, body, s.cfg.TimeoutDuration()); err != nil {
		return fmt.Errorf("send 0x9102: %w", err)
	}
	if in.Control == 0 && in.CloseAV == 0 {
		s.revoke(grantLive, StreamID(in.Key, in.Channel))
	}
	return nil
}

func (s *Server) ReportLoss(in LossInput) error {
	if _, ok := validSIM(in.Key); !ok {
		return fmt.Errorf("%w: invalid SIM", ErrBadInput)
	}
	if in.Channel == 0 {
		in.Channel = 1
	}
	if in.Rate > 100 {
		return fmt.Errorf("%w: loss rate must be 0-100", ErrBadInput)
	}
	if err := s.sender.send(in.Key, cmdLoss, []byte{in.Channel, in.Rate}, s.cfg.TimeoutDuration()); err != nil {
		return fmt.Errorf("send 0x9105: %w", err)
	}
	return nil
}

func (s *Server) QueryAV(key string) (AVProperties, error) {
	if _, ok := validSIM(key); !ok {
		return AVProperties{}, fmt.Errorf("%w: invalid SIM", ErrBadInput)
	}
	body, err := s.sendBody(key, cmdAVQuery, nil, cmdAVReport, s.cfg.TimeoutDuration())
	if err != nil {
		return AVProperties{}, fmt.Errorf("query av: %w", err)
	}
	prop, err := parseAV(body)
	if err != nil {
		return AVProperties{}, err
	}
	s.mu.Lock()
	if t := s.terminals[key]; t != nil {
		cp := prop
		t.AV = &cp
	}
	s.mu.Unlock()
	return prop, nil
}

func (s *Server) Playback(in PlaybackInput) (string, error) {
	unlock := s.lockControl(in.Key)
	defer unlock()
	if _, ok := validSIM(in.Key); !ok {
		return "", fmt.Errorf("%w: invalid SIM", ErrBadInput)
	}
	if in.Channel == 0 {
		in.Channel = 1
	}
	if in.Start.IsZero() || in.End.IsZero() || !in.End.After(in.Start) {
		return "", fmt.Errorf("%w: playback needs a start and a later end", ErrBadInput)
	}
	transport := strings.ToLower(strings.TrimSpace(in.Transport))
	if transport == "" {
		transport = s.cfg.Transport
	}
	if transport != TransportTCP && transport != TransportUDP {
		return "", fmt.Errorf("%w: transport must be tcp or udp", ErrBadInput)
	}
	body := mediaPrefix(s.cfg.MediaIP, s.cfg.MediaTCPPort, s.cfg.MediaUDPPort, transport)
	body = append(body, in.Channel, in.AVType, in.StreamType, in.Storage, in.Mode, in.Speed)
	body = append(body, bcdTime(in.Start)...)
	body = append(body, bcdTime(in.End)...)
	id := StreamID(in.Key, in.Channel)
	previous := s.grant(grantPB, id)
	if err := s.sender.send(in.Key, cmdPlayback, body, s.cfg.TimeoutDuration()); err != nil {
		s.restoreGrant(grantPB, id, previous)
		return "", fmt.Errorf("send 0x9201: %w", err)
	}
	return id, nil
}

func (s *Server) PlaybackControl(in PlaybackControlInput) error {
	unlock := s.lockControl(in.Key)
	defer unlock()
	if _, ok := validSIM(in.Key); !ok {
		return fmt.Errorf("%w: invalid SIM", ErrBadInput)
	}
	if in.Channel == 0 {
		in.Channel = 1
	}
	if in.Control > 6 {
		return fmt.Errorf("%w: invalid playback control", ErrBadInput)
	}
	body := []byte{in.Channel, in.Control, in.Speed}
	if in.Control == 5 {
		if in.DragTo.IsZero() {
			return fmt.Errorf("%w: drag requires drag_to", ErrBadInput)
		}
		body = append(body, bcdTime(in.DragTo)...)
	} else {
		body = append(body, make([]byte, 6)...)
	}
	if err := s.sender.send(in.Key, cmdPlaybackCtrl, body, s.cfg.TimeoutDuration()); err != nil {
		return fmt.Errorf("send 0x9202: %w", err)
	}
	if in.Control == 2 {
		s.revoke(grantPB, StreamID(in.Key, in.Channel))
	}
	return nil
}

func (s *Server) QueryResources(in ResourceQuery) ([]Resource, error) {
	if _, ok := validSIM(in.Key); !ok {
		return nil, fmt.Errorf("%w: invalid SIM", ErrBadInput)
	}
	if in.Start.IsZero() || in.End.IsZero() || !in.End.After(in.Start) {
		return nil, fmt.Errorf("%w: resource query needs a start and a later end", ErrBadInput)
	}
	body := make([]byte, 24)
	body[0] = in.Channel
	copy(body[1:7], bcdTime(in.Start))
	copy(body[7:13], bcdTime(in.End))
	body[21] = in.AVType
	body[22] = in.StreamType
	body[23] = in.Storage
	raw, err := s.sendBody(in.Key, cmdResourceQuery, body, cmdResourceList, s.cfg.TimeoutDuration())
	if err != nil {
		return nil, fmt.Errorf("query resources: %w", err)
	}
	return parseResources(raw)
}

func (s *Server) Upload(in UploadInput) (uint16, error) {
	if _, ok := validSIM(in.Key); !ok {
		return 0, fmt.Errorf("%w: invalid SIM", ErrBadInput)
	}
	if in.Channel == 0 {
		in.Channel = 1
	}
	if in.Start.IsZero() || in.End.IsZero() || !in.End.After(in.Start) {
		return 0, fmt.Errorf("%w: upload needs a start and a later end", ErrBadInput)
	}
	if in.Host == "" {
		in.Host = s.cfg.MediaIP
	}
	if in.Port == 0 {
		in.Port = s.cfg.FTPPort
	}
	if in.User == "" {
		in.User = s.cfg.FTPUser
	}
	if in.Password == "" {
		in.Password = s.cfg.FTPPassword
	}
	if in.Path == "" {
		in.Path = fmt.Sprintf("/jt1078/%s/%d", in.Key, in.Channel)
	}
	if in.Host == "" || in.Port <= 0 || in.User == "" || in.Password == "" {
		return 0, fmt.Errorf("%w: ftp host, port, user, and password are required", ErrBadInput)
	}
	body := ftpField(nil, in.Host)
	body = binary.BigEndian.AppendUint16(body, uint16(in.Port))
	body = ftpField(body, in.User)
	body = ftpField(body, in.Password)
	body = ftpField(body, in.Path)
	body = append(body, in.Channel)
	body = append(body, bcdTime(in.Start)...)
	body = append(body, bcdTime(in.End)...)
	body = append(body, make([]byte, 8)...)
	body = append(body, in.AVType, in.StreamType, in.Storage, in.Condition)
	seq, err := s.sendSeq(in.Key, cmdFileUpload, body, s.cfg.TimeoutDuration())
	if err != nil {
		return 0, fmt.Errorf("send 0x9206: %w", err)
	}
	s.mu.Lock()
	if t := s.terminals[in.Key]; t != nil {
		t.Uploads = append(t.Uploads, UploadTask{Serial: seq})
	}
	s.mu.Unlock()
	return seq, nil
}

func (s *Server) UploadControl(in UploadControlInput) error {
	if _, ok := validSIM(in.Key); !ok {
		return fmt.Errorf("%w: invalid SIM", ErrBadInput)
	}
	if in.Control > 2 {
		return fmt.Errorf("%w: invalid upload control", ErrBadInput)
	}
	body := make([]byte, 3)
	binary.BigEndian.PutUint16(body, in.Serial)
	body[2] = in.Control
	if err := s.sender.send(in.Key, cmdUploadCtrl, body, s.cfg.TimeoutDuration()); err != nil {
		return fmt.Errorf("send 0x9207: %w", err)
	}
	return nil
}

func (s *Server) PTZ(in PTZInput) error {
	if _, ok := validSIM(in.Key); !ok {
		return fmt.Errorf("%w: invalid SIM", ErrBadInput)
	}
	if in.Channel == 0 {
		in.Channel = 1
	}
	var id uint16
	var body []byte
	switch strings.ToLower(strings.TrimSpace(in.Action)) {
	case "rotate":
		if in.Direct > 4 {
			return fmt.Errorf("%w: rotate direction must be 0-4", ErrBadInput)
		}
		id = cmdPTZRotate
		body = []byte{in.Channel, in.Direct, in.Speed}
	case "focus":
		if in.Direct > 1 {
			return fmt.Errorf("%w: focus direction must be 0 or 1", ErrBadInput)
		}
		id = cmdPTZFocus
		body = []byte{in.Channel, in.Direct}
	case "iris":
		if in.Direct > 1 {
			return fmt.Errorf("%w: iris direction must be 0 or 1", ErrBadInput)
		}
		id = cmdPTZIris
		body = []byte{in.Channel, in.Direct}
	case "wiper":
		if in.Direct > 1 {
			return fmt.Errorf("%w: wiper flag must be 0 or 1", ErrBadInput)
		}
		id = cmdPTZWiper
		body = []byte{in.Channel, in.Direct}
	case "infrared":
		if in.Direct > 1 {
			return fmt.Errorf("%w: infrared flag must be 0 or 1", ErrBadInput)
		}
		id = cmdPTZInfrared
		body = []byte{in.Channel, in.Direct}
	case "zoom":
		if in.Direct > 1 {
			return fmt.Errorf("%w: zoom direction must be 0 or 1", ErrBadInput)
		}
		id = cmdPTZZoom
		body = []byte{in.Channel, in.Direct}
	default:
		return fmt.Errorf("%w: action must be rotate, focus, iris, wiper, infrared, or zoom", ErrBadInput)
	}
	if err := s.sender.send(in.Key, id, body, s.cfg.TimeoutDuration()); err != nil {
		return fmt.Errorf("send ptz: %w", err)
	}
	return nil
}

func mediaPrefix(ip string, tcpPort, udpPort int, transport string) []byte {
	rawIP := []byte(ip)
	body := make([]byte, 1+len(rawIP)+4)
	body[0] = byte(len(rawIP))
	copy(body[1:], rawIP)
	pos := 1 + len(rawIP)
	if transport == TransportTCP {
		binary.BigEndian.PutUint16(body[pos:], uint16(tcpPort))
	} else {
		binary.BigEndian.PutUint16(body[pos+2:], uint16(udpPort))
	}
	return body
}

func ftpField(dst []byte, s string) []byte {
	b := []byte(s)
	if len(b) > 255 {
		b = b[:255]
	}
	dst = append(dst, byte(len(b)))
	return append(dst, b...)
}

func bcdTime(t time.Time) []byte {
	s := t.Format("060102150405")
	out := make([]byte, 6)
	for i := 0; i < 6; i++ {
		out[i] = ((s[i*2] - '0') << 4) | (s[i*2+1] - '0')
	}
	return out
}

func parseBCDTime(b []byte) (time.Time, error) {
	if len(b) < 6 {
		return time.Time{}, errors.New("short bcd time")
	}
	var s strings.Builder
	s.Grow(12)
	for i := 0; i < 6; i++ {
		hi, lo := b[i]>>4, b[i]&0x0f
		if hi > 9 || lo > 9 {
			return time.Time{}, errors.New("invalid bcd time")
		}
		s.WriteByte('0' + hi)
		s.WriteByte('0' + lo)
	}
	return time.ParseInLocation("060102150405", s.String(), time.Local)
}

func parseAV(body []byte) (AVProperties, error) {
	if len(body) != 10 {
		return AVProperties{}, errors.New("short 0x1003")
	}
	b := body
	return AVProperties{
		AudioCodec:      b[0],
		AudioChannels:   b[1],
		AudioSampleRate: b[2],
		AudioSampleBits: b[3],
		AudioFrameLen:   int(binary.BigEndian.Uint16(b[4:6])),
		AudioOutput:     b[6],
		VideoCodec:      b[7],
		MaxAudioCh:      b[8],
		MaxVideoCh:      b[9],
	}, nil
}

func parseResources(body []byte) ([]Resource, error) {
	if len(body) < 6 {
		return nil, errors.New("short 0x1205")
	}
	count := binary.BigEndian.Uint32(body[2:6])
	const item = 28
	if uint32(len(body)-6) < count*item {
		return nil, errors.New("truncated 0x1205")
	}
	if count > 10000 {
		return nil, errors.New("0x1205 too large")
	}
	out := make([]Resource, 0, count)
	p := body[6:]
	for i := uint32(0); i < count; i++ {
		start, err := parseBCDTime(p[1:7])
		if err != nil {
			return nil, err
		}
		end, err := parseBCDTime(p[7:13])
		if err != nil {
			return nil, err
		}
		out = append(out, Resource{
			Channel: p[0],
			Start:   start,
			End:     end,
			Alarm:   binary.BigEndian.Uint64(p[13:21]),
			AVType:  p[21],
			Stream:  p[22],
			Storage: p[23],
			Size:    binary.BigEndian.Uint32(p[24:28]),
		})
		p = p[item:]
	}
	return out, nil
}

func applyRegister(t *Terminal, body []byte, version byte) {
	if t == nil || len(body) < 25 {
		return
	}
	t.Province = binary.BigEndian.Uint16(body[0:2])
	t.City = binary.BigEndian.Uint16(body[2:4])
	var maker, model, id []byte
	var colorAt int
	switch {
	case version != 0 && len(body) >= 76:
		maker, model, id = body[4:15], body[15:45], body[45:75]
		colorAt = 75
	case len(body) >= 37:
		maker, model, id = body[4:9], body[9:29], body[29:36]
		colorAt = 36
	default:
		maker, model, id = body[4:9], body[9:17], body[17:24]
		colorAt = 24
	}
	t.Maker = cstring(maker)
	t.Model = cstring(model)
	t.TerminalID = cstring(id)
	if colorAt < len(body) {
		t.PlateColor = body[colorAt]
		t.Plate = plateString(body[colorAt+1:])
	}
}

func cstring(b []byte) string {
	return string(bytes.TrimRight(b, "\x00 "))
}

func plateString(b []byte) string {
	b = bytes.TrimRight(b, "\x00 ")
	if len(b) == 0 || utf8.Valid(b) {
		return string(b)
	}
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(decoded)
}
