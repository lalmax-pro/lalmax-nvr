//go:build livekit_integration

package livekitsfu

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// TestSimulcastQualitySwitch uses separate synthetic RTP payloads for two
// layers, so receiving a different layer proves the SFU actually switched.
func TestSimulcastQualitySwitch(t *testing.T) {
	t.Chdir(t.TempDir())
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := tcp.Addr().(*net.TCPAddr).Port
	tcp.Close()
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udpPort := udp.LocalAddr().(*net.UDPAddr).Port
	udp.Close()
	const key, secret = "testkey", "testsecret-testsecret-testsecret-123456"
	yaml := fmt.Sprintf("development: false\nport: %d\nbind_addresses: [127.0.0.1]\nkeys:\n  %s: %s\nrtc:\n  node_ip: 127.0.0.1\n  enable_loopback_candidate: true\n  ips:\n    includes: [127.0.0.1/32]\n  tcp_port: 0\n  port_range_start: 0\n  port_range_end: 0\n  udp_port: %d\n", port, key, secret, udpPort)
	server, err := NewServer(yaml)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunPrepared(ctx, server) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("server did not stop")
		}
	})
	for deadline := time.Now().Add(10 * time.Second); !server.IsRunning(); {
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	info := lksdk.ConnectInfo{APIKey: key, APISecret: secret, RoomName: "simulcast", ParticipantIdentity: "publisher"}
	publisher, err := lksdk.ConnectToRoom(url, info, lksdk.NewRoomCallback(), lksdk.WithAutoSubscribe(false))
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Disconnect()
	codec := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}
	qualities := []struct {
		quality livekit.VideoQuality
		width   uint32
		height  uint32
		marker  byte
	}{
		{livekit.VideoQuality_LOW, 320, 180, 0x21},
		{livekit.VideoQuality_HIGH, 1280, 720, 0x42},
	}
	var tracks []*lksdk.LocalTrack
	for _, quality := range qualities {
		track, err := lksdk.NewLocalTrack(codec, lksdk.WithSimulcast("simulcast-test", &livekit.VideoLayer{Quality: quality.quality, Width: quality.width, Height: quality.height}))
		if err != nil {
			t.Fatal(err)
		}
		tracks = append(tracks, track)
	}
	if _, err := publisher.LocalParticipant.PublishSimulcastTrack(tracks, &lksdk.TrackPublicationOptions{Name: "camera", Source: livekit.TrackSource_CAMERA}); err != nil {
		t.Fatal(err)
	}
	payloads := make([][]byte, len(qualities))
	for i, quality := range qualities {
		// LiveKit detects H.264 keyframes from an SPS NAL, so send a STAP-A
		// containing SPS, PPS and an IDR for every synthetic frame.
		payloads[i] = []byte{24}
		for _, nalu := range [][]byte{
			{0x67, 0x42, 0xc0, 0x1f, 0x0f, 0xd9, 0x1f, 0x88, 0x88, 0x84, 0, 0, 3, 0, 4, 0, 0, 3, 0, 0xc8, 0x3c, 0x60, 0xc9, 0x20},
			{0x68, 0x87, 0xcb, 0x83, 0xcb, 0x20},
			{0x65, 0x88, 0x84, 0x0a, 0xf2, 0x62, 0x80, 0, 0xa7, quality.marker},
		} {
			var length [2]byte
			binary.BigEndian.PutUint16(length[:], uint16(len(nalu)))
			payloads[i] = append(payloads[i], length[:]...)
			payloads[i] = append(payloads[i], nalu...)
		}
	}
	producerCtx, stopProducer := context.WithCancel(ctx)
	defer stopProducer()
	go func() {
		ticker := time.NewTicker(33 * time.Millisecond)
		defer ticker.Stop()
		var sequence uint16
		for {
			select {
			case <-producerCtx.Done():
				return
			case <-ticker.C:
				sequence++
				for i, track := range tracks {
					_ = track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: uint32(sequence) * 3000, Marker: true}, Payload: payloads[i]}, nil)
				}
			}
		}
	}()
	publication := make(chan *lksdk.RemoteTrackPublication, 1)
	packets := make(chan byte, 512)
	cb := lksdk.NewRoomCallback()
	cb.OnTrackSubscribed = func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
		select {
		case publication <- pub:
		default:
		}
		go func() {
			for {
				packet, _, err := track.ReadRTP()
				if err != nil {
					return
				}
				if len(packet.Payload) != 0 {
					select {
					case packets <- packet.Payload[len(packet.Payload)-1]:
					default:
					}
				}
			}
		}()
	}
	info.ParticipantIdentity = "viewer"
	viewer, err := lksdk.ConnectToRoom(url, info, cb)
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Disconnect()
	var pub *lksdk.RemoteTrackPublication
	select {
	case pub = <-publication:
	case <-time.After(15 * time.Second):
		t.Fatal("viewer did not subscribe")
	}
	for _, requested := range []struct {
		quality livekit.VideoQuality
		marker  byte
	}{{livekit.VideoQuality_HIGH, 0x42}, {livekit.VideoQuality_LOW, 0x21}, {livekit.VideoQuality_HIGH, 0x42}} {
		// Discard packets queued before changing the requested quality.
	drain:
		for {
			select {
			case <-packets:
			default:
				break drain
			}
		}
		if err := pub.SetVideoQuality(requested.quality); err != nil {
			t.Fatal(err)
		}
		deadline := time.NewTimer(15 * time.Second)
		matched := 0
		seen := make(map[byte]int)
		for matched < 3 {
			select {
			case marker := <-packets:
				seen[marker]++
				if marker == requested.marker {
					matched++
				} else {
					matched = 0
				}
			case <-deadline.C:
				t.Fatalf("quality %s did not produce its RTP layer: %v", requested.quality, seen)
			}
		}
		deadline.Stop()
	}
}
