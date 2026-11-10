package recorder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// TestSFUDecodableBridgeRecording is opt-in because it needs FFmpeg and libx264.
// It generates test video and Opus silence, and never opens an audio/video device.
func TestSFUDecodableBridgeRecording(t *testing.T) {
	testSFUDecodableBridgeRecording(t, false)
}

func TestSFUDecodableBridgeRecordingWithLongOpusGap(t *testing.T) {
	testSFUDecodableBridgeRecording(t, true)
}

func testSFUDecodableBridgeRecording(t *testing.T, longGap bool) {
	t.Helper()
	if os.Getenv("SFU_DECODE_RECORDING_TEST") != "1" {
		t.Skip("set SFU_DECODE_RECORDING_TEST=1 to run the FFmpeg integration test")
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "input.h264")
	duration, videoFrames, audioFrames, expectedAudio := "1", 25, 50, "opus,51"
	if longGap {
		duration, videoFrames, audioFrames, expectedAudio = "4", 100, 200, "opus,101"
	}
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x96:rate=25:duration="+duration, "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-x264-params", "threads=1:slices=1:keyint=25:min-keyint=25:scenecut=0:repeat-headers=1", "-an", "-f", "h264", "-y", input)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	raw, err := os.ReadFile(input)
	require.NoError(t, err)
	reader, err := h264reader.NewReader(bytes.NewReader(raw))
	require.NoError(t, err)
	var parameterSets [][]byte
	var frames [][]byte
	for {
		nal, err := reader.NextNAL()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		switch nal.Data[0] & 31 {
		case 7, 8:
			if len(parameterSets) < 2 {
				parameterSets = append(parameterSets, nal.Data)
			}
		case 1, 5:
			frames = append(frames, nal.Data)
		}
	}
	require.Len(t, parameterSets, 2)
	require.Len(t, frames, videoFrames)

	groups := maxlogic.GetGroupManagerInstance()
	key := maxlogic.NewStreamKey("live", "sfu_test_decodable_recording")
	group, created := groups.GetOrCreateGroup(key, "sfu-decodable-recording-test", nil, 1, 256)
	require.True(t, created)
	t.Cleanup(func() { group.OnStop(); groups.RemoveGroupIfMatch(key, group) })
	store := newTestManager(t)
	engine := &media.EmbeddedLalmax{}
	rec := NewH264Recorder(H264Config{CameraID: key.StreamName, SegmentDur: time.Minute, AudioEnabled: true, PreserveSourceTimestamps: true,
		FrameSource: func(ctx context.Context) (media.FrameSubscription, error) {
			return engine.SubscribeFrames(ctx, media.SubscribeFramesRequest{StreamID: key.StreamName, AppName: "live"})
		},
	}, store)
	rec.Hub = model.NewStreamHub()
	var received atomic.Int32
	require.NoError(t, rec.Hub.SubscribeAudio("test", func(_ int64, codec model.AudioCodec, _ []byte) {
		if codec == model.AudioOpus {
			received.Add(1)
		}
	}))
	t.Cleanup(func() { rec.Hub.UnsubscribeAudio("test") })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
	refs := bridge.AVReferences{Audio: bridge.ClockReference{NTP: 100 << 32, RTP: 48000, Rate: 48000}, Video: bridge.ClockReference{NTP: 100 << 32, RTP: 90000, Rate: 90000}, AudioSSRC: 42, VideoSSRC: 42}
	bridgeCtx, stopBridge := context.WithCancel(ctx)
	defer stopBridge()
	done := make(chan error, 1)
	go func() {
		done <- bridge.RunAVToLal(bridgeCtx, audio, video, publisher, key.StreamName, func(context.Context, uint32) (bridge.AVReferences, error) { return refs, nil })
	}()
	select {
	case <-publisher.ready:
	case <-time.After(time.Second):
		t.Fatal("bridge did not start")
	}
	payloader := &codecs.H264Payloader{}
	for _, nal := range parameterSets {
		payloader.Payload(1200, nal)
	}
	var videoSequence uint16
	sendVideo := func(nal []byte, ts uint32) {
		payloads := payloader.Payload(1200, nal)
		for i, payload := range payloads {
			videoSequence++
			require.NoError(t, videoFan.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: videoSequence, Timestamp: ts, Marker: i == len(payloads)-1}, Payload: payload}))
		}
	}
	sendVideo(frames[0], 90000)
	require.Eventually(t, func() bool { rec.mu.Lock(); defer rec.mu.Unlock(); return rec.muxer != nil }, 2*time.Second, time.Millisecond)
	for i := 0; i < audioFrames; i++ {
		if i > 0 && i%2 == 0 {
			sendVideo(frames[i/2], 90000+uint32(i*1800))
		}
		if !longGap || i < 20 || i >= 140 {
			require.NoError(t, audioFan.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: uint16(i + 1), Timestamp: 48000 + uint32(i*960), Marker: true}, Payload: []byte{0xf8, 0xff, 0xfe}}))
		}
		time.Sleep(3 * time.Millisecond)
	}
	expectedReceived := int32(audioFrames)
	if longGap {
		expectedReceived -= 120
	}
	require.Eventually(t, func() bool { return received.Load() == expectedReceived }, 3*time.Second, time.Millisecond)
	require.NoError(t, rec.Stop())
	stopBridge()
	select {
	case err := <-done:
		require.True(t, errors.Is(err, context.Canceled), "%v", err)
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop")
	}
	files, err := store.ListFiles(key.StreamName)
	require.NoError(t, err)
	require.NotEmpty(t, files)
	cmd = exec.Command("ffprobe", "-v", "error", "-count_frames", "-select_streams", "v:0", "-show_entries", "stream=codec_name,width,height,nb_read_frames", "-of", "csv=p=0", files[0])
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.Equal(t, fmt.Sprintf("h264,160,96,%d", videoFrames), strings.TrimSpace(string(out)))
	cmd = exec.Command("ffprobe", "-v", "error", "-count_packets", "-select_streams", "a:0", "-show_entries", "stream=codec_name,nb_read_packets", "-of", "csv=p=0", files[0])
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.Equal(t, expectedAudio, strings.TrimSpace(string(out)))
	cmd = exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=duration", "-of", "default=nw=1:nk=1", files[0])
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	audioDuration, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	require.NoError(t, err)
	expectedDuration, err := strconv.ParseFloat(duration, 64)
	require.NoError(t, err)
	require.InDelta(t, expectedDuration, audioDuration, 0.001)
	cmd = exec.Command("ffmpeg", "-v", "error", "-xerror", "-i", files[0], "-map", "0:v:0", "-map", "0:a:0", "-f", "null", "-")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	if artifact := os.Getenv("SFU_DECODE_RECORDING_MP4"); artifact != "" {
		data, err := os.ReadFile(files[0])
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(artifact, data, 0o600))
	}
}
