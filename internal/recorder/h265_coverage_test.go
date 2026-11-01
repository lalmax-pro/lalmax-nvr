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

// h265 test NALU helpers (slice bodies only feed sample tables).
var (
	hx265Trail = []byte{1 << 1, 1, 0x80, 0x33}
	hx265RASL  = []byte{8 << 1, 1, 0x80, 0x22}
	hx265CRA   = []byte{21 << 1, 1, 0x80, 0x11}
)

func TestCovH265_GettersDefaultsAndMetrics(t *testing.T) {
	store := newHXStore(t)
	m := metrics.NewMetrics()
	rec := NewH265Recorder(H265Config{
		CameraID:     "cam-h265-get",
		StreamID:     "stream-h265",
		RTSPURL:      "rtsp://127.0.0.1:1/live",
		AudioEnabled: true,
	}, store, m)

	require.Equal(t, DefaultSegmentDur, rec.cfg.SegmentDur)
	require.Equal(t, DefaultRingBufCap, rec.cfg.RingBufCap)
	require.Equal(t, DefaultMaxBackoff, rec.cfg.MaxBackoff)
	require.Equal(t, DefaultInitBackoff, rec.cfg.InitBackoff)
	require.Equal(t, defaultFrameWatchdogTimeout, rec.cfg.FrameWatchdogTimeout)
	require.NotNil(t, rec.preroll)

	require.Nil(t, rec.GetHub())
	rec.Hub = model.NewStreamHub()
	require.NotNil(t, rec.GetHub())

	require.Nil(t, rec.VPS())
	require.Nil(t, rec.SPS())
	require.Nil(t, rec.PPS())
	rec.vps = []byte{0x40, 0x01}
	rec.sps = []byte{0x42, 0x01}
	rec.pps = []byte{0x44, 0x01}
	require.Equal(t, []byte{0x40, 0x01}, rec.VPS())
	require.Equal(t, []byte{0x42, 0x01}, rec.SPS())
	require.Equal(t, []byte{0x44, 0x01}, rec.PPS())

	require.True(t, rec.AudioEnabled())
	require.Empty(t, rec.AudioCodec())
	require.Nil(t, rec.AudioMuxerConfig())
	rec.audioCodec = "aac"
	rec.audioMuxerConfig = []byte{0x11, 0x90}
	require.Equal(t, "aac", rec.AudioCodec())
	require.Equal(t, []byte{0x11, 0x90}, rec.AudioMuxerConfig())
	require.Equal(t, "rtsp://127.0.0.1:1/live", rec.SourceURL())

	// Metrics helpers with a non-nil metrics registry.
	rec.incActive()
	rec.decActive()
	rec.recordSegmentCreated()
	rec.recordBytes(123)
	rec.recordError("connection")

	// Metrics helpers must also be safe without metrics.
	plain := NewH265Recorder(H265Config{CameraID: "cam-h265-plain"}, newHXStore(t))
	plain.incActive()
	plain.decActive()
	plain.recordSegmentCreated()
	plain.recordBytes(1)
	plain.recordError("connection")
	require.False(t, plain.AudioEnabled())
	require.Nil(t, plain.GetHub())

	// Stop before Start must not block.
	require.NoError(t, plain.Stop())
}

