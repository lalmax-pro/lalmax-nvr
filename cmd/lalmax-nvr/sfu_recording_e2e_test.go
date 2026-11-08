package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/h264reader"
	"github.com/stretchr/testify/require"
)

// This opt-in test uses synthetic video and silent Opus; it never opens a media device.
func TestNVRSFUBridgeRecordingPlanEndToEnd(t *testing.T) {
	if os.Getenv("SFU_NVR_RECORDING_TEST") != "1" {
		t.Skip("set SFU_NVR_RECORDING_TEST=1 to run the NVR/FFmpeg integration")
	}
	t.Chdir(t.TempDir())
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "source.h264")
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x96:rate=25:duration=1", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-x264-params", "threads=1:slices=1:keyint=25:min-keyint=25:scenecut=0:repeat-headers=1", "-an", "-f", "h264", "-y", videoPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	raw, err := os.ReadFile(videoPath)
	require.NoError(t, err)
	reader, err := h264reader.NewReader(bytes.NewReader(raw))
	require.NoError(t, err)
	var sps, pps []byte
	var frames [][]byte
	for {
		nal, err := reader.NextNAL()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		switch nal.Data[0] & 31 {
		case 7:
			sps = nal.Data
		case 8:
			pps = nal.Data
		case 1, 5:
			frames = append(frames, nal.Data)
		}
	}
	require.NotEmpty(t, sps)
	require.NotEmpty(t, pps)
	require.Len(t, frames, 25)

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
	request := func(method, path, body string, want int) []byte {
		t.Helper()
		req, err := http.NewRequest(method, origin+path, strings.NewReader(body))
		require.NoError(t, err)
		req.SetBasicAuth(cfg.Auth.Username, "local-test-password")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, want, resp.StatusCode, "%s", data)
		return data
	}
	var ticket struct {
		Ticket string `json:"ticket"`
	}
	require.NoError(t, json.Unmarshal(request(http.MethodPost, "/api/sfu/ticket", `{}`, http.StatusCreated), &ticket))
	require.NotEmpty(t, ticket.Ticket)
	headers := http.Header{"Origin": []string{origin}}
	ws, _, err := websocket.DefaultDialer.Dial("ws://"+cfg.Server.Listen+"/api/sfu/ws?ticket="+ticket.Ticket, headers)
	require.NoError(t, err)
	defer ws.Close()
	require.NoError(t, ws.WriteJSON(map[string]any{"id": "video", "type": "publish", "kind": "video", "source": "camera", "codec": "video/H264", "track_id": "video"}))
	videoPub := pacingRoomRead(t, ws, "video")
	require.Empty(t, videoPub.Error)
	require.NotNil(t, videoPub.Publication)
	require.NoError(t, ws.WriteJSON(map[string]any{"id": "audio", "type": "publish", "kind": "audio", "source": "microphone", "codec": "audio/opus", "track_id": "audio"}))
	audioPub := pacingRoomRead(t, ws, "audio")
	require.Empty(t, audioPub.Error)
	require.NotNil(t, audioPub.Publication)
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings), webrtc.WithInterceptorRegistry(&interceptor.Registry{})).NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	video, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, "video", "nvr-record")
	require.NoError(t, err)
	audio, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "nvr-record")
	require.NoError(t, err)
	videoSender, err := pc.AddTrack(video)
	require.NoError(t, err)
	audioSender, err := pc.AddTrack(audio)
	require.NoError(t, err)
	for _, sender := range []*webrtc.RTPSender{videoSender, audioSender} {
		go func(s *webrtc.RTPSender) {
			for {
				if _, _, err := s.ReadRTCP(); err != nil {
					return
				}
			}
		}(sender)
	}
	require.NoError(t, ws.WriteJSON(map[string]any{"id": "offer", "type": "offer", "sdp": pacingRoomOffer(t, pc)}))
	answer := pacingRoomRead(t, ws, "offer")
	require.Empty(t, answer.Error)
	require.NoError(t, pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}))
	require.Eventually(t, func() bool { return pc.ConnectionState() == webrtc.PeerConnectionStateConnected }, 5*time.Second, 10*time.Millisecond)
	vParams, aParams := videoSender.GetParameters(), audioSender.GetParameters()
	require.Len(t, vParams.Encodings, 1)
	require.Len(t, aParams.Encodings, 1)
	stop, stopped := make(chan struct{}), make(chan struct{})
	mediaErrors := make(chan error, 1)
	var allowNextKeyframe atomic.Bool
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		payloader := &codecs.H264Payloader{}
		var vSeq, aSeq uint16
		var i uint32
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if i%2 == 0 {
					frame := frames[(i/2)%uint32(len(frames))]
					if frame[0]&31 == 5 && i != 0 && !allowNextKeyframe.Load() {
						i++
						continue
					}
					if frame[0]&31 == 5 {
						payloader.Payload(1200, sps)
						payloader.Payload(1200, pps)
					}
					payloads := payloader.Payload(1200, frame)
					for j, payload := range payloads {
						vSeq++
						if err := video.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: vSeq, Timestamp: 90000 + i*1800, Marker: j == len(payloads)-1}, Payload: payload}); err != nil {
							mediaErrors <- err
							return
						}
					}
				}
				aSeq++
				if err := audio.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: aSeq, Timestamp: 48000 + i*960, Marker: true}, Payload: []byte{0xf8, 0xff, 0xfe}}); err != nil {
					mediaErrors <- err
					return
				}
				if i%10 == 0 {
					if err := pc.WriteRTCP([]rtcp.Packet{&rtcp.SenderReport{SSRC: uint32(vParams.Encodings[0].SSRC), NTPTime: 100 << 32, RTPTime: 90000}, &rtcp.SenderReport{SSRC: uint32(aParams.Encodings[0].SSRC), NTPTime: 100 << 32, RTPTime: 48000}}); err != nil {
						mediaErrors <- err
						return
					}
				}
				i++
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-stopped })
	require.Eventually(t, func() bool {
		snap := app.sfuRuntime.room.Snapshot()
		ready := map[string]bool{}
		for _, p := range snap.Publications {
			ready[p.ID] = p.RTPReady
		}
		return ready[videoPub.Publication.ID] && ready[audioPub.Publication.ID]
	}, 5*time.Second, 20*time.Millisecond)
	var bridge struct {
		ID       string `json:"id"`
		StreamID string `json:"stream_id"`
	}
	body, _ := json.Marshal(map[string]string{"publication_id": videoPub.Publication.ID, "audio_publication_id": audioPub.Publication.ID})
	require.NoError(t, json.Unmarshal(request(http.MethodPost, "/api/sfu/bridges/", string(body), http.StatusAccepted), &bridge))
	require.NotEmpty(t, bridge.ID)
	require.NotEmpty(t, bridge.StreamID)
	require.Eventually(t, func() bool {
		ids, err := app.aliveRecordingStreams(context.Background())
		if err != nil {
			return false
		}
		for _, id := range ids {
			if id == bridge.StreamID {
				return true
			}
		}
		return false
	}, 8*time.Second, 30*time.Millisecond, "bridge did not publish to actual media engine")
	var plan struct {
		ID string `json:"id"`
	}
	planBody, _ := json.Marshal(map[string]any{"stream_id": bridge.StreamID, "name": "SFU test", "mode": "continuous", "enabled": true})
	require.NoError(t, json.Unmarshal(request(http.MethodPost, "/api/recording-plans", string(planBody), http.StatusCreated), &plan))
	require.NotEmpty(t, plan.ID)
	info, err := app.mediaEngine.GetStream(context.Background(), bridge.StreamID)
	require.NoError(t, err)
	require.NotNil(t, info)
	require.Empty(t, info.VideoCodec, "the plan must precede codec availability")
	// Let the first reconciliation observe the active stream without a codec.
	time.Sleep(250 * time.Millisecond)
	allowNextKeyframe.Store(true)
	require.Eventually(t, func() bool {
		for _, id := range app.recTasks.RunningIDs() {
			if id == bridge.StreamID {
				return true
			}
		}
		return false
	}, 8*time.Second, 30*time.Millisecond, "plan did not start recorder")
	time.Sleep(3 * time.Second)
	var status struct {
		State string `json:"state"`
	}
	require.NoError(t, json.Unmarshal(request(http.MethodGet, "/api/sfu/bridges/"+bridge.ID, "", http.StatusOK), &status))
	require.Equal(t, "pending", status.State)
	request(http.MethodDelete, "/api/recording-plans/"+plan.ID, "", http.StatusOK)
	require.Eventually(t, func() bool {
		for _, id := range app.recTasks.RunningIDs() {
			if id == bridge.StreamID {
				return false
			}
		}
		return true
	}, 8*time.Second, 30*time.Millisecond, "plan removal did not stop recorder")
	select {
	case err := <-mediaErrors:
		t.Fatal(err)
	default:
	}
	var files []string
	require.Eventually(t, func() bool {
		files, err = app.store.ListFiles(bridge.StreamID)
		return err == nil && len(files) > 0
	}, 5*time.Second, 50*time.Millisecond, "recording file did not appear: %v", err)
	cmd = exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", files[0])
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.Contains(t, string(out), "h264")
	require.Contains(t, string(out), "opus")
	for _, stream := range []struct {
		selector string
		minimum  int
	}{{"v:0", 10}, {"a:0", 10}} {
		cmd = exec.Command("ffprobe", "-v", "error", "-count_packets", "-select_streams", stream.selector, "-show_entries", "stream=nb_read_packets", "-of", "default=nw=1:nk=1", files[0])
		out, err = cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		count, err := strconv.Atoi(strings.TrimSpace(string(out)))
		require.NoError(t, err, "%s", out)
		require.GreaterOrEqual(t, count, stream.minimum, "%s", stream.selector)
	}
	cmd = exec.Command("ffmpeg", "-v", "error", "-xerror", "-i", files[0], "-map", "0:v:0", "-map", "0:a:0", "-f", "null", "-")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	if artifact := os.Getenv("SFU_NVR_RECORDING_MP4"); artifact != "" {
		data, err := os.ReadFile(files[0])
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(artifact, data, 0600))
	}
}
