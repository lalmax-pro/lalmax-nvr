package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
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
	videoFilter := "testsrc2=size=160x96:rate=25:duration=1"
	markers := os.Getenv("SFU_NVR_RECORDING_MARKERS_TEST") == "1"
	if markers {
		videoFilter = `color=c=black:s=160x96:r=25:d=1,drawbox=x=0:y=0:w=iw:h=ih:color=white:t=fill:enable='between(t,0.48,0.52)'`
	}
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", videoFilter, "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-x264-params", "threads=1:slices=1:keyint=25:min-keyint=25:scenecut=0:repeat-headers=1", "-an", "-f", "h264", "-y", videoPath)
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
	var audioPayloads [][]byte
	if markers {
		audioPath := filepath.Join(dir, "markers.opus")
		filter := `aevalsrc=if(between(t\,0.48\,0.50)\,0.5*sin(2*PI*1000*t)\,0):s=48000:d=1`
		cmd = exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", filter, "-ac", "2", "-c:a", "libopus", "-frame_duration", "20", "-b:a", "64k", "-y", audioPath)
		out, err = cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		cmd = exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0", "-show_packets", "-show_data", "-show_entries", "packet=data", "-of", "json", audioPath)
		out, err = cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		var packets struct {
			Packets []struct {
				Data string `json:"data"`
			} `json:"packets"`
		}
		require.NoError(t, json.Unmarshal(out, &packets))
		require.GreaterOrEqual(t, len(packets.Packets), 50)
		for _, packet := range packets.Packets[:50] {
			var payload []byte
			for _, line := range strings.Split(strings.TrimSpace(packet.Data), "\n") {
				parts := strings.SplitN(line, ":", 2)
				require.Len(t, parts, 2)
				chunk, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(strings.SplitN(parts[1], "  ", 2)[0]), " ", ""))
				require.NoError(t, err)
				payload = append(payload, chunk...)
			}
			audioPayloads = append(audioPayloads, payload)
		}
	}

	cfg, cfgPath := testConfig(t)
	isolateEmbeddedMedia(t, cfg)
	cfg.Server.Listen = freeTCPAddr(t)
	cfg.Auth.Username = "admin"
	cfg.Auth.Password = "local-test-password"
	cfg.SFU.Enabled = true
	if os.Getenv("SFU_NVR_RECORDING_LONG_TEST") == "1" {
		cfg.Storage.SegmentDuration = "2m"
	}
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
	request := func(method, path, body string, want int, keys ...string) []byte {
		t.Helper()
		req, err := http.NewRequest(method, origin+path, strings.NewReader(body))
		require.NoError(t, err)
		req.SetBasicAuth(cfg.Auth.Username, "local-test-password")
		req.Header.Set("Content-Type", "application/json")
		if len(keys) != 0 {
			req.Header.Set("Idempotency-Key", keys[0])
		}
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
				audioPayload := []byte{0xf8, 0xff, 0xfe}
				if markers {
					audioPayload = audioPayloads[i%uint32(len(audioPayloads))]
				}
				if err := audio.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: aSeq, Timestamp: 48000 + i*960, Marker: true}, Payload: audioPayload}); err != nil {
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
	key := "nvr-recording-test-request"
	require.NoError(t, json.Unmarshal(request(http.MethodPost, "/api/sfu/bridges/", string(body), http.StatusAccepted, key), &bridge))
	require.NotEmpty(t, bridge.ID)
	require.NotEmpty(t, bridge.StreamID)
	var resolved struct {
		ID            string `json:"id"`
		PublicationID string `json:"publication_id"`
	}
	require.NoError(t, json.Unmarshal(request(http.MethodGet, "/api/sfu/bridge-requests", "", http.StatusOK, key), &resolved))
	require.Equal(t, bridge.ID, resolved.ID)
	require.Equal(t, videoPub.Publication.ID, resolved.PublicationID)
	request(http.MethodGet, "/api/sfu/bridge-requests", "", http.StatusNotFound, "unknown-request")
	// Model a lost create response: recover and stop this request's bridge,
	// while another bridge for the same publication remains untouched.
	lostKey := "nvr-recording-test-lost-response"
	request(http.MethodPost, "/api/sfu/bridges/", string(body), http.StatusAccepted, lostKey)
	var recovered struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(request(http.MethodGet, "/api/sfu/bridge-requests", "", http.StatusOK, lostKey), &recovered))
	require.NotEmpty(t, recovered.ID)
	require.NotEqual(t, bridge.ID, recovered.ID)
	request(http.MethodPost, "/api/sfu/bridges/"+recovered.ID+"/stop", "{}", http.StatusNoContent)
	require.Eventually(t, func() bool {
		var status struct {
			State string `json:"state"`
		}
		err := json.Unmarshal(request(http.MethodGet, "/api/sfu/bridges/"+recovered.ID, "", http.StatusOK), &status)
		return err == nil && (status.State == "stopped" || status.State == "failed")
	}, 5*time.Second, 50*time.Millisecond)
	request(http.MethodDelete, "/api/sfu/bridges/"+recovered.ID, "", http.StatusNoContent)
	var original struct {
		State string `json:"state"`
	}
	require.NoError(t, json.Unmarshal(request(http.MethodGet, "/api/sfu/bridges/"+bridge.ID, "", http.StatusOK), &original))
	require.Equal(t, "pending", original.State)
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
	recordFor := 3 * time.Second
	if os.Getenv("SFU_NVR_RECORDING_LONG_TEST") == "1" {
		recordFor = 60 * time.Second
	}
	time.Sleep(recordFor)
	var status struct {
		State string `json:"state"`
	}
	require.NoError(t, json.Unmarshal(request(http.MethodGet, "/api/sfu/bridges/"+bridge.ID, "", http.StatusOK), &status))
	require.Equal(t, "pending", status.State)
	sourceEnd := os.Getenv("SFU_NVR_SOURCE_END_TEST") == "1"
	if sourceEnd {
		require.NoError(t, ws.WriteJSON(map[string]any{"id": "unpublish-video", "type": "unpublish", "publication_id": videoPub.Publication.ID}))
		reply := pacingRoomRead(t, ws, "unpublish-video")
		require.Empty(t, reply.Error)
		require.Eventually(t, func() bool {
			err := json.Unmarshal(request(http.MethodGet, "/api/sfu/bridges/"+bridge.ID, "", http.StatusOK), &status)
			return err == nil && (status.State == "stopped" || status.State == "failed")
		}, 8*time.Second, 50*time.Millisecond, "source removal did not terminate bridge")
		request(http.MethodGet, "/api/sfu/bridges/"+bridge.ID+"/playback", "", http.StatusNotFound)
	} else {
		request(http.MethodDelete, "/api/recording-plans/"+plan.ID, "", http.StatusOK)
	}
	require.Eventually(t, func() bool {
		for _, id := range app.recTasks.RunningIDs() {
			if id == bridge.StreamID {
				return false
			}
		}
		return true
	}, 8*time.Second, 30*time.Millisecond, "source or plan removal did not stop recorder")
	if sourceEnd {
		request(http.MethodDelete, "/api/recording-plans/"+plan.ID, "", http.StatusOK)
	}
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
	var durations []float64
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
		if recordFor >= time.Minute {
			minimum := 1350
			if stream.selector == "a:0" {
				minimum = 2700
			}
			require.GreaterOrEqual(t, count, minimum, "%s", stream.selector)
			cmd = exec.Command("ffprobe", "-v", "error", "-select_streams", stream.selector, "-show_entries", "stream=duration", "-of", "default=nw=1:nk=1", files[0])
			out, err = cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			duration, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
			require.NoError(t, err, "%s", out)
			require.GreaterOrEqual(t, duration, 55.0, "%s", stream.selector)
			t.Logf("%s: packets=%d, duration=%.3fs", stream.selector, count, duration)
			durations = append(durations, duration)
		}
	}
	if len(durations) == 2 {
		require.LessOrEqual(t, math.Abs(durations[0]-durations[1]), 0.3)
	}
	cmd = exec.Command("ffmpeg", "-v", "error", "-xerror", "-i", files[0], "-map", "0:v:0", "-map", "0:a:0", "-f", "null", "-")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	if markers {
		cmd = exec.Command("ffmpeg", "-v", "error", "-xerror", "-i", files[0], "-map", "0:v:0", "-fps_mode", "passthrough", "-pix_fmt", "gray", "-f", "rawvideo", "-")
		pixels, err := cmd.Output()
		require.NoError(t, err)
		const frameSize = 160 * 96
		require.Zero(t, len(pixels)%frameSize)
		cmd = exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_frames", "-show_entries", "frame=best_effort_timestamp_time", "-of", "json", files[0])
		out, err = cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		var videoTimes struct {
			Frames []struct {
				Timestamp string `json:"best_effort_timestamp_time"`
			} `json:"frames"`
		}
		require.NoError(t, json.Unmarshal(out, &videoTimes))
		require.Len(t, videoTimes.Frames, len(pixels)/frameSize)
		var flashes []float64
		for i := 0; i < len(pixels)/frameSize; i++ {
			var sum int
			for _, value := range pixels[i*frameSize : (i+1)*frameSize] {
				sum += int(value)
			}
			at, err := strconv.ParseFloat(videoTimes.Frames[i].Timestamp, 64)
			require.NoError(t, err)
			if sum/frameSize > 200 && (len(flashes) == 0 || at-flashes[len(flashes)-1] > 0.2) {
				flashes = append(flashes, at)
			}
		}
		cmd = exec.Command("ffmpeg", "-v", "error", "-xerror", "-i", files[0], "-map", "0:a:0", "-af", "aresample=async=1:first_pts=0", "-ac", "1", "-f", "f32le", "-")
		pcm, err := cmd.Output()
		require.NoError(t, err)
		const sampleRate, window = 48000, 480
		var signals []float64
		for start := 0; start+window*4 <= len(pcm); start += window * 4 {
			var power float64
			for j := start; j < start+window*4; j += 4 {
				x := float64(math.Float32frombits(binary.LittleEndian.Uint32(pcm[j:])))
				power += x * x
			}
			at := float64(start/4) / sampleRate
			if math.Sqrt(power/window) > 0.03 && (len(signals) == 0 || at-signals[len(signals)-1] > 0.2) {
				signals = append(signals, at)
			}
		}
		t.Logf("decoded markers: video=%v audio=%v", flashes, signals)
		require.GreaterOrEqual(t, len(flashes), 2)
		require.Equal(t, len(flashes), len(signals))
		for i := range flashes {
			require.InDelta(t, flashes[i], signals[i], 0.04, "decoded marker alignment")
		}
	}
	if artifact := os.Getenv("SFU_NVR_RECORDING_MP4"); artifact != "" {
		data, err := os.ReadFile(files[0])
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(artifact, data, 0600))
	}
}
