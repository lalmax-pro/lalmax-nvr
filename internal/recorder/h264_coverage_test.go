package recorder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/gortsplib/v5/pkg/format"

	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/muxer"
)

func TestCovH264_GettersDefaultsAndMetrics(t *testing.T) {
	store := newHXStore(t)
	m := metrics.NewMetrics()
	rec := NewH264Recorder(H264Config{
		CameraID:     "cam-h264-get",
		StreamID:     "stream-h264",
		RTSPURL:      "rtsp://127.0.0.1:1/live",
		AudioEnabled: true,
	}, store, m)

	require.Equal(t, DefaultSegmentDur, rec.cfg.SegmentDur)
	require.Equal(t, DefaultRingBufCap, rec.cfg.RingBufCap)
	require.Equal(t, defaultFrameWatchdogTimeout, rec.cfg.FrameWatchdogTimeout)
	require.NotNil(t, rec.preroll)

	require.Nil(t, rec.GetHub())
	rec.Hub = model.NewStreamHub()
	require.NotNil(t, rec.GetHub())

	require.Nil(t, rec.SPS())
	require.Nil(t, rec.PPS())
	rec.sps = []byte{0x67, 0x42}
	rec.pps = []byte{0x68, 0xce}
	require.Equal(t, []byte{0x67, 0x42}, rec.SPS())
	require.Equal(t, []byte{0x68, 0xce}, rec.PPS())

	require.True(t, rec.AudioEnabled())
	require.Empty(t, rec.AudioCodec())
	require.Nil(t, rec.AudioMuxerConfig())
	rec.audioCodec = "aac"
	rec.audioMuxerConfig = []byte{0x12, 0x10}
	require.Equal(t, "aac", rec.AudioCodec())
	require.Equal(t, []byte{0x12, 0x10}, rec.AudioMuxerConfig())
	require.Equal(t, "rtsp://127.0.0.1:1/live", rec.SourceURL())

	rec.incActive()
	rec.decActive()
	rec.recordSegmentCreated()
	rec.recordBytes(321)
	rec.recordError("connection")

	plain := NewH264Recorder(H264Config{CameraID: "cam-h264-plain"}, newHXStore(t))
	plain.incActive()
	plain.decActive()
	plain.recordSegmentCreated()
	plain.recordBytes(1)
	plain.recordError("connection")
	require.False(t, plain.AudioEnabled())
	require.Nil(t, plain.GetHub())
	require.NoError(t, plain.Stop())
}

func TestCovH264_PauseResumeAndAdaptive(t *testing.T) {
	store := newHXStore(t)
	rec := NewH264Recorder(H264Config{
		CameraID: "cam-h264-pause",
		Adaptive: NewAdaptiveGate(time.Hour),
	}, store)

	require.False(t, rec.IsPaused())
	rec.Pause()
	require.True(t, rec.IsPaused())
	require.Equal(t, model.StatusPaused, rec.Status())

	// Paused frames only feed the preroll ring.
	hxFeedH264(rec, testSPS, testPPS, testIDR, testP)
	require.Zero(t, rec.frameCount)
	require.Nil(t, rec.muxer)

	hxFeedH264(rec, testIDR)
	rec.frameCh = make(chan []byte, 16)
	rec.Resume()
	require.False(t, rec.IsPaused())
	require.Equal(t, model.StatusRecording, rec.Status())
	require.NotEmpty(t, len(rec.frameCh))

	// Pausing with an open muxer finalizes the segment.
	hxFeedH264(rec, testSPS, testPPS, testIDR)
	require.NotNil(t, rec.muxer)
	rec.Pause()
	require.Nil(t, rec.muxer)
	segments, closed := store.stats()
	require.Equal(t, 1, segments)
	require.Equal(t, 1, closed)

	rec.TriggerAdaptive(time.Minute)
	rec.cfg.Adaptive = nil
	rec.TriggerAdaptive(0)
}

