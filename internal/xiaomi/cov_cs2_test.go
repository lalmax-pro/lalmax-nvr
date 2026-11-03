// SPDX-License-Identifier: MIT
//
// Coverage tests for the CS2 P2P transport. A local fake camera speaks the CS2
// handshake (UDP punch + optional TCP switch) and the DRW channel framing, so
// the whole transport and both data channels are exercised without hardware.

package xiaomi

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// covCS2Device is an in-process fake Xiaomi camera speaking CS2.
type covCS2Device struct {
	t         *testing.T
	transport string // "udp" or "tcp"
	port      int
	udp       *net.UDPConn
	tcp       net.Listener
	key       []byte // MISS media encryption key (set by the test)

	mu        sync.Mutex
	clientUDP *net.UDPAddr
	tcpConn   net.Conn
	step      int // handshake step
	seq       [4]uint16
	commands  []uint32
	rawFrames [][]byte
	pings     int
	authOK    bool
	autoMedia bool

	mediaOnce sync.Once
	mediaDone chan struct{}
	closeOnce sync.Once
}

func newCovCS2Device(t *testing.T, transport string) *covCS2Device {
	t.Helper()
	for attempt := 0; attempt < 30; attempt++ {
		tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		port := tcpLn.Addr().(*net.TCPAddr).Port
		udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
		if err != nil {
			_ = tcpLn.Close()
			continue
		}
		d := &covCS2Device{t: t, transport: transport, port: port, udp: udpConn, tcp: tcpLn, authOK: true, mediaDone: make(chan struct{})}
		go d.serveUDP()
		if transport == "tcp" {
			go d.serveTCP()
		} else {
			_ = tcpLn.Close()
			d.tcp = nil
		}
		t.Cleanup(d.Close)
		return d
	}
	t.Fatal("could not bind a local CS2 device port")
	return nil
}

func (d *covCS2Device) Close() {
	d.closeOnce.Do(func() {
		close(d.mediaDone)
		_ = d.udp.Close()
		if d.tcp != nil {
			_ = d.tcp.Close()
		}
		d.mu.Lock()
		if d.tcpConn != nil {
			_ = d.tcpConn.Close()
		}
		d.mu.Unlock()
	})
}

func (d *covCS2Device) host() string {
	return fmt.Sprintf("127.0.0.1:%d", d.port)
}

func covCS2Pad(n int, msgType byte) []byte {
	msg := make([]byte, n)
	msg[0] = cs2Magic
	msg[1] = msgType
	return msg
}

func (d *covCS2Device) serveUDP() {
	buf := make([]byte, 2048)
	for {
		n, addr, err := d.udp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		d.mu.Lock()
		switch d.step {
		case 0:
			d.clientUDP = addr
			d.step = 1
			d.mu.Unlock()
			_, _ = d.udp.WriteToUDP(covCS2Pad(16, cs2MsgPunchPkt), addr)
			continue
		case 1:
			ready := byte(cs2MsgP2PRdyUDP)
			if d.transport == "tcp" {
				ready = cs2MsgP2PRdyTCP
			}
			d.step = 2
			d.mu.Unlock()
			_, _ = d.udp.WriteToUDP(covCS2Pad(16, ready), addr)
			continue
		}
		d.mu.Unlock()

		frame := append([]byte(nil), buf[:n]...)
		d.handleFrame(frame)
	}
}

func (d *covCS2Device) serveTCP() {
	for {
		conn, err := d.tcp.Accept()
		if err != nil {
			return
		}
		d.mu.Lock()
		d.tcpConn = conn
		d.mu.Unlock()
		go d.readTCP(conn)
	}
}

func (d *covCS2Device) readTCP(conn net.Conn) {
	defer conn.Close()
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(conn, hdr); err != nil {
			return
		}
		n := int(binary.BigEndian.Uint16(hdr))
		if n == 0 {
			continue
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return
		}
		d.handleFrame(payload)
	}
}

