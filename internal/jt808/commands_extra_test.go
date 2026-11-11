package jt808

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// recordingSender captures outbound commands so command builders can be tested
// without a live terminal connection.
type recordingSender struct {
	ids    []uint16
	keys   []string
	bodies [][]byte
	err    error
}

func (r *recordingSender) send(key string, id uint16, body []byte, _ time.Duration) error {
	r.keys = append(r.keys, key)
	r.ids = append(r.ids, id)
	r.bodies = append(r.bodies, append([]byte(nil), body...))
	return r.err
}

func (r *recordingSender) last() []byte {
	if len(r.bodies) == 0 {
		return nil
	}
	return r.bodies[len(r.bodies)-1]
}

func commandServer(sender commandSender) *Server {
	return &Server{
		cfg:       Config{MediaIP: "10.9.8.7", MediaTCPPort: 1078, MediaUDPPort: 2078, Transport: TransportTCP, Timeout: "1s"},
		sender:    sender,
		terminals: map[string]*Terminal{},
	}
}

func onlineServer(sender commandSender) *Server {
	s := commandServer(sender)
	s.terminals["1003"] = &Terminal{Key: "1003", Online: true, Authenticated: true}
	return s
}

func TestReportLossValidatesAndSends(t *testing.T) {
	sender := &recordingSender{}
	s := commandServer(sender)

	if err := s.ReportLoss(LossInput{Key: "12ab", Channel: 1}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("invalid SIM error = %v", err)
	}
	if err := s.ReportLoss(LossInput{Key: "1003", Rate: 101}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("rate error = %v", err)
	}
	if err := s.ReportLoss(LossInput{Key: "1003"}); err != nil {
		t.Fatal(err)
	}
	if sender.ids[len(sender.ids)-1] != cmdLoss {
		t.Fatalf("command = %x", sender.ids)
	}
	if got := sender.last(); len(got) != 2 || got[0] != 1 || got[1] != 0 {
		t.Fatalf("body = %x", got)
	}
	sender.err = errors.New("link down")
	if err := s.ReportLoss(LossInput{Key: "1003", Channel: 3, Rate: 12}); err == nil || !strings.Contains(err.Error(), "send 0x9105") {
		t.Fatalf("send error = %v", err)
	}
}

func TestLiveControlValidationAndRevoke(t *testing.T) {
	sender := &recordingSender{}
	s := onlineServer(sender)
	s.grants = map[string]int{grantLive + "1003_1": 1}

	if err := s.LiveControl(LiveControlInput{Key: "bad"}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("invalid SIM error = %v", err)
	}
	if err := s.LiveControl(LiveControlInput{Key: "1003", Control: 5}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("control error = %v", err)
	}
	if err := s.LiveControl(LiveControlInput{Key: "1003", CloseAV: 3}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("close av error = %v", err)
	}
	if err := s.LiveControl(LiveControlInput{Key: "1003", Stream: 2}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("stream error = %v", err)
	}
	if err := s.LiveControl(LiveControlInput{Key: "1003", Channel: 2, Control: 1, CloseAV: 2, Stream: 1}); err != nil {
		t.Fatal(err)
	}
	if sender.ids[len(sender.ids)-1] != cmdStop {
		t.Fatalf("command = %x", sender.ids)
	}
	if got := sender.last(); string(got) != string([]byte{2, 1, 2, 1}) {
		t.Fatalf("body = %x", got)
	}
	// Closing video with "close all" revokes the live grant for the channel.
	if err := s.LiveControl(LiveControlInput{Key: "1003", Channel: 1, Control: 0, CloseAV: 0}); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	_, stillGranted := s.grants[grantLive+"1003_1"]
	s.mu.RUnlock()
	if stillGranted {
		t.Fatal("close command did not revoke the live grant")
	}
	sender.err = errors.New("link down")
	if err := s.LiveControl(LiveControlInput{Key: "1003", Channel: 1}); err == nil || !strings.Contains(err.Error(), "send 0x9102") {
		t.Fatalf("send error = %v", err)
	}
}