func TestCovH264_WriteFramesBranches(t *testing.T) {
	store := newHXStore(t)
	rec := NewH264Recorder(H264Config{CameraID: "cam-h264-write", SegmentDur: time.Nanosecond}, store)

	// Parameter sets are cached, not written.
	hxFeedH264(rec, testSPS, testPPS)
	require.Nil(t, rec.muxer)
	require.Equal(t, testSPS, rec.sps)
	require.Equal(t, testPPS, rec.pps)

	// Too-short and pre-IDR NALUs are skipped.
	hxFeedH264(rec, []byte{0x01}, testP)
	require.Nil(t, rec.muxer)
	require.Zero(t, rec.frameCount)

	// An IDR starts the segment.
	hxFeedH264(rec, testIDR, testP)
	require.NotNil(t, rec.muxer)
	require.Equal(t, 2, rec.frameCount)
	require.True(t, rec.pendingRotate)

	// Rotation happens on the next IDR.
	hxFeedH264(rec, testIDR, testP)
	require.Equal(t, 2, rec.frameCount)
	require.Equal(t, 1, store.statsClosed())
	rec.closeCurrentSegment()
	require.Equal(t, 2, store.statsClosed())

	// Repeated parameter sets do not rotate; a changed SPS does.
	hxFeedH264(rec, testSPS, testPPS, testIDR)
	require.NotNil(t, rec.muxer)
	hxFeedH264(rec, testSPS, testPPS)
	require.NotNil(t, rec.muxer)
	hxFeedH264(rec, testSPS2)
	require.Nil(t, rec.muxer, "SPS change rotates the segment")
	require.Equal(t, testSPS2, rec.sps)

	// A changed PPS also rotates.
	hxFeedH264(rec, testIDR)
	require.NotNil(t, rec.muxer)
	changedPPS := append([]byte(nil), testPPS...)
	changedPPS[len(changedPPS)-1] ^= 0xFF
	hxFeedH264(rec, changedPPS)
	require.Nil(t, rec.muxer)
	require.Equal(t, changedPPS, rec.pps)
}

func TestCovH264_WriteFramesEdgeCases(t *testing.T) {
	t.Run("adaptive gate refuses sparse frames", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{
			CameraID: "cam-h264-adaptive",
			Adaptive: NewAdaptiveGate(time.Hour),
		}, newHXStore(t))
		hxFeedH264(rec, testSPS, testPPS, testIDR)
		require.Equal(t, 1, rec.frameCount)
		hxFeedH264(rec, testP)
		require.Equal(t, 1, rec.frameCount)
		hxFeedH264(rec, testIDR)
		require.Equal(t, 1, rec.frameCount)
		rec.closeCurrentSegment()
	})

	t.Run("idr without parameter sets is skipped", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{CameraID: "cam-h264-nops"}, newHXStore(t))
		hxFeedH264(rec, testIDR)
		require.Nil(t, rec.muxer)
		require.Zero(t, rec.frameCount)
	})

	t.Run("store create error", func(t *testing.T) {
		store := newHXStore(t)
		store.createErr = errors.New("disk full")
		rec := NewH264Recorder(H264Config{CameraID: "cam-h264-create"}, store)
		hxFeedH264(rec, testSPS, testPPS, testIDR)
		require.Nil(t, rec.muxer)
		require.Zero(t, rec.frameCount)
	})

	t.Run("unsupported audio codec keeps video", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{CameraID: "cam-h264-audio-err"}, newHXStore(t))
		rec.audioCodec = "bogus"
		rec.audioMuxerConfig = []byte{0x01, 0x02}
		hxFeedH264(rec, testSPS, testPPS, testIDR)
		require.NotNil(t, rec.muxer)
		require.Zero(t, rec.audioTrackID)
		require.Equal(t, 1, rec.frameCount)
		rec.closeCurrentSegment()
	})

	t.Run("write sample error is survived", func(t *testing.T) {
		store := newHXStore(t)
		store.tempAsDir = true
		rec := NewH264Recorder(H264Config{CameraID: "cam-h264-write-err"}, store)
		hxFeedH264(rec, testSPS, testPPS, testIDR)
		require.NotNil(t, rec.muxer)
		require.Zero(t, rec.frameCount)
		rec.closeCurrentSegment()
		require.Nil(t, rec.muxer)
	})

	t.Run("db insert error is logged", func(t *testing.T) {
		db := &hjDB{err: errors.New("db down")}
		rec := NewH264Recorder(H264Config{CameraID: "cam-h264-db-err", DB: db}, newHXStore(t), metrics.NewMetrics())
		hxFeedH264(rec, testSPS, testPPS, testIDR)
		rec.closeCurrentSegment()
		calls, recs := db.snapshot()
		require.Equal(t, 1, calls)
		require.Empty(t, recs)
	})
}

