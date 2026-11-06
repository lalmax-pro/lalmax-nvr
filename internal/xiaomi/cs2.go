// SPDX-License-Identifier: MIT
//
// Xiaomi CS2 P2P transport adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package xiaomi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// CS2Dial establishes a CS2 P2P connection to a Xiaomi device.
// transport: "udp" (default), "tcp", or "" (tries both).
func CS2Dial(host, transport string, idleTimeout time.Duration) (*CS2Conn, error) {
	return CS2DialContext(context.Background(), host, transport, idleTimeout)
}

func CS2DialContext(ctx context.Context, host, transport string, idleTimeout time.Duration) (*CS2Conn, error) {
	conn, err := cs2HandshakeContext(ctx, host, transport)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}

	_, isTCP := conn.(*cs2TCPConn)

	c := &CS2Conn{
		Conn:  conn,
		isTCP: isTCP,
		channels: [4]*cs2DataChannel{
			newCS2DataChannel(0, 10), nil, newCS2DataChannel(250, 100), nil,
		},
	}
	if idleTimeout == 0 {
		c.idleTimeout = defaultIdleTimeout
	} else {
		c.idleTimeout = idleTimeout
	}
	go c.worker()
	return c, nil
}

// CS2Conn wraps a CS2 P2P connection (UDP or TCP) to a Xiaomi device.
type CS2Conn struct {
	net.Conn
	isTCP       bool
	idleTimeout time.Duration

	closeOnce sync.Once
	closeErr  error
	mu        sync.Mutex
	err       error
	seqCh0    uint16
	seqCh3    uint16

	channels [4]*cs2DataChannel

	cmdMu      sync.Mutex
	writeMu    sync.Mutex
	packetMu   sync.Mutex
	ackMu      sync.Mutex
	pendingAck *cs2CommandAck
	initOnce   sync.Once
	done       chan struct{}
}

type cs2CommandAck struct {
	seq  uint16
	done chan struct{}
	once sync.Once
}

// writeFrame owns the write deadline while holding writeMu. Cancellation of
// one command cannot leave an expired deadline on a later control frame.
func (c *CS2Conn) writeFrame(ctx context.Context, frame []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	deadline := time.Now().Add(time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.Conn.SetWriteDeadline(deadline)
	canceled := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() { _ = c.Conn.SetWriteDeadline(time.Now()); close(canceled) })
	defer func() {
		if !stopCancel() {
			<-canceled
		}
		_ = c.Conn.SetWriteDeadline(time.Time{})
	}()
	n, err := c.Conn.Write(frame)
	if ctx.Err() != nil {
		return n, ctx.Err()
	}
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	return n, err
}

func (c *CS2Conn) Close() error {
	c.closeOnce.Do(func() {
		if c.Conn != nil {
			c.closeErr = c.Conn.Close()
		}
	})
	return c.closeErr
}

func (c *CS2Conn) initSignals() {
	c.initOnce.Do(func() { c.done = make(chan struct{}) })
}

const (
	cs2Magic        = 0xF1
	cs2MagicDrw     = 0xD1
	cs2MagicTCP     = 0x68
	cs2MsgLanSearch = 0x30
	cs2MsgPunchPkt  = 0x41
	cs2MsgP2PRdyUDP = 0x42
	cs2MsgP2PRdyTCP = 0x43
	cs2MsgDrw       = 0xD0
	cs2MsgDrwAck    = 0xD1
	cs2MsgPing      = 0xE0
	cs2MsgPong      = 0xE1
	cs2MsgClose     = 0xF0
	cs2MsgCloseAck  = 0xF1
)
const defaultIdleTimeout = 30 * time.Second

const cs2HdrSize = 32

// cs2ReadTimeout is the timeout for Pop() calls in ReadPacket and ReadCommand.
// If no data arrives within this period, the call returns a timeout error.
const cs2ReadTimeout = 15 * time.Second

func cs2Handshake(host, transport string) (net.Conn, error) {
	return cs2HandshakeContext(context.Background(), host, transport)
}