func TestCovH265_PauseResumeAndAdaptive(t *testing.T) {
	store := newHXStore(t)
	rec := NewH265Recorder(H265Config{
		CameraID: "cam-h265-pause",
		Adaptive: NewAdaptiveGate(time.Hour),
	}, store)

	require.False(t, rec.IsPaused())
	// Pause without an active muxer.
	rec.Pause()
	require.True(t, rec.IsPaused())
	require.Equal(t, model.StatusPaused, rec.Status())

	// While paused frames are consumed for the preroll ring only.
	hxFeedH265(rec, testVPS265, testSPS265, testPPS265, testIDR265, hx265Trail)
	require.Zero(t, rec.frameCount)
	require.Nil(t, rec.muxer)

	// Resume drains the preroll ring into a fresh frame channel.
	hxFeedH265(rec, testIDR265)
	rec.frameCh = make(chan []byte, 16)
	rec.Resume()
	require.False(t, rec.IsPaused())
	require.Equal(t, model.StatusRecording, rec.Status())
	require.NotEmpty(t, len(rec.frameCh), "preroll should hand frames back after resume")

	// Pause with an active muxer finalizes the segment.
	hxFeedH265(rec, testVPS265, testSPS265, testPPS265, testIDR265)
	require.NotNil(t, rec.muxer)
	rec.Pause()
	require.Nil(t, rec.muxer)
	segments, closed := store.stats()
	require.Equal(t, 1, segments)
	require.Equal(t, 1, closed)

	// Adaptive triggering is nil-safe and works with a gate.
	rec.TriggerAdaptive(2 * time.Minute)
	rec.cfg.Adaptive = nil
	rec.TriggerAdaptive(0)
	require.True(t, rec.IsPaused())
}

func TestCovH265_WriteFramesBranches(t *testing.T) {
	store := newHXStore(t)
	rec := NewH265Recorder(H265Config{
		CameraID:   "cam-h265-write",
		SegmentDur: time.Nanosecond, // every segment is immediately due to rotate
	}, store)

	// Parameter sets are cached but never written as samples.
	hxFeedH265(rec, testVPS265, testSPS265, testPPS265)
	require.Nil(t, rec.muxer)
	require.Equal(t, testVPS265, rec.vps)
	require.Equal(t, testSPS265, rec.sps)
	require.Equal(t, testPPS265, rec.pps)

	// Too-short NALUs and non-random-access NALUs before the first segment are skipped.
	hxFeedH265(rec, []byte{0x01}, hx265Trail)
	require.Nil(t, rec.muxer)
	require.Zero(t, rec.frameCount)

	// A CRA starts the segment; its leading RASL picture is dropped.
	hxFeedH265(rec, hx265CRA, hx265RASL, hx265Trail)
	require.NotNil(t, rec.muxer)
	require.Equal(t, 2, rec.frameCount)
	require.True(t, rec.pendingRotate, "segment duration already elapsed")

	// Rotation happens at the next random access picture.
	hxFeedH265(rec, hx265CRA, hx265RASL, hx265Trail)
	require.Equal(t, 2, rec.frameCount)
	require.Equal(t, 1, store.statsClosed(), "previous segment was rotated at the CRA")
	rec.closeCurrentSegment()
	require.Equal(t, 2, store.statsClosed())
}

// statsClosed returns the number of finalized segments.
func (s *hxStore) statsClosed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func TestCovH265_ParameterSetChangesRotate(t *testing.T) {
	store := newHXStore(t)
	rec := NewH265Recorder(H265Config{CameraID: "cam-h265-ps", SegmentDur: time.Hour}, store)

	hxFeedH265(rec, testVPS265, testSPS265, testPPS265, hx265CRA)
	require.NotNil(t, rec.muxer)
	require.Equal(t, 1, rec.frameCount)

	// Repeating the same parameter sets must not rotate.
	hxFeedH265(rec, testVPS265, testSPS265, testPPS265)
	require.NotNil(t, rec.muxer)

	changedVPS := append([]byte(nil), testVPS265...)
	changedVPS[len(changedVPS)-1] ^= 0xFF
	hxFeedH265(rec, changedVPS)
	require.Nil(t, rec.muxer, "VPS change rotates the segment")

	changedSPS := append([]byte(nil), testSPS265...)
	changedSPS[len(changedSPS)-1] ^= 0xFF
	hxFeedH265(rec, changedSPS) // rotate branch with no open muxer
	require.Equal(t, changedSPS, rec.sps)

	changedPPS := append([]byte(nil), testPPS265...)
	changedPPS[len(changedPPS)-1] ^= 0xFF
	hxFeedH265(rec, changedPPS)
	require.Equal(t, changedPPS, rec.pps)

	// A new segment starts at the next random access picture with the new sets.
	hxFeedH265(rec, hx265CRA)
	require.NotNil(t, rec.muxer)
	require.Equal(t, 1, store.statsClosed())
}

