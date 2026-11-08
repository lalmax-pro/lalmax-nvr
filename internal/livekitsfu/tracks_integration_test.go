//go:build livekit_integration

package livekitsfu

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
)

type groupTestHook struct{ messages chan base.RtmpMsg }

func (h *groupTestHook) OnMsg(m base.RtmpMsg) {
	select {
	case h.messages <- m.Clone():
	default:
	}
}
func (*groupTestHook) OnStop() {}

// TestLiveKitTracksToLalAV exercises a real LiveKit subscriber, both RTP
// depacketizers, RTCP sender-report clocks and a real lal Group. Its publisher
// and viewer use generated RTP only; no capture device or speaker is used.
func TestLiveKitTracksToLalAV(t *testing.T) {
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
	deadline := time.Now().Add(10 * time.Second)
	for !server.IsRunning() {
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	publisher, err := lksdk.ConnectToRoom(endpoint, lksdk.ConnectInfo{APIKey: key, APISecret: secret, RoomName: "bridge", ParticipantIdentity: "publisher"}, lksdk.NewRoomCallback(), lksdk.WithAutoSubscribe(false))
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Disconnect()
	video, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"})
	if err != nil {
		t.Fatal(err)
	}
	audio, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2})
	if err != nil {
		t.Fatal(err)
	}
	videoPub, err := publisher.LocalParticipant.PublishTrack(video, &lksdk.TrackPublicationOptions{Name: "video", Source: livekit.TrackSource_CAMERA})
	if err != nil {
		t.Fatal(err)
	}
	audioPub, err := publisher.LocalParticipant.PublishTrack(audio, &lksdk.TrackPublicationOptions{Name: "audio", Source: livekit.TrackSource_MICROPHONE, DisableDTX: true})
	if err != nil {
		t.Fatal(err)
	}
	var watched atomic.Int64
	cb := lksdk.NewRoomCallback()
	cb.OnTrackSubscribed = func(track *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
		go func() {
			for {
				if _, _, err := track.ReadRTP(); err != nil {
					return
				}
				watched.Add(1)
			}
		}()
	}
	viewer, err := lksdk.ConnectToRoom(endpoint, lksdk.ConnectInfo{APIKey: key, APISecret: secret, RoomName: "bridge", ParticipantIdentity: "viewer"}, cb)
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Disconnect()
	payload := []byte{24}
	for _, nalu := range [][]byte{{0x67, 0x42, 0xc0, 0x1f, 0x0f, 0xd9, 0x1f, 0x88, 0x88, 0x84, 0, 0, 3, 0, 4, 0, 0, 3, 0, 0xc8, 0x3c, 0x60, 0xc9, 0x20}, {0x68, 0x87, 0xcb, 0x83, 0xcb, 0x20}, {0x65, 0x88, 0x84, 0x0a, 0xf2, 0x62, 0x80, 0, 0xa7, 0xbe}} {
		var n [2]byte
		binary.BigEndian.PutUint16(n[:], uint16(len(nalu)))
		payload = append(payload, n[:]...)
		payload = append(payload, nalu...)
	}
	producerCtx, stopProducer := context.WithCancel(ctx)
	defer stopProducer()
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		var n uint16
		for {
			select {
			case <-producerCtx.Done():
				return
			case <-ticker.C:
				n++
				_ = audio.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: n, Timestamp: uint32(n) * 960}, Payload: []byte{0xf8, 0xff, 0xfe}}, nil)
				_ = video.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: n, Timestamp: uint32(n) * 1800, Marker: true}, Payload: payload}, nil)
			}
		}
	}()
	issuer, err := NewTokenIssuer(key, secret)
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.IssueJoin(JoinGrant{Room: "bridge", Identity: "bridge-subscriber", ValidFor: time.Minute, CanSubscribe: true, Hidden: true})
	if err != nil {
		t.Fatal(err)
	}
	request := TrackRequest{RoomSID: publisher.SID(), VideoSID: videoPub.SID(), AudioSID: audioPub.SID()}
	staleToken, err := issuer.IssueJoin(JoinGrant{Room: "bridge", Identity: "stale-subscriber", ValidFor: time.Minute, CanSubscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	stale := request
	stale.RoomSID = "RM_stale"
	if _, err := OpenTracks(ctx, ctx, endpoint, staleToken, stale); !errors.Is(err, ErrRoomChanged) {
		t.Fatalf("stale room accepted: %v", err)
	}
	badPairToken, err := issuer.IssueJoin(JoinGrant{Room: "bridge", Identity: "invalid-pair", ValidFor: time.Minute, CanSubscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	badPair := TrackRequest{RoomSID: publisher.SID(), VideoSID: audioPub.SID(), AudioSID: videoPub.SID()}
	if _, err := OpenTracks(ctx, ctx, endpoint, badPairToken, badPair); !errors.Is(err, ErrTrackPair) {
		t.Fatalf("invalid track pair accepted: %v", err)
	}
	setupCtx, setupCancel := context.WithTimeout(ctx, 15*time.Second)
	defer setupCancel()
	tracks, err := OpenTracks(setupCtx, ctx, endpoint, token, request)
	if err != nil {
		t.Fatal(err)
	}
	defer tracks.Close()
	for _, participant := range viewer.GetRemoteParticipants() {
		if participant.Identity() == "bridge-subscriber" {
			t.Fatal("bridge subscriber was visible to the viewer")
		}
	}
	setupCancel() // the bridge lifetime must not depend on the setup request
	manager := logic.NewServerManager(func(o *logic.Option) {
		o.ConfRawContent = []byte(`{"hls":{"enable":true,"on_demand":true,"url_pattern":"/hls/"}}`)
	})
	hook := &groupTestHook{messages: make(chan base.RtmpMsg, 1024)}
	manager.WithOnHookSession(func(_, _ string) logic.ICustomizeHookSessionContext { return hook })
	bridgeCtx, stopBridge := context.WithCancel(ctx)
	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- tracks.RunToGroup(bridgeCtx, manager, "livekit-av") }()
	var gotAudio, gotVideo bool
	wait := time.NewTimer(30 * time.Second)
	defer wait.Stop()
	for !gotAudio || !gotVideo {
		select {
		case msg := <-hook.messages:
			if msg.Header.MsgTypeId == base.RtmpTypeIdAudio {
				gotAudio = true
			}
			if msg.Header.MsgTypeId == base.RtmpTypeIdVideo {
				gotVideo = true
			}
		case err := <-bridgeDone:
			t.Fatalf("bridge ended: %v", err)
		case <-wait.C:
			t.Fatalf("group missing media: video=%v audio=%v", gotVideo, gotAudio)
		}
	}
	before := watched.Load()
	stopBridge()
	select {
	case err := <-bridgeDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("bridge shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bridge failed to stop")
	}
	deadline = time.Now().Add(5 * time.Second)
	for watched.Load() <= before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if watched.Load() <= before {
		t.Fatal("viewer stopped receiving after bridge closed")
	}
	if group := manager.GetGroup("live", "livekit-av"); group != nil && group.HasInSession() {
		t.Fatal("group publisher not released")
	}
	// Unpublishing the exact selected track must end a new audio bridge, while
	// the separate viewer remains connected to the publisher's video.
	endToken, err := issuer.IssueJoin(JoinGrant{Room: "bridge", Identity: "end-subscriber", ValidFor: time.Minute, CanSubscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	endTracks, err := OpenTracks(ctx, ctx, endpoint, endToken, TrackRequest{RoomSID: publisher.SID(), AudioSID: audioPub.SID()})
	if err != nil {
		t.Fatal(err)
	}
	defer endTracks.Close()
	endManager := logic.NewServerManager(func(o *logic.Option) {
		o.ConfRawContent = []byte(`{"hls":{"enable":true,"on_demand":true,"url_pattern":"/hls/"}}`)
	})
	endHook := &groupTestHook{messages: make(chan base.RtmpMsg, 128)}
	endManager.WithOnHookSession(func(_, _ string) logic.ICustomizeHookSessionContext { return endHook })
	endDone := make(chan error, 1)
	go func() { endDone <- endTracks.RunToGroup(ctx, endManager, "ended-audio") }()
	select {
	case <-endHook.messages:
	case <-time.After(5 * time.Second):
		t.Fatal("audio bridge did not produce media")
	}
	if err := publisher.LocalParticipant.UnpublishTrack(audioPub.SID()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-endDone:
		if err == nil || errors.Is(err, context.Canceled) {
			t.Fatalf("source end not reported: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("audio bridge did not stop on unpublish")
	}
	if group := endManager.GetGroup("live", "ended-audio"); group != nil && group.HasInSession() {
		t.Fatal("ended bridge retained the group")
	}
}