func TestPlaybackValidationAndGrants(t *testing.T) {
	start := time.Date(2024, 5, 6, 7, 8, 9, 0, time.Local)
	end := start.Add(time.Hour)
	sender := &recordingSender{}
	s := onlineServer(sender)

	if _, err := s.Playback(PlaybackInput{Key: "bad", Start: start, End: end}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("invalid SIM error = %v", err)
	}
	if _, err := s.Playback(PlaybackInput{Key: "1003"}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("missing range error = %v", err)
	}
	if _, err := s.Playback(PlaybackInput{Key: "1003", Start: end, End: start}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("inverted range error = %v", err)
	}
	if _, err := s.Playback(PlaybackInput{Key: "1003", Start: start, End: end, Transport: "srt"}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("transport error = %v", err)
	}
	id, err := s.Playback(PlaybackInput{Key: "1003", Start: start, End: end, Mode: 1, Speed: 2})
	if err != nil {
		t.Fatal(err)
	}
	if id != "1003_1" || sender.ids[len(sender.ids)-1] != cmdPlayback {
		t.Fatalf("id=%s command=%x", id, sender.ids)
	}
	body := sender.last()
	pos := 1 + int(body[0])
	if string(body[1:pos]) != s.cfg.MediaIP {
		t.Fatalf("media ip encoded as %q", body[1:pos])
	}
	// channel, av, stream, storage, mode, speed followed by two BCD timestamps
	if len(body) != pos+4+6+6+6 {
		t.Fatalf("body length = %d", len(body))
	}
	if body[pos+4] != 1 || body[pos+5] != 0 || body[pos+6] != 0 || body[pos+7] != 0 || body[pos+8] != 1 || body[pos+9] != 2 {
		t.Fatalf("playback fields = %x", body[pos:])
	}
	if !s.AllowStream("1003_1") {
		t.Fatal("playback did not grant the stream")
	}
	// A failing send keeps the existing grant alive.
	sender.err = errors.New("link down")
	if _, err := s.Playback(PlaybackInput{Key: "1003", Start: start, End: end}); err == nil || !strings.Contains(err.Error(), "send 0x9201") {
		t.Fatalf("send error = %v", err)
	}
	if !s.AllowStream("1003_1") {
		t.Fatal("failed playback removed the existing grant")
	}
	// A failing first attempt leaves no grant behind.
	s2 := onlineServer(&recordingSender{err: errors.New("link down")})
	if _, err := s2.Playback(PlaybackInput{Key: "1003", Start: start, End: end}); err == nil {
		t.Fatal("expected send failure")
	}
	if s2.AllowStream("1003_1") {
		t.Fatal("failed playback granted the stream")
	}
}

func TestPlaybackControlVariants(t *testing.T) {
	sender := &recordingSender{}
	s := onlineServer(sender)
	s.grants = map[string]int{grantPB + "1003_1": 1}
	start := time.Date(2024, 1, 2, 3, 4, 5, 0, time.Local)

	if err := s.PlaybackControl(PlaybackControlInput{Key: "x"}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("invalid SIM error = %v", err)
	}
	if err := s.PlaybackControl(PlaybackControlInput{Key: "1003", Control: 7}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("control error = %v", err)
	}
	if err := s.PlaybackControl(PlaybackControlInput{Key: "1003", Control: 5}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("drag error = %v", err)
	}
	if err := s.PlaybackControl(PlaybackControlInput{Key: "1003", Control: 5, DragTo: start}); err != nil {
		t.Fatal(err)
	}
	if got := sender.last(); len(got) != 3+6 {
		t.Fatalf("drag body = %x", got)
	}
	if err := s.PlaybackControl(PlaybackControlInput{Key: "1003", Control: 1, Speed: 4}); err != nil {
		t.Fatal(err)
	}
	if got := sender.last(); len(got) != 3+6 || got[:3][2] != 4 {
		t.Fatalf("control body = %x", got)
	}
	// Control 2 ends playback and revokes the grant.
	if err := s.PlaybackControl(PlaybackControlInput{Key: "1003", Channel: 1, Control: 2}); err != nil {
		t.Fatal(err)
	}
	if s.AllowStream("1003_1") {
		t.Fatal("playback end left the grant")
	}
	sender.err = errors.New("link down")
	if err := s.PlaybackControl(PlaybackControlInput{Key: "1003", Control: 1}); err == nil || !strings.Contains(err.Error(), "send 0x9202") {
		t.Fatalf("send error = %v", err)
	}
}

