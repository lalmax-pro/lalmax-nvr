// SPDX-License-Identifier: MIT
// Copyright (c) 2022 Alexey Khit

package tutk

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

func Dial(host, uid, username, password string) (*Conn, error) {
	return DialContext(context.Background(), host, uid, username, password)
}

func DialContext(ctx context.Context, host, uid, username, password string) (*Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	addr, err := net.ResolveUDPAddr("udp", host)
	if err != nil {
		// Default port for listening incoming LAN connections.
		// Important. It's not using for real connection.
		addr = &net.UDPAddr{IP: net.ParseIP(host), Port: 32761}
	}

	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}

	c := &Conn{UDPConn: udpConn, addr: addr, done: make(chan struct{}), cmdGate: make(chan struct{}, 1)}
	stopCancel := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stopCancel()

	sid := GenSessionID()

	_ = c.SetDeadline(time.Now().Add(5 * time.Second))

	if addr.Port != 10001 {
		err = c.connectDirect(uid, sid)
	} else {
		err = c.connectRemote(uid, sid)
	}
	if err != nil {
		_ = c.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}

	if c.ver[0] >= 25 {
		c.session = NewSession25(c, sid)
	} else {
		c.session = NewSession16(c, sid)
	}

	if err = c.clientStart(username, password); err != nil {
		_ = c.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}

	if err = ctx.Err(); err != nil {
		_ = c.Close()
		return nil, err
	}
	_ = c.SetDeadline(time.Time{})
	go c.worker()

	return c, nil
}

type Conn struct {
	*net.UDPConn
	addr        *net.UDPAddr
	addrMu      sync.RWMutex
	session     Session
	idleTimeout time.Duration

	ver        []byte
	err        error
	errMu      sync.Mutex
	cmdGate    chan struct{}
	ackMu      sync.Mutex
	pendingAck *pendingCommand
	done       chan struct{}
	closeOnce  sync.Once
}

type pendingCommand struct {
	seq  uint16
	done chan struct{}
}

func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() { close(c.done); err = c.UDPConn.Close() })
	return err
}

// Read overwrite net.Conn
func (c *Conn) Read(buf []byte) (n int, err error) {
	for {
		var addr *net.UDPAddr
		if n, addr, err = c.UDPConn.ReadFromUDP(buf); err != nil {
			return 0, err
		}

		c.addrMu.Lock()
		if !c.addr.IP.Equal(addr.IP) || n < 16 {
			c.addrMu.Unlock()
			continue // skip messages from another IP
		}

		if c.addr.Port != addr.Port {
			c.addr.Port = addr.Port
		}
		c.addrMu.Unlock()

		ReverseTransCodePartial(buf, buf[:n])
		//log.Printf("<- %x", buf[:n])
		return n, nil
	}
}

// Write overwrite net.Conn
func (c *Conn) Write(b []byte) (n int, err error) {
	//log.Printf("-> %x", b)
	c.addrMu.RLock()
	addr := *c.addr
	c.addrMu.RUnlock()
	return c.UDPConn.WriteToUDP(TransCodePartial(nil, b), &addr)
}

// RemoteAddr overwrite net.Conn
func (c *Conn) RemoteAddr() net.Addr {
	c.addrMu.RLock()
	defer c.addrMu.RUnlock()
	addr := *c.addr
	addr.IP = append(net.IP(nil), c.addr.IP...)
	return &addr
}

func (c *Conn) Protocol() string {
	return "tutk+udp"
}

func (c *Conn) Version() string {
	if len(c.ver) < 5 {
		if len(c.ver) == 0 {
			return "TUTK"
		}
		return fmt.Sprintf("TUTK/%d", c.ver[0])
	}
	return fmt.Sprintf("TUTK/%d SDK %d.%d.%d.%d", c.ver[0], c.ver[1], c.ver[2], c.ver[3], c.ver[4])
}

func (c *Conn) ReadCommand() (ctrlType uint32, ctrlData []byte, err error) {
	return c.session.RecvIOCtrlTimeout(c.idleTimeout)
}

func (c *Conn) WriteCommand(ctrlType uint32, ctrlData []byte) error {
	return c.WriteCommandContext(context.Background(), ctrlType, ctrlData)
}

func (c *Conn) WriteCommandContext(ctx context.Context, ctrlType uint32, ctrlData []byte) error {
	select {
	case c.cmdGate <- struct{}{}:
		defer func() { <-c.cmdGate }()
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return io.EOF
	}

	if len(ctrlData) > 65400 {
		return fmt.Errorf("tutk: command too large")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	buf := c.session.SendIOCtrl(ctrlType, ctrlData)
	ack := &pendingCommand{seq: binary.LittleEndian.Uint16(buf[32:34]), done: make(chan struct{})}
	c.ackMu.Lock()
	c.pendingAck = ack
	c.ackMu.Unlock()
	defer func() {
		c.ackMu.Lock()
		if c.pendingAck == ack {
			c.pendingAck = nil
		}
		c.ackMu.Unlock()
	}()

	for attempt := 0; attempt < 5; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.session.SessionWrite(0, buf); err != nil {
			_ = c.Close()
			return err
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ack.done:
			timer.Stop()
			return nil
		case <-ctx.Done():
			timer.Stop()
			_ = c.Close()
			return ctx.Err()
		case <-c.done:
			timer.Stop()
			return io.EOF
		case <-timer.C:
		}
	}
	_ = c.Close()
	return fmt.Errorf("tutk: can't send command %d", ctrlType)
}

