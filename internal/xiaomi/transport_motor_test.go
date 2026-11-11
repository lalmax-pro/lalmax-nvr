// SPDX-License-Identifier: MIT

package xiaomi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

func commandACK(channel byte, seq uint16) []byte {
	return []byte{cs2Magic, cs2MsgDrwAck, 0, 6, cs2MagicDrw, channel, 0, 1, byte(seq >> 8), byte(seq)}
}

func TestCS2CommandACKMatchesChannelAndSequence(t *testing.T) {
	t.Helper()
	c := newTestCS2Conn(t, &mockCS2Conn{})
	pending := &cs2CommandAck{seq: 9, done: make(chan struct{})}
	c.pendingAck = pending
	for _, frame := range [][]byte{commandACK(2, 9), commandACK(0, 8), {1}, {cs2Magic, cs2MsgDrwAck, 0, 6, cs2MagicDrw, 0, 0, 3, 0, 9}} {
		c.acceptCommandACK(frame)
		select {
		case <-pending.done:
			t.Fatal("unrelated ACK accepted")
		default:
		}
	}
	c.acceptCommandACK(commandACK(0, 9))
	c.acceptCommandACK(commandACK(0, 9))
	select {
	case <-pending.done:
	default:
		t.Fatal("matching ACK ignored")
	}
}

func TestCS2ConcurrentCommandsAndACKs(t *testing.T) {
	t.Helper()
	raw := &covScriptedConn{in: make(chan []byte, 128), writes: make(chan []byte, 128)}
	c := newTestCS2Conn(t, raw)
	workerDone := make(chan struct{})
	go func() { c.worker(); close(workerDone) }()
	responderDone := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		defer close(responderDone)
		for {
			select {
			case <-stop:
				return
			case frame := <-raw.writes:
				if len(frame) >= 8 && frame[1] == cs2MsgDrw {
					seq := binary.BigEndian.Uint16(frame[6:8])
					raw.in <- commandACK(2, seq)
					raw.in <- commandACK(0, seq-1)
					raw.in <- commandACK(0, seq)
				}
			}
		}
	}()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			errs <- c.WriteCommandContext(ctx, missCmdEncoded, []byte{1})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	close(stop)
	<-responderDone
	close(raw.in)
	<-workerDone
}

func TestCS2RejectsTruncatedMessagesWithoutPanic(t *testing.T) {
	t.Helper()
	for _, channel := range []int{0, 2} {
		c := newTestCS2Conn(t, &mockCS2Conn{})
		require.NoError(t, c.channels[channel].Push(makeDataWithSize("x")))
		require.NotPanics(t, func() {
			if channel == 0 {
				_, _, err := c.ReadCommand()
				require.Error(t, err)
			} else {
				_, _, err := c.ReadPacket()
				require.Error(t, err)
			}
		})
	}
	// A short payload split after its size header must still be delivered for validation.
	split := newTestCS2Conn(t, &mockCS2Conn{})
	require.NoError(t, split.channels[2].Push(binary.BigEndian.AppendUint32(nil, 1)))
	require.NoError(t, split.channels[2].Push([]byte{0}))
	_, _, err := split.ReadPacket()
	require.Error(t, err)
	raw := &mockCS2Conn{reads: [][]byte{{cs2Magic}, {cs2Magic, cs2MsgDrw, 0, 0}, {cs2Magic, cs2MsgDrw, 0, 0, cs2MagicDrw, 255, 0, 0}, {cs2Magic, cs2MsgPing, 0, 0}}, err: io.EOF}
	c := newTestCS2Conn(t, raw)
	require.NotPanics(t, c.worker)
	found := false
	for _, frame := range raw.writes {
		if len(frame) >= 2 && frame[1] == cs2MsgPong {
			found = true
		}
	}
	require.True(t, found, "worker must keep processing after malformed frames")
}

func TestH265IRAPIncludesParameterSets(t *testing.T) {
	t.Helper()
	for _, typ := range []byte{16, 19, 20, 21, 23} {
		pub := &covPubSession{}
		r := NewXiaomiRecorder(XiaomiRecorderConfig{}, nil)
		r.pubSession = pub
		r.streamStart = time.Now()
		r.codec = model.FormatH265
		r.vps = []byte{0x40, 1}
		r.sps = []byte{0x42, 1}
		r.pps = []byte{0x44, 1}
		r.forwardVideo([]byte{typ << 1, 1, 0xaa})
		require.Len(t, parseAVCCNALUs(pub.last().Payload, 0), 4)
	}
}

func motorCommands(t *testing.T, client *MISSClient, conn *mockMISSConn) []struct {
	Direction string
	Speed     int
} {
	t.Helper()
	conn.mu.Lock()
	defer conn.mu.Unlock()
	var result []struct {
		Direction string
		Speed     int
	}
	for _, cmd := range conn.writtenCmds {
		decoded, err := Decode(cmd.data, client.key)
		require.NoError(t, err)
		if len(decoded) < 4 || binary.BigEndian.Uint32(decoded) != missCmdMotorReq {
			continue
		}
		var body struct {
			Direction string
			Speed     int
		}
		require.NoError(t, json.Unmarshal(decoded[4:], &body))
		result = append(result, body)
	}
	return result
}

