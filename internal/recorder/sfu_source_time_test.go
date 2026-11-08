package recorder

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestSFURecordingPreservesSourceTimeline(t *testing.T) {
	store := newTestManager(t)
	frames := make(chan media.MediaFrame, 128)
	rec := NewH264Recorder(H264Config{
		CameraID: "sfu-timeline", SegmentDur: time.Minute, AudioEnabled: true, PreserveSourceTimestamps: true,
		FrameSource: func(context.Context) (media.FrameSubscription, error) { return &fakeFrameSub{ch: frames}, nil },
	}, store)
	rec.Hub = model.NewStreamHub()
	var audioFrames atomic.Int32
	require.NoError(t, rec.Hub.SubscribeAudio("test", func(_ int64, codec model.AudioCodec, _ []byte) {
		if codec == model.AudioOpus {
			audioFrames.Add(1)
		}
	}))
	t.Cleanup(func() { rec.Hub.UnsubscribeAudio("test") })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, rec.Start(ctx))
	t.Cleanup(func() { _ = rec.Stop() })
	frames <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", Data: annexB(testSPS)}
	frames <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", Data: annexB(testPPS)}
	frames <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", IsKey: true, Data: annexB(testIDR)}
	require.Eventually(t, func() bool { rec.mu.Lock(); defer rec.mu.Unlock(); return rec.muxer != nil }, time.Second, time.Millisecond)
	for i := 1; i <= 20; i++ {
		pts := time.Duration(i) * 20 * time.Millisecond
		frames <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", PTS: pts, Data: annexB(testP)}
		frames <- media.MediaFrame{Kind: media.FrameAudio, Codec: "opus", PTS: pts, Data: []byte{0xf8, 0xff, 0xfe}}
		if i == 10 {
			frames <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", PTS: 10 * time.Millisecond, Data: annexB(testP)}
		}
		time.Sleep(3 * time.Millisecond) // deliberately faster than the media clock
	}
	require.Eventually(t, func() bool { return audioFrames.Load() == 20 }, time.Second, time.Millisecond)
	require.NoError(t, rec.Stop())
	files, err := store.ListFiles("sfu-timeline")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	// The first stts belongs to video; all 21 samples have 20 ms duration.
	idx := bytes.Index(data, []byte("stts"))
	require.GreaterOrEqual(t, idx, 0)
	payload := data[idx+4:]
	require.GreaterOrEqual(t, len(payload), 8)
	entries := int(binary.BigEndian.Uint32(payload[4:8]))
	require.LessOrEqual(t, 8+entries*8, len(payload))
	var samples, duration uint32
	for i := 0; i < entries; i++ {
		n := binary.BigEndian.Uint32(payload[8+i*8:])
		d := binary.BigEndian.Uint32(payload[12+i*8:])
		require.Equal(t, uint32(20), d, "each sample must use the interval to its successor")
		samples += n
		duration += n * d
	}
	require.Equal(t, uint32(21), samples)
	require.Equal(t, uint32(420), duration, "video duration must follow source PTS, not the accelerated arrival")
	edit := bytes.Index(data, []byte("elst"))
	require.GreaterOrEqual(t, edit, 0)
	require.GreaterOrEqual(t, len(data), edit+16)
	require.Equal(t, uint32(100), binary.BigEndian.Uint32(data[edit+12:edit+16]), "20 ms audio offset plus 80 ms Opus priming")
	if artifact := os.Getenv("SFU_TIMELINE_TEST_MP4"); artifact != "" {
		require.NoError(t, os.WriteFile(artifact, data, 0o600))
	}
}

