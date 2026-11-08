package recorder

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4/pkg/media/h264reader"
	maxlogic "github.com/q191201771/lalmax/logic"
	"github.com/q191201771/lalmax/sfu/bridge"
	"github.com/q191201771/lalmax/sfu/rtpforward"
	"github.com/stretchr/testify/require"
)

// This opt-in test uses synthesized images and audio, and does not open any media device.
func TestSFUBridgeRecordingKeepsEncodedAVMarkersAligned(t *testing.T) {
	if os.Getenv("SFU_AV_SYNC_TEST") != "1" {
		t.Skip("set SFU_AV_SYNC_TEST=1 to use FFmpeg and libx264/libopus")
	}
	for _, tc := range []struct {
		name        string
		audioOffset time.Duration
		pace        time.Duration
	}{
		{"aligned", 0, 3 * time.Millisecond},
		{"audio_200ms_later_fast", 200 * time.Millisecond, 3 * time.Millisecond},
		{"audio_200ms_earlier_fast", -200 * time.Millisecond, 3 * time.Millisecond},
		{"audio_200ms_later_realtime", 200 * time.Millisecond, 20 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) { testSFUEncodedAVMarkers(t, tc.audioOffset, tc.pace, false) })
	}
}

func TestSFULongBridgeRecordingKeepsEncodedAVMarkersAligned(t *testing.T) {
	if os.Getenv("SFU_AV_LONG_TEST") != "1" {
		t.Skip("set SFU_AV_LONG_TEST=1 to run the one-minute recording test")
	}
	testSFUEncodedAVMarkers(t, 200*time.Millisecond, 20*time.Millisecond, true)
}