func TestQueryResourcesValidation(t *testing.T) {
	s := onlineServer(&recordingSender{})
	start := time.Date(2024, 3, 4, 5, 6, 7, 0, time.Local)
	if _, err := s.QueryResources(ResourceQuery{Key: "bad", Start: start, End: start.Add(time.Hour)}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("invalid SIM error = %v", err)
	}
	if _, err := s.QueryResources(ResourceQuery{Key: "1003"}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("missing range error = %v", err)
	}
	if _, err := s.QueryResources(ResourceQuery{Key: "1003", Start: start, End: start.Add(-time.Second)}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("inverted range error = %v", err)
	}
}

func TestUploadValidation(t *testing.T) {
	s := onlineServer(&recordingSender{})
	start := time.Date(2024, 3, 4, 5, 6, 7, 0, time.Local)
	if _, err := s.Upload(UploadInput{Key: "bad"}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("invalid SIM error = %v", err)
	}
	if _, err := s.Upload(UploadInput{Key: "1003"}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("missing range error = %v", err)
	}
	if _, err := s.Upload(UploadInput{Key: "1003", Start: start, End: start.Add(-time.Second)}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("inverted range error = %v", err)
	}
	// No FTP defaults configured: the caller must provide every field.
	if _, err := s.Upload(UploadInput{Key: "1003", Start: start, End: start.Add(time.Hour)}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("missing ftp error = %v", err)
	}
	s.cfg.FTPPort = 2121
	s.cfg.FTPUser = "ftp-user"
	s.cfg.FTPPassword = "ftp-pass"
	_, err := s.Upload(UploadInput{Key: "1003", Start: start, End: start.Add(time.Hour)})
	if err == nil || !strings.Contains(err.Error(), "terminal 1003 offline") {
		t.Fatalf("expected offline send error, got %v", err)
	}
}

func TestUploadControlValidation(t *testing.T) {
	sender := &recordingSender{}
	s := onlineServer(sender)
	if err := s.UploadControl(UploadControlInput{Key: "bad"}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("invalid SIM error = %v", err)
	}
	if err := s.UploadControl(UploadControlInput{Key: "1003", Control: 3}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("control error = %v", err)
	}
	if err := s.UploadControl(UploadControlInput{Key: "1003", Serial: 7, Control: 2}); err != nil {
		t.Fatal(err)
	}
	if sender.ids[len(sender.ids)-1] != cmdUploadCtrl {
		t.Fatalf("command = %x", sender.ids)
	}
	if got := sender.last(); len(got) != 3 || got[0] != 0 || got[1] != 7 || got[2] != 2 {
		t.Fatalf("body = %x", got)
	}
	sender.err = errors.New("link down")
	if err := s.UploadControl(UploadControlInput{Key: "1003"}); err == nil || !strings.Contains(err.Error(), "send 0x9207") {
		t.Fatalf("send error = %v", err)
	}
}

func TestPTZActions(t *testing.T) {
	want := map[string]uint16{
		"rotate": cmdPTZRotate, "focus": cmdPTZFocus, "iris": cmdPTZIris,
		"wiper": cmdPTZWiper, "infrared": cmdPTZInfrared, "zoom": cmdPTZZoom,
	}
	for action, id := range want {
		sender := &recordingSender{}
		s := onlineServer(sender)
		if err := s.PTZ(PTZInput{Key: "1003", Action: " " + strings.ToUpper(action) + " ", Speed: 3}); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if sender.ids[len(sender.ids)-1] != id {
			t.Fatalf("%s command = %x", action, sender.ids)
		}
		body := sender.last()
		if body[0] != 1 {
			t.Fatalf("%s channel = %d", action, body[0])
		}
		if action == "rotate" && (len(body) != 3 || body[2] != 3) {
			t.Fatalf("rotate body = %x", body)
		}
	}
	s := onlineServer(&recordingSender{})
	if err := s.PTZ(PTZInput{Key: "bad"}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("invalid SIM error = %v", err)
	}
	if err := s.PTZ(PTZInput{Key: "1003", Action: "spin"}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("unknown action error = %v", err)
	}
	for action, bad := range map[string]byte{"rotate": 5, "focus": 2, "iris": 2, "wiper": 2, "infrared": 2, "zoom": 2} {
		if err := s.PTZ(PTZInput{Key: "1003", Action: action, Direct: bad}); !errors.Is(err, ErrBadInput) {
			t.Fatalf("%s direction error = %v", action, err)
		}
	}
	sender := &recordingSender{err: errors.New("link down")}
	failing := onlineServer(sender)
	if err := failing.PTZ(PTZInput{Key: "1003", Action: "focus"}); err == nil || !strings.Contains(err.Error(), "send ptz") {
		t.Fatalf("send error = %v", err)
	}
}