// handleFrame processes one raw CS2 message coming from the client.
func (d *covCS2Device) handleFrame(frame []byte) {
	if len(frame) < 2 {
		return
	}
	switch frame[1] {
	case cs2MsgPing:
		d.mu.Lock()
		d.pings++
		d.mu.Unlock()
	case cs2MsgDrw:
		if len(frame) < 16 {
			return
		}
		ch := frame[5]
		cmd := binary.BigEndian.Uint32(frame[12:16])
		d.mu.Lock()
		d.commands = append(d.commands, cmd)
		if ch != 0 {
			d.rawFrames = append(d.rawFrames, append([]byte(nil), frame...))
		}
		d.mu.Unlock()

		if ch == 0 {
			d.handleCommand(cmd)
		}
		if d.transport == "udp" {
			ack := []byte{cs2Magic, cs2MsgDrwAck, 0, 6, cs2MagicDrw, ch, 0, 1, frame[6], frame[7]}
			_ = d.writeMsg(ack)
		}
	}
}

func (d *covCS2Device) handleCommand(cmd uint32) {
	switch cmd {
	case missCmdAuthReq:
		d.mu.Lock()
		ok := d.authOK
		d.mu.Unlock()
		body := []byte(`{"result":"success"}`)
		if !ok {
			body = []byte(`{"result":"failure"}`)
		}
		data := binary.LittleEndian.AppendUint32(nil, missCmdAuthRes)
		data = append(data, body...)
		_ = d.sendDrw(0, data)
	case missCmdEncoded:
		// Any encrypted command (start media, start audio, ...) makes the fake
		// camera push frames, the way a real camera starts streaming.
		d.mu.Lock()
		auto := d.autoMedia
		d.mu.Unlock()
		if auto {
			d.mediaOnce.Do(func() { go d.sendMediaSequence() })
		}
	}
}

func (d *covCS2Device) writeMsg(msg []byte) error {
	d.mu.Lock()
	if d.transport == "tcp" {
		conn := d.tcpConn
		d.mu.Unlock()
		if conn == nil {
			return fmt.Errorf("no tcp conn")
		}
		frame := make([]byte, 8+len(msg))
		binary.BigEndian.PutUint16(frame, uint16(len(msg)))
		frame[2] = cs2MagicTCP
		copy(frame[8:], msg)
		_, err := conn.Write(frame)
		return err
	}
	addr := d.clientUDP
	d.mu.Unlock()
	if addr == nil {
		return fmt.Errorf("no udp client")
	}
	_, err := d.udp.WriteToUDP(msg, addr)
	return err
}

// sendDrw emits a DRW message on the given channel with the next sequence number.
func (d *covCS2Device) sendDrw(ch byte, payload []byte) error {
	d.mu.Lock()
	seq := d.seq[ch]
	d.seq[ch]++
	d.mu.Unlock()

	msg := make([]byte, 12+len(payload))
	msg[0] = cs2Magic
	msg[1] = cs2MsgDrw
	binary.BigEndian.PutUint16(msg[2:], uint16(len(msg)))
	msg[4] = cs2MagicDrw
	msg[5] = ch
	binary.BigEndian.PutUint16(msg[6:], seq)
	binary.BigEndian.PutUint32(msg[8:], uint32(len(payload)))
	copy(msg[12:], payload)
	return d.writeMsg(msg)
}

func (d *covCS2Device) sendControl(msgType byte) error {
	return d.writeMsg(covCS2Pad(8, msgType))
}

func (d *covCS2Device) sendMediaPacket(codecID uint32, ts uint64, plaintext []byte) error {
	enc, err := Encode(plaintext, d.key)
	if err != nil {
		return err
	}
	hdr := make([]byte, missHdrSize)
	binary.LittleEndian.PutUint32(hdr, uint32(len(enc)))
	binary.LittleEndian.PutUint32(hdr[4:], codecID)
	binary.LittleEndian.PutUint64(hdr[16:], ts)
	return d.sendDrw(2, append(hdr, enc...))
}

