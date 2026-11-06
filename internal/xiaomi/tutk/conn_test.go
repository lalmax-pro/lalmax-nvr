// SPDX-License-Identifier: MIT

package tutk

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestMatchesCommandAck(t *testing.T) {
	msg := make([]byte, 56)
	msg[28], msg[29] = 0, 0x71
	binary.LittleEndian.PutUint16(msg[32:34], 13)
	if !matchesCommandAck(msg, 13, 16) || matchesCommandAck(msg, 12, 16) {
		t.Fatal("command ACK must match its sequence")
	}
	msg[14] = 1
	if matchesCommandAck(msg, 13, 16) {
		t.Fatal("wrong channel acknowledged command")
	}
	if matchesCommandAck(msg[:30], 13, 16) {
		t.Fatal("short frame accepted")
	}
	msg[14] = 0
	msg[28], msg[29] = 9, 0
	binary.LittleEndian.PutUint16(msg[40:42], 13)
	if !matchesCommandAck(msg, 13, 25) || matchesCommandAck(msg, 14, 25) || matchesCommandAck(msg, 13, 16) {
		t.Fatal("invalid counter acknowledgement")
	}
}

func TestDialContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DialContext(ctx, "127.0.0.1", "uid", "user", "pass"); err != context.Canceled {
		t.Fatalf("got %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := DialContext(ctx, "127.0.0.1", "uid", "user", "pass"); err != context.DeadlineExceeded {
		t.Fatalf("got %v", err)
	}
}

func TestShortChannelOneMessage(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	s := NewSession16(a, make([]byte, 8))
	if got := s.SessionRead(1, make([]byte, 24)); got != msgUnknown {
		t.Fatalf("got %d", got)
	}
}

type discardConn struct{}

func (discardConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (discardConn) Write(p []byte) (int, error)      { return len(p), nil }
func (discardConn) Close() error                     { return nil }
func (discardConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (discardConn) RemoteAddr() net.Addr             { return &net.UDPAddr{} }
func (discardConn) SetDeadline(time.Time) error      { return nil }
func (discardConn) SetReadDeadline(time.Time) error  { return nil }
func (discardConn) SetWriteDeadline(time.Time) error { return nil }

func FuzzSessionRead(f *testing.F) {
	for _, seed := range [][]byte{make([]byte, 24), make([]byte, 28), make([]byte, 36), {0, 0x70}} {
		f.Add(byte(0), seed)
	}
	f.Fuzz(func(t *testing.T, channel byte, data []byte) {
		if len(data) > 2048 {
			t.Skip()
		}
		for _, s := range []Session{NewSession16(discardConn{}, make([]byte, 8)), NewSession25(discardConn{}, make([]byte, 8))} {
			s.SessionRead(channel%2, data)
		}
	})
}

func TestMediaReadTimeout(t *testing.T) {
	s := NewSession16(discardConn{}, make([]byte, 8))
	_, _, err := s.RecvFrameDataTimeout(10 * time.Millisecond)
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected timeout, got %v", err)
	}
	_, _, err = s.RecvIOCtrlTimeout(10 * time.Millisecond)
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected command timeout, got %v", err)
	}
}

func TestQueuedCommandCancellation(t *testing.T) {
	c := &Conn{cmdGate: make(chan struct{}, 1), done: make(chan struct{})}
	c.cmdGate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.WriteCommandContext(ctx, 1, nil); err != context.Canceled {
		t.Fatalf("got %v", err)
	}
}

func TestAudioOutputCapability(t *testing.T) {
	c := &Conn{}
	if c.SupportsAudioOutput() {
		t.Fatal("uninitialized connection must not claim audio output")
	}
	c.session = NewSession16(discardConn{}, make([]byte, 8))
	if !c.SupportsAudioOutput() {
		t.Fatal("version 16 audio output is available")
	}
	c.session = NewSession25(discardConn{}, make([]byte, 8))
	if c.SupportsAudioOutput() {
		t.Fatal("version 25 audio output is not implemented")
	}
}