func TestSFURecordingPreservesVideoGapOnPreviousSample(t *testing.T) {
	store := newTestManager(t)
	rec := NewH264Recorder(H264Config{CameraID: "sfu-gap", SegmentDur: time.Minute, PreserveSourceTimestamps: true}, store)
	rec.frameCh = make(chan h264QueuedFrame, 6)
	for _, item := range []struct {
		nal []byte
		pts time.Duration
	}{
		{testSPS, 0}, {testPPS, 0}, {testIDR, 0}, {testP, 20 * time.Millisecond}, {testP, 120 * time.Millisecond}, {testP, 140 * time.Millisecond},
	} {
		rec.frameCh <- h264QueuedFrame{data: annexB(item.nal), pts: item.pts, sourceTime: true}
	}
	close(rec.frameCh)
	rec.writeFrames(make(chan struct{}))
	rec.closeCurrentSegment()
	files, err := store.ListFiles("sfu-gap")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	idx := bytes.Index(data, []byte("stts"))
	require.GreaterOrEqual(t, idx, 0)
	payload := data[idx+4:]
	require.GreaterOrEqual(t, len(payload), 40)
	require.Equal(t, uint32(4), binary.BigEndian.Uint32(payload[4:8]))
	require.Equal(t, []uint32{1, 20, 1, 100, 1, 20, 1, 20}, []uint32{
		binary.BigEndian.Uint32(payload[8:12]), binary.BigEndian.Uint32(payload[12:16]),
		binary.BigEndian.Uint32(payload[16:20]), binary.BigEndian.Uint32(payload[20:24]),
		binary.BigEndian.Uint32(payload[24:28]), binary.BigEndian.Uint32(payload[28:32]),
		binary.BigEndian.Uint32(payload[32:36]), binary.BigEndian.Uint32(payload[36:40]),
	})
}

func TestSFURecordingKeepsFractionalOpusDurations(t *testing.T) {
	store := newTestManager(t)
	frames := make(chan media.MediaFrame, 128)
	rec := NewH264Recorder(H264Config{
		CameraID: "sfu-fractional", SegmentDur: time.Minute, AudioEnabled: true, PreserveSourceTimestamps: true,
		FrameSource: func(context.Context) (media.FrameSubscription, error) { return &fakeFrameSub{ch: frames}, nil },
	}, store)
	rec.Hub = model.NewStreamHub()
	var audioFrames atomic.Int32
	require.NoError(t, rec.Hub.SubscribeAudio("fractional-test", func(_ int64, codec model.AudioCodec, _ []byte) {
		if codec == model.AudioOpus {
			audioFrames.Add(1)
		}
	}))
	t.Cleanup(func() { rec.Hub.UnsubscribeAudio("fractional-test") })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, rec.Start(ctx))
	t.Cleanup(func() { _ = rec.Stop() })
	frames <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", Data: annexB(testSPS)}
	frames <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", Data: annexB(testPPS)}
	frames <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", IsKey: true, Data: annexB(testIDR)}
	require.Eventually(t, func() bool { rec.mu.Lock(); defer rec.mu.Unlock(); return rec.muxer != nil }, time.Second, time.Millisecond)
	for i := range 400 {
		// RTMP timestamps are integral milliseconds, while Opus packets are 2.5 ms.
		pts := time.Duration(i*5/2) * time.Millisecond
		frames <- media.MediaFrame{Kind: media.FrameAudio, Codec: "opus", PTS: pts, Data: []byte{0xe0, 0xff, 0xfe}}
		time.Sleep(time.Millisecond)
	}
	require.Eventually(t, func() bool { return audioFrames.Load() == 400 }, 5*time.Second, time.Millisecond)
	require.NoError(t, rec.Stop())
	files, err := store.ListFiles("sfu-fractional")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	first := bytes.Index(data, []byte("mdhd"))
	require.GreaterOrEqual(t, first, 0)
	second := bytes.Index(data[first+4:], []byte("mdhd"))
	require.GreaterOrEqual(t, second, 0)
	second += first + 4
	require.GreaterOrEqual(t, len(data), second+24)
	require.Equal(t, uint32(48000), binary.BigEndian.Uint32(data[second+16:second+20]))
	require.Equal(t, uint32(48000), binary.BigEndian.Uint32(data[second+20:second+24]), "400 packets must span one second")
	stsz := bytes.Index(data, []byte("stsz"))
	require.GreaterOrEqual(t, stsz, 0)
	audioStsz := bytes.Index(data[stsz+4:], []byte("stsz"))
	require.GreaterOrEqual(t, audioStsz, 0)
	audioStsz += stsz + 4
	require.GreaterOrEqual(t, len(data), audioStsz+16)
	require.Equal(t, uint32(400), binary.BigEndian.Uint32(data[audioStsz+12:audioStsz+16]), "timestamp rounding must not insert silence")
	if artifact := os.Getenv("SFU_FRACTIONAL_RECORDING_MP4"); artifact != "" {
		require.NoError(t, os.WriteFile(artifact, data, 0o600))
	}
}