func TestMediaPrefixTransportEncoding(t *testing.T) {
	tcp := mediaPrefix("192.0.2.5", 1078, 2078, TransportTCP)
	if tcp[0] != 9 || string(tcp[1:10]) != "192.0.2.5" {
		t.Fatalf("tcp prefix = %x", tcp)
	}
	if got := uint16(tcp[10])<<8 | uint16(tcp[11]); got != 1078 {
		t.Fatalf("tcp port = %d", got)
	}
	if tcp[12] != 0 || tcp[13] != 0 {
		t.Fatalf("tcp reserved = %x", tcp[10:])
	}
	udp := mediaPrefix("10.0.0.1", 1078, 2078, TransportUDP)
	pos := 1 + len("10.0.0.1")
	if udp[pos] != 0 || udp[pos+1] != 0 {
		t.Fatalf("udp reserved = %x", udp[pos:])
	}
	if got := uint16(udp[pos+2])<<8 | uint16(udp[pos+3]); got != 2078 {
		t.Fatalf("udp port = %d", got)
	}
}

func TestFTPFieldTruncatesAt255(t *testing.T) {
	got := ftpField(nil, strings.Repeat("a", 300))
	if got[0] != 255 || len(got) != 256 {
		t.Fatalf("len=%d first=%d", len(got), got[0])
	}
	got = ftpField([]byte{9}, "abc")
	if string(got) != "\x09\x03abc" {
		t.Fatalf("field = %x", got)
	}
}

func TestBCDTimeRoundTrip(t *testing.T) {
	at := time.Date(2024, 12, 31, 23, 59, 58, 0, time.Local)
	encoded := bcdTime(at)
	if len(encoded) != 6 || encoded[0] != 0x24 || encoded[1] != 0x12 || encoded[5] != 0x58 {
		t.Fatalf("encoded = %x", encoded)
	}
	decoded, err := parseBCDTime(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.Equal(at) {
		t.Fatalf("decoded = %s", decoded)
	}
	if _, err := parseBCDTime([]byte{1, 2, 3}); err == nil {
		t.Fatal("short input accepted")
	}
	if _, err := parseBCDTime([]byte{0x1a, 1, 2, 3, 4, 5}); err == nil || !strings.Contains(err.Error(), "invalid bcd") {
		t.Fatalf("bad nibble error = %v", err)
	}
}

func TestParseAVRejectsShortBody(t *testing.T) {
	if _, err := parseAV(make([]byte, 9)); err == nil {
		t.Fatal("short body accepted")
	}
	props, err := parseAV([]byte{1, 2, 3, 4, 0, 10, 5, 6, 7, 8})
	if err != nil {
		t.Fatal(err)
	}
	if props.AudioFrameLen != 10 || props.VideoCodec != 6 || props.MaxVideoCh != 8 {
		t.Fatalf("props = %+v", props)
	}
}

func TestParseResourcesValidation(t *testing.T) {
	if _, err := parseResources([]byte{0, 0, 0}); err == nil {
		t.Fatal("short body accepted")
	}
	truncated := make([]byte, 6+28)
	truncated[5] = 2 // count = 2 but only one item present
	if _, err := parseResources(truncated); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncated error = %v", err)
	}
	huge := make([]byte, 6+10001*28)
	huge[4] = 0x27
	huge[5] = 0x11 // count = 10001
	if _, err := parseResources(huge); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("too large error = %v", err)
	}

	start := time.Date(2024, 5, 6, 7, 8, 9, 0, time.Local)
	end := start.Add(time.Hour)
	body := []byte{0, 0, 0, 0, 0, 1}
	item := make([]byte, 28)
	item[0] = 3
	copy(item[1:7], bcdTime(start))
	copy(item[7:13], bcdTime(end))
	item[13] = 1 // alarm high byte
	item[20] = 2 // alarm low byte
	item[21] = 1
	item[22] = 2
	item[23] = 3
	item[27] = 4 // size low byte
	body = append(body, item...)
	resources, err := parseResources(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 {
		t.Fatalf("resources = %d", len(resources))
	}
	r := resources[0]
	if r.Channel != 3 || !r.Start.Equal(start) || !r.End.Equal(end) || r.Alarm != (1<<56|2) || r.AVType != 1 || r.Stream != 2 || r.Storage != 3 || r.Size != 4 {
		t.Fatalf("resource = %+v", r)
	}

	badStart := append([]byte(nil), body...)
	badStart[6+1] = 0xaa
	if _, err := parseResources(badStart); err == nil {
		t.Fatal("invalid start timestamp accepted")
	}
	badEnd := append([]byte(nil), body...)
	badEnd[6+7] = 0xaa
	if _, err := parseResources(badEnd); err == nil {
		t.Fatal("invalid end timestamp accepted")
	}
}