func cs2HandshakeContext(ctx context.Context, host, transport string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := cs2NewUDPConn(host, 32108)
	if err != nil {
		return nil, err
	}

	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	req := []byte{cs2Magic, cs2MsgLanSearch, 0, 0}
	res, err := conn.(*cs2UDPConn).WriteUntil(req, func(res []byte) bool {
		return res[1] == cs2MsgPunchPkt
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	var msgUDP, msgTCP byte

	if transport == "" || transport == "udp" {
		msgUDP = cs2MsgP2PRdyUDP
	}
	if transport == "" || transport == "tcp" {
		msgTCP = cs2MsgP2PRdyTCP
	}

	res, err = conn.(*cs2UDPConn).WriteUntil(res, func(res []byte) bool {
		return res[1] == msgUDP || res[1] == msgTCP
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	_ = conn.SetDeadline(time.Time{})

	if res[1] == msgTCP {
		_ = conn.Close()
		return cs2NewTCPConnContext(ctx, conn.RemoteAddr().String())
	}

	return conn, nil
}

func (c *CS2Conn) worker() {
	c.initSignals()
	defer close(c.done)
	defer c.Close()
	defer func() { c.channels[0].Close(); c.channels[2].Close() }()
	defer c.workerExitGuard()

	const (
		pingInterval = 1 * time.Second
	)
	var keepaliveTS time.Time // only for TCP
	lastData := time.Now()
	buf := make([]byte, 65535)

	for {
		// Set a short read deadline for TCP to wake up and send keepalive pings.
		// For UDP use the full idle timeout since there is no ping mechanism.
		if c.isTCP {
			_ = c.Conn.SetReadDeadline(time.Now().Add(pingInterval))
		} else {
			_ = c.Conn.SetReadDeadline(time.Now().Add(c.idleTimeout))
		}

		n, err := c.Conn.Read(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// TCP: send keepalive ping on each timeout wakeup.
				if c.isTCP && time.Now().After(keepaliveTS) {
					_, _ = c.writeFrame(context.Background(), []byte{cs2Magic, cs2MsgPing, 0, 0})
					keepaliveTS = time.Now().Add(pingInterval)
				}

				// Detect truly dead connection: no data for idleTimeout.
				if time.Since(lastData) > c.idleTimeout {
					c.setErr(fmt.Errorf("cs2: no data for %v", c.idleTimeout))
					return
				}
				continue
			}
			c.setErr(fmt.Errorf("cs2: %w", err))
			return
		}

		if n < 4 || buf[0] != cs2Magic {
			continue
		}
		if int(binary.BigEndian.Uint16(buf[2:4])) > n-4 {
			continue
		}
		lastData = time.Now()
		// Keepalive cadence is independent of incoming media traffic.
		if c.isTCP && time.Now().After(keepaliveTS) {
			if _, err := c.writeFrame(context.Background(), []byte{cs2Magic, cs2MsgPing, 0, 0}); err != nil {
				c.setErr(err)
				return
			}
			keepaliveTS = time.Now().Add(pingInterval)
		}
		switch buf[1] {
		case cs2MsgDrw:
			if n < 8 || buf[4] != cs2MagicDrw {
				continue
			}
			ch := buf[5]
			if int(ch) >= len(c.channels) || c.channels[ch] == nil {
				continue
			}
			channel := c.channels[ch]

			if c.isTCP {
				err = channel.Push(buf[8:n])
			} else {
				var pushed int

				seqHI, seqLO := buf[6], buf[7]
				seq := uint16(seqHI)<<8 | uint16(seqLO)
				pushed, err = channel.PushSeq(seq, buf[8:n])

				if pushed >= 0 {
					// For UDP we should send ACK.
					ack := []byte{cs2Magic, cs2MsgDrwAck, 0, 6, cs2MagicDrw, ch, 0, 1, seqHI, seqLO}
					_, _ = c.writeFrame(context.Background(), ack)
				}
			}

			if err != nil {
				c.setErr(fmt.Errorf("cs2: %w", err))
				return
			}

		case cs2MsgPing:
			if _, err := c.writeFrame(context.Background(), []byte{cs2Magic, cs2MsgPong, 0, 0}); err != nil {
				c.setErr(err)
				return
			}
		case cs2MsgPong:
		case cs2MsgP2PRdyUDP, cs2MsgP2PRdyTCP:
			xiaomiLogger.Info("cs2: received ready message", "type", fmt.Sprintf("0x%02x", buf[1]))
		case cs2MsgClose:
			xiaomiLogger.Info("cs2: received close message", "type", fmt.Sprintf("0x%02x", buf[1]))
			// Send close ACK before exiting
			_, _ = c.writeFrame(context.Background(), []byte{cs2Magic, cs2MsgCloseAck, 0, 0})
			c.setErr(fmt.Errorf("cs2: received close from camera"))
			return
		case cs2MsgCloseAck:
			xiaomiLogger.Info("cs2: received close ACK", "type", fmt.Sprintf("0x%02x", buf[1]))
		case cs2MsgDrwAck: // only for UDP
			c.acceptCommandACK(buf[:n])
		default:
			xiaomiLogger.Info("cs2: received unknown message", "type", fmt.Sprintf("0x%02x", buf[1]), "len", n)
		}
	}
}

// Protocol returns the transport protocol string ("cs2+tcp" or "cs2+udp").
func (c *CS2Conn) Protocol() string {
	if c.isTCP {
		return "cs2+tcp"
	}
	return "cs2+udp"
}

// Version returns the protocol version string.
func (c *CS2Conn) Version() string {
	return "CS2"
}

func (c *CS2Conn) setErr(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}

func (c *CS2Conn) getErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Error returns the connection error, or io.EOF if cleanly closed.
func (c *CS2Conn) Error() error {
	if c.getErr() != nil {
		return c.getErr()
	}
	return io.EOF
}

// workerExitGuard ensures c.err is set when worker() exits.
// Must be called as a defer in worker(). Handles panic recovery and
// ensures a descriptive error is always present (never bare io.EOF).
func (c *CS2Conn) workerExitGuard() {
	if r := recover(); r != nil {
		c.setErr(fmt.Errorf("cs2: panic: %v", r))
	}
	if c.getErr() == nil {
		c.setErr(fmt.Errorf("cs2: connection closed"))
	}
}

// ReadCommand reads a command response from channel 0.
func (c *CS2Conn) ReadCommand() (cmd uint32, data []byte, err error) {
	buf, ok := c.channels[0].Pop(cs2ReadTimeout)
	if !ok {
		if c.getErr() != nil {
			return 0, nil, c.getErr()
		}
		return 0, nil, fmt.Errorf("cs2: no command data for %v", cs2ReadTimeout)
	}
	if len(buf) < 4 {
		return 0, nil, fmt.Errorf("cs2: command header too short: %d", len(buf))
	}
	cmd = binary.LittleEndian.Uint32(buf)
	data = buf[4:]
	return
}

// acceptCommandACK matches channel and sequence so unrelated or late ACKs
// cannot acknowledge the current command.
func (c *CS2Conn) acceptCommandACK(frame []byte) {
	if len(frame) < 8 || frame[4] != cs2MagicDrw || frame[5] != 0 {
		return
	}
	count := int(binary.BigEndian.Uint16(frame[6:8]))
	if count == 0 || count > (len(frame)-8)/2 {
		return
	}
	c.ackMu.Lock()
	defer c.ackMu.Unlock()
	pending := c.pendingAck
	if pending == nil {
		return
	}
	for i := 0; i < count; i++ {
		if binary.BigEndian.Uint16(frame[8+2*i:]) == pending.seq {
			pending.once.Do(func() { close(pending.done) })
			return
		}
	}
}

// WriteCommand sends a command on channel 0 with ACK retry for UDP.
func (c *CS2Conn) WriteCommand(cmd uint32, data []byte) error {
	return c.WriteCommandContext(context.Background(), cmd, data)
}

func (c *CS2Conn) WriteCommandContext(ctx context.Context, cmd uint32, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.cmdMu.Lock()
	defer c.cmdMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	c.initSignals()
	seq := c.seqCh0
	req := cs2MarshalCmd(0, seq, cmd, data)
	c.seqCh0++
	if c.isTCP {
		_, err := c.writeFrame(ctx, req)
		return err
	}
	pending := &cs2CommandAck{seq: seq, done: make(chan struct{})}
	c.ackMu.Lock()
	c.pendingAck = pending
	c.ackMu.Unlock()
	defer func() { c.ackMu.Lock(); c.pendingAck = nil; c.ackMu.Unlock() }()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for attempt := 0; attempt < 5; attempt++ {
		if _, err := c.writeFrame(ctx, req); err != nil {
			return err
		}
		select {
		case <-pending.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return c.Error()
		case <-timer.C:
		}
		timer.Reset(time.Second)
	}
	return fmt.Errorf("cs2: command %d was not acknowledged", cmd)
}

// ReadPacket reads a media packet from channel 2.
func (c *CS2Conn) ReadPacket() (hdr, payload []byte, err error) {
	data, ok := c.channels[2].Pop(cs2ReadTimeout)
	if !ok {
		if c.getErr() != nil {
			return nil, nil, c.getErr()
		}
		return nil, nil, fmt.Errorf("cs2: no media data for %v", cs2ReadTimeout)
	}
	if len(data) < cs2HdrSize {
		return nil, nil, fmt.Errorf("cs2: media header too short: %d", len(data))
	}
	return data[:cs2HdrSize], data[cs2HdrSize:], nil
}

// WritePacket writes a media packet on channel 3.
func (c *CS2Conn) WritePacket(hdr, payload []byte) error {
	c.packetMu.Lock()
	defer c.packetMu.Unlock()
	const offset = 12

	n := cs2HdrSize + uint32(len(payload))
	req := make([]byte, n+offset)
	req[0] = cs2Magic
	req[1] = cs2MsgDrw
	binary.BigEndian.PutUint16(req[2:], uint16(n+8))

	req[4] = cs2MagicDrw
	req[5] = 3 // channel
	binary.BigEndian.PutUint16(req[6:], c.seqCh3)
	c.seqCh3++
	binary.BigEndian.PutUint32(req[8:], n)
	copy(req[offset:], hdr)
	copy(req[offset+cs2HdrSize:], payload)

	_, err := c.writeFrame(context.Background(), req)
	return err
}

func cs2MarshalCmd(channel byte, seq uint16, cmd uint32, payload []byte) []byte {
	size := len(payload)
	req := make([]byte, 4+4+4+4+size)

	// 1. message header (4 bytes)
	req[0] = cs2Magic
	req[1] = cs2MsgDrw
	binary.BigEndian.PutUint16(req[2:], uint16(4+4+4+size))

	// 2. drw header (4 bytes)
	req[4] = cs2MagicDrw
	req[5] = channel
	binary.BigEndian.PutUint16(req[6:], seq)

	// 3. payload size (4 bytes)
	binary.BigEndian.PutUint32(req[8:], uint32(4+size))

	// 4. payload command (4 bytes)
	binary.BigEndian.PutUint32(req[12:], cmd)

	// 5. payload
	copy(req[16:], payload)

	return req
}

func cs2NewUDPConn(host string, port int) (net.Conn, error) {
	conn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}

	addr, err := net.ResolveUDPAddr("udp", host)
	if err != nil {
		addr = &net.UDPAddr{IP: net.ParseIP(host), Port: port}
	}

	return &cs2UDPConn{UDPConn: conn, addr: addr}, nil
}

type cs2UDPConn struct {
	*net.UDPConn
	addr *net.UDPAddr
}

func (c *cs2UDPConn) Read(b []byte) (n int, err error) {
	var addr *net.UDPAddr
	for {
		n, addr, err = c.UDPConn.ReadFromUDP(b)
		if err != nil {
			return 0, err
		}

		if addr.IP.Equal(c.addr.IP) && addr.Port == c.addr.Port {
			return
		}
	}
}

func (c *cs2UDPConn) Write(b []byte) (n int, err error) {
	return c.UDPConn.WriteToUDP(b, c.addr)
}

func (c *cs2UDPConn) RemoteAddr() net.Addr {
	return c.addr
}

func (c *cs2UDPConn) WriteUntil(req []byte, ok func(res []byte) bool) ([]byte, error) {
	stopRetransmit := make(chan struct{})
	defer close(stopRetransmit)

	go func() {
		time.Sleep(time.Nanosecond)
		for {
			select {
			case <-stopRetransmit:
				return
			default:
			}
			if _, err := c.Write(req); err != nil {
				return
			}
			select {
			case <-stopRetransmit:
				return
			case <-time.After(time.Second):
			}
		}
	}()

	buf := make([]byte, 1200)

	for {
		n, addr, err := c.UDPConn.ReadFromUDP(buf)
		if err != nil {
			return nil, err
		}

		if string(addr.IP) != string(c.addr.IP) || n < 16 {
			continue
		}

		if ok(buf[:n]) {
			c.addr.Port = addr.Port
			return buf[:n], nil
		}
	}
}

func cs2NewTCPConn(addr string) (net.Conn, error) {
	return cs2NewTCPConnContext(context.Background(), addr)
}

func cs2NewTCPConnContext(ctx context.Context, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return &cs2TCPConn{TCPConn: conn.(*net.TCPConn), rd: bufio.NewReader(conn)}, nil
}

type cs2TCPConn struct {
	*net.TCPConn
	rd *bufio.Reader
}

func (c *cs2TCPConn) Read(p []byte) (n int, err error) {
	tmp := make([]byte, 8)
	if _, err = io.ReadFull(c.rd, tmp); err != nil {
		return
	}
	n = int(binary.BigEndian.Uint16(tmp))
	if len(p) < n {
		return 0, fmt.Errorf("cs2 tcp: buffer too small")
	}
	_, err = io.ReadFull(c.rd, p[:n])
	return
}

func (c *cs2TCPConn) Write(req []byte) (n int, err error) {
	n = len(req)
	buf := make([]byte, 8+n)
	binary.BigEndian.PutUint16(buf, uint16(n))
	buf[2] = cs2MagicTCP
	copy(buf[8:], req)
	_, err = c.TCPConn.Write(buf)
	return
}

func newCS2DataChannel(pushSize, popSize int) *cs2DataChannel {
	c := &cs2DataChannel{}
	if pushSize > 0 {
		c.pushBuf = make(map[uint16][]byte, pushSize)
		c.pushSize = pushSize
	}
	if popSize >= 0 {
		c.popBuf = make(chan []byte, popSize)
	}
	return c
}

type cs2DataChannel struct {
	waitSeq  uint16
	pushBuf  map[uint16][]byte
	pushSize int

	waitData []byte
	waitSize int
	popBuf   chan []byte
}

func (c *cs2DataChannel) Push(b []byte) error {
	c.waitData = append(c.waitData, b...)

	for c.waitSize > 0 || len(c.waitData) >= 4 {
		// Every new data starts with size. There can be several data inside one packet.
		if c.waitSize == 0 {
			c.waitSize = int(binary.BigEndian.Uint32(c.waitData))
			if c.waitSize == 0 || c.waitSize > 8<<20 {
				return fmt.Errorf("cs2: invalid payload size %d", c.waitSize)
			}
			c.waitData = c.waitData[4:]
		}
		if c.waitSize > len(c.waitData) {
			break
		}

		select {
		case c.popBuf <- c.waitData[:c.waitSize]:
		default:
			// Drop oldest frame to make room for new one.
			// For video streams, dropping a frame is far better than
			// disconnecting and reconnecting the entire P2P session.
			select {
			case <-c.popBuf:
			default:
			}
			select {
			case c.popBuf <- c.waitData[:c.waitSize]:
			default:
				return fmt.Errorf("cs2: pop buffer still full after drain")
			}
		}

		c.waitData = c.waitData[c.waitSize:]
		c.waitSize = 0
	}
	return nil
}

func (c *cs2DataChannel) Pop(timeout time.Duration) ([]byte, bool) {
	select {
	case data, ok := <-c.popBuf:
		return data, ok
	case <-time.After(timeout):
		return nil, false
	}
}

func (c *cs2DataChannel) Close() {
	close(c.popBuf)
}

// PushSeq returns how many seq were processed.
// Returns 0 if seq was saved or processed earlier.
// Returns -1 if seq could not be saved (buffer full or disabled).
func (c *cs2DataChannel) PushSeq(seq uint16, data []byte) (int, error) {
	diff := int16(seq - c.waitSeq)
	// Check if this is seq from the future.
	if diff > 0 {
		// Support disabled buffer.
		if c.pushSize == 0 {
			return -1, nil
		}
		// Check if we don't have this seq in the buffer.
		if c.pushBuf[seq] == nil {
			// Check if there is enough space in the buffer.
			if len(c.pushBuf) == c.pushSize {
				return -1, nil
			}
			c.pushBuf[seq] = bytes.Clone(data)
		}
		return 0, nil
	}

	// Check if this is seq from the past.
	if diff < 0 {
		return 0, nil
	}

	for i := 1; ; i++ {
		if err := c.Push(data); err != nil {
			return i, err
		}
		c.waitSeq++
		// Check if we have next seq in the buffer.
		if data = c.pushBuf[c.waitSeq]; data != nil {
			delete(c.pushBuf, c.waitSeq)
		} else {
			return i, nil
		}
	}
}