func TestCovH264_CloseSegmentPublishesEvents(t *testing.T) {
	store := newHXStore(t)
	db := &hjDB{}
	bus := event.NewEventBus(16)
	segCh := make(chan event.Event, 4)
	reconnCh := make(chan event.Event, 4)
	require.NoError(t, bus.Subscribe(event.TopicSegmentCompleted, segCh, 4))
	require.NoError(t, bus.Subscribe(event.TopicRecorderReconnected, reconnCh, 4))

	rec := NewH264Recorder(H264Config{
		CameraID: "cam-h264-close",
		StreamID: "stream-h264",
		DB:       db,
		EventBus: bus,
	}, store, metrics.NewMetrics())

	hxFeedH264(rec, testSPS, testPPS, testIDR, testP)
	require.NotNil(t, rec.muxer)
	require.Equal(t, 2, rec.frameCount)

	rec.disconnectedAt = time.Now().Add(-3 * time.Second)
	rec.reconnectTime = time.Now().Add(-2 * time.Second)
	rec.retryCount = 5
	rec.gapReason = "connection_lost"
	rec.hasPendingReconnect = true

	rec.closeCurrentSegment()
	require.Nil(t, rec.muxer)
	require.False(t, rec.hasPendingReconnect)
	require.Zero(t, rec.retryCount)
	require.Empty(t, rec.gapReason)

	calls, recs := db.snapshot()
	require.Equal(t, 1, calls)
	require.Len(t, recs, 1)
	require.Equal(t, model.FormatH264, recs[0].Format)
	require.Equal(t, "stream-h264", recs[0].StreamID)
	require.Equal(t, 2, recs[0].FrameCount)
	require.Greater(t, recs[0].FileSize, int64(0))
	require.False(t, recs[0].ReconnectedAt.IsZero())
	require.Equal(t, "connection_lost", recs[0].GapReason)

	select {
	case ev := <-segCh:
		sc, ok := ev.Data.(event.SegmentCompleted)
		require.True(t, ok)
		require.Equal(t, recs[0].ID, sc.RecordingID)
	case <-time.After(time.Second):
		t.Fatal("expected segment.completed event")
	}
	select {
	case ev := <-reconnCh:
		rc, ok := ev.Data.(event.RecorderReconnected)
		require.True(t, ok)
		require.Equal(t, 5, rc.RetryCount)
		require.NotEmpty(t, rc.Downtime)
	case <-time.After(time.Second):
		t.Fatal("expected recorder.reconnected event")
	}

	rec.closeCurrentSegment() // no-op
	_, recs2 := db.snapshot()
	require.Len(t, recs2, 1)
}

func TestCovH264_CloseSegmentStoreAndMuxerErrors(t *testing.T) {
	t.Run("store close error still inserts recording", func(t *testing.T) {
		store := newHXStore(t)
		store.closeErr = errors.New("rename failed")
		db := &hjDB{}
		rec := NewH264Recorder(H264Config{CameraID: "cam-h264-store-err", DB: db}, store, metrics.NewMetrics())
		hxFeedH264(rec, testSPS, testPPS, testIDR)
		rec.closeCurrentSegment()
		calls, recs := db.snapshot()
		require.Equal(t, 1, calls)
		require.Len(t, recs, 1)
		require.Zero(t, recs[0].FileSize)
	})

	t.Run("muxer close error cleans state", func(t *testing.T) {
		badDir := t.TempDir()
		m := muxer.NewMP4Muxer(badDir)
		trackID, err := m.AddH264Track(testSPS, testPPS)
		require.NoError(t, err)

		rec := NewH264Recorder(H264Config{CameraID: "cam-h264-mux-err"}, newHXStore(t), metrics.NewMetrics())
		rec.muxer = m
		rec.trackID = trackID
		rec.curTempPath = badDir
		rec.curFinalPath = filepath.Join(t.TempDir(), "final.mp4")
		rec.frameCount = 1
		rec.segStart = time.Now().Add(-time.Second)

		rec.closeCurrentSegment()
		require.Nil(t, rec.muxer)
		require.Empty(t, rec.curTempPath)
		require.Empty(t, rec.curFinalPath)
		require.Zero(t, rec.frameCount)
		_, err = os.Stat(badDir)
		require.True(t, os.IsNotExist(err))
	})
}