func TestPlateStringDecoding(t *testing.T) {
	if got := plateString(nil); got != "" {
		t.Fatalf("empty plate = %q", got)
	}
	if got := plateString([]byte("AB1234\x00 ")); got != "AB1234" {
		t.Fatalf("ascii plate = %q", got)
	}
	// UTF-8 input is returned unchanged.
	if got := plateString([]byte("粤B1234")); got != "粤B1234" {
		t.Fatalf("utf8 plate = %q", got)
	}
	// GBK-encoded "车牌" cannot be valid UTF-8 and is transcoded.
	gbk := []byte{0xb3, 0xb5, 0xc5, 0xc6}
	if got := plateString(gbk); got != "车牌" {
		t.Fatalf("gbk plate = %q", got)
	}
	if got := cstring([]byte(" abc \x00\x00")); got != " abc" {
		t.Fatalf("cstring = %q", got)
	}
}

func TestApplyRegisterVersions(t *testing.T) {
	applyRegister(nil, make([]byte, 40), 1)
	short := &Terminal{}
	applyRegister(short, make([]byte, 10), 0)
	if short.Province != 0 || short.Maker != "" {
		t.Fatalf("short body applied: %+v", short)
	}

	t.Run("2013", func(t *testing.T) {
		body := make([]byte, 37)
		body[0], body[1] = 0, 34
		body[2], body[3] = 1, 8
		copy(body[4:9], []byte("MAKER"))
		copy(body[9:29], []byte("MODEL-13"))
		copy(body[29:36], []byte("A123456"))
		body[36] = 2
		terminal := &Terminal{}
		applyRegister(terminal, append(body, []byte("PLATE1")...), 0)
		if terminal.Province != 34 || terminal.City != 264 || terminal.Maker != "MAKER" || terminal.Model != "MODEL-13" || terminal.TerminalID != "A123456" || terminal.PlateColor != 2 || terminal.Plate != "PLATE1" {
			t.Fatalf("terminal = %+v", terminal)
		}
	})

	t.Run("2019", func(t *testing.T) {
		body := make([]byte, 76)
		copy(body[4:15], []byte("MAKER2019"))
		copy(body[15:45], []byte("MODEL-201906"))
		copy(body[45:75], []byte("TERM2019"))
		body[75] = 5
		terminal := &Terminal{}
		applyRegister(terminal, append(body, []byte("PLATE2")...), 1)
		if terminal.Maker != "MAKER2019" || terminal.Model != "MODEL-201906" || terminal.TerminalID != "TERM2019" || terminal.PlateColor != 5 || terminal.Plate != "PLATE2" {
			t.Fatalf("terminal = %+v", terminal)
		}
	})

	t.Run("legacy", func(t *testing.T) {
		body := make([]byte, 25)
		copy(body[4:9], []byte("MAKER"))
		copy(body[9:17], []byte("MODEL"))
		copy(body[17:24], []byte("TERM000"))
		terminal := &Terminal{}
		applyRegister(terminal, body, 0)
		if terminal.Maker != "MAKER" || terminal.Model != "MODEL" || terminal.TerminalID != "TERM000" {
			t.Fatalf("terminal = %+v", terminal)
		}
		// No plate colour byte: the plate must stay untouched.
		if terminal.PlateColor != 0 || terminal.Plate != "" {
			t.Fatalf("plate decoded without colour byte: %+v", terminal)
		}
	})
}

func TestQueryAVRejectsInvalidSIM(t *testing.T) {
	s := commandServer(&recordingSender{})
	if _, err := s.QueryAV("nope"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("error = %v", err)
	}
	if _, err := s.QueryAV("1003"); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("offline error = %v", err)
	}
}
