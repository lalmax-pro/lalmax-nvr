package jt808

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// discardConn is a net.Conn that accepts writes and discards them, which lets
// protocol branches be exercised without wiring a pipe for every write.
type discardConn struct {
	writes int
	closed int
}

func (c *discardConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *discardConn) Write(p []byte) (int, error)      { c.writes++; return len(p), nil }
func (c *discardConn) Close() error                     { c.closed++; return nil }
func (c *discardConn) LocalAddr() net.Addr              { return dummyAddr("local") }
func (c *discardConn) RemoteAddr() net.Addr             { return dummyAddr("remote") }
func (c *discardConn) SetDeadline(time.Time) error      { return nil }
func (c *discardConn) SetReadDeadline(time.Time) error  { return nil }
func (c *discardConn) SetWriteDeadline(time.Time) error { return nil }

type dummyAddr string

func (d dummyAddr) Network() string { return "dummy" }
func (d dummyAddr) String() string  { return string(d) }

type stubListener struct{ closed int }

func (l *stubListener) Accept() (net.Conn, error) { return nil, io.EOF }
func (l *stubListener) Close() error              { l.closed++; return nil }
func (l *stubListener) Addr() net.Addr            { return dummyAddr("stub") }

// authedPipeWith starts a server over an in-memory pipe and authenticates a
// terminal with SIM 1003.
func authedPipeWith(t *testing.T, cfg Config) (*Server, net.Conn, <-chan struct{}) {
	t.Helper()
	if cfg.MediaIP == "" {
		cfg.MediaIP = "127.0.0.1"
	}
	cfg.Enabled = true
	cfg.AuthCode = "secret"
	if cfg.Timeout == "" {
		cfg.Timeout = "1s"
	}
	s, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() { s.handle(server); close(done) }()
	t.Cleanup(func() { client.Close(); <-done })
	sendPacket(t, client, packet{id: cmdAuth, sim: "1003", body: []byte("secret")})
	readFrame(t, client)
	waitAuthenticated(t, s)
	return s, client, done
}