// sendMediaSequence mimics a camera pushing codec params, IDR/P frames and audio.
func (d *covCS2Device) sendMediaSequence() {
	annexB := func(nalu ...[]byte) []byte {
		var out []byte
		for _, n := range nalu {
			out = append(out, 0, 0, 0, 1)
			out = append(out, n...)
		}
		return out
	}
	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x40}
	p := []byte{0x41, 0x10, 0x00, 0x0c}

	_ = d.sendMediaPacket(missCodecH264, 1000, annexB(sps, pps, idr))
	_ = d.sendMediaPacket(missCodecH264, 1033, annexB(p))
	_ = d.sendMediaPacket(missCodecPCMA, 1050, []byte{0x11, 0x22, 0x33})
	// Unknown codec IDs are skipped by the recorder.
	_ = d.sendMediaPacket(7, 1060, []byte{0x01, 0x02})

	// Keep pushing frames so a reader blocked in ReadPacket wakes up quickly.
	ts := uint64(1070)
	for {
		select {
		case <-d.mediaDone:
			return
		case <-time.After(10 * time.Millisecond):
		}
		ts += 33
		_ = d.sendMediaPacket(missCodecH264, ts, annexB(p))
	}
}

func (d *covCS2Device) waitHandshake(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.transport == "tcp" {
			return d.tcpConn != nil
		}
		return d.step >= 2
	}, 3*time.Second, 5*time.Millisecond)
}

func (d *covCS2Device) snapshot() ([]uint32, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]uint32(nil), d.commands...), d.pings
}

// --- Dialling and media traffic -----------------------------------------

func TestCovCS2DialUDPSession(t *testing.T) {
	device := newCovCS2Device(t, "udp")
	device.key = bytes.Repeat([]byte{7}, 32)

	conn, err := CS2Dial(device.host(), "udp", 0)
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.Equal(t, "cs2+udp", conn.Protocol())
	require.Equal(t, "CS2", conn.Version())
	device.waitHandshake(t)

	// Command channel (0) and media channel (2).
	require.NoError(t, device.sendDrw(0, append(binary.LittleEndian.AppendUint32(nil, missCmdDevInfoRes), []byte(`{"x":1}`)...)))
	cmd, data, err := conn.ReadCommand()
	require.NoError(t, err)
	require.Equal(t, uint32(missCmdDevInfoRes), cmd)
	require.JSONEq(t, `{"x":1}`, string(data))

	require.NoError(t, device.sendMediaPacket(missCodecH264, 1234, []byte{0x00, 0x00, 0x00, 0x01, 0x65}))
	hdr, payload, err := conn.ReadPacket()
	require.NoError(t, err)
	require.Len(t, hdr, missHdrSize)
	require.Equal(t, uint32(missCodecH264), binary.LittleEndian.Uint32(hdr[4:]))
	require.NotEmpty(t, payload)

	// UDP WriteCommand waits for the DRW ACK (the fake device sends it).
	require.NoError(t, conn.WriteCommand(missCmdVideoStart, []byte(`{}`)))
	cmds, _ := device.snapshot()
	require.Contains(t, cmds, uint32(missCmdVideoStart))

	// WritePacket is captured on channel 3.
	hdrOut := make([]byte, missHdrSize)
	require.NoError(t, conn.WritePacket(hdrOut, []byte("audio")))
	require.Eventually(t, func() bool {
		device.mu.Lock()
		defer device.mu.Unlock()
		return len(device.rawFrames) > 0
	}, 2*time.Second, 5*time.Millisecond)

	require.NoError(t, conn.Close())
	require.Eventually(t, func() bool { return conn.Error() != nil }, 2*time.Second, 10*time.Millisecond)
}