func TestCovH264_ConsumeVideoFrames(t *testing.T) {
	t.Run("video audio and filtering", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{CameraID: "cam-h264-consume", AudioEnabled: true}, newHXStore(t), metrics.NewMetrics())
		rec.frameCh = make(chan []byte, 8)
		rec.Hub = model.NewStreamHub()
		sub := newHXSub()
		alive := make(chan struct{}, 1)
		connected := false

		sub.ch <- media.MediaFrame{Kind: media.FrameVideo, Data: []byte{1, 2, 3}}                // too short
		sub.ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h265", Data: annexB(testIDR)} // wrong codec
		sub.ch <- media.MediaFrame{Kind: media.FrameAudio, Codec: "aac", IsSeqHeader: true, Data: []byte{0x12, 0x10}}
		sub.ch <- hxAudioFrame("aac", []byte{0x01, 0x02, 0x03})
		sub.ch <- hxAudioFrame("opus", []byte{0x05})
		sub.ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", IsKey: true, PTS: time.Second, Data: annexB(testIDR)}
		close(sub.ch)

		err := rec.consumeVideoFrames(context.Background(), sub, "h264", alive, &connected)
		require.ErrorContains(t, err, "frame subscription ended")
		require.True(t, connected)
		require.Equal(t, "aac", rec.audioCodec)
		require.Equal(t, []byte{0x12, 0x10}, rec.audioMuxerConfig)
		require.Equal(t, 1, len(rec.frameCh))
		select {
		case <-alive:
		default:
			t.Fatal("expected the watchdog to be signalled")
		}
	})

	t.Run("g711u audio frames are configured", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{CameraID: "cam-h264-consume-g711u", AudioEnabled: true}, newHXStore(t))
		rec.frameCh = make(chan []byte, 4)
		sub := newHXSub()
		sub.ch <- hxAudioFrame("g711u", []byte{0x01, 0x02})
		close(sub.ch)
		connected := false
		err := rec.consumeVideoFrames(context.Background(), sub, "h264", make(chan struct{}, 1), &connected)
		require.Error(t, err)
		require.Equal(t, "g711", rec.audioCodec)
		require.True(t, rec.g711MULaw)
		require.Equal(t, 8000, rec.g711SampleRate)
		require.Len(t, rec.audioMuxerConfig, 5)
	})

	t.Run("g711a audio frames are configured", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{CameraID: "cam-h264-consume-g711a", AudioEnabled: true}, newHXStore(t))
		rec.frameCh = make(chan []byte, 4)
		sub := newHXSub()
		sub.ch <- hxAudioFrame("g711a", []byte{0x03, 0x04})
		close(sub.ch)
		connected := false
		err := rec.consumeVideoFrames(context.Background(), sub, "h264", make(chan struct{}, 1), &connected)
		require.Error(t, err)
		require.Equal(t, "g711", rec.audioCodec)
		require.False(t, rec.g711MULaw)
	})

	t.Run("audio disabled drops audio frames", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{CameraID: "cam-h264-consume-noaudio"}, newHXStore(t))
		rec.frameCh = make(chan []byte, 4)
		sub := newHXSub()
		sub.ch <- hxAudioFrame("aac", []byte{0x01})
		close(sub.ch)
		connected := false
		err := rec.consumeVideoFrames(context.Background(), sub, "h264", make(chan struct{}, 1), &connected)
		require.ErrorContains(t, err, "ended before first frame")
		require.False(t, connected)
		require.Empty(t, rec.audioCodec)
	})

	t.Run("ring buffer overflow drops frames", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{CameraID: "cam-h264-overflow"}, newHXStore(t), metrics.NewMetrics())
		rec.frameCh = make(chan []byte)
		sub := newHXSub()
		for i := 0; i < 3; i++ {
			sub.ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", Data: annexB(testIDR)}
		}
		close(sub.ch)
		connected := false
		err := rec.consumeVideoFrames(context.Background(), sub, "h264", make(chan struct{}, 1), &connected)
		require.Error(t, err)
		require.Equal(t, int64(3), rec.dropped.Load())
	})

	t.Run("subscription error is returned", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{CameraID: "cam-h264-consume-err"}, newHXStore(t))
		rec.frameCh = make(chan []byte, 1)
		sub := newHXSub()
		sub.errc <- errors.New("subscription broken")
		connected := false
		err := rec.consumeVideoFrames(context.Background(), sub, "h264", make(chan struct{}, 1), &connected)
		require.ErrorContains(t, err, "subscription broken")
	})

	t.Run("context cancellation stops the loop", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{CameraID: "cam-h264-consume-ctx"}, newHXStore(t))
		rec.frameCh = make(chan []byte, 1)
		sub := newHXSub()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		connected := false
		err := rec.consumeVideoFrames(ctx, sub, "h264", make(chan struct{}, 1), &connected)
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestCovH264_FrameSourcePaths(t *testing.T) {
	t.Run("frame source error", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{
			CameraID: "cam-h264-fs-err",
			FrameSource: func(context.Context) (media.FrameSubscription, error) {
				return nil, errors.New("no in-process frames")
			},
		}, newHXStore(t))
		err, connected := rec.connectAndRecordFrames(context.Background())
		require.ErrorContains(t, err, "no in-process frames")
		require.False(t, connected)
	})

	t.Run("subscription ends before first frame", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{
			CameraID: "cam-h264-fs-empty",
			FrameSource: func(context.Context) (media.FrameSubscription, error) {
				ch := make(chan media.MediaFrame)
				close(ch)
				return &hxSub{ch: ch, errc: make(chan error)}, nil
			},
		}, newHXStore(t))
		err, connected := rec.connectAndRecordFrames(context.Background())
		require.ErrorContains(t, err, "ended before first frame")
		require.False(t, connected)
	})

	t.Run("records from frames then reports end", func(t *testing.T) {
		store := newHXStore(t)
		ch := make(chan media.MediaFrame, 8)
		rec := NewH264Recorder(H264Config{
			CameraID: "cam-h264-fs-ok",
			FrameSource: func(context.Context) (media.FrameSubscription, error) {
				return &hxSub{ch: ch, errc: make(chan error)}, nil
			},
		}, store)
		ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", IsKey: true, Data: annexB(testSPS)}
		ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", IsKey: true, Data: annexB(testPPS)}
		ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", IsKey: true, Data: annexB(testIDR)}
		close(ch)

		err, connected := rec.connectAndRecordFrames(context.Background())
		require.ErrorContains(t, err, "frame subscription ended")
		require.True(t, connected)
		require.Nil(t, rec.muxer)
		require.Equal(t, 1, store.statsClosed())
	})

	t.Run("recovery is reported on the next segment", func(t *testing.T) {
		ch := make(chan media.MediaFrame, 4)
		rec := NewH264Recorder(H264Config{
			CameraID: "cam-h264-fs-reconnect",
			FrameSource: func(context.Context) (media.FrameSubscription, error) {
				return &hxSub{ch: ch, errc: make(chan error)}, nil
			},
		}, newHXStore(t))
		rec.disconnectedAt = time.Now().Add(-time.Second)
		rec.retryCount = 2
		ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", IsKey: true, Data: annexB(testSPS)}
		ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", IsKey: true, Data: annexB(testPPS)}
		ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", IsKey: true, Data: annexB(testIDR)}
		close(ch)

		err, connected := rec.connectAndRecordFrames(context.Background())
		require.Error(t, err)
		require.True(t, connected)
		require.True(t, rec.hasPendingReconnect)
		require.False(t, rec.reconnectTime.IsZero())
	})

	t.Run("frame watchdog closes a silent subscription", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{
			CameraID:             "cam-h264-fs-watchdog",
			FrameWatchdogTimeout: 5 * time.Millisecond,
			FrameSource: func(context.Context) (media.FrameSubscription, error) {
				return newHXClosableSub(), nil
			},
		}, newHXStore(t))

		done := make(chan struct{})
		var err error
		go func() {
			defer close(done)
			err, _ = rec.connectAndRecordFrames(context.Background())
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("frame watchdog did not close the silent subscription")
		}
		require.Error(t, err)
	})

	t.Run("context cancellation stops the frame loop", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{
			CameraID: "cam-h264-fs-ctx",
			FrameSource: func(context.Context) (media.FrameSubscription, error) {
				return &hxSub{ch: make(chan media.MediaFrame), errc: make(chan error)}, nil
			},
		}, newHXStore(t))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			err, _ := rec.connectAndRecordFrames(ctx)
			done <- err
		}()
		time.Sleep(20 * time.Millisecond)
		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(5 * time.Second):
			t.Fatal("frame source loop did not stop on context cancellation")
		}
	})
}