func TestCovH265_AudioTrackAndStoreErrors(t *testing.T) {
	t.Run("store create error", func(t *testing.T) {
		store := newHXStore(t)
		store.createErr = errors.New("disk full")
		rec := NewH265Recorder(H265Config{CameraID: "cam-h265-create"}, store)
		hxFeedH265(rec, testVPS265, testSPS265, testPPS265, hx265CRA)
		require.Nil(t, rec.muxer)
		require.Zero(t, rec.frameCount)
	})

	t.Run("unsupported audio codec keeps video", func(t *testing.T) {
		store := newHXStore(t)
		rec := NewH265Recorder(H265Config{CameraID: "cam-h265-audio-err"}, store)
		rec.audioCodec = "bogus"
		rec.audioMuxerConfig = []byte{0x01, 0x02, 0x03}
		hxFeedH265(rec, testVPS265, testSPS265, testPPS265, hx265CRA)
		require.NotNil(t, rec.muxer)
		require.Zero(t, rec.audioTrackID, "audio track must be skipped on error")
		require.Equal(t, 1, rec.frameCount)
		rec.closeCurrentSegment()
	})

	t.Run("write sample error is survived", func(t *testing.T) {
		store := newHXStore(t)
		store.tempAsDir = true
		rec := NewH265Recorder(H265Config{CameraID: "cam-h265-write-err"}, store)
		hxFeedH265(rec, testVPS265, testSPS265, testPPS265, hx265CRA)
		require.NotNil(t, rec.muxer)
		require.Zero(t, rec.frameCount, "failed samples are not counted")
		// Finalizing a muxer whose file could not be opened reports the error and
		// cleans up the temporary path.
		rec.closeCurrentSegment()
		require.Nil(t, rec.muxer)
	})
}

func TestCovH265_CloseSegmentPublishesEvents(t *testing.T) {
	store := newHXStore(t)
	db := &hjDB{}
	bus := event.NewEventBus(16)
	segCh := make(chan event.Event, 4)
	reconnCh := make(chan event.Event, 4)
	require.NoError(t, bus.Subscribe(event.TopicSegmentCompleted, segCh, 4))
	require.NoError(t, bus.Subscribe(event.TopicRecorderReconnected, reconnCh, 4))

	rec := NewH265Recorder(H265Config{
		CameraID:   "cam-h265-close",
		StreamID:   "stream-h265",
		SegmentDur: time.Hour,
		DB:         db,
		EventBus:   bus,
	}, store, metrics.NewMetrics())

	hxFeedH265(rec, testVPS265, testSPS265, testPPS265, hx265CRA, hx265Trail)
	require.NotNil(t, rec.muxer)
	require.Equal(t, 2, rec.frameCount)

	// Simulate a completed reconnection before the segment is finalized.
	rec.disconnectedAt = time.Now().Add(-3 * time.Second)
	rec.reconnectTime = time.Now().Add(-2 * time.Second)
	rec.retryCount = 4
	rec.gapReason = "connection_lost"
	rec.hasPendingReconnect = true

	rec.closeCurrentSegment()
	require.Nil(t, rec.muxer)
	require.False(t, rec.hasPendingReconnect, "reconnect metadata is consumed once")
	require.Zero(t, rec.retryCount)
	require.Empty(t, rec.gapReason)

	calls, recs := db.snapshot()
	require.Equal(t, 1, calls)
	require.Len(t, recs, 1)
	require.Equal(t, model.FormatH265, recs[0].Format)
	require.Equal(t, "stream-h265", recs[0].StreamID)
	require.Equal(t, 2, recs[0].FrameCount)
	require.Greater(t, recs[0].FileSize, int64(0))
	require.False(t, recs[0].ReconnectedAt.IsZero())
	require.Equal(t, "connection_lost", recs[0].GapReason)

	select {
	case ev := <-segCh:
		sc, ok := ev.Data.(event.SegmentCompleted)
		require.True(t, ok)
		require.Equal(t, "cam-h265-close", sc.CameraID)
		require.Equal(t, recs[0].ID, sc.RecordingID)
	case <-time.After(time.Second):
		t.Fatal("expected segment.completed event")
	}
	select {
	case ev := <-reconnCh:
		rc, ok := ev.Data.(event.RecorderReconnected)
		require.True(t, ok)
		require.Equal(t, "cam-h265-close", rc.CameraID)
		require.Equal(t, "connection_lost", rc.GapReason)
		require.Equal(t, 4, rc.RetryCount)
	case <-time.After(time.Second):
		t.Fatal("expected recorder.reconnected event")
	}

	// A second close is a no-op.
	rec.closeCurrentSegment()
	_, recs2 := db.snapshot()
	require.Len(t, recs2, 1)
}

