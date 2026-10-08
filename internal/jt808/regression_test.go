package jt808

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func authenticatedPipe(t *testing.T) (*Server, net.Conn, <-chan struct{}) {
	t.Helper()
	s, err := NewServer(Config{Enabled: true, MediaIP: "127.0.0.1", AuthCode: "secret", Timeout: "1s"})
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() { s.handle(server); close(done) }()
	t.Cleanup(func() { client.Close(); <-done })
	sendPacket(t, client, packet{id: cmdAuth, sim: "1003", body: []byte("secret")})
	readFrame(t, client)
	deadline := time.Now().Add(time.Second)
	for !s.ListTerminals()[0].Authenticated && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !s.ListTerminals()[0].Authenticated {
		t.Fatal("authentication not committed")
	}
	return s, client, done
}

func TestQueryAVStandardResponse(t *testing.T) {
	s, client, _ := authenticatedPipe(t)
	result := make(chan error, 1)
	go func() {
		av, err := s.QueryAV("1003")
		if err == nil && (av.AudioCodec != 6 || av.AudioFrameLen != 160 || av.VideoCodec != 98 || av.MaxVideoCh != 4) {
			err = ErrBadInput
		}
		result <- err
	}()
	query := readFrame(t, client)
	if query.id != cmdAVQuery {
		t.Fatalf("command=%x", query.id)
	}
	// No request sequence in the body; the terminal uses its own header sequence.
	sendPacket(t, client, packet{id: cmdAVReport, sim: "1003", seq: 99, body: []byte{6, 1, 0, 1, 0, 160, 1, 98, 4, 4}})
	readFrame(t, client)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if s.ListTerminals()[0].AV == nil {
		t.Fatal("AV properties not stored")
	}
}

func TestGrantLifecycle(t *testing.T) {
	s, client, done := authenticatedPipe(t)
	f := &fakeSender{}
	s.sender = f
	for i := 0; i < 2; i++ {
		if _, err := s.Play(PlayInput{Key: "1003", Channel: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if !s.AllowStream("1003_1") {
		t.Fatal("play not authorized")
	}
	if err := s.StopPlay(StopPlayInput{Key: "1003", Channel: 1}); err != nil {
		t.Fatal(err)
	}
	if s.AllowStream("1003_1") {
		t.Fatal("stop left authorization")
	}
	if _, err := s.Play(PlayInput{Key: "1003", Channel: 1}); err != nil {
		t.Fatal(err)
	}
	f.err = ErrBadInput
	if _, err := s.Play(PlayInput{Key: "1003", Channel: 1}); err == nil {
		t.Fatal("expected rejection")
	}
	if !s.AllowStream("1003_1") {
		t.Fatal("failed repeated play removed existing grant")
	}
	client.Close()
	<-done
	if s.AllowStream("1003_1") {
		t.Fatal("disconnect left authorization")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.grants) != 0 {
		t.Fatal("disconnect left stale grants")
	}
}

func TestStopClosesUnidentifiedConnections(t *testing.T) {
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
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	client, err := net.Dial("tcp", s.cfg.ListenAddr())
	if err != nil {
		s.Stop()
		t.Fatal(err)
	}
	defer client.Close()
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.RLock()
		n := len(s.conns)
		s.mu.RUnlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			s.Stop()
			t.Fatal("connection not accepted")
		}
		time.Sleep(time.Millisecond)
	}
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		client.Close()
		<-done
		t.Fatal("Stop blocked on unidentified connection")
	}
}

func TestStartReportsOccupiedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	s, err := NewServer(Config{Enabled: true, Port: ln.Addr().(*net.TCPAddr).Port, MediaIP: "127.0.0.1", AuthCode: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(); err == nil {
		s.Stop()
		t.Fatal("occupied port accepted")
	}
}

func TestQueryAVConcurrentQueriesAreSerialized(t *testing.T) {
	s, client, _ := authenticatedPipe(t)
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := s.QueryAV("1003"); results <- err }()
	}
	for i := 0; i < 2; i++ {
		query := readFrame(t, client)
		if query.id != cmdAVQuery {
			t.Fatal("wrong query")
		}
		sendPacket(t, client, packet{id: cmdAVReport, sim: "1003", seq: uint16(100 + i), body: []byte{6, 1, 0, 1, 0, 160, 1, 98, 4, 4}})
		ack := readFrame(t, client)
		if binary.BigEndian.Uint16(ack.body[2:]) != cmdAVReport {
			t.Fatal("wrong acknowledgment")
		}
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestFailedAuthenticationRevokesMedia(t *testing.T) {
	s, client, _ := authenticatedPipe(t)
	s.grant(grantLive, "1003_1")
	s.grant(grantPB, "1003_2")
	sendPacket(t, client, packet{id: cmdAuth, sim: "1003", seq: 10, body: []byte("wrong")})
	ack := readFrame(t, client)
	if ack.body[4] == 0 {
		t.Fatal("invalid auth accepted")
	}
	deadline := time.Now().Add(time.Second)
	for s.ListTerminals()[0].Authenticated && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.AllowStream("1003_1") || s.AllowStream("1003_2") {
		t.Fatal("failed authentication left media authorized")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.grants) != 0 {
		t.Fatal("failed authentication left grants")
	}
}