func testSFUEncodedAVMarkers(t *testing.T, audioOffset, pace time.Duration, long bool) {
	t.Helper()
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "source.h264")
	videoFilter := `color=c=black:s=160x96:r=25:d=2,drawbox=x=0:y=0:w=iw:h=ih:color=white:t=fill:enable='between(t,0.48,0.52)+between(t,1.48,1.52)'`
	audioFilter := `aevalsrc=if(between(t\,0.48\,0.50)+between(t\,1.48\,1.50)\,0.5*sin(2*PI*1000*t)\,0):s=48000:d=2`
	videoCount, audioCount, markerCount := 50, 100, 2
	segmentDuration, timeout := time.Minute, 20*time.Second
	if long {
		videoFilter = `color=c=black:s=160x96:r=25:d=60,drawbox=x=0:y=0:w=iw:h=ih:color=white:t=fill:enable='between(t,0.48,0.52)+between(t,30.0,30.04)+between(t,59.0,59.04)'`
		audioFilter = `aevalsrc=if(between(t\,0.48\,0.50)+between(t\,30.0\,30.02)+between(t\,59.0\,59.02)\,0.5*sin(2*PI*1000*t)\,0):s=48000:d=60`
		videoCount, audioCount, markerCount = 1500, 3000, 3
		segmentDuration, timeout = 2*time.Minute, 90*time.Second
	}
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", videoFilter, "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-x264-params", "threads=1:slices=1:keyint=25:min-keyint=25:scenecut=0:repeat-headers=1", "-an", "-f", "h264", "-y", videoPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	videoData, err := os.ReadFile(videoPath)
	require.NoError(t, err)
	reader, err := h264reader.NewReader(bytes.NewReader(videoData))
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
			if sps == nil {
				sps = nal.Data
			}
		case 8:
			if pps == nil {
				pps = nal.Data
			}
		case 1, 5:
			frames = append(frames, nal.Data)
		}
	}
	require.NotEmpty(t, sps)
	require.NotEmpty(t, pps)
	require.Len(t, frames, videoCount)
	audioPath := filepath.Join(dir, "source.opus")
	cmd = exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", audioFilter, "-ac", "2", "-c:a", "libopus", "-frame_duration", "20", "-b:a", "64k", "-y", audioPath)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	cmd = exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0", "-show_packets", "-show_data", "-show_entries", "packet=data", "-of", "json", audioPath)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	var audioPackets struct {
		Packets []struct {
			Data string `json:"data"`
		} `json:"packets"`
	}
	require.NoError(t, json.Unmarshal(out, &audioPackets))
	require.GreaterOrEqual(t, len(audioPackets.Packets), audioCount)
	var packets [][]byte
	for _, p := range audioPackets.Packets[:audioCount] {
		var data []byte
		for _, line := range strings.Split(strings.TrimSpace(p.Data), "\n") {
			parts := strings.SplitN(line, ":", 2)
			require.Len(t, parts, 2)
			chunk, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(strings.SplitN(parts[1], "  ", 2)[0]), " ", ""))
			require.NoError(t, err)
			data = append(data, chunk...)
		}
		packets = append(packets, data)
	}
	groups := maxlogic.GetGroupManagerInstance()
	key := maxlogic.NewStreamKey("live", "sfu_test_av_marker_sync")
	group, created := groups.GetOrCreateGroup(key, "sfu-av-marker-sync-test", nil, 1, 256)
	require.True(t, created)
	t.Cleanup(func() { group.OnStop(); groups.RemoveGroupIfMatch(key, group) })
	store := newTestManager(t)
	engine := &media.EmbeddedLalmax{}
	rec := NewH264Recorder(H264Config{CameraID: key.StreamName, SegmentDur: segmentDuration, AudioEnabled: true, PreserveSourceTimestamps: true,
		FrameSource: func(ctx context.Context) (media.FrameSubscription, error) {
			return engine.SubscribeFrames(ctx, media.SubscribeFramesRequest{StreamID: key.StreamName, AppName: "live"})
		},
	}, store)
	rec.Hub = model.NewStreamHub()
	var videoReceived atomic.Int32
	require.NoError(t, rec.Hub.Subscribe("video-test", func(_ int64, units [][]byte) {
		for _, unit := range units {
			if len(unit) > 0 && (unit[0]&31 == 1 || unit[0]&31 == 5) {
				videoReceived.Add(1)
			}
		}
	}))
	t.Cleanup(func() { rec.Hub.Unsubscribe("video-test") })
	var received atomic.Int32
	require.NoError(t, rec.Hub.SubscribeAudio("test", func(_ int64, codec model.AudioCodec, _ []byte) {
		if codec == model.AudioOpus {
			received.Add(1)
		}
	}))
	t.Cleanup(func() { rec.Hub.UnsubscribeAudio("test") })
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	require.NoError(t, rec.Start(ctx))
	t.Cleanup(func() { _ = rec.Stop() })
	require.Eventually(t, func() bool { return len(group.StatSubscribers()) > 0 }, time.Second, time.Millisecond)
	audioFan, err := rtpforward.New(1, 256)
	require.NoError(t, err)
	defer audioFan.Close()
	videoFan, err := rtpforward.New(1, 256)
	require.NoError(t, err)
	defer videoFan.Close()
	audio, err := audioFan.Subscribe("audio")
	require.NoError(t, err)
	video, err := videoFan.Subscribe("video")
	require.NoError(t, err)
	publisher := &bridgeRecordingPublisher{group: group, ready: make(chan struct{})}
	audioNTP, videoNTP := uint64(100)<<32, uint64(100)<<32
	if audioOffset >= 0 {
		audioNTP += uint64(audioOffset) * uint64(1<<32) / uint64(time.Second)
	} else {
		videoNTP += uint64(-audioOffset) * uint64(1<<32) / uint64(time.Second)
	}
	refs := bridge.AVReferences{Audio: bridge.ClockReference{NTP: audioNTP, RTP: 48000, Rate: 48000}, Video: bridge.ClockReference{NTP: videoNTP, RTP: 90000, Rate: 90000}, AudioSSRC: 42, VideoSSRC: 42}
	bridgeCtx, stopBridge := context.WithCancel(ctx)
	defer stopBridge()
	done := make(chan error, 1)
	go func() {
		done <- bridge.RunAVToLal(bridgeCtx, audio, video, publisher, key.StreamName, func(context.Context) (bridge.AVReferences, error) { return refs, nil })
	}()
	select {
	case <-publisher.ready:
	case <-time.After(time.Second):
		t.Fatal("bridge did not start")
	}
	payloader := &codecs.H264Payloader{}
	payloader.Payload(1200, sps)
	payloader.Payload(1200, pps)
	var seq uint16
	sendVideo := func(nal []byte, ts uint32) {
		payloads := payloader.Payload(1200, nal)
		for i, payload := range payloads {
			seq++
			require.NoError(t, videoFan.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: seq, Timestamp: ts, Marker: i == len(payloads)-1}, Payload: payload}))
		}
	}
	sendVideo(frames[0], 90000)
	require.Eventually(t, func() bool { rec.mu.Lock(); defer rec.mu.Unlock(); return rec.muxer != nil }, 2*time.Second, time.Millisecond)
	for i, payload := range packets {
		if i > 0 && i%2 == 0 {
			sendVideo(frames[i/2], 90000+uint32(i*1800))
		}
		require.NoError(t, audioFan.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: uint16(i + 1), Timestamp: 48000 + uint32(i*960), Marker: true}, Payload: payload}))
		time.Sleep(pace)
	}
	expectedAudio := int32(audioCount)
	if audioOffset < 0 {
		// The segment starts at the first video keyframe, so earlier audio is not recorded.
		expectedAudio -= int32((-audioOffset) / (20 * time.Millisecond))
	}
	require.Eventually(t, func() bool { return received.Load() == expectedAudio }, 3*time.Second, time.Millisecond)
	require.Eventually(t, func() bool { return videoReceived.Load() == int32(videoCount) }, 3*time.Second, time.Millisecond, "all video frames must reach the recorder before stopping")
	require.NoError(t, rec.Stop())
	stopBridge()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop")
	}
	files, err := store.ListFiles(key.StreamName)
	require.NoError(t, err)
	require.NotEmpty(t, files)
	cmd = exec.Command("ffmpeg", "-v", "error", "-xerror", "-i", files[0], "-map", "0:v:0", "-fps_mode", "passthrough", "-pix_fmt", "gray", "-f", "rawvideo", "-")
	videoPixels, err := cmd.Output()
	require.NoError(t, err)
	const width, height = 160, 96
	require.Equal(t, videoCount*width*height, len(videoPixels))
	var flashTimes []float64
	for i := 0; i < videoCount; i++ {
		frame := videoPixels[i*width*height : (i+1)*width*height]
		var sum int
		for _, v := range frame {
			sum += int(v)
		}
		if sum/(width*height) > 200 && (len(flashTimes) == 0 || float64(i)/25-flashTimes[len(flashTimes)-1] > .2) {
			flashTimes = append(flashTimes, float64(i)/25)
		}
	}
	require.Len(t, flashTimes, markerCount)
	cmd = exec.Command("ffmpeg", "-v", "error", "-xerror", "-i", files[0], "-map", "0:a:0", "-af", "aresample=async=1:first_pts=0", "-ac", "1", "-f", "f32le", "-")
	pcm, err := cmd.Output()
	require.NoError(t, err)
	const sampleRate, window = 48000, 480
	var signalTimes []float64
	for start := 0; start+window*4 <= len(pcm); start += window * 4 {
		var power float64
		for i := start; i < start+window*4; i += 4 {
			x := float64(math.Float32frombits(binary.LittleEndian.Uint32(pcm[i:])))
			power += x * x
		}
		if math.Sqrt(power/window) > .03 {
			at := float64(start/4) / sampleRate
			if len(signalTimes) == 0 || at-signalTimes[len(signalTimes)-1] > .2 {
				signalTimes = append(signalTimes, at)
			}
		}
	}
	require.Len(t, signalTimes, markerCount)
	for i := range flashTimes {
		require.InDelta(t, flashTimes[i]+audioOffset.Seconds(), signalTimes[i], .03, "audio and visual markers must remain within 30 ms")
	}
	t.Logf("decoded marker times: video=%v audio=%v", flashTimes, signalTimes)
}