func TestCovH265_CloseSegmentStoreAndMuxerErrors(t *testing.T) {
	t.Run("store close error still inserts recording", func(t *testing.T) {
		store := newHXStore(t)
		store.closeErr = errors.New("rename failed")
		db := &hjDB{}
		rec := NewH265Recorder(H265Config{CameraID: "cam-h265-store-err", DB: db}, store, metrics.NewMetrics())
		hxFeedH265(rec, testVPS265, testSPS265, testPPS265, hx265CRA)
		require.NotNil(t, rec.muxer)
		rec.closeCurrentSegment()
		calls, recs := db.snapshot()
		require.Equal(t, 1, calls)
		require.Len(t, recs, 1)
		require.Zero(t, recs[0].FileSize, "final file never appeared")
	})

	t.Run("muxer close error cleans state", func(t *testing.T) {
		badDir := t.TempDir()
		m := muxer.NewMP4Muxer(badDir) // opening a directory as an MP4 file fails
		trackID, err := m.AddH265Track(testVPS265, testSPS265, testPPS265)
		require.NoError(t, err)

		rec := NewH265Recorder(H265Config{CameraID: "cam-h265-mux-err"}, newHXStore(t), metrics.NewMetrics())
		rec.muxer = m
		rec.trackID = trackID
		rec.curTempPath = badDir
		rec.curFinalPath = filepath.Join(t.TempDir(), "final.mp4")
		rec.frameCount = 1
		rec.segStart = time.Now().Add(-time.Second)

		rec.closeCurrentSegment()
		require.Nil(t, rec.muxer)
		require.Zero(t, rec.audioTrackID)
		require.Empty(t, rec.curTempPath)
		require.Empty(t, rec.curFinalPath)
		require.Zero(t, rec.frameCount)
		_, err = os.Stat(badDir)
		require.True(t, os.IsNotExist(err), "temporary directory is removed on muxer failure")
	})
}

