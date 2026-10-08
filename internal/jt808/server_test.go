package jt808

import (
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"
)

type fakeSender struct {
	id   uint16
	body []byte
	err  error
}

func (f *fakeSender) send(_ string, id uint16, body []byte, _ time.Duration) error {
	f.id = id
	f.body = body
	return f.err
}
func TestConfigValidate(t *testing.T) {
	cfg := Config{Enabled: true}
	if cfg.Validate() == nil {
		t.Fatal("expected error")
	}
	cfg.MediaIP = "127.0.0.1"
	cfg.AuthCode = "secret"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 808 || cfg.MediaTCPPort != 1078 || cfg.Transport != TransportTCP {
		t.Fatalf("defaults=%+v", cfg)
	}
}
func TestPlaySends9101TCP(t *testing.T) {
	f := &fakeSender{}
	s := &Server{cfg: Config{MediaIP: "192.168.1.8", MediaTCPPort: 1078, Transport: "tcp"}, sender: f}
	id, err := s.Play(PlayInput{Key: "1003", Channel: 1})
	if err != nil {
		t.Fatal(err)
	}
	if id != "1003_1" || f.id != cmdPlay {
		t.Fatalf("id=%s command=%x", id, f.id)
	}
	pos := 1 + int(f.body[0])
	if string(f.body[1:pos]) != "192.168.1.8" || binary.BigEndian.Uint16(f.body[pos:]) != 1078 || binary.BigEndian.Uint16(f.body[pos+2:]) != 0 || f.body[pos+4] != 1 {
		t.Fatalf("body=%x", f.body)
	}
}
func TestPlaySends9101UDP(t *testing.T) {
	f := &fakeSender{}
	s := &Server{cfg: Config{MediaIP: "10.0.0.2", MediaUDPPort: 2078, Transport: "tcp"}, sender: f}
	_, err := s.Play(PlayInput{Key: "9", Channel: 2, Transport: "udp"})
	if err != nil {
		t.Fatal(err)
	}
	pos := 1 + int(f.body[0])
	if binary.BigEndian.Uint16(f.body[pos:]) != 0 || binary.BigEndian.Uint16(f.body[pos+2:]) != 2078 || f.body[pos+4] != 2 {
		t.Fatalf("body=%x", f.body)
	}
}
func TestStopAndError(t *testing.T) {
	f := &fakeSender{}
	s := &Server{cfg: Config{}, sender: f}
	if err := s.StopPlay(StopPlayInput{Key: "1003", Channel: 1}); err != nil {
		t.Fatal(err)
	}
	if f.id != cmdStop || string(f.body) != string([]byte{1, 0, 0, 0}) {
		t.Fatalf("stop=%x %x", f.id, f.body)
	}
	f.err = errors.New("offline")
	if _, err := s.Play(PlayInput{Key: "1003"}); err == nil {
		t.Fatal("error not propagated")
	}
}
func TestPacketRoundTrip(t *testing.T) {
	for _, version := range []byte{0, 1} {
		original := packet{id: cmdPlay, sim: "1003", seq: 42, version: version, body: []byte{0x7d, 0x7e, 1}}
		encoded, err := encodePacket(original)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodePacket(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if decoded.id != original.id || decoded.seq != 42 || decoded.sim != "1003" || string(decoded.body) != string(original.body) {
			t.Fatalf("decoded=%+v", decoded)
		}
		encoded[len(encoded)-2] ^= 1
		if _, err := decodePacket(encoded); err == nil {
			t.Fatal("bad checksum accepted")
		}
	}
}
func TestPlayOverTCPAcknowledgment(t *testing.T) {
	s, err := NewServer(Config{Enabled: true, MediaIP: "127.0.0.1", AuthCode: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	server, device := net.Pipe()
	defer device.Close()
	done := make(chan struct{})
	go func() { s.handle(server); close(done) }()
	sendPacket(t, device, packet{id: cmdAuth, sim: "1003", seq: 1, body: []byte("secret")})
	_ = readFrame(t, device)
	deadline := time.Now().Add(time.Second)
	for !s.ListTerminals()[0].Authenticated && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !s.ListTerminals()[0].Authenticated {
		t.Fatal("auth not committed")
	}
	result := make(chan error, 1)
	go func() { _, err := s.Play(PlayInput{Key: "1003", Channel: 1}); result <- err }()
	p := readFrame(t, device)
	if p.id != cmdPlay {
		t.Fatalf("id=%x", p.id)
	}
	ack := make([]byte, 5)
	binary.BigEndian.PutUint16(ack, p.seq)
	binary.BigEndian.PutUint16(ack[2:], p.id)
	sendPacket(t, device, packet{id: cmdGeneralReply, sim: "1003", seq: 2, body: ack})
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	_ = device.Close()
	<-done
}