func TestMotorReusesSessionAndStopsAtLeaseExpiry(t *testing.T) {
	t.Helper()
	client, conn := newTestMISSClient()
	r := NewXiaomiRecorder(XiaomiRecorderConfig{}, nil)
	r.motorLease = 30 * time.Millisecond
	r.attachMotorClient(client)
	t.Cleanup(func() { r.detachMotorClient(client) })
	require.NoError(t, r.MotorControl(context.Background(), "left", 5))
	require.Eventually(t, func() bool { return len(motorCommands(t, client, conn)) >= 2 }, time.Second, 5*time.Millisecond)
	commands := motorCommands(t, client, conn)
	require.Equal(t, "left", commands[0].Direction)
	require.Equal(t, 5, commands[0].Speed)
	require.Equal(t, "stop", commands[1].Direction)
	require.Zero(t, commands[1].Speed)
}

func TestMotorStopAndReconnectInvalidateOldLease(t *testing.T) {
	t.Helper()
	client, conn := newTestMISSClient()
	next, nextConn := newTestMISSClient()
	r := NewXiaomiRecorder(XiaomiRecorderConfig{}, nil)
	r.motorLease = 30 * time.Millisecond
	r.attachMotorClient(client)
	require.NoError(t, r.MotorControl(context.Background(), "up", 3))
	require.NoError(t, r.MotorControl(context.Background(), "stop", 0))
	r.attachMotorClient(next)
	r.detachMotorClient(client)
	r.motorLease = time.Hour
	require.NoError(t, r.MotorControl(context.Background(), "right", 10))
	time.Sleep(60 * time.Millisecond)
	require.Len(t, motorCommands(t, client, conn), 2)
	require.Len(t, motorCommands(t, next, nextConn), 1, "old lease must not stop a new session")
	r.stopMotorBeforeClose()
	r.detachMotorClient(next)
	require.Equal(t, "stop", motorCommands(t, next, nextConn)[1].Direction)
	require.ErrorIs(t, r.MotorControl(context.Background(), "left", 5), ErrNotConnected)
}

func TestMotorInvalidInputsDoNotSendCommands(t *testing.T) {
	t.Helper()
	client, conn := newTestMISSClient()
	for _, args := range []struct {
		direction string
		speed     int
	}{{"unknown", 5}, {"left", 0}, {"up", 101}, {"stop", 2}} {
		require.Error(t, client.MotorControlContext(context.Background(), args.direction, args.speed))
	}
	require.Empty(t, motorCommands(t, client, conn))
}

func TestMotorDetachStopsActiveDevice(t *testing.T) {
	t.Helper()
	client, conn := newTestMISSClient()
	r := NewXiaomiRecorder(XiaomiRecorderConfig{}, nil)
	r.attachMotorClient(client)
	require.NoError(t, r.MotorControl(context.Background(), "down", 5))
	r.detachMotorClient(client)
	commands := motorCommands(t, client, conn)
	require.Len(t, commands, 2)
	require.Equal(t, "stop", commands[1].Direction)
	require.ErrorIs(t, r.MotorControl(context.Background(), "left", 5), ErrNotConnected)
}

type signalWriteConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *signalWriteConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Write(p)
}

func TestCS2CanceledWriteDoesNotBlockStop(t *testing.T) {
	t.Helper()
	raw, peer := net.Pipe()
	defer peer.Close()
	signaled := &signalWriteConn{Conn: raw, started: make(chan struct{})}
	c := newTestCS2Conn(t, signaled)
	c.isTCP = true
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.WriteCommandContext(ctx, missCmdEncoded, []byte{1}) }()
	select {
	case <-signaled.started:
	case <-time.After(time.Second):
		t.Fatal("command did not reach the socket")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("command write ignored cancellation")
	}
}

func TestCS2PayloadSizeLimits(t *testing.T) {
	t.Helper()
	for _, size := range []uint32{0, (8 << 20) + 1} {
		channel := newCS2DataChannel(0, 1)
		require.Error(t, channel.Push(binary.BigEndian.AppendUint32(nil, size)))
	}
}

func TestCS2LargeTCPFrame(t *testing.T) {
	t.Helper()
	device := newCovCS2Device(t, "tcp")
	conn, err := CS2Dial(device.host(), "tcp", time.Second)
	require.NoError(t, err)
	defer conn.Close()
	require.Eventually(t, func() bool { device.mu.Lock(); defer device.mu.Unlock(); return device.tcpConn != nil }, time.Second, time.Millisecond)
	payload := bytes.Repeat([]byte{1}, 4096)
	data := binary.LittleEndian.AppendUint32(nil, missCmdDevInfoRes)
	require.NoError(t, device.sendDrw(0, append(data, payload...)))
	cmd, got, err := conn.ReadCommand()
	require.NoError(t, err)
	require.Equal(t, uint32(missCmdDevInfoRes), cmd)
	require.Equal(t, payload, got)
}
