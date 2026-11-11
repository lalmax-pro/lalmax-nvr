package recorder

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

func TestCovMJPEG_DefaultsGettersAndLifecycle(t *testing.T) {
	store := newHJStore(t)
	m := metrics.NewMetrics()
	rec := NewMJPEGRecorder(MJPEGConfig{CameraID: "cam-mjpeg"}, store, m)
	require.Equal(t, DefaultSegmentDur, rec.cfg.SegmentDur)
	require.Equal(t, 1, rec.cfg.SampleInterval)
	require.Equal(t, DefaultMaxBackoff, rec.cfg.MaxBackoff)
	require.Equal(t, DefaultInitBackoff, rec.cfg.InitBackoff)
	require.Nil(t, rec.GetHub())
	rec.Hub = model.NewStreamHub()
	require.NotNil(t, rec.GetHub())
	require.Equal(t, model.StatusStopped, rec.Status())
	require.NoError(t, rec.Stop())

	// Metric helpers with and without a registry.
	rec.incActive()
	rec.decActive()
	rec.recordSegmentCreated()
	rec.recordBytes(10)
	rec.recordError("connection")
	plain := NewMJPEGRecorder(MJPEGConfig{CameraID: "cam-mjpeg-plain", SampleInterval: -1}, newHJStore(t))
	plain.incActive()
	plain.decActive()
	plain.recordSegmentCreated()
	plain.recordBytes(1)
	plain.recordError("connection")
	require.Equal(t, 1, plain.cfg.SampleInterval)
}

func TestCovMJPEG_StartTwiceAndConnectErrors(t *testing.T) {
	t.Run("start twice", func(t *testing.T) {
		srv := newMjpegTestServer(t)
		defer srv.close()
		rec := NewMJPEGRecorder(MJPEGConfig{
			CameraID:   "cam-mjpeg-twice",
			RTSPURL:    srv.rtspURL,
			SegmentDur: time.Hour,
		}, newHJStore(t))
		require.NoError(t, rec.Start(context.Background()))
		require.ErrorContains(t, rec.Start(context.Background()), "already running")
		require.NoError(t, rec.Stop())
		require.Equal(t, model.StatusStopped, rec.Status())
	})

	t.Run("invalid url", func(t *testing.T) {
		rec := NewMJPEGRecorder(MJPEGConfig{CameraID: "cam-mjpeg-badurl", RTSPURL: "://bad"}, newHJStore(t))
		require.NoError(t, rec.Start(context.Background()))
		require.Eventually(t, func() bool {
			return rec.Status() == model.StatusReconnecting
		}, 5*time.Second, 2*time.Millisecond)
		require.NoError(t, rec.Stop())
	})

	t.Run("unreachable server", func(t *testing.T) {
		rec := NewMJPEGRecorder(MJPEGConfig{
			CameraID: "cam-mjpeg-unreachable",
			RTSPURL:  "rtsp://127.0.0.1:1/live",
		}, newHJStore(t), metrics.NewMetrics())
		require.NoError(t, rec.Start(context.Background()))
		require.Eventually(t, func() bool {
			return rec.Status() == model.StatusReconnecting
		}, 5*time.Second, 2*time.Millisecond)
		require.NoError(t, rec.Stop())
	})

	t.Run("stream has no mjpeg media", func(t *testing.T) {
		srv := newTestRTSPServer(t) // H264 only
		defer srv.close()
		rec := NewMJPEGRecorder(MJPEGConfig{
			CameraID: "cam-mjpeg-nomedia",
			RTSPURL:  srv.rtspURL,
		}, newHJStore(t))
		require.NoError(t, rec.Start(context.Background()))
		require.Eventually(t, func() bool {
			return rec.Status() == model.StatusReconnecting
		}, 5*time.Second, 2*time.Millisecond)
		require.NoError(t, rec.Stop())
	})

	t.Run("segment creation fails", func(t *testing.T) {
		srv := newMjpegTestServer(t)
		defer srv.close()
		store := newHJStore(t)
		store.createErr = errors.New("disk full")
		rec := NewMJPEGRecorder(MJPEGConfig{
			CameraID: "cam-mjpeg-create",
			RTSPURL:  srv.rtspURL,
		}, store)
		require.NoError(t, rec.Start(context.Background()))
		srv.waitPlay(t, 5*time.Second)
		srv.sendFrames(3, 10*time.Millisecond)
		require.Eventually(t, func() bool {
			return atomic.LoadInt64(&rec.frameSeq) > 0
		}, 5*time.Second, 5*time.Millisecond)
		require.NoError(t, rec.Stop())
		frames, _, closed := store.snapshot()
		require.Zero(t, frames)
		require.Zero(t, closed)
	})
}