func TestCovH265_ConsumeVideoFrames(t *testing.T) {
	newSub := newHXSub

	t.Run("video audio and filtering", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{CameraID: "cam-h265-consume", AudioEnabled: true}, newHXStore(t), metrics.NewMetrics())
		rec.frameCh = make(chan []byte, 8)
		rec.Hub = model.NewStreamHub()
		sub := newSub()
		alive := make(chan struct{}, 1)
		connected := false

		// Filtered frames.
		sub.ch <- media.MediaFrame{Kind: media.FrameVideo, Data: []byte{1, 2, 3}}                   // too short
		sub.ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h264", Data: annexB(testIDR265)} // wrong codec
		sub.ch <- media.MediaFrame{Kind: media.FrameAudio, Codec: "aac", IsSeqHeader: true, Data: []byte{0x11, 0x90}}
		sub.ch <- hxAudioFrame("aac", []byte{0x01, 0x02, 0x03})
		sub.ch <- hxAudioFrame("opus", []byte{0x05}) // unsupported codec ignored
		// Real video frame.
		sub.ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h265", IsKey: true, PTS: time.Second, Data: annexB(testIDR265)}
		close(sub.ch)

		err := rec.consumeVideoFrames(context.Background(), sub, alive, &connected)
		require.ErrorContains(t, err, "frame subscription ended")
		require.True(t, connected)
		require.Equal(t, "aac", rec.audioCodec)
		require.Equal(t, []byte{0x11, 0x90}, rec.audioMuxerConfig)
		require.Equal(t, 1, len(rec.frameCh), "only the video frame reaches the writer")
		select {
		case <-alive:
		default:
			t.Fatal("expected the frame watchdog to be signalled")
		}
	})

	t.Run("g711 audio frames are configured from the stream", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{CameraID: "cam-h265-consume-g711", AudioEnabled: true}, newHXStore(t))
		rec.frameCh = make(chan []byte, 4)
		sub := newSub()
		sub.ch <- hxAudioFrame("g711u", []byte{0x01, 0x02})
		close(sub.ch)
		connected := false
		err := rec.consumeVideoFrames(context.Background(), sub, make(chan struct{}, 1), &connected)
		require.Error(t, err)
		require.Equal(t, "g711", rec.audioCodec)
		require.True(t, rec.g711MULaw)
		require.Equal(t, 8000, rec.g711SampleRate)
		require.Len(t, rec.audioMuxerConfig, 5)
	})

	t.Run("g711a audio frames default to a-law", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{CameraID: "cam-h265-consume-g711a", AudioEnabled: true}, newHXStore(t))
		rec.frameCh = make(chan []byte, 4)
		sub := newSub()
		sub.ch <- hxAudioFrame("g711a", []byte{0x03, 0x04})
		close(sub.ch)
		connected := false
		err := rec.consumeVideoFrames(context.Background(), sub, make(chan struct{}, 1), &connected)
		require.Error(t, err)
		require.Equal(t, "g711", rec.audioCodec)
		require.False(t, rec.g711MULaw)
	})

	t.Run("audio disabled drops audio frames", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{CameraID: "cam-h265-consume-noaudio"}, newHXStore(t))
		rec.frameCh = make(chan []byte, 4)
		sub := newSub()
		sub.ch <- hxAudioFrame("aac", []byte{0x01})
		close(sub.ch)
		connected := false
		err := rec.consumeVideoFrames(context.Background(), sub, make(chan struct{}, 1), &connected)
		require.ErrorContains(t, err, "ended before first frame")
		require.False(t, connected)
		require.Empty(t, rec.audioCodec)
	})

	t.Run("ring buffer overflow drops frames", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{CameraID: "cam-h265-overflow"}, newHXStore(t), metrics.NewMetrics())
		rec.frameCh = make(chan []byte) // unbuffered, nobody reading
		sub := newSub()
		for i := 0; i < 3; i++ {
			sub.ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h265", Data: annexB(testIDR265)}
		}
		close(sub.ch)
		connected := false
		err := rec.consumeVideoFrames(context.Background(), sub, make(chan struct{}, 1), &connected)
		require.Error(t, err)
		require.Equal(t, int64(3), rec.dropped.Load())
	})

	t.Run("subscription error is returned", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{CameraID: "cam-h265-consume-err"}, newHXStore(t))
		rec.frameCh = make(chan []byte, 1)
		sub := newSub()
		sub.errc <- errors.New("subscription broken")
		connected := false
		err := rec.consumeVideoFrames(context.Background(), sub, make(chan struct{}, 1), &connected)
		require.ErrorContains(t, err, "subscription broken")
	})

	t.Run("context cancellation stops the loop", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{CameraID: "cam-h265-consume-ctx"}, newHXStore(t))
		rec.frameCh = make(chan []byte, 1)
		sub := newSub()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		connected := false
		err := rec.consumeVideoFrames(ctx, sub, make(chan struct{}, 1), &connected)
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestCovH265_FrameSourcePaths(t *testing.T) {
	t.Run("frame source error", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{
			CameraID: "cam-h265-fs-err",
			FrameSource: func(context.Context) (media.FrameSubscription, error) {
				return nil, errors.New("no in-process frames")
			},
		}, newHXStore(t))
		err, connected := rec.connectAndRecordFrames(context.Background())
		require.ErrorContains(t, err, "no in-process frames")
		require.False(t, connected)
	})

	t.Run("subscription ends before first frame", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{
			CameraID: "cam-h265-fs-empty",
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
		rec := NewH265Recorder(H265Config{
			CameraID: "cam-h265-fs-ok",
			FrameSource: func(context.Context) (media.FrameSubscription, error) {
				return &hxSub{ch: ch, errc: make(chan error)}, nil
			},
		}, store)
		ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h265", IsKey: true, Data: annexB(testVPS265)}
		ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h265", IsKey: true, Data: annexB(testSPS265)}
		ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h265", IsKey: true, Data: annexB(testPPS265)}
		ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h265", IsKey: true, Data: annexB(testIDR265)}
		close(ch)

		err, connected := rec.connectAndRecordFrames(context.Background())
		require.ErrorContains(t, err, "frame subscription ended")
		require.True(t, connected)
		require.Nil(t, rec.muxer, "segment is finalized when the source ends")
		require.Equal(t, 1, store.statsClosed())
	})

	t.Run("context cancellation stops the frame loop", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{
			CameraID: "cam-h265-fs-ctx",
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

func TestCovH265_ConnectAndRecordFallbacks(t *testing.T) {
	t.Run("unsupported frames without RTSP URL returns error", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{
			CameraID: "cam-h265-nofallback",
			FrameSource: func(context.Context) (media.FrameSubscription, error) {
				return nil, media.ErrFramesNotSupported
			},
		}, newHXStore(t))
		err, connected := rec.connectAndRecord(context.Background())
		require.ErrorIs(t, err, media.ErrFramesNotSupported)
		require.False(t, connected)
	})

	t.Run("unsupported frames falls back to rtsp", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{
			CameraID: "cam-h265-fallback",
			RTSPURL:  "://not-a-url",
			FrameSource: func(context.Context) (media.FrameSubscription, error) {
				return nil, media.ErrFramesNotSupported
			},
		}, newHXStore(t))
		err, connected := rec.connectAndRecord(context.Background())
		require.ErrorContains(t, err, "invalid RTSP URL")
		require.False(t, connected)
	})

	t.Run("frame source failure without RTSP URL", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{
			CameraID: "cam-h265-fs-only",
			FrameSource: func(context.Context) (media.FrameSubscription, error) {
				return nil, errors.New("source unavailable")
			},
		}, newHXStore(t))
		err, connected := rec.connectAndRecord(context.Background())
		require.ErrorContains(t, err, "source unavailable")
		require.False(t, connected)
	})
}

