package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

// This opt-in test exercises NVR-authenticated tickets and real local Pion
// PeerConnections with synthetic RTP. It verifies a late third participant can
// subscribe and that removing one viewer does not interrupt the remaining one.
func TestNVRSFUAuthenticatedThreeMemberDynamicSubscription(t *testing.T) {
	if os.Getenv("SFU_NVR_MULTI_MEMBER_TEST") != "1" {
		t.Skip("set SFU_NVR_MULTI_MEMBER_TEST=1 to run the NVR multi-member integration")
	}
	t.Chdir(t.TempDir())
	cfg, cfgPath := testConfig(t)
	isolateEmbeddedMedia(t, cfg)
	cfg.Server.Listen = freeTCPAddr(t)
	cfg.Auth.Username = "admin"
	cfg.Auth.Password = "local-test-password"
	cfg.SFU.Enabled = true
	cfg.JT808.Enabled = false
	cfg.MQTT.Enabled = false
	disabled := false
	cfg.FTP.Enabled = &disabled
	cfg.DLNA.Enabled = &disabled
	cfg.RTMP.Enabled = &disabled
	cfg.SRT.Enabled = &disabled
	cfg.WHIP.Enabled = &disabled

	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- app.Start() }()
	waitForTCP(t, cfg.Server.Listen)
	t.Cleanup(func() {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("app shutdown: %v", err)
			}
		case <-time.After(35 * time.Second):
			t.Error("app did not stop")
		}
		signal.Reset(syscall.SIGTERM)
	})
	require.Eventually(t, func() bool { return app.sfuRuntime.server.Ready() }, 10*time.Second, 20*time.Millisecond)

	origin := "http://" + cfg.Server.Listen
	issueTicket := func() string {
		req, reqErr := http.NewRequest(http.MethodPost, origin+"/api/sfu/ticket", strings.NewReader(`{}`))
		require.NoError(t, reqErr)
		req.SetBasicAuth(cfg.Auth.Username, "local-test-password")
		req.Header.Set("Content-Type", "application/json")
		resp, reqErr := http.DefaultClient.Do(req)
		require.NoError(t, reqErr)
		defer resp.Body.Close()
		var result struct {
			Ticket string `json:"ticket"`
		}
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		require.NotEmpty(t, result.Ticket)
		return result.Ticket
	}
	dial := func(ticket string) *websocket.Conn {
		headers := http.Header{"Origin": []string{origin}}
		ws, _, dialErr := websocket.DefaultDialer.Dial("ws://"+cfg.Server.Listen+"/api/sfu/ws?ticket="+ticket, headers)
		require.NoError(t, dialErr)
		t.Cleanup(func() { _ = ws.Close() })
		return ws
	}

	publisherWS := dial(issueTicket())
	require.NoError(t, publisherWS.WriteJSON(map[string]any{"id": "publish-video", "type": "publish", "kind": "video", "source": "camera", "codec": "video/H264", "track_id": "camera"}))
	published := pacingRoomRead(t, publisherWS, "publish-video")
	require.Empty(t, published.Error)
	require.NotNil(t, published.Publication)
	require.NotEmpty(t, published.Publication.ID)

	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	publisher, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = publisher.Close() })
	video, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, "camera", "camera-track")
	require.NoError(t, err)
	sender, err := publisher.AddTrack(video)
	require.NoError(t, err)
	go func() {
		for {
			if _, _, readErr := sender.ReadRTCP(); readErr != nil {
				return
			}
		}
	}()
	require.NoError(t, publisherWS.WriteJSON(map[string]any{"id": "publisher-offer", "type": "offer", "sdp": pacingRoomOffer(t, publisher)}))
	answer := pacingRoomRead(t, publisherWS, "publisher-offer")
	require.Empty(t, answer.Error)
	require.NoError(t, publisher.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}))
	require.Eventually(t, func() bool { return publisher.ConnectionState() == webrtc.PeerConnectionStateConnected }, 8*time.Second, 10*time.Millisecond)

	stopSource := make(chan struct{})
	defer close(stopSource)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		var sequence uint16
		for {
			select {
			case <-stopSource:
				return
			case <-ticker.C:
				sequence++
				_ = video.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: uint32(sequence) * 1800, Marker: true}, Payload: []byte{0x41, 0x01}})
			}
		}
	}()
	room, ok := app.sfuRuntime.rooms.Get(nvrSFURoom)
	require.True(t, ok)
	require.Eventually(t, func() bool {
		for _, publication := range room.Snapshot().Publications {
			if publication.ID == published.Publication.ID {
				return publication.RTPReady
			}
		}
		return false
	}, 8*time.Second, 10*time.Millisecond, "publisher RTP did not become ready")

	type viewer struct {
		ws          *websocket.Conn
		pc          *webrtc.PeerConnection
		transceiver *webrtc.RTPTransceiver
		tracks      atomic.Int64
		packets     atomic.Int64
	}
	renegotiate := func(v *viewer, offerID string) {
		t.Helper()
		require.NoError(t, v.ws.WriteJSON(map[string]any{"id": offerID, "type": "offer", "sdp": pacingRoomOffer(t, v.pc)}))
		viewerAnswer := pacingRoomRead(t, v.ws, offerID)
		require.Empty(t, viewerAnswer.Error)
		require.NoError(t, v.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: viewerAnswer.SDP}))
		require.Eventually(t, func() bool { return v.pc.ConnectionState() == webrtc.PeerConnectionStateConnected }, 8*time.Second, 10*time.Millisecond)
	}
	newViewer := func(name string) *viewer {
		v := &viewer{ws: dial(issueTicket())}
		pcSettings := webrtc.SettingEngine{}
		pcSettings.SetIncludeLoopbackCandidate(true)
		v.pc, err = webrtc.NewAPI(webrtc.WithSettingEngine(pcSettings)).NewPeerConnection(webrtc.Configuration{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = v.pc.Close() })
		v.pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			v.tracks.Add(1)
			go func() {
				for {
					if _, _, readErr := track.ReadRTP(); readErr != nil {
						return
					}
					v.packets.Add(1)
				}
			}()
		})
		require.NoError(t, v.ws.WriteJSON(map[string]any{"id": "subscribe-" + name, "type": "subscribe", "publication_id": published.Publication.ID}))
		subscription := pacingRoomRead(t, v.ws, "subscribe-"+name)
		require.Empty(t, subscription.Error)
		transceiver, addErr := v.pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
		require.NoError(t, addErr)
		v.transceiver = transceiver
		renegotiate(v, "viewer-offer-"+name)
		require.Eventually(t, func() bool { return v.packets.Load() > 0 }, 8*time.Second, 10*time.Millisecond, "viewer %s received no RTP", name)
		return v
	}

	first := newViewer("first")
	second := newViewer("second") // joins after media is already flowing
	require.Eventually(t, func() bool { return len(room.Snapshot().Subscriptions) == 2 }, 3*time.Second, 10*time.Millisecond)
	tracksBeforeToggle := second.tracks.Load()
	require.NoError(t, second.ws.WriteJSON(map[string]any{"id": "unsubscribe-second", "type": "unsubscribe", "publication_id": published.Publication.ID}))
	unsubscribed := pacingRoomRead(t, second.ws, "unsubscribe-second")
	require.Empty(t, unsubscribed.Error)
	second.transceiver.Stop()
	renegotiate(second, "unsubscribe-offer-second")
	require.Eventually(t, func() bool { return len(room.Snapshot().Subscriptions) == 1 }, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, second.ws.WriteJSON(map[string]any{"id": "resubscribe-second", "type": "subscribe", "publication_id": published.Publication.ID}))
	resubscribed := pacingRoomRead(t, second.ws, "resubscribe-second")
	require.Empty(t, resubscribed.Error)
	second.transceiver, err = second.pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	require.NoError(t, err)
	renegotiate(second, "resubscribe-offer-second")
	require.Eventually(t, func() bool { return len(room.Snapshot().Subscriptions) == 2 }, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return second.tracks.Load() > tracksBeforeToggle }, 5*time.Second, 10*time.Millisecond, "viewer did not receive a new track after resubscription")
	packetsAfterResubscribe := second.packets.Load()
	require.Eventually(t, func() bool { return second.packets.Load() > packetsAfterResubscribe }, 3*time.Second, 10*time.Millisecond, "viewer received no RTP after resubscription")
	beforeLeave := first.packets.Load()
	require.Greater(t, beforeLeave, int64(0))
	require.NoError(t, second.ws.Close())
	require.Eventually(t, func() bool {
		snapshot := room.Snapshot()
		return len(snapshot.Participants) == 2 && len(snapshot.Subscriptions) == 1
	}, 5*time.Second, 10*time.Millisecond, "departed viewer and its subscription were not removed from the room")
	require.Eventually(t, func() bool { return first.packets.Load() > beforeLeave }, 3*time.Second, 10*time.Millisecond, "remaining viewer stopped receiving RTP after peer left")
}