func TestCovH264_ConnectAndRecordFallbacks(t *testing.T) {
	t.Run("unsupported frames without RTSP URL returns error", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{
			CameraID: "cam-h264-nofallback",
			FrameSource: func(context.Context) (media.FrameSubscription, error) {
				return nil, media.ErrFramesNotSupported
			},
		}, newHXStore(t))
		err, connected := rec.connectAndRecord(context.Background())
		require.ErrorIs(t, err, media.ErrFramesNotSupported)
		require.False(t, connected)
	})

	t.Run("unsupported frames falls back to rtsp", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{
			CameraID: "cam-h264-fallback",
			RTSPURL:  "://not-a-url",
			FrameSource: func(context.Context) (media.FrameSubscription, error) {
				return nil, media.ErrFramesNotSupported
			},
		}, newHXStore(t))
		err, connected := rec.connectAndRecord(context.Background())
		require.ErrorContains(t, err, "invalid RTSP URL")
		require.False(t, connected)
	})

	t.Run("generic source error with rtsp configured is returned", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{
			CameraID: "cam-h264-fs-generic",
			RTSPURL:  "rtsp://127.0.0.1:1/live",
			FrameSource: func(context.Context) (media.FrameSubscription, error) {
				return nil, errors.New("temporary frame failure")
			},
		}, newHXStore(t))
		err, connected := rec.connectAndRecord(context.Background())
		require.ErrorContains(t, err, "temporary frame failure")
		require.False(t, connected)
	})
}

