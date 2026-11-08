//go:build livekit_integration

package livekitsfu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// TestEmbeddedTrack exercises the actual embedded server and Go SDK with a
// synthetic Opus packet. It does not access audio or video devices.
func TestEmbeddedTrack(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := tcp.Addr().(*net.TCPAddr).Port
	if err := tcp.Close(); err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udpPort := udp.LocalAddr().(*net.UDPAddr).Port
	if err := udp.Close(); err != nil {
		t.Fatal(err)
	}
	const key, secret = "testkey", "testsecret-testsecret-testsecret-123456"
	yaml := fmt.Sprintf("development: false\nport: %d\nbind_addresses: [127.0.0.1]\nkeys:\n  %s: %s\nrtc:\n  node_ip: 127.0.0.1\n  enable_loopback_candidate: true\n  ips:\n    includes: [127.0.0.1/32]\n  tcp_port: 0\n  port_range_start: 0\n  port_range_end: 0\n  udp_port: %d\n", port, key, secret, udpPort)
	server, err := NewServer(yaml)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, server); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("server stopped: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("server did not stop")
		}
	})
	for deadline := time.Now().Add(10 * time.Second); !server.IsRunning(); {
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		select {
		case err := <-done:
			t.Fatalf("server exited: %v", err)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	received := make(chan []byte, 1)
	cb := lksdk.NewRoomCallback()
	cb.OnTrackSubscribed = func(track *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
		packet, _, err := track.ReadRTP()
		if err == nil {
			select {
			case received <- packet.Payload:
			default:
			}
		}
	}
	subscriber, err := lksdk.ConnectToRoom(endpoint, lksdk.ConnectInfo{APIKey: key, APISecret: secret, RoomName: "probe", ParticipantIdentity: "subscriber"}, cb)
	if err != nil {
		t.Fatal(err)
	}
	defer subscriber.Disconnect()
	publisher, err := lksdk.ConnectToRoom(endpoint, lksdk.ConnectInfo{APIKey: key, APISecret: secret, RoomName: "probe", ParticipantIdentity: "publisher"}, lksdk.NewRoomCallback(), lksdk.WithAutoSubscribe(false))
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Disconnect()
	track, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	payload := []byte{0xf8, 0xff, 0xfe}
	var sequence uint16
	for {
		select {
		case <-ticker.C:
			sequence++
			packet := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: uint32(sequence) * 960}, Payload: payload}
			if err := track.WriteRTP(packet, nil); err != nil {
				t.Fatal(err)
			}
		case got := <-received:
			if !bytes.Equal(got, payload) {
				t.Fatalf("unexpected RTP payload %x", got)
			}
			return
		case <-deadline.C:
			t.Fatal("track did not arrive")
		}
	}
}