func TestCovMJPEG_WriteFramesEdgeCases(t *testing.T) {
	jpeg := generateTestJPEG()

	t.Run("empty and sampled frames", func(t *testing.T) {
		store := newHJStore(t)
		rec := NewMJPEGRecorder(MJPEGConfig{
			CameraID:       "cam-mjpeg-write",
			SampleInterval: 2,
			SegmentDur:     time.Nanosecond,
		}, store)
		rec.frameCh = make(chan []byte, 8)
		rec.frameCh <- nil
		rec.frameCh <- []byte{}
		rec.frameCh <- jpeg // sequence 1: skipped by sampling
		rec.frameCh <- jpeg // sequence 2: written, segment immediately due
		close(rec.frameCh)
		rec.writeFrames(make(chan struct{}))

		frames, _, closed := store.snapshot()
		require.Equal(t, 1, frames)
		require.Equal(t, 1, closed)
	})

	t.Run("store write error", func(t *testing.T) {
		store := newHJStore(t)
		store.writeErr = errors.New("write failed")
		rec := NewMJPEGRecorder(MJPEGConfig{CameraID: "cam-mjpeg-write-err", SegmentDur: time.Hour}, store)
		rec.frameCh = make(chan []byte, 2)
		rec.frameCh <- jpeg
		close(rec.frameCh)
		rec.writeFrames(make(chan struct{}))
		require.Zero(t, rec.frameCount)
	})
}