func TestCovCS2DialTCPSessionControlMessages(t *testing.T) {
	device := newCovCS2Device(t, "tcp")

	conn, err := CS2Dial(device.host(), "tcp", time.Minute)
	require.NoError(t, err)
	require.Equal(t, "cs2+tcp", conn.Protocol())
	device.waitHandshake(t)

	// WriteCommand over TCP is fire-and-forget and returns immediately.
	require.NoError(t, conn.WriteCommand(missCmdAuthReq, []byte(`{}`)))
	require.NoError(t, device.sendDrw(0, append(binary.LittleEndian.AppendUint32(nil, missCmdGetAudioFormatRes), []byte("fmt")...)))
	cmd, data, err := conn.ReadCommand()
	require.NoError(t, err)
	require.Equal(t, uint32(missCmdGetAudioFormatRes), cmd)
	require.Equal(t, "fmt", string(data))

	// Control frames the worker must tolerate.
	for _, msgType := range []byte{cs2MsgPing, cs2MsgPong, cs2MsgP2PRdyUDP, cs2MsgP2PRdyTCP, cs2MsgDrwAck, 0x99} {
		require.NoError(t, device.sendControl(msgType))
	}

	// The worker sends a keepalive ping once the 1s TCP read deadline expires.
	require.Eventually(t, func() bool {
		_, pings := device.snapshot()
		return pings > 0
	}, 3*time.Second, 20*time.Millisecond)

	// A close frame ends the worker and it answers with a close ACK.
	require.NoError(t, device.sendControl(cs2MsgClose))
	require.Eventually(t, func() bool {
		e := conn.getErr()
		return e != nil && bytes.Contains([]byte(e.Error()), []byte("received close"))
	}, 2*time.Second, 10*time.Millisecond)
	require.Contains(t, conn.Error().Error(), "received close from camera")
	require.Equal(t, conn.getErr(), conn.Error())
}

func TestCovCS2UDPIdleTimeout(t *testing.T) {
	device := newCovCS2Device(t, "udp")

	conn, err := CS2Dial(device.host(), "udp", 150*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, 150*time.Millisecond, conn.idleTimeout)
	device.waitHandshake(t)

	// The device stays silent, so the worker times out and closes the channels.
	require.Eventually(t, func() bool {
		_, _, readErr := conn.ReadCommand()
		return readErr != nil && bytes.Contains([]byte(readErr.Error()), []byte("no data for"))
	}, 3*time.Second, 20*time.Millisecond)

	_, _, err = conn.ReadPacket()
	require.Error(t, err)
	require.NoError(t, conn.Close())
}

func TestCovCS2DialDefaultIdleTimeoutValue(t *testing.T) {
	device := newCovCS2Device(t, "udp")
	conn, err := CS2Dial(device.host(), "udp", 0)
	require.NoError(t, err)
	require.Equal(t, defaultIdleTimeout, conn.idleTimeout)
	require.NoError(t, conn.Close())
}

func TestCovCS2HandshakeTCPDialFailure(t *testing.T) {
	device := newCovCS2Device(t, "tcp")
	// Stop accepting TCP connections but keep the UDP handshake alive: the
	// handshake succeeds and the TCP dial then fails.
	require.NoError(t, device.tcp.Close())
	_, err := CS2Dial(device.host(), "tcp", time.Second)
	require.Error(t, err)
}

func TestCovCS2NewTCPConnError(t *testing.T) {
	// Port 1 on loopback is not listening.
	_, err := cs2NewTCPConn("127.0.0.1:1")
	require.Error(t, err)
}

func TestCovCS2WriteUntilClosedSocket(t *testing.T) {
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	conn := &cs2UDPConn{UDPConn: udp, addr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}}
	require.NoError(t, udp.Close())

	_, err = conn.WriteUntil([]byte{cs2Magic, cs2MsgLanSearch, 0, 0}, func([]byte) bool { return true })
	require.Error(t, err)
}