func TestCovH265_ConnectAndRecordRTSPErrors(t *testing.T) {
	t.Run("invalid url", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{CameraID: "cam-h265-badurl", RTSPURL: "://bad"}, newHXStore(t))
		err, connected := rec.connectAndRecordRTSP(context.Background())
		require.ErrorContains(t, err, "invalid RTSP URL")
		require.False(t, connected)
	})

	t.Run("unreachable server", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{CameraID: "cam-h265-unreachable", RTSPURL: "rtsp://127.0.0.1:1/live"}, newHXStore(t))
		err, connected := rec.connectAndRecordRTSP(context.Background())
		require.ErrorContains(t, err, "DESCRIBE")
		require.False(t, connected)
	})

	t.Run("no h265 media in stream", func(t *testing.T) {
		srv := newTestRTSPServer(t) // H264-only in-process server
		defer srv.close()
		rec := NewH265Recorder(H265Config{
			CameraID: "cam-h265-nomedia",
			RTSPURL:  srv.rtspURL,
			Username: "admin",
			Password: "pass",
		}, newHXStore(t))
		err, connected := rec.connectAndRecordRTSP(context.Background())
		require.ErrorContains(t, err, "H265 media not found")
		require.False(t, connected)
	})
}

func TestCovH265_RunLoopAndReconnectMetadata(t *testing.T) {
	t.Run("run retries and reports reconnecting", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{
			CameraID: "cam-h265-run",
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
		srv := newTestRTSPServerH265WithAudio(t)
		defer srv.close()

		store := newHXStore(t)
		rec := NewH265Recorder(H265Config{
			CameraID:     "cam-h265-audio",
			RTSPURL:      srv.rtspURL,
			SegmentDur:   time.Hour,
			AudioEnabled: true,
			RingBufCap:   64,
		}, store, metrics.NewMetrics())
		rec.Hub = model.NewStreamHub()
		// A previous disconnect must be reported on the recovered segment.
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
}

func TestCovH265_MoreWriteFramesBranches(t *testing.T) {
	t.Run("adaptive gate refuses sparse frames", func(t *testing.T) {
		store := newHXStore(t)
		rec := NewH265Recorder(H265Config{
			CameraID: "cam-h265-adaptive",
			Adaptive: NewAdaptiveGate(time.Hour),
		}, store)
		hxFeedH265(rec, testVPS265, testSPS265, testPPS265, hx265CRA)
		require.Equal(t, 1, rec.frameCount)
		// P-frames are only observed, never written, in the calm state.
		hxFeedH265(rec, hx265Trail)
		require.Equal(t, 1, rec.frameCount)
		// A second IDR inside the sparse interval is also refused.
		hxFeedH265(rec, hx265CRA)
		require.Equal(t, 1, rec.frameCount)
		rec.closeCurrentSegment()
	})

	t.Run("random access without parameter sets is skipped", func(t *testing.T) {
		store := newHXStore(t)
		rec := NewH265Recorder(H265Config{CameraID: "cam-h265-nops"}, store)
		hxFeedH265(rec, hx265CRA)
		require.Nil(t, rec.muxer)
		require.Zero(t, rec.frameCount)
		_, closed := store.stats()
		require.Zero(t, closed)
	})

	t.Run("db insert error is logged", func(t *testing.T) {
		store := newHXStore(t)
		db := &hjDB{err: errors.New("db down")}
		rec := NewH265Recorder(H265Config{CameraID: "cam-h265-db-err", DB: db}, store, metrics.NewMetrics())
		hxFeedH265(rec, testVPS265, testSPS265, testPPS265, hx265CRA)
		require.NotNil(t, rec.muxer)
		rec.closeCurrentSegment()
		require.Nil(t, rec.muxer)
		calls, recs := db.snapshot()
		require.Equal(t, 1, calls)
		require.Empty(t, recs)
	})
}

func TestCovH265_FrameSourceReconnectAndWatchdog(t *testing.T) {
	t.Run("reconnect metadata set when frames recover", func(t *testing.T) {
		store := newHXStore(t)
		ch := make(chan media.MediaFrame, 4)
		rec := NewH265Recorder(H265Config{
			CameraID: "cam-h265-fs-reconnect",
			FrameSource: func(context.Context) (media.FrameSubscription, error) {
				return &hxSub{ch: ch, errc: make(chan error)}, nil
			},
		}, store)
		rec.disconnectedAt = time.Now().Add(-time.Second)
		rec.retryCount = 2
		ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h265", IsKey: true, Data: annexB(testVPS265)}
		ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h265", IsKey: true, Data: annexB(testSPS265)}
		ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h265", IsKey: true, Data: annexB(testPPS265)}
		ch <- media.MediaFrame{Kind: media.FrameVideo, Codec: "h265", IsKey: true, Data: annexB(testIDR265)}
		close(ch)

		err, connected := rec.connectAndRecordFrames(context.Background())
		require.Error(t, err)
		require.True(t, connected)
		require.True(t, rec.hasPendingReconnect, "recovery is reported on the next segment")
		require.False(t, rec.reconnectTime.IsZero())
	})

	t.Run("frame watchdog closes a silent subscription", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{
			CameraID:             "cam-h265-fs-watchdog",
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

	t.Run("generic source error with rtsp configured is returned", func(t *testing.T) {
		rec := NewH265Recorder(H265Config{
			CameraID: "cam-h265-fs-generic",
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

func TestCovH265_G711AudioTrack(t *testing.T) {
	srv := newHXG711Server(t, &format.H265{
		PayloadTyp: 96,
		VPS:        testVPS265,
		SPS:        testSPS265,
		PPS:        testPPS265,
	}, true)

	rec := NewH265Recorder(H265Config{
		CameraID:     "cam-h265-g711",
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
	require.True(t, rec.g711MULaw)
	require.Equal(t, 8000, rec.g711SampleRate)
	require.Len(t, rec.AudioMuxerConfig(), 5)
	require.NoError(t, rec.Stop())
}