func TestCovMJPEG_CloseSegmentDBAndEvents(t *testing.T) {
	jpeg := generateTestJPEG()

	t.Run("no open segment", func(t *testing.T) {
		rec := NewMJPEGRecorder(MJPEGConfig{CameraID: "cam-mjpeg-idle"}, newHJStore(t))
		rec.closeCurrentSegment()
	})

	t.Run("records a segment and publishes events", func(t *testing.T) {
		store := newHJStore(t)
		db := &hjDB{}
		bus := event.NewEventBus(8)
		segCh := make(chan event.Event, 2)
		reconnCh := make(chan event.Event, 2)
		require.NoError(t, bus.Subscribe(event.TopicSegmentCompleted, segCh, 2))
		require.NoError(t, bus.Subscribe(event.TopicRecorderReconnected, reconnCh, 2))

		rec := NewMJPEGRecorder(MJPEGConfig{
			CameraID: "cam-mjpeg-close",
			DB:       db,
			EventBus: bus,
		}, store, metrics.NewMetrics())

		tempPath, finalPath, err := store.CreateSegment("cam-mjpeg-close", "mjpeg")
		require.NoError(t, err)
		_, err = store.WriteFrame(tempPath, jpeg)
		require.NoError(t, err)
		rec.curTempPath = tempPath
		rec.curFinalPath = finalPath
		rec.frameCount = 1
		rec.segStart = time.Now().Add(-time.Second)

		// First segment after recovery carries reconnect metadata.
		rec.disconnectedAt = time.Now().Add(-5 * time.Second)
		rec.reconnectTime = time.Now().Add(-4 * time.Second)
		rec.retryCount = 2
		rec.gapReason = "connection_lost"
		rec.hasPendingReconnect = true

		rec.closeCurrentSegment()
		require.Empty(t, rec.curTempPath)
		require.Empty(t, rec.curFinalPath)
		require.Zero(t, rec.frameCount)
		require.False(t, rec.hasPendingReconnect)
		require.Zero(t, rec.retryCount)

		calls, recs := db.snapshot()
		require.Equal(t, 1, calls)
		require.Len(t, recs, 1)
		require.Equal(t, model.FormatMJPEG, recs[0].Format)
		require.Equal(t, 1, recs[0].FrameCount)
		require.Greater(t, recs[0].FileSize, int64(0))
		require.Equal(t, "connection_lost", recs[0].GapReason)
		require.False(t, recs[0].ReconnectedAt.IsZero())

		select {
		case <-segCh:
		case <-time.After(time.Second):
			t.Fatal("expected segment.completed event")
		}
		select {
		case <-reconnCh:
		case <-time.After(time.Second):
			t.Fatal("expected recorder.reconnected event")
		}
	})

	t.Run("db insert error and close error", func(t *testing.T) {
		store := newHJStore(t)
		store.closeErr = errors.New("rename failed")
		db := &hjDB{err: errors.New("db down")}
		rec := NewMJPEGRecorder(MJPEGConfig{
			CameraID: "cam-mjpeg-errors",
			DB:       db,
			EventBus: event.NewEventBus(4),
		}, store, metrics.NewMetrics())
		rec.curTempPath = "/tmp/mjpeg-tmp"
		rec.curFinalPath = "/tmp/mjpeg-final"
		rec.frameCount = 2
		rec.segStart = time.Now().Add(-time.Second)
		rec.closeCurrentSegment()

		calls, recs := db.snapshot()
		require.Equal(t, 1, calls)
		require.Empty(t, recs)
		require.Zero(t, rec.frameCount)
	})

	t.Run("no db and no recording id", func(t *testing.T) {
		rec := NewMJPEGRecorder(MJPEGConfig{
			CameraID: "cam-mjpeg-nodb",
			EventBus: event.NewEventBus(4),
		}, newHJStore(t))
		rec.curTempPath = "/tmp/mjpeg-tmp"
		rec.curFinalPath = "/tmp/mjpeg-final"
		rec.frameCount = 1
		rec.hasPendingReconnect = true
		rec.disconnectedAt = time.Now()
		rec.reconnectTime = time.Now()
		rec.closeCurrentSegment()
		// Without a DB there is no recording ID, so no events are published and
		// the pending reconnect state is kept for the next segment.
		require.True(t, rec.hasPendingReconnect)
	})
}

func TestCovMJPEG_RecordsAndReconnectsMetadata(t *testing.T) {
	srv := newMjpegTestServer(t)
	defer srv.close()

	store := newHJStore(t)
	db := &hjDB{}
	bus := event.NewEventBus(8)
	segCh := make(chan event.Event, 4)
	require.NoError(t, bus.Subscribe(event.TopicSegmentCompleted, segCh, 4))

	rec := NewMJPEGRecorder(MJPEGConfig{
		CameraID:   "cam-mjpeg-live",
		RTSPURL:    srv.rtspURL,
		SegmentDur: time.Hour,
		DB:         db,
		EventBus:   bus,
	}, store, metrics.NewMetrics())
	rec.Hub = model.NewStreamHub()
	rec.disconnectedAt = time.Now().Add(-2 * time.Second)
	rec.retryCount = 1
	rec.gapReason = "connection_lost"

	require.NoError(t, rec.Start(context.Background()))
	srv.waitPlay(t, 5*time.Second)
	srv.sendFrames(2, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		frames, _, _ := store.snapshot()
		return frames >= 1
	}, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, rec.Stop())

	calls, recs := db.snapshot()
	require.Equal(t, 1, calls)
	require.Len(t, recs, 1)
	require.False(t, recs[0].ReconnectedAt.IsZero())
	require.Equal(t, "connection_lost", recs[0].GapReason)

	select {
	case <-segCh:
	case <-time.After(time.Second):
		t.Fatal("expected segment.completed event")
	}
}
