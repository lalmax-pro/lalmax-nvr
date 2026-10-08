// Package jtsim is a JT/T 808 terminal that also publishes JT/T 1078 media.
// Acceptance tests use it to drive NVR signaling and the lalmax receiver together.
package jtsim

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/q191201771/lalmax/jt1078"
)

// Options selects the simulated terminal.
type Options struct {
	SIM     string
	Version byte // 0: JT/T 808-2013 header, non-zero: 2019 header
}

// Command is a platform downlink the terminal understood.
type Command struct {
	ID       uint16
	Seq      uint16
	MediaIP  string
	TCPPort  int
	UDPPort  int
	Channel  byte
	DataType byte
	Stream   byte
}

// Session is one registered and authenticated terminal.
// It is not safe for concurrent use.
type Session struct {
	opt      Options
	conn     net.Conn
	seq      uint16
	buf      []byte
	AuthCode string
}

// Register dials a JT808 server, sends 0x0100, and completes 0x0102 with the
// auth code from 0x8100.
func Register(ctx context.Context, addr string, opt Options) (*Session, error) {
	if !digits(opt.SIM) {
		return nil, errors.New("sim must be decimal")
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &Session{opt: opt, conn: conn}
	if err := s.write(msgRegister, make([]byte, 25)); err != nil {
		conn.Close()
		return nil, err
	}
	reply, err := s.read(ctx)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if reply.id != msgRegisterReply || len(reply.body) < 3 || reply.body[2] != 0 {
		conn.Close()
		return nil, fmt.Errorf("register rejected: id=%04x body=%x", reply.id, reply.body)
	}
	s.AuthCode = string(reply.body[3:])
	authBody := []byte(s.AuthCode)
	if opt.Version != 0 {
		if len(s.AuthCode) > 255 {
			conn.Close()
			return nil, errors.New("auth code too long for 2019")
		}
		authBody = append([]byte{byte(len(s.AuthCode))}, s.AuthCode...)
	}
	if err := s.write(msgAuth, authBody); err != nil {
		conn.Close()
		return nil, err
	}
	ack, err := s.read(ctx)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if ack.id != msgPlatformAck || len(ack.body) != 5 || ack.body[4] != 0 {
		conn.Close()
		return nil, fmt.Errorf("auth rejected: id=%04x body=%x", ack.id, ack.body)
	}
	return s, nil
}

// Serve reads 0x9101, publishes packets to the address in that command, then
// waits for 0x9102. Both commands are acknowledged with 0x0001.
func (s *Session) Serve(ctx context.Context, packets []jt1078.Packet) error {
	play, err := s.read(ctx)
	if err != nil {
		return err
	}
	cmd, err := playCommand(play)
	if err != nil {
		return err
	}
	if err := s.ack(play); err != nil {
		return err
	}
	if err := publish(ctx, cmd, packets); err != nil {
		return err
	}
	stop, err := s.read(ctx)
	if err != nil {
		return err
	}
	if stop.id != msgStop {
		return fmt.Errorf("expected 0x9102, got %04x", stop.id)
	}
	return s.ack(stop)
}

// Close closes the signaling connection.
func (s *Session) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

func (s *Session) write(id uint16, body []byte) error {
	s.seq++
	raw, err := encodeMessage(message{id: id, sim: s.opt.SIM, seq: s.seq, version: s.opt.Version, body: body})
	if err != nil {
		return err
	}
	_, err = s.conn.Write(raw)
	return err
}

func (s *Session) ack(m message) error {
	body := make([]byte, 5)
	binary.BigEndian.PutUint16(body, m.seq)
	binary.BigEndian.PutUint16(body[2:], m.id)
	return s.write(msgGeneralReply, body)
}

func (s *Session) read(ctx context.Context) (message, error) {
	tmp := make([]byte, 512)
	for {
		if m, rest, ok, err := takeFrame(s.buf); ok || err != nil {
			s.buf = rest
			return m, err
		}
		if ctx.Err() != nil {
			return message{}, ctx.Err()
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, err := s.conn.Read(tmp)
		if n > 0 {
			s.buf = append(s.buf, tmp[:n]...)
		}
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return message{}, err
		}
	}
}

func takeFrame(buf []byte) (message, []byte, bool, error) {
	start := -1
	for i, b := range buf {
		if b != 0x7e {
			continue
		}
		if start < 0 {
			start = i
			continue
		}
		frame := buf[start : i+1]
		msg, err := decodeMessage(frame)
		return msg, append([]byte(nil), buf[i+1:]...), true, err
	}
	if start > 0 {
		buf = buf[start:]
	}
	return message{}, buf, false, nil
}

func playCommand(m message) (Command, error) {
	if m.id != msgPlay {
		return Command{}, fmt.Errorf("expected 0x9101, got %04x", m.id)
	}
	if len(m.body) < 1 {
		return Command{}, io.ErrUnexpectedEOF
	}
	n := int(m.body[0])
	if len(m.body) < 1+n+7 {
		return Command{}, io.ErrUnexpectedEOF
	}
	pos := 1 + n
	return Command{
		ID:       m.id,
		Seq:      m.seq,
		MediaIP:  string(m.body[1:pos]),
		TCPPort:  int(binary.BigEndian.Uint16(m.body[pos:])),
		UDPPort:  int(binary.BigEndian.Uint16(m.body[pos+2:])),
		Channel:  m.body[pos+4],
		DataType: m.body[pos+5],
		Stream:   m.body[pos+6],
	}, nil
}

func publish(ctx context.Context, cmd Command, packets []jt1078.Packet) error {
	if cmd.TCPPort > 0 {
		return publishTCP(ctx, fmt.Sprintf("%s:%d", cmd.MediaIP, cmd.TCPPort), packets)
	}
	if cmd.UDPPort > 0 {
		return publishUDP(ctx, fmt.Sprintf("%s:%d", cmd.MediaIP, cmd.UDPPort), packets)
	}
	return errors.New("0x9101 has no media port")
}

func publishTCP(ctx context.Context, addr string, packets []jt1078.Packet) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	raw := make([]byte, 0, 256*len(packets))
	for _, pkt := range packets {
		b, err := pkt.Encode()
		if err != nil {
			return err
		}
		raw = append(raw, b...)
	}
	_, err = conn.Write(raw)
	return err
}

func publishUDP(ctx context.Context, addr string, packets []jt1078.Packet) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "udp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, pkt := range packets {
		b, err := pkt.Encode()
		if err != nil {
			return err
		}
		if _, err := conn.Write(b); err != nil {
			return err
		}
	}
	return nil
}
