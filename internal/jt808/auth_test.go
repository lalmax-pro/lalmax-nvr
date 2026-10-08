package jt808

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func readFrame(t *testing.T, conn net.Conn) packet {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	frame := make([]byte, 0, 256)
	started := false
	for {
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range buf[:n] {
			if b == 0x7e && !started {
				started = true
				frame = append(frame, b)
				continue
			}
			if !started {
				continue
			}
			frame = append(frame, b)
			if b == 0x7e && len(frame) > 2 {
				p, err := decodePacket(frame)
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
		}
	}
}
func sendPacket(t *testing.T, conn net.Conn, p packet) {
	t.Helper()
	data, err := encodePacket(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(data); err != nil {
		t.Fatal(err)
	}
}
func TestRegistrationAndAuthOverTCP(t *testing.T) {
	s, err := NewServer(Config{Enabled: true, Port: 808, MediaIP: "127.0.0.1", AuthCode: "strong-secret"})
	if err != nil {
		t.Fatal(err)
	}
	server, device := net.Pipe()
	defer device.Close()
	done := make(chan struct{})
	go func() { s.handle(server); close(done) }()
	register := make([]byte, 25)
	sendPacket(t, device, packet{id: cmdRegister, sim: "1003", seq: 7, body: register})
	p := readFrame(t, device)
	if p.id != cmdRegisterReply || binary.BigEndian.Uint16(p.body[:2]) != 7 || p.body[2] != 0 || string(p.body[3:]) != "strong-secret" {
		t.Fatalf("registration reply=%+v", p)
	}
	if len(s.ListTerminals()) != 1 || s.ListTerminals()[0].Authenticated {
		t.Fatal("unauthenticated terminal marked online")
	}
	sendPacket(t, device, packet{id: cmdAuth, sim: "1003", seq: 8, body: []byte("bad-secret")})
	p = readFrame(t, device)
	if p.id != cmdGeneralAck || p.body[4] != 1 {
		t.Fatalf("invalid auth response=%+v", p)
	}
	sendPacket(t, device, packet{id: cmdAuth, sim: "1003", seq: 9, body: []byte("strong-secret")})
	p = readFrame(t, device)
	if p.body[4] != 0 {
		t.Fatalf("auth response=%+v", p)
	}
	deadline := time.Now().Add(time.Second)
	for !s.ListTerminals()[0].Authenticated && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !s.ListTerminals()[0].Authenticated {
		t.Fatal("authentication not committed")
	}
	_ = device.Close()
	<-done
	if s.ListTerminals()[0].Online {
		t.Fatal("terminal still online")
	}
}
