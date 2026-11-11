package mqtt

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/require"
)

// s2Broker is a minimal MQTT 3.1.1 broker: it answers CONNECT, SUBSCRIBE and
// PINGREQ so the paho client can reach its connected state without a network.
type s2Broker struct {
	ln       net.Listener
	mu       sync.Mutex
	conns    []net.Conn
	packets  chan byte
	stopOnce sync.Once
}

func s2StartBroker(t *testing.T) *s2Broker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	b := &s2Broker{ln: ln, packets: make(chan byte, 32)}
	go b.accept()
	t.Cleanup(b.stop)
	return b
}

func (b *s2Broker) addr() string { return b.ln.Addr().String() }

func (b *s2Broker) stop() {
	b.stopOnce.Do(func() {
		_ = b.ln.Close()
		b.mu.Lock()
		for _, c := range b.conns {
			_ = c.Close()
		}
		b.mu.Unlock()
	})
}

func (b *s2Broker) accept() {
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		b.conns = append(b.conns, conn)
		b.mu.Unlock()
		go b.serve(conn)
	}
}

func (b *s2Broker) serve(conn net.Conn) {
	for {
		header, payload, err := s2ReadPacket(conn)
		if err != nil {
			return
		}
		kind := header >> 4
		b.note(kind)
		switch kind {
		case 1: // CONNECT -> CONNACK
			_, _ = conn.Write([]byte{0x20, 0x02, 0x00, 0x00})
		case 8: // SUBSCRIBE -> SUBACK for each filter
			if len(payload) < 2 {
				return
			}
			filters := s2CountFilters(payload[2:])
			resp := []byte{0x90, byte(2 + filters), payload[0], payload[1]}
			for i := 0; i < filters; i++ {
				resp = append(resp, 0x00)
			}
			_, _ = conn.Write(resp)
		case 12: // PINGREQ -> PINGRESP
			_, _ = conn.Write([]byte{0xd0, 0x00})
		case 14: // DISCONNECT
			return
		}
	}
}

func (b *s2Broker) note(kind byte) {
	select {
	case b.packets <- kind:
	default:
	}
}

// s2CountFilters counts topic filters in a SUBSCRIBE payload.
func s2CountFilters(payload []byte) int {
	count := 0
	for i := 0; i+2 <= len(payload); {
		length := int(payload[i])<<8 | int(payload[i+1])
		i += 2 + length + 1
		if i > len(payload) {
			break
		}
		count++
	}
	if count == 0 {
		count = 1
	}
	return count
}

func s2ReadPacket(conn net.Conn) (byte, []byte, error) {
	var header [1]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return 0, nil, err
	}
	remaining := 0
	multiplier := 1
	for {
		var b [1]byte
		if _, err := io.ReadFull(conn, b[:]); err != nil {
			return 0, nil, err
		}
		remaining += int(b[0]&0x7f) * multiplier
		if b[0]&0x80 == 0 {
			break
		}
		multiplier *= 128
		if multiplier > 128*128*128 {
			return 0, nil, errors.New("malformed remaining length")
		}
	}
	payload := make([]byte, remaining)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

// s2Publish encodes a QoS 0 PUBLISH packet for the given topic and payload.
func s2Publish(topic string, payload []byte) []byte {
	body := make([]byte, 0, 2+len(topic)+len(payload))
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(topic)))
	body = append(body, length[:]...)
	body = append(body, topic...)
	body = append(body, payload...)
	remaining := len(body)
	header := []byte{0x30}
	for {
		digit := byte(remaining % 128)
		remaining /= 128
		if remaining > 0 {
			digit |= 0x80
		}
		header = append(header, digit)
		if remaining == 0 {
			break
		}
	}
	return append(header, body...)
}

func (b *s2Broker) waitPacket(t *testing.T, kind byte) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case got := <-b.packets:
			if got == kind {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for MQTT packet type %d", kind)
		}
	}
}

func (b *s2Broker) send(t *testing.T, data []byte) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	require.NotEmpty(t, b.conns)
	for _, conn := range b.conns {
		_, _ = conn.Write(data)
	}
}

func TestS2ClientConnectsAndReceivesTriggers(t *testing.T) {
	broker := s2StartBroker(t)
	actions := make(chan string, 4)
	client := NewClient("tcp://"+broker.addr(), "s2-client", "cam", "user", "secret", func(cameraID, action string) {
		select {
		case actions <- cameraID + ":" + action:
		default:
		}
	})
	require.True(t, client.IsConfigured())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Start(ctx) }()

	// SUBSCRIBE proves the OnConnect handler ran.
	broker.waitPacket(t, 1)
	broker.waitPacket(t, 8)

	broker.send(t, s2Publish("cam/trigger/front-door", []byte(`{"action":" RECORD "}`)))
	select {
	case got := <-actions:
		require.Equal(t, "front-door:record", got)
	case <-time.After(3 * time.Second):
		t.Fatal("trigger callback was not invoked")
	}

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after cancellation")
	}

	// Stop disconnects a connected client and is idempotent.
	require.NoError(t, client.Stop())
	require.NoError(t, client.Stop())
}

func TestS2ClientHandleMessageFilters(t *testing.T) {
	var actions []string
	client := NewClient("tcp://127.0.0.1:1", "id", "cam", "", "", func(cameraID, action string) {
		actions = append(actions, cameraID+":"+action)
	})

	// Topic outside the trigger prefix is ignored.
	client.handleMessage(nil, &mockMessage{topic: "other/topic", payload: []byte(`{"action":"record"}`)})
	require.Empty(t, actions)

	// Malformed JSON is ignored.
	client.handleMessage(nil, &mockMessage{topic: "cam/trigger/cam1", payload: []byte("not-json")})
	require.Empty(t, actions)

	// Unsupported actions are ignored.
	client.handleMessage(nil, &mockMessage{topic: "cam/trigger/cam1", payload: []byte(`{"action":"dance"}`)})
	require.Empty(t, actions)

	// Empty camera IDs are ignored.
	client.handleMessage(nil, &mockMessage{topic: "cam/trigger/", payload: []byte(`{"action":"record"}`)})
	require.Empty(t, actions)

	// Supported actions call back.
	client.handleMessage(nil, &mockMessage{topic: "cam/trigger/cam1", payload: []byte(`{"action":"stop"}`)})
	require.Equal(t, []string{"cam1:stop"}, actions)

	// A nil callback is tolerated.
	noCallback := NewClient("tcp://127.0.0.1:1", "id", "cam", "", "", nil)
	noCallback.handleMessage(nil, &mockMessage{topic: "cam/trigger/cam1", payload: []byte(`{"action":"record"}`)})
}

func TestS2ClientStopWithoutConnection(t *testing.T) {
	client := NewClient("", "id", "cam", "", "", nil)
	require.False(t, client.IsConfigured())
	require.NoError(t, client.Stop())
	require.NoError(t, client.Start(context.Background()))

	// A connected mock client is disconnected by Stop.
	connected := &mockPahoClient{connected: true}
	client.mqttClient = connected
	require.NoError(t, client.Stop())
}

func TestS2ClientStartRetriesUntilCancelled(t *testing.T) {
	// An unroutable broker makes the initial connect fail; Start retries until
	// the context expires.
	client := NewClient("tcp://127.0.0.1:1", "id", "cam", "", "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := client.Start(ctx)
	require.NoError(t, err)
}

var _ mqtt.Message = (*mockMessage)(nil)
var _ = strings.TrimSpace