func TestCovCS2TCPConnReadErrors(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()

	raw, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer raw.Close()
	server := <-accepted
	defer server.Close()

	tcpConn := raw.(*net.TCPConn)
	conn := &cs2TCPConn{TCPConn: tcpConn, rd: bufio.NewReader(tcpConn)}

	// Frame announces more payload than the caller's buffer can hold.
	frame := make([]byte, 8)
	binary.BigEndian.PutUint16(frame, 64)
	frame[2] = cs2MagicTCP
	_, err = server.Write(frame)
	require.NoError(t, err)
	_, err = conn.Read(make([]byte, 4))
	require.ErrorContains(t, err, "buffer too small")

	// A plain write/read round trip works with a large enough buffer.
	msg := bytes.Repeat([]byte{0xAB}, 10)
	_, err = conn.Write(msg)
	require.NoError(t, err)
	hdr := make([]byte, 8)
	_, err = io.ReadFull(server, hdr)
	require.NoError(t, err)
	require.Equal(t, uint16(len(msg)), binary.BigEndian.Uint16(hdr))
	require.Equal(t, byte(cs2MagicTCP), hdr[2])
	got := make([]byte, len(msg))
	_, err = io.ReadFull(server, got)
	require.NoError(t, err)
	require.Equal(t, msg, got)

	// Closing the peer makes the framed read fail.
	require.NoError(t, server.Close())
	frame2 := make([]byte, 8)
	binary.BigEndian.PutUint16(frame2, 16)
	_, _ = raw.Write(frame2)
	_, err = conn.Read(make([]byte, 16))
	require.Error(t, err)
}

// --- Data channel unit tests --------------------------------------------

func TestCovCS2DataChannelPushSeqMatrix(t *testing.T) {
	// Disabled reorder buffer: future seq cannot be stored.
	disabled := newCS2DataChannel(0, 4)
	n, err := disabled.PushSeq(3, []byte("future"))
	require.NoError(t, err)
	require.Equal(t, -1, n)

	// Small reorder buffer: stores up to pushSize future seqs, dedupes, refuses overflow.
	ch := newCS2DataChannel(2, 4)
	n, err = ch.PushSeq(2, []byte("two"))
	require.NoError(t, err)
	require.Equal(t, 0, n)
	n, err = ch.PushSeq(2, []byte("two-again"))
	require.NoError(t, err)
	require.Equal(t, 0, n)
	n, err = ch.PushSeq(3, []byte("three"))
	require.NoError(t, err)
	require.Equal(t, 0, n)
	n, err = ch.PushSeq(4, []byte("four"))
	require.NoError(t, err)
	require.Equal(t, -1, n, "buffer is full")

	// Old sequence numbers are dropped.
	n, err = ch.PushSeq(0, []byte("zero"))
	require.NoError(t, err)
	require.Equal(t, 1, n, "the in-order sequence number is delivered")

	// In-order delivery drains the buffered continuations.
	ch2 := newCS2DataChannel(4, 8)
	n, err = ch2.PushSeq(1, []byte("one"))
	require.NoError(t, err)
	require.Equal(t, 0, n)
	n, err = ch2.PushSeq(0, []byte("zero"))
	require.NoError(t, err)
	require.Equal(t, 2, n, "seq 0 and the buffered seq 1 are delivered")
}

func TestCovCS2DataChannelPushSplitsAndDrops(t *testing.T) {
	// Two messages inside a single frame.
	frame := binary.BigEndian.AppendUint32(nil, 3)
	frame = append(frame, 'a', 'a', 'a')
	frame = binary.BigEndian.AppendUint32(frame, 2)
	frame = append(frame, 'b', 'b')

	ch := newCS2DataChannel(0, 4)
	require.NoError(t, ch.Push(frame))
	msg, ok := ch.Pop(time.Second)
	require.True(t, ok)
	require.Equal(t, []byte("aaa"), msg)
	msg, ok = ch.Pop(time.Second)
	require.True(t, ok)
	require.Equal(t, []byte("bb"), msg)

	// Partial message stays buffered until the rest arrives.
	ch2 := newCS2DataChannel(0, 2)
	partial := binary.BigEndian.AppendUint32(nil, 5)
	partial = append(partial, "ab"...)
	require.NoError(t, ch2.Push(partial))
	_, ok = ch2.Pop(10 * time.Millisecond)
	require.False(t, ok)
	require.NoError(t, ch2.Push(append([]byte(nil), "cde"...)))
	msg, ok = ch2.Pop(time.Second)
	require.True(t, ok)
	require.Equal(t, []byte("abcde"), msg)

	// A full pop buffer drains the oldest frame to make room for the newest.
	ch3 := newCS2DataChannel(0, 1)
	msgBytes := func(s string) []byte {
		b := binary.BigEndian.AppendUint32(nil, uint32(len(s)))
		return append(b, s...)
	}
	require.NoError(t, ch3.Push(msgBytes("old")))
	require.NoError(t, ch3.Push(msgBytes("new")))
	msg, ok = ch3.Pop(time.Second)
	require.True(t, ok)
	require.Equal(t, []byte("new"), msg)

	// Closing the channel makes Pop report a closed channel.
	ch3.Close()
	_, ok = ch3.Pop(time.Second)
	require.False(t, ok)
}