func (c *Conn) SetIdleTimeout(timeout time.Duration) {
	c.idleTimeout = timeout
}

func (c *Conn) ReadPacket() (hdr, payload []byte, err error) {
	return c.session.RecvFrameDataTimeout(c.idleTimeout)
}

func (c *Conn) SupportsAudioOutput() bool {
	if c.session == nil {
		return false
	}
	_, unsupported := c.session.(*Session25)
	return !unsupported
}

func (c *Conn) WritePacket(hdr, payload []byte) error {
	if len(hdr) != 32 || len(payload) > 65400 {
		return fmt.Errorf("tutk: invalid audio packet size")
	}
	buf := c.session.SendFrameData(hdr, payload)
	if len(buf) == 0 {
		return fmt.Errorf("tutk: audio output is not supported by this protocol version")
	}
	return c.session.SessionWrite(1, buf)
}

func (c *Conn) Error() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	if c.err != nil {
		return c.err
	}
	return io.EOF
}

func (c *Conn) worker() {
	defer c.Close()
	defer c.session.Close()

	buf := make([]byte, 1200)

	for {
		n, err := c.Read(buf)
		if err != nil {
			c.errMu.Lock()
			c.err = fmt.Errorf("%s: %w", "tutk", err)
			c.errMu.Unlock()
			return
		}

		switch c.handleMsg(buf[:n]) {
		case msgError:
			return
		case msgCommandAck:
			c.ackMu.Lock()
			if c.pendingAck != nil && matchesCommandAck(buf[:n], c.pendingAck.seq, c.ver[0]) {
				close(c.pendingAck.done)
				c.pendingAck = nil
			}
			c.ackMu.Unlock()
		}
	}
}

const (
	msgUnknown = iota
	msgError
	msgPing
	msgUnknownPing
	msgClientStart
	msgClientStart2
	msgClientStartAck2
	msgCommand
	msgCommandAck
	msgCounters
	msgMediaChunk
	msgMediaFrame
	msgMediaReorder
	msgMediaLost
	msgCh5

	msgUnknown0007 // time sync without data?
	msgUnknown0008 // time sync with data?
	msgUnknown0010
	msgUnknown0013
	msgUnknown0900
	msgUnknown0a08
	msgUnknownCh1c
	msgDafang0012
)

func matchesCommandAck(msg []byte, seq uint16, version byte) bool {
	if len(msg) < 44 || msg[14] != 0 {
		return false
	}
	cmd := msg[28:]
	if cmd[0] == 0 && cmd[1] == 0x71 {
		return binary.LittleEndian.Uint16(cmd[4:6]) == seq
	}
	if version >= 25 && cmd[0] == 0x09 {
		return int16(binary.LittleEndian.Uint16(cmd[12:14])-seq) >= 0
	}
	return false
}

func (c *Conn) handleMsg(msg []byte) int {
	if len(msg) < 16 {
		return msgUnknown
	}
	// off sample
	// 0   0402      tutk magic
	// 2   120a      tutk version (120a, 190a...)
	// 4   0800      msg size = len(b)-16
	// 6   0000      channel seq
	// 8   28041200  msg type
	// 14  0100      channel (not all msg)
	// 28  0700      msg data (not all msg)
	switch msg[8] {
	case 0x08:
		switch ch := msg[14]; ch {
		case 0, 1:
			if len(msg) < 28 {
				return msgUnknown
			}
			return c.session.SessionRead(ch, msg[28:])
		case 5:
			if len(msg) == 48 {
				_, _ = c.Write(msgAckCh5(msg))
				return msgCh5
			}
		case 0x1c:
			return msgUnknownCh1c
		}
	case 0x18:
		return msgUnknownPing
	case 0x28:
		if len(msg) == 24 {
			_, _ = c.Write(msgAckPing(msg))
			return msgPing
		}
	}
	return msgUnknown
}

func msgAckPing(msg []byte) []byte {
	// <- [24] 0402120a 08000000 28041200 000000005b0d4202070aa8c0
	// -> [24] 04021a0a 08000000 27042100 000000005b0d4202070aa8c0
	msg[8] = 0x27
	msg[10] = 0x21
	return msg
}

func msgAckCh5(msg []byte) []byte {
	// <- [48] 0402190a 20000400 07042100 7ecc05000c0000007ecc93c456c2561f 5a97c2f101050000000000000000000000010000
	// -> [48] 0402190a 20000400 08041200 7ecc05000c0000007ecc93c456c2561f 5a97c2f141050000000000000000000000010000
	msg[8] = 0x07
	msg[10] = 0x21
	msg[32] = 0x41
	return msg
}