func TestCovH264_ConnectAndRecordRTSPErrors(t *testing.T) {
	t.Run("invalid url", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{CameraID: "cam-h264-badurl", RTSPURL: "://bad"}, newHXStore(t))
		err, connected := rec.connectAndRecordRTSP(context.Background())
		require.ErrorContains(t, err, "invalid RTSP URL")
		require.False(t, connected)
	})

	t.Run("unreachable server", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{CameraID: "cam-h264-unreachable", RTSPURL: "rtsp://127.0.0.1:1/live"}, newHXStore(t))
		err, connected := rec.connectAndRecordRTSP(context.Background())
		require.ErrorContains(t, err, "DESCRIBE")
		require.False(t, connected)
	})

	t.Run("no h264 media in stream", func(t *testing.T) {
		srv := newTestRTSPServerH265(t) // H265-only in-process server
		defer srv.close()
		rec := NewH264Recorder(H264Config{
			CameraID: "cam-h264-nomedia",
			RTSPURL:  srv.rtspURL,
			Username: "admin",
			Password: "pass",
		}, newHXStore(t))
		err, connected := rec.connectAndRecordRTSP(context.Background())
		require.ErrorContains(t, err, "H264 media not found")
		require.False(t, connected)
	})

	t.Run("audio enabled but stream has no audio", func(t *testing.T) {
		srv := newTestRTSPServer(t)
		defer srv.close()
		store := newHXStore(t)
		rec := NewH264Recorder(H264Config{
			CameraID:     "cam-h264-noaudio",
			RTSPURL:      srv.rtspURL,
			AudioEnabled: true,
			RingBufCap:   32,
		}, store, metrics.NewMetrics())

		require.NoError(t, rec.Start(context.Background()))
		srv.waitPlay(t, 5*time.Second)
		srv.sendFrames(2, 10*time.Millisecond)
		require.Eventually(t, func() bool {
			segments, _ := store.stats()
			return segments >= 1
		}, 5*time.Second, 5*time.Millisecond)
		require.NoError(t, rec.Stop())
		require.Empty(t, rec.AudioCodec())
		require.GreaterOrEqual(t, store.statsClosed(), 1)
	})
}