func TestCovCS2DataChannelPopTimeout(t *testing.T) {
	ch := newCS2DataChannel(0, 1)
	start := time.Now()
	_, ok := ch.Pop(20 * time.Millisecond)
	require.False(t, ok)
	require.GreaterOrEqual(t, time.Since(start), 15*time.Millisecond)
}

func TestCovCS2WorkerControlFramesUDP(t *testing.T) {
	// Drive the worker with a mocked net.Conn so UDP-only branches are hit
	// deterministically (DRW on channel 2 and the DRW ACK callback).
	frames := make(chan []byte, 8)
	conn := &covScriptedConn{in: frames, writes: make(chan []byte, 8)}
	c := &CS2Conn{
		Conn:        conn,
		idleTimeout: time.Minute,
		channels: [4]*cs2DataChannel{
			newCS2DataChannel(10, 10), nil, newCS2DataChannel(100, 100), nil,
		},
	}
	done := make(chan struct{})
	go func() {
		c.worker()
		close(done)
	}()

	// A UDP DRW frame carrying one 4-byte-sized payload.
	payload := binary.BigEndian.AppendUint32(nil, 5)
	payload = append(payload, 1, 2, 3, 4, 5)
	drw := make([]byte, 8+len(payload))
	drw[0] = cs2Magic
	drw[1] = cs2MsgDrw
	drw[4] = cs2MagicDrw
	drw[5] = 2
	copy(drw[8:], payload)
	frames <- drw

	data, ok := c.channels[2].Pop(2 * time.Second)
	require.True(t, ok)
	require.Equal(t, []byte{1, 2, 3, 4, 5}, data)

	// The worker must have acknowledged the frame.
	select {
	case ack := <-conn.writes:
		require.Equal(t, byte(cs2MsgDrwAck), ack[1])
	case <-time.After(2 * time.Second):
		t.Fatal("expected a DRW ACK")
	}

	// A cmdAck callback fires on DRW ACK messages.
	acked := make(chan struct{}, 1)
	c.mu.Lock()
	c.cmdAck = func() {
		select {
		case acked <- struct{}{}:
		default:
		}
	}
	c.mu.Unlock()
	frames <- covCS2Pad(8, cs2MsgDrwAck)
	select {
	case <-acked:
	case <-time.After(2 * time.Second):
		t.Fatal("cmdAck callback was not invoked")
	}

	// Closing the input ends the worker and surfaces an error.
	close(frames)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit")
	}
	require.Error(t, c.Error())
}

// covScriptedConn feeds preset frames to the worker and records writes.
type covScriptedConn struct {
	in     chan []byte
	writes chan []byte
}

func (c *covScriptedConn) Read(p []byte) (int, error) {
	frame, ok := <-c.in
	if !ok {
		return 0, io.EOF
	}
	return copy(p, frame), nil
}

func (c *covScriptedConn) Write(p []byte) (int, error) {
	select {
	case c.writes <- append([]byte(nil), p...):
	default:
	}
	return len(p), nil
}

func (c *covScriptedConn) Close() error { return nil }

func (c *covScriptedConn) LocalAddr() net.Addr              { return covScriptedAddr{} }
func (c *covScriptedConn) RemoteAddr() net.Addr             { return covScriptedAddr{} }
func (c *covScriptedConn) SetDeadline(time.Time) error      { return nil }
func (c *covScriptedConn) SetReadDeadline(time.Time) error  { return nil }
func (c *covScriptedConn) SetWriteDeadline(time.Time) error { return nil }

type covScriptedAddr struct{}

func (covScriptedAddr) Network() string { return "scripted" }
func (covScriptedAddr) String() string  { return "scripted" }