func waitAuthenticated(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		terminals := s.ListTerminals()
		if len(terminals) == 1 && terminals[0].Authenticated && terminals[0].Online {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("terminal was not authenticated")
}

func ackFor(p packet, result byte) packet {
	body := make([]byte, 5)
	binary.BigEndian.PutUint16(body, p.seq)
	binary.BigEndian.PutUint16(body[2:], p.id)
	body[4] = result
	return packet{id: cmdGeneralReply, sim: p.sim, seq: p.seq + 1, body: body}
}

func TestServerSendRejectsTerminalResult(t *testing.T) {
	s, client, _ := authedPipeWith(t, Config{})
	result := make(chan error, 1)
	go func() { _, err := s.Play(PlayInput{Key: "1003", Channel: 1}); result <- err }()
	request := readFrame(t, client)
	if request.id != cmdPlay {
		t.Fatalf("command = %x", request.id)
	}
	sendPacket(t, client, ackFor(request, 1))
	err := <-result
	if err == nil || !strings.Contains(err.Error(), "terminal rejected command") {
		t.Fatalf("error = %v", err)
	}
}

func TestUploadAndQueryResourcesOverPipe(t *testing.T) {
	start := time.Date(2024, 5, 6, 7, 8, 9, 0, time.Local)
	end := start.Add(time.Hour)

	t.Run("upload", func(t *testing.T) {
		s, client, _ := authedPipeWith(t, Config{FTPPort: 2121, FTPUser: "ftp", FTPPassword: "pass"})
		result := make(chan error, 1)
		var seq uint16
		go func() {
			var err error
			seq, err = s.Upload(UploadInput{Key: "1003", Start: start, End: end})
			result <- err
		}()
		request := readFrame(t, client)
		if request.id != cmdFileUpload {
			t.Fatalf("command = %x", request.id)
		}
		body := request.body
		if body[0] != uint8(len("127.0.0.1")) || string(body[1:1+len("127.0.0.1")]) != "127.0.0.1" {
			t.Fatalf("host field = %x", body[:12])
		}
		pos := 1 + len("127.0.0.1")
		if binary.BigEndian.Uint16(body[pos:]) != 2121 {
			t.Fatalf("port = %d", binary.BigEndian.Uint16(body[pos:]))
		}
		if !bytes.Contains(body, []byte("/jt1078/1003/1")) {
			t.Fatalf("default path missing from %x", body)
		}
		sendPacket(t, client, ackFor(request, 0))
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		if seq == 0 {
			t.Fatal("upload sequence was not returned")
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			terminals := s.ListTerminals()
			if len(terminals) == 1 && len(terminals[0].Uploads) == 1 && terminals[0].Uploads[0].Serial == seq {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("upload task not recorded: %+v", s.ListTerminals())
	})

	t.Run("rejected", func(t *testing.T) {
		s, client, _ := authedPipeWith(t, Config{FTPPort: 2121, FTPUser: "ftp", FTPPassword: "pass"})
		result := make(chan error, 1)
		go func() {
			_, err := s.Upload(UploadInput{Key: "1003", Start: start, End: end})
			result <- err
		}()
		request := readFrame(t, client)
		sendPacket(t, client, ackFor(request, 2))
		err := <-result
		if err == nil || !strings.Contains(err.Error(), "terminal rejected command") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("resources", func(t *testing.T) {
		s, client, _ := authedPipeWith(t, Config{})
		result := make(chan []Resource, 1)
		errCh := make(chan error, 1)
		go func() {
			resources, err := s.QueryResources(ResourceQuery{Key: "1003", Channel: 2, Start: start, End: end, AVType: 1, StreamType: 2, Storage: 3})
			result <- resources
			errCh <- err
		}()
		request := readFrame(t, client)
		if request.id != cmdResourceQuery {
			t.Fatalf("command = %x", request.id)
		}
		if len(request.body) != 24 || request.body[0] != 2 || request.body[21] != 1 || request.body[22] != 2 || request.body[23] != 3 {
			t.Fatalf("query body = %x", request.body)
		}
		item := make([]byte, 28)
		item[0] = 2
		copy(item[1:7], bcdTime(start))
		copy(item[7:13], bcdTime(end))
		item[23] = 3
		payload := make([]byte, 6)
		binary.BigEndian.PutUint16(payload, request.seq)
		binary.BigEndian.PutUint32(payload[2:], 1)
		payload = append(payload, item...)
		sendPacket(t, client, packet{id: cmdResourceList, sim: "1003", seq: request.seq + 1, body: payload})
		ack := readFrame(t, client)
		if ack.id != cmdGeneralAck {
			t.Fatalf("ack id = %x", ack.id)
		}
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
		resources := <-result
		if len(resources) != 1 || resources[0].Channel != 2 || !resources[0].Start.Equal(start) {
			t.Fatalf("resources = %+v", resources)
		}
	})
}

func TestSendWaitErrors(t *testing.T) {
	base := func() *Server {
		s := &Server{
			cfg:       Config{Transport: TransportTCP, Timeout: "50ms"},
			sessions:  map[string]*session{},
			terminals: map[string]*Terminal{},
		}
		s.sender = s
		return s
	}

	t.Run("no session", func(t *testing.T) {
		s := base()
		if _, err := s.Play(PlayInput{Key: "1003"}); err == nil || !strings.Contains(err.Error(), "offline or unauthenticated") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("unauthenticated terminal", func(t *testing.T) {
		s := base()
		s.sessions["1003"] = &session{conn: &discardConn{}, pending: map[uint16]waiter{}}
		s.terminals["1003"] = &Terminal{Key: "1003", Online: true}
		if _, err := s.Play(PlayInput{Key: "1003"}); err == nil || !strings.Contains(err.Error(), "offline or unauthenticated") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("stopped server", func(t *testing.T) {
		s := base()
		s.sessions["1003"] = &session{conn: &discardConn{}, pending: map[uint16]waiter{}}
		s.terminals["1003"] = &Terminal{Key: "1003", Online: true, Authenticated: true}
		s.stopped = true
		if _, err := s.Play(PlayInput{Key: "1003"}); err == nil || !strings.Contains(err.Error(), "offline or unauthenticated") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("pending closed", func(t *testing.T) {
		s := base()
		s.sessions["1003"] = &session{conn: &discardConn{}, pending: nil}
		s.terminals["1003"] = &Terminal{Key: "1003", Online: true, Authenticated: true}
		if _, err := s.Play(PlayInput{Key: "1003"}); !errors.Is(err, io.EOF) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("write failure removes waiter", func(t *testing.T) {
		s := base()
		conn := &failingWriteConn{}
		s.sessions["1003"] = &session{conn: conn, pending: map[uint16]waiter{}}
		s.terminals["1003"] = &Terminal{Key: "1003", Online: true, Authenticated: true}
		if _, err := s.Play(PlayInput{Key: "1003"}); err == nil {
			t.Fatal("write failure not reported")
		}
		s.mu.RLock()
		pending := len(s.sessions["1003"].pending)
		s.mu.RUnlock()
		if pending != 0 {
			t.Fatalf("waiter leaked: %d", pending)
		}
	})

	t.Run("response timeout", func(t *testing.T) {
		s, client, _ := authedPipeWith(t, Config{Timeout: "40ms"})
		result := make(chan error, 1)
		go func() { _, err := s.Play(PlayInput{Key: "1003"}); result <- err }()
		_ = readFrame(t, client)
		err := <-result
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v", err)
		}
		s.mu.RLock()
		ss := s.sessions["1003"]
		s.mu.RUnlock()
		ss.mu.Lock()
		pending := len(ss.pending)
		ss.mu.Unlock()
		if pending != 0 {
			t.Fatalf("waiter leaked after timeout: %d", pending)
		}
	})

	t.Run("disconnect closes waiter", func(t *testing.T) {
		s, client, _ := authedPipeWith(t, Config{Timeout: "2s"})
		result := make(chan error, 1)
		go func() { _, err := s.Play(PlayInput{Key: "1003"}); result <- err }()
		_ = readFrame(t, client)
		client.Close()
		select {
		case err := <-result:
			if !errors.Is(err, io.EOF) {
				t.Fatalf("error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("waiter was not released on disconnect")
		}
	})
}

// failingWriteConn fails every write, exercising sendWait's write error path.
type failingWriteConn struct{ discardConn }

func (c *failingWriteConn) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestPushFragmentGrouping(t *testing.T) {
	ss := &session{frags: map[uint16]*fragGroup{}}

	if _, ok := ss.pushFrag(packet{id: cmdPlay, total: 2, index: 0}); ok {
		t.Fatal("index 0 accepted")
	}
	if _, ok := ss.pushFrag(packet{id: cmdPlay, total: 2, index: 3}); ok {
		t.Fatal("index above total accepted")
	}
	if _, ok := ss.pushFrag(packet{id: cmdPlay, total: 65, index: 1}); ok {
		t.Fatal("total above limit accepted")
	}
	if _, ok := ss.pushFrag(packet{id: cmdPlay, seq: 7, total: 2, index: 2, body: []byte("B")}); ok {
		t.Fatal("partial group assembled early")
	}
	assembled, ok := ss.pushFrag(packet{id: cmdPlay, seq: 9, total: 2, index: 1, body: []byte("A")})
	if !ok || string(assembled.body) != "AB" || assembled.seq != 9 || assembled.total != 0 || assembled.index != 0 || !assembled.noAck {
		t.Fatalf("assembled = %+v ok=%v", assembled, ok)
	}
	if len(ss.frags) != 0 {
		t.Fatal("completed group was not removed")
	}

	// A different total revives the group from scratch.
	if _, ok := ss.pushFrag(packet{id: cmdStop, seq: 1, total: 2, index: 1, body: []byte("A")}); ok {
		t.Fatal("partial group assembled early")
	}
	if _, ok := ss.pushFrag(packet{id: cmdStop, seq: 2, total: 3, index: 1, body: []byte("X")}); ok {
		t.Fatal("partial group assembled early")
	}
	if _, ok := ss.pushFrag(packet{id: cmdStop, seq: 3, total: 3, index: 2, body: []byte("Y")}); ok {
		t.Fatal("partial group assembled early")
	}
	assembled, ok = ss.pushFrag(packet{id: cmdStop, seq: 4, total: 3, index: 3, body: []byte("Z")})
	if !ok || string(assembled.body) != "XYZ" || assembled.seq != 2 {
		t.Fatalf("assembled = %+v ok=%v", assembled, ok)
	}

	// Duplicate fragments replace the previous payload instead of adding up.
	ss = &session{frags: map[uint16]*fragGroup{}}
	if _, ok := ss.pushFrag(packet{id: cmdPlay, total: 2, index: 1, body: []byte("LONG")}); ok {
		t.Fatal("partial group assembled early")
	}
	if _, ok := ss.pushFrag(packet{id: cmdPlay, total: 2, index: 1, body: []byte("BB")}); ok {
		t.Fatal("duplicate fragment completed the group")
	}
	assembled, ok = ss.pushFrag(packet{id: cmdPlay, total: 2, index: 2, body: []byte("C")})
	if !ok || string(assembled.body) != "BBC" {
		t.Fatalf("assembled = %+v ok=%v", assembled, ok)
	}

	// Exceeding the 1 MiB reassembly budget drops the group.
	ss = &session{frags: map[uint16]*fragGroup{}}
	if _, ok := ss.pushFrag(packet{id: cmdPlay, total: 3, index: 1, body: make([]byte, 1<<20)}); ok {
		t.Fatal("partial group assembled early")
	}
	if _, ok := ss.pushFrag(packet{id: cmdPlay, total: 3, index: 2, body: []byte{1}}); ok {
		t.Fatal("oversized group accepted")
	}
	if len(ss.frags) != 0 {
		t.Fatal("oversized group was kept")
	}
}

func TestFragmentFlowOverPipe(t *testing.T) {
	_, client, _ := authedPipeWith(t, Config{})
	sendPacket(t, client, packet{id: cmdHeartbeat, sim: "1003", seq: 1, total: 2, index: 1, body: []byte{0x01}})
	first := readFrame(t, client)
	sendPacket(t, client, packet{id: cmdHeartbeat, sim: "1003", seq: 2, total: 2, index: 2, body: []byte{0x02}})
	second := readFrame(t, client)
	third := readFrame(t, client)
	for i, p := range []packet{first, second, third} {
		if p.id != cmdGeneralAck {
			t.Fatalf("frame %d id = %x", i, p.id)
		}
	}
}

func newDirectServer(code string) (*Server, *session, *discardConn) {
	conn := &discardConn{}
	s := &Server{
		cfg:       Config{Enabled: true, MediaIP: "127.0.0.1", AuthCode: code},
		sessions:  map[string]*session{},
		terminals: map[string]*Terminal{},
		grants:    map[string]int{},
	}
	s.sender = s
	ss := &session{conn: conn, sim: "1003", pending: map[uint16]waiter{}, frags: map[uint16]*fragGroup{}}
	s.terminals["1003"] = &Terminal{Key: "1003", Online: true, LastSeen: time.Now(), JoinedAt: time.Now()}
	return s, ss, conn
}

func TestHandlePacketRegisterAndLogout(t *testing.T) {
	s, ss, conn := newDirectServer("secret")
	short := make([]byte, 24)
	s.handlePacket(ss, packet{id: cmdRegister, sim: "1003", seq: 1, body: short})
	if conn.writes != 0 {
		t.Fatal("short registration was acknowledged")
	}

	register := make([]byte, 25)
	copy(register[4:9], []byte("MAKER"))
	s.handlePacket(ss, packet{id: cmdRegister, sim: "1003", seq: 2, body: register})
	if conn.writes != 1 {
		t.Fatalf("registration writes = %d", conn.writes)
	}
	if maker := s.terminals["1003"].Maker; maker != "MAKER" {
		t.Fatalf("registration not applied: %q", maker)
	}

	s.terminals["1003"].Authenticated = true
	s.handlePacket(ss, packet{id: cmdLogout, sim: "1003", seq: 3})
	if s.terminals["1003"].Online || s.terminals["1003"].Authenticated {
		t.Fatal("logout left the terminal online")
	}
	if conn.closed != 1 {
		t.Fatalf("logout did not close the connection: %d", conn.closed)
	}
}

func TestHandlePacketAuthVariants(t *testing.T) {
	t.Run("2019 header truncates to announced size", func(t *testing.T) {
		s, ss, conn := newDirectServer("secret")
		ss.version = 1
		s.handlePacket(ss, packet{id: cmdAuth, sim: "1003", seq: 1, version: 1})
		if conn.writes != 0 || s.terminals["1003"].Authenticated {
			t.Fatal("empty 2019 auth body was accepted")
		}
		s.handlePacket(ss, packet{id: cmdAuth, sim: "1003", seq: 2, version: 1, body: []byte{6, 'x'}})
		if conn.writes != 0 || s.terminals["1003"].Authenticated {
			t.Fatal("oversized length prefix was accepted")
		}
		s.handlePacket(ss, packet{id: cmdAuth, sim: "1003", seq: 3, version: 1, body: []byte{6, 's', 'e', 'c', 'r', 'e', 't'}})
		if conn.writes != 1 || !s.terminals["1003"].Authenticated {
			t.Fatalf("2019 auth not accepted: writes=%d auth=%v", conn.writes, s.terminals["1003"].Authenticated)
		}
	})

	t.Run("failed auth clears grants", func(t *testing.T) {
		s, ss, conn := newDirectServer("secret")
		s.terminals["1003"].Authenticated = true
		s.grants[grantPB+"1003_1"] = 1
		s.handlePacket(ss, packet{id: cmdAuth, sim: "1003", seq: 1, body: []byte("wrong")})
		if s.terminals["1003"].Authenticated {
			t.Fatal("invalid auth accepted")
		}
		if len(s.grants) != 0 {
			t.Fatalf("grants survived failed auth: %v", s.grants)
		}
		if conn.writes != 1 {
			t.Fatalf("ack writes = %d", conn.writes)
		}
	})
}

func TestHandlePacketRepliesAndReports(t *testing.T) {
	t.Run("general reply", func(t *testing.T) {
		s, ss, conn := newDirectServer("secret")
		s.handlePacket(ss, packet{id: cmdGeneralReply, sim: "1003", seq: 1, body: []byte{1, 2}})
		if conn.writes != 0 {
			t.Fatal("short general reply acknowledged")
		}
		ch := make(chan inbound, 1)
		ss.pending[5] = waiter{replyTo: cmdGeneralReply, ch: ch}
		body := make([]byte, 5)
		binary.BigEndian.PutUint16(body, 5)
		body[4] = 0
		s.handlePacket(ss, packet{id: cmdGeneralReply, sim: "1003", seq: 2, body: body})
		select {
		case got := <-ch:
			if got.result != 0 {
				t.Fatalf("result = %d", got.result)
			}
		default:
			t.Fatal("reply was not delivered")
		}
		// A delivery for an unknown sequence is ignored.
		s.deliver(ss, 99, cmdGeneralReply, inbound{})
	})

	t.Run("AV report", func(t *testing.T) {
		s, ss, conn := newDirectServer("secret")
		s.handlePacket(ss, packet{id: cmdAVReport, sim: "1003", seq: 1, body: make([]byte, 9)})
		if conn.writes != 0 {
			t.Fatal("invalid AV report acknowledged")
		}
		ch := make(chan inbound, 1)
		ss.pending[7] = waiter{replyTo: cmdAVReport, ch: ch}
		body := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		s.handlePacket(ss, packet{id: cmdAVReport, sim: "1003", seq: 2, body: body})
		select {
		case got := <-ch:
			if len(got.body) != 10 {
				t.Fatalf("AV body = %x", got.body)
			}
		default:
			t.Fatal("AV report was not delivered")
		}
		if conn.writes != 1 {
			t.Fatalf("AV ack writes = %d", conn.writes)
		}
		// Reassembled packets are already acknowledged.
		ss.pending[8] = waiter{replyTo: cmdAVReport, ch: ch}
		s.handlePacket(ss, packet{id: cmdAVReport, sim: "1003", seq: 3, body: body, noAck: true})
		if conn.writes != 1 {
			t.Fatalf("noAck AV report acknowledged: %d", conn.writes)
		}
	})
}

func TestHandlePacketResourceAndUploadReplies(t *testing.T) {
	t.Run("short resource list", func(t *testing.T) {
		s, ss, conn := newDirectServer("secret")
		s.handlePacket(ss, packet{id: cmdResourceList, sim: "1003", seq: 1, body: []byte{0}})
		if conn.writes != 1 {
			t.Fatalf("ack writes = %d", conn.writes)
		}
	})

	t.Run("upload done", func(t *testing.T) {
		s, ss, conn := newDirectServer("secret")
		s.terminals["1003"].Uploads = []UploadTask{{Serial: 9}}
		ch := make(chan inbound, 1)
		ss.pending[9] = waiter{replyTo: cmdUploadDone, ch: ch}
		body := make([]byte, 4)
		binary.BigEndian.PutUint16(body, 9)
		body[2] = 0
		s.handlePacket(ss, packet{id: cmdUploadDone, sim: "1003", seq: 2, body: body})
		select {
		case <-ch:
		default:
			t.Fatal("upload result was not delivered")
		}
		if !s.terminals["1003"].Uploads[0].Done || s.terminals["1003"].Uploads[0].Result != 0 {
			t.Fatalf("upload task = %+v", s.terminals["1003"].Uploads[0])
		}
		if conn.writes != 1 {
			t.Fatalf("ack writes = %d", conn.writes)
		}
		// Already acknowledged reassembled packets do not produce another ack.
		s.handlePacket(ss, packet{id: cmdUploadDone, sim: "1003", seq: 3, body: body, noAck: true})
		if conn.writes != 1 {
			t.Fatalf("noAck upload result acknowledged: %d", conn.writes)
		}
	})

	t.Run("heartbeat and unknown commands", func(t *testing.T) {
		s, ss, conn := newDirectServer("secret")
		s.handlePacket(ss, packet{id: cmdHeartbeat, sim: "1003", seq: 1, body: nil})
		s.handlePacket(ss, packet{id: 0x9999, sim: "1003", seq: 2, body: nil})
		if conn.writes != 2 {
			t.Fatalf("ack writes = %d", conn.writes)
		}
		s.handlePacket(ss, packet{id: 0x9999, sim: "1003", seq: 3, body: nil, noAck: true})
		if conn.writes != 2 {
			t.Fatalf("noAck unknown command acknowledged: %d", conn.writes)
		}
	})
}

func TestHandlePacketSessionBinding(t *testing.T) {
	t.Run("new session registers", func(t *testing.T) {
		s := &Server{cfg: Config{Enabled: true, AuthCode: "secret"}, sessions: map[string]*session{}, terminals: map[string]*Terminal{}, grants: map[string]int{}}
		s.sender = s
		conn := &discardConn{}
		ss := &session{conn: conn, pending: map[uint16]waiter{}, frags: map[uint16]*fragGroup{}}
		s.handlePacket(ss, packet{id: cmdHeartbeat, sim: "1003", seq: 1})
		if ss.sim != "1003" || s.sessions["1003"] != ss || s.terminals["1003"] == nil {
			t.Fatalf("session not registered: sim=%q", ss.sim)
		}
	})

	t.Run("duplicate SIM is rejected", func(t *testing.T) {
		s := &Server{cfg: Config{Enabled: true, AuthCode: "secret"}, sessions: map[string]*session{}, terminals: map[string]*Terminal{}, grants: map[string]int{}}
		s.sender = s
		existing := &session{conn: &discardConn{}, sim: "1003", pending: map[uint16]waiter{}}
		s.sessions["1003"] = existing
		s.terminals["1003"] = &Terminal{Key: "1003", Online: true}
		conn := &discardConn{}
		ss := &session{conn: conn, pending: map[uint16]waiter{}, frags: map[uint16]*fragGroup{}}
		s.handlePacket(ss, packet{id: cmdHeartbeat, sim: "1003", seq: 1})
		if conn.closed != 1 {
			t.Fatalf("duplicate SIM connection not closed: %d", conn.closed)
		}
	})

	t.Run("stopped server rejects new sessions", func(t *testing.T) {
		s := &Server{cfg: Config{Enabled: true, AuthCode: "secret"}, sessions: map[string]*session{}, terminals: map[string]*Terminal{}, grants: map[string]int{}}
		s.sender = s
		s.stopped = true
		conn := &discardConn{}
		ss := &session{conn: conn, pending: map[uint16]waiter{}, frags: map[uint16]*fragGroup{}}
		s.handlePacket(ss, packet{id: cmdHeartbeat, sim: "1003", seq: 1})
		if conn.closed != 1 {
			t.Fatalf("stopped server accepted a connection: %d", conn.closed)
		}
	})

	t.Run("SIM change is rejected", func(t *testing.T) {
		s := &Server{cfg: Config{Enabled: true, AuthCode: "secret"}, sessions: map[string]*session{}, terminals: map[string]*Terminal{}, grants: map[string]int{}}
		s.sender = s
		conn := &discardConn{}
		ss := &session{conn: conn, sim: "1003", pending: map[uint16]waiter{}, frags: map[uint16]*fragGroup{}}
		s.handlePacket(ss, packet{id: cmdHeartbeat, sim: "1004", seq: 1})
		if conn.closed != 1 {
			t.Fatalf("SIM change not rejected: %d", conn.closed)
		}
	})

	t.Run("invalid SIM is ignored", func(t *testing.T) {
		s := &Server{cfg: Config{Enabled: true, AuthCode: "secret"}, sessions: map[string]*session{}, terminals: map[string]*Terminal{}, grants: map[string]int{}}
		s.sender = s
		conn := &discardConn{}
		ss := &session{conn: conn, pending: map[uint16]waiter{}, frags: map[uint16]*fragGroup{}}
		s.handlePacket(ss, packet{id: cmdHeartbeat, sim: "abc", seq: 1})
		if conn.writes != 0 || conn.closed != 0 {
			t.Fatalf("invalid SIM handled: writes=%d closed=%d", conn.writes, conn.closed)
		}
	})
}

func TestHandleSkipsStoppedAndOversizedFrames(t *testing.T) {
	t.Run("stopped server drops connection", func(t *testing.T) {
		s, err := NewServer(Config{Enabled: true, MediaIP: "127.0.0.1", AuthCode: "secret"})
		if err != nil {
			t.Fatal(err)
		}
		s.stopped = true
		server, client := net.Pipe()
		done := make(chan struct{})
		go func() { s.handle(server); close(done) }()
		client.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("handle did not return for a stopped server")
		}
	})

	t.Run("oversized frame is discarded", func(t *testing.T) {
		s, err := NewServer(Config{Enabled: true, MediaIP: "127.0.0.1", AuthCode: "secret"})
		if err != nil {
			t.Fatal(err)
		}
		server, client := net.Pipe()
		done := make(chan struct{})
		go func() { s.handle(server); close(done) }()
		junk := append([]byte{0x7e}, bytes.Repeat([]byte{0xaa}, 9000)...)
		if _, err := client.Write(junk); err != nil {
			t.Fatal(err)
		}
		client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("handle did not recover from an oversized frame")
		}
	})
}

func TestNoteUpload(t *testing.T) {
	s := &Server{terminals: map[string]*Terminal{}}
	s.noteUpload("1003", nil)
	body := make([]byte, 3)
	body[2] = 1
	s.noteUpload("1003", body) // unknown terminal
	body[1] = 7
	s.noteUpload("1003", body) // still unknown
	s.terminals["1003"] = &Terminal{Key: "1003", Uploads: []UploadTask{{Serial: 7}}}
	s.noteUpload("1003", body)
	if !s.terminals["1003"].Uploads[0].Done || s.terminals["1003"].Uploads[0].Result != 1 || s.terminals["1003"].Uploads[0].At.IsZero() {
		t.Fatalf("upload = %+v", s.terminals["1003"].Uploads[0])
	}
	other := make([]byte, 3)
	other[1] = 8
	s.noteUpload("1003", other)
	if len(s.terminals["1003"].Uploads) != 1 {
		t.Fatal("unknown serial created a task")
	}
}

func TestListTerminalsSnapshot(t *testing.T) {
	s := &Server{terminals: map[string]*Terminal{
		"2000": {Key: "2000", Online: true},
		"1000": {Key: "1000", Online: true, AV: &AVProperties{AudioCodec: 6}, Uploads: []UploadTask{{Serial: 1}}},
	}}
	out := s.ListTerminals()
	if len(out) != 2 || out[0].Key != "1000" || out[1].Key != "2000" {
		t.Fatalf("list = %+v", out)
	}
	out[0].AV.AudioCodec = 99
	out[0].Uploads[0].Serial = 99
	if s.terminals["1000"].AV.AudioCodec != 6 || s.terminals["1000"].Uploads[0].Serial != 1 {
		t.Fatal("ListTerminals leaked internal state")
	}
}

func TestValidSIMAndStreamID(t *testing.T) {
	for _, bad := range []string{"", "12a", strings.Repeat("1", 21), "12 3"} {
		if _, ok := validSIM(bad); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
	if sim, ok := validSIM("001003"); !ok || sim != "001003" {
		t.Fatalf("valid SIM rejected: %q %v", sim, ok)
	}
	if got := StreamID("001003", 2); got != "1003_2" {
		t.Fatalf("stream id = %q", got)
	}
	if got := StreamID("000", 1); got != "0_1" {
		t.Fatalf("all-zero stream id = %q", got)
	}
}

func TestAllowStreamBranches(t *testing.T) {
	var nilServer *Server
	if nilServer.AllowStream("1003_1") {
		t.Fatal("nil server allowed a stream")
	}
	s := &Server{terminals: map[string]*Terminal{}, grants: map[string]int{}}
	if s.AllowStream("") {
		t.Fatal("empty name allowed")
	}
	if s.AllowStream("1003") {
		t.Fatal("name without channel separator allowed")
	}
	if s.AllowStream("1003_1") {
		t.Fatal("unknown terminal allowed")
	}
	s.terminals["1003"] = &Terminal{Key: "1003", Online: true, Authenticated: true}
	if s.AllowStream("1003_1") {
		t.Fatal("ungranted stream allowed")
	}
	s.grants[grantLive+"1003_1"] = 1
	if !s.AllowStream("1003_1") {
		t.Fatal("live grant ignored")
	}
	delete(s.grants, grantLive+"1003_1")
	s.grants[grantPB+"1003_1"] = 1
	if !s.AllowStream("1003_1") {
		t.Fatal("playback grant ignored")
	}
	s.terminals["1003"].Online = false
	if s.AllowStream("1003_1") {
		t.Fatal("offline terminal allowed")
	}
	s.terminals["1003"].Online = true
	s.stopped = true
	if s.AllowStream("1003_1") {
		t.Fatal("stopped server allowed")
	}
}

func TestRestoreGrantBranches(t *testing.T) {
	s := &Server{terminals: map[string]*Terminal{}, grants: map[string]int{}}
	s.terminals["1003"] = &Terminal{Key: "1003", Online: true, Authenticated: true}
	s.restoreGrant(grantLive, "1003_1", 2)
	if s.grants[grantLive+"1003_1"] != 2 {
		t.Fatalf("grant = %v", s.grants)
	}
	s.restoreGrant(grantLive, "1003_2", 0)
	if _, ok := s.grants[grantLive+"1003_2"]; ok {
		t.Fatal("zero grant restored")
	}
	s.terminals["1003"].Authenticated = false
	s.restoreGrant(grantLive, "1003_1", 2)
	if _, ok := s.grants[grantLive+"1003_1"]; ok {
		t.Fatal("grant restored for unauthenticated terminal")
	}
	s.terminals["1003"].Authenticated = true
	s.stopped = true
	s.restoreGrant(grantPB, "1003_1", 3)
	if _, ok := s.grants[grantPB+"1003_1"]; ok {
		t.Fatal("grant restored on stopped server")
	}
}

func TestWriteReplyReportsEncodingErrors(t *testing.T) {
	ss := &session{sim: "not-a-sim"}
	if err := ss.writeReply(cmdGeneralAck, nil); err == nil {
		t.Fatal("invalid SIM accepted")
	}
	conn := &discardConn{}
	ss = &session{conn: conn, sim: "1003"}
	if err := ss.writeReply(cmdGeneralAck, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if conn.writes != 1 {
		t.Fatalf("writes = %d", conn.writes)
	}
}

func TestNewServerRejectsInvalidConfig(t *testing.T) {
	if _, err := NewServer(Config{Enabled: true}); err == nil {
		t.Fatal("invalid config accepted")
	}
}

func TestServerStartAfterStop(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	s, err := NewServer(Config{Enabled: true, Port: port, MediaIP: "127.0.0.1", AuthCode: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	s.Stop()
	if err := s.Start(); err == nil {
		s.Stop()
		t.Fatal("server restarted after stop")
	}
}

func TestServerStartReportsExistingListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	s, err := NewServer(Config{Enabled: true, Port: port, MediaIP: "127.0.0.1", AuthCode: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	stub := &stubListener{}
	s.listener = stub
	if err := s.Start(); err == nil {
		s.Stop()
		t.Fatal("second listener accepted")
	}
	if s.listener != stub {
		t.Fatal("existing listener was replaced")
	}
	s.listener = nil
}

func TestConfigValidateErrorBranches(t *testing.T) {
	base := func() Config {
		return Config{Enabled: true, Port: 808, MediaIP: "127.0.0.1", AuthCode: "secret"}
	}
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"disabled ignores everything", func(c *Config) { c.Enabled = false; c.MediaIP = ""; c.AuthCode = "" }, ""},
		{"port too large", func(c *Config) { c.Port = 70000 }, "port must be 1-65535"},
		{"port negative", func(c *Config) { c.Port = -1 }, "port must be 1-65535"},
		{"media tcp port", func(c *Config) { c.MediaTCPPort = -5 }, "media ports"},
		{"media udp port", func(c *Config) { c.MediaUDPPort = 70000 }, "media ports"},
		{"missing media ip", func(c *Config) { c.MediaIP = "  " }, "media_ip is required"},
		{"missing auth code", func(c *Config) { c.AuthCode = "" }, "auth_code must be 1-128"},
		{"long auth code", func(c *Config) { c.AuthCode = strings.Repeat("a", 129) }, "auth_code must be 1-128"},
		{"unparsable media ip", func(c *Config) { c.MediaIP = "not-an-ip" }, "reachable IP"},
		{"unspecified media ip", func(c *Config) { c.MediaIP = "0.0.0.0" }, "reachable IP"},
		{"bad transport", func(c *Config) { c.Transport = "quic" }, "transport must be tcp or udp"},
		{"bad timeout", func(c *Config) { c.Timeout = "nope" }, "positive duration"},
		{"zero timeout", func(c *Config) { c.Timeout = "0s" }, "positive duration"},
		{"valid udp with padding", func(c *Config) { c.Transport = " UDP " }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPlayInputValidation(t *testing.T) {
	s := onlineServer(&recordingSender{})
	if _, err := s.Play(PlayInput{Key: "bad"}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("invalid SIM error = %v", err)
	}
	if _, err := s.Play(PlayInput{Key: "1003", DataType: 6}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("data type error = %v", err)
	}
	if _, err := s.Play(PlayInput{Key: "1003", Stream: 2}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("stream type error = %v", err)
	}
	if _, err := s.Play(PlayInput{Key: "1003", Transport: "srt"}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("transport error = %v", err)
	}
}

func TestQueryResourcesSendFailure(t *testing.T) {
	s := onlineServer(&recordingSender{})
	start := time.Date(2024, 3, 4, 5, 6, 7, 0, time.Local)
	if _, err := s.QueryResources(ResourceQuery{Key: "1003", Start: start, End: start.Add(time.Hour)}); err == nil || !strings.Contains(err.Error(), "query resources") {
		t.Fatalf("error = %v", err)
	}
}

func TestStartAcceptLoopRejectsConnectionsAfterStop(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	s, err := NewServer(Config{Enabled: true, Port: port, MediaIP: "127.0.0.1", AuthCode: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	// Simulate a shutdown that raced with Accept: the listener is still open but
	// the server is marked stopped, so the accepted connection must be dropped.
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	client, err := net.Dial("tcp", s.cfg.ListenAddr())
	if err != nil {
		s.Stop()
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		client.Close()
		s.Stop()
		t.Fatal("connection accepted after stop")
	}
	client.Close()
	s.Stop()
}