func TestCovH264_RunLoopAndReconnectMetadata(t *testing.T) {
	t.Run("run retries and reports reconnecting", func(t *testing.T) {
		rec := NewH264Recorder(H264Config{
			CameraID: "cam-h264-run",
			RTSPURL:  "rtsp://127.0.0.1:1/live",
		}, newHXStore(t), metrics.NewMetrics())

		require.NoError(t, rec.Start(context.Background()))
		require.ErrorContains(t, rec.Start(context.Background()), "already running")
		require.Eventually(t, func() bool {
			return rec.Status() == model.StatusReconnecting
		}, 5*time.Second, 2*time.Millisecond)
		require.NoError(t, rec.Stop())
		require.Equal(t, model.StatusStopped, rec.Status())
	})

	t.Run("rtsp success records frames with audio", func(t *testing.T) {
		srv := newTestRTSPServerWithAudio(t)
		defer srv.close()

		store := newHXStore(t)
		rec := NewH264Recorder(H264Config{
			CameraID:     "cam-h264-audio",
			RTSPURL:      srv.rtspURL,
			SegmentDur:   time.Hour,
			AudioEnabled: true,
			RingBufCap:   64,
		}, store, metrics.NewMetrics())
		rec.Hub = model.NewStreamHub()
		rec.disconnectedAt = time.Now().Add(-2 * time.Second)
		rec.retryCount = 2
		rec.gapReason = "connection_lost"

		require.NoError(t, rec.Start(context.Background()))
		srv.waitPlay(t, 5*time.Second)
		srv.sendFrames(3, 10*time.Millisecond)
		srv.sendAudioFrame([]byte{0x01, 0x02, 0x03, 0x04})
		require.Eventually(t, func() bool {
			segments, _ := store.stats()
			return segments >= 1
		}, 5*time.Second, 5*time.Millisecond)
		require.NoError(t, rec.Stop())

		require.True(t, rec.hasPendingReconnect || rec.disconnectedAt.IsZero())
		require.GreaterOrEqual(t, store.statsClosed(), 1)
	})

	t.Run("rtsp frame watchdog reconnects a silent stream", func(t *testing.T) {
		srv := newTestRTSPServer(t)
		defer srv.close()

		rec := NewH264Recorder(H264Config{
			CameraID:             "cam-h264-rtsp-watchdog",
			RTSPURL:              srv.rtspURL,
			FrameWatchdogTimeout: 20 * time.Millisecond,
			RingBufCap:           16,
		}, newHXStore(t), metrics.NewMetrics())

		require.NoError(t, rec.Start(context.Background()))
		srv.waitPlay(t, 5*time.Second)
		// No frames are sent, so the watchdog must close the connection and the
		// run loop must go back to reconnecting.
		require.Eventually(t, func() bool {
			return rec.Status() == model.StatusReconnecting
		}, 10*time.Second, 5*time.Millisecond)
		require.NoError(t, rec.Stop())
	})
}

func TestCovH264_G711AudioTrack(t *testing.T) {
	srv := newHXG711Server(t, &format.H264{
		PayloadTyp:        96,
		PacketizationMode: 1,
		SPS:               testSPS,
		PPS:               testPPS,
	}, false) // A-law

	rec := NewH264Recorder(H264Config{
		CameraID:     "cam-h264-g711",
		RTSPURL:      srv.rtspURL,
		AudioEnabled: true,
		RingBufCap:   32,
	}, newHXStore(t), metrics.NewMetrics())
	rec.Hub = model.NewStreamHub()

	require.NoError(t, rec.Start(context.Background()))
	srv.waitPlay(t)
	srv.sendAudio(t, []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06})
	require.Eventually(t, func() bool {
		return rec.AudioCodec() == "g711"
	}, 5*time.Second, 5*time.Millisecond)
	require.False(t, rec.g711MULaw)
	require.Equal(t, 8000, rec.g711SampleRate)
	require.Len(t, rec.AudioMuxerConfig(), 5)
	require.NoError(t, rec.Stop())
}
