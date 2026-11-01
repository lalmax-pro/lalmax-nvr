package recorder

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

// --- fakes for the HTTP JPEG recorder ---

// hjStore is an in-memory/temp-dir SegmentStore used by the HTTP JPEG tests.
// Each segment is a directory of JPEG files, mirroring storage.Manager.
type hjStore struct {
	mu sync.Mutex

	dir            string
	seq            int
	frames         int
	totalBytes     int
	segmentsClosed int

	createErr error
	writeErr  error
	closeErr  error
}

func newHJStore(t *testing.T) *hjStore {
	t.Helper()
	return &hjStore{dir: t.TempDir()}
}

func (s *hjStore) CreateSegment(cameraID string, _ string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return "", "", s.createErr
	}
	s.seq++
	base := filepath.Join(s.dir, fmt.Sprintf("%s-seg-%03d", cameraID, s.seq))
	tmp := base + "-tmp"
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", "", err
	}
	return tmp, base, nil
}

func (s *hjStore) WriteFrame(tempPath string, data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	s.frames++
	name := filepath.Join(tempPath, fmt.Sprintf("frame-%04d.jpg", s.frames))
	if err := os.WriteFile(name, data, 0o644); err != nil {
		return 0, err
	}
	s.totalBytes += len(data)
	return len(data), nil
}

func (s *hjStore) CloseSegment(tempPath, finalPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeErr != nil {
		return s.closeErr
	}
	s.segmentsClosed++
	return os.Rename(tempPath, finalPath)
}

func (s *hjStore) snapshot() (frames, totalBytes, closed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frames, s.totalBytes, s.segmentsClosed
}

// hjDB is a RecordingDB that records inserted rows in memory.
type hjDB struct {
	mu    sync.Mutex
	err   error
	calls int
	recs  []*model.Recording
}

func (d *hjDB) InsertRecording(_ context.Context, r *model.Recording) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.err != nil {
		return d.err
	}
	cp := *r
	d.recs = append(d.recs, &cp)
	return nil
}

func (d *hjDB) InsertRecordingWithRetry(_ context.Context, r *model.Recording, _ int, _ time.Duration) error {
	return d.InsertRecording(context.Background(), r)
}

func (d *hjDB) snapshot() (calls int, recs []*model.Recording) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls, append([]*model.Recording(nil), d.recs...)
}

// hjPanicTransport panics inside http.Client.Do to exercise the panic recovery.
type hjPanicTransport struct{}

func (hjPanicTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("boom in round trip")
}

// hjWriteMultipartFrame writes one MJPEG part. When withLength is false the
// Content-Length header is omitted, forcing the read-until-boundary parser.
func hjWriteMultipartFrame(w io.Writer, boundary string, data []byte, withLength bool) {
	if withLength {
		fmt.Fprintf(w, "--%s\r\n", boundary)
		fmt.Fprintf(w, "Content-Type: image/jpeg\r\n")
		fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(data))
		_, _ = w.Write(data)
		_, _ = io.WriteString(w, "\r\n")
		return
	}
	fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\n\r\n", boundary)
	_, _ = w.Write(data)
	_, _ = io.WriteString(w, "\r\n")
}

// hjStreamServer returns an httptest MJPEG server streaming frames with
// Content-Length headers and keeping the connection open until done is closed.
func hjStreamServer(t *testing.T, boundary string, jpeg []byte, frameCount int) (*httptest.Server, chan struct{}, *atomic.Int32) {
	t.Helper()
	done := make(chan struct{})
	var reqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		w.Header().Set("Content-Type", "multipart/x-mixed-replace;boundary="+boundary)
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < frameCount; i++ {
			hjWriteMultipartFrame(w, boundary, jpeg, true)
			if flusher != nil {
				flusher.Flush()
			}
		}
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(done)
		srv.Close()
	})
	return srv, done, &reqs
}

// --- constructor / getters / lifecycle ---

func TestCovHTTPJPEG_DefaultsAndGetters(t *testing.T) {
	r := NewHTTPJPEGRecorder(HTTPJPEGConfig{CameraID: "cam-defaults", URL: "http://127.0.0.1:1/stream"}, newHJStore(t))
	require.Equal(t, DefaultSegmentDur, r.cfg.SegmentDur)
	require.Equal(t, DefaultMaxBackoff, r.cfg.MaxBackoff)
	require.Equal(t, DefaultInitBackoff, r.cfg.InitBackoff)
	require.Nil(t, r.GetHub())
	require.Nil(t, r.Hub)
	require.Equal(t, model.StatusStopped, r.Status())
	require.NotNil(t, r.client)
	require.Equal(t, time.Duration(0), r.client.Timeout)

	// Stop on a never-started recorder must be a no-op.
	require.NoError(t, r.Stop())
	require.Equal(t, model.StatusStopped, r.Status())

	r.setStatus(model.StatusReconnecting)
	require.Equal(t, model.StatusReconnecting, r.Status())
	r.setStatus(model.StatusStopped)
}

func TestCovHTTPJPEG_RecordsMultipartFrames(t *testing.T) {
	const boundary = "cov-boundary"
	jpeg := generateTestJPEG()
	store := newHJStore(t)
	db := &hjDB{}
	bus := event.NewEventBus(64)
	segCh := make(chan event.Event, 8)
	reconnCh := make(chan event.Event, 8)
	require.NoError(t, bus.Subscribe(event.TopicSegmentCompleted, segCh, 8))
	require.NoError(t, bus.Subscribe(event.TopicRecorderReconnected, reconnCh, 8))

	srv, _, reqs := hjStreamServer(t, boundary, jpeg, 3)

	r := NewHTTPJPEGRecorder(HTTPJPEGConfig{
		CameraID:   "cam-http",
		URL:        srv.URL,
		SegmentDur: time.Hour,
		DB:         db,
		EventBus:   bus,
	}, store, metrics.NewMetrics())
	r.Hub = model.NewStreamHub()

	require.NoError(t, r.Start(context.Background()))
	require.Eventually(t, func() bool {
		frames, _, _ := store.snapshot()
		return frames >= 3
	}, 5*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { return reqs.Load() >= 1 }, 5*time.Second, 5*time.Millisecond)
	require.Equal(t, model.StatusRecording, r.Status())

	require.NoError(t, r.Stop())
	require.Equal(t, model.StatusStopped, r.Status())

	frames, totalBytes, closed := store.snapshot()
	require.Equal(t, 3, frames)
	require.Equal(t, 3*len(jpeg), totalBytes)
	require.Equal(t, 1, closed, "single segment expected after Stop")

	calls, recs := db.snapshot()
	require.Equal(t, 1, calls)
	require.Len(t, recs, 1)
	require.Equal(t, "cam-http", recs[0].CameraID)
	require.Equal(t, model.FormatMJPEG, recs[0].Format)
	require.Equal(t, 3, recs[0].FrameCount)
	require.Greater(t, recs[0].FileSize, int64(0))
	require.Greater(t, recs[0].Duration, 0.0)

	select {
	case ev := <-segCh:
		sc, ok := ev.Data.(event.SegmentCompleted)
		require.True(t, ok)
		require.Equal(t, "cam-http", sc.CameraID)
		require.Equal(t, recs[0].ID, sc.RecordingID)
		require.Equal(t, string(model.FormatMJPEG), sc.Format)
		require.Equal(t, recs[0].FileSize, sc.FileSize)
	case <-time.After(time.Second):
		t.Fatal("expected segment.completed event")
	}
	select {
	case ev := <-reconnCh:
		t.Fatalf("unexpected reconnected event on first connection: %+v", ev)
	default:
	}
}

func TestCovHTTPJPEG_FramesWithoutContentLength(t *testing.T) {
	const boundary = "no-length"
	jpeg := generateTestJPEG()
	store := newHJStore(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace;boundary="+boundary)
		w.WriteHeader(http.StatusOK)
		hjWriteMultipartFrame(w, boundary, jpeg, false)
		// readUntilBoundary needs a following boundary marker to terminate the
		// body, so emit one and then keep the stream open.
		fmt.Fprintf(w, "--%s", boundary)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	r := NewHTTPJPEGRecorder(HTTPJPEGConfig{CameraID: "cam-nolen", URL: srv.URL, SegmentDur: time.Hour}, store)
	require.NoError(t, r.Start(context.Background()))
	require.Eventually(t, func() bool {
		frames, _, _ := store.snapshot()
		return frames >= 1
	}, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, r.Stop())

	frames, _, closed := store.snapshot()
	require.GreaterOrEqual(t, frames, 1)
	require.GreaterOrEqual(t, closed, 1)
}

func TestCovHTTPJPEG_SkipsInvalidFrames(t *testing.T) {
	const boundary = "invalid-frame"
	jpeg := generateTestJPEG()
	store := newHJStore(t)
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace;boundary="+boundary)
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		// Not JPEG magic, then a short 1-byte frame, then a valid frame.
		hjWriteMultipartFrame(w, boundary, []byte{0x00, 0x11, 0x22}, true)
		hjWriteMultipartFrame(w, boundary, []byte{0xFF}, true)
		hjWriteMultipartFrame(w, boundary, jpeg, true)
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	defer func() {
		close(done)
		srv.Close()
	}()

	r := NewHTTPJPEGRecorder(HTTPJPEGConfig{CameraID: "cam-invalid", URL: srv.URL, SegmentDur: time.Hour}, store)
	require.NoError(t, r.Start(context.Background()))
	require.Eventually(t, func() bool {
		frames, _, _ := store.snapshot()
		return frames >= 1
	}, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, r.Stop())

	frames, totalBytes, _ := store.snapshot()
	require.Equal(t, 1, frames, "invalid frames must be skipped")
	require.Equal(t, len(jpeg), totalBytes)
}

func TestCovHTTPJPEG_RotatesSegments(t *testing.T) {
	const boundary = "rotate"
	jpeg := generateTestJPEG()
	store := newHJStore(t)
	db := &hjDB{}
	bus := event.NewEventBus(8)
	srv, _, _ := hjStreamServer(t, boundary, jpeg, 4)

	// A sub-nanosecond segment duration rotates after every frame.
	r := NewHTTPJPEGRecorder(HTTPJPEGConfig{
		CameraID:   "cam-rotate",
		URL:        srv.URL,
		SegmentDur: time.Nanosecond,
		DB:         db,
		EventBus:   bus,
	}, store)

	require.NoError(t, r.Start(context.Background()))
	require.Eventually(t, func() bool {
		frames, _, _ := store.snapshot()
		return frames >= 4
	}, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, r.Stop())

	_, _, closed := store.snapshot()
	require.GreaterOrEqual(t, closed, 4, "each frame should close its segment")
	calls, recs := db.snapshot()
	require.GreaterOrEqual(t, calls, 4)
	require.GreaterOrEqual(t, len(recs), 4)
	for _, rec := range recs {
		require.Equal(t, 1, rec.FrameCount)
	}
}

func TestCovHTTPJPEG_BasicAuth(t *testing.T) {
	const boundary = "auth"
	jpeg := generateTestJPEG()
	store := newHJStore(t)
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "multipart/x-mixed-replace;boundary="+boundary)
		w.WriteHeader(http.StatusOK)
		hjWriteMultipartFrame(w, boundary, jpeg, true)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	defer func() {
		close(done)
		srv.Close()
	}()

	r := NewHTTPJPEGRecorder(HTTPJPEGConfig{
		CameraID: "cam-auth",
		URL:      srv.URL,
		Username: "admin",
		Password: "secret",
	}, store)
	require.NoError(t, r.Start(context.Background()))
	require.Eventually(t, func() bool {
		frames, _, _ := store.snapshot()
		return frames >= 1
	}, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, r.Stop())
}

func TestCovHTTPJPEG_StartTwiceRejected(t *testing.T) {
	store := newHJStore(t)
	r := NewHTTPJPEGRecorder(HTTPJPEGConfig{CameraID: "cam-twice", URL: "http://127.0.0.1:1/none"}, store)
	require.NoError(t, r.Start(context.Background()))
	// Status is Recording immediately; a second Start must be rejected.
	require.ErrorContains(t, r.Start(context.Background()), "already running")
	require.NoError(t, r.Stop())
}

// --- error / reconnect paths ---

func TestCovHTTPJPEG_ConnectErrorPaths(t *testing.T) {
	jpeg := generateTestJPEG()

	closedSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closedSrv.URL
	closedSrv.Close()

	srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv500.Close()

	srvBadType := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusOK)
	}))
	defer srvBadType.Close()

	streamingHandler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace;boundary=b")
		w.WriteHeader(http.StatusOK)
		hjWriteMultipartFrame(w, "b", jpeg, true)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}
	srvStream := httptest.NewServer(http.HandlerFunc(streamingHandler))
	defer srvStream.Close()

	tests := []struct {
		name    string
		cfg     HTTPJPEGConfig
		prepare func(*hjStore)
	}{
		{
			name: "invalid url",
			cfg:  HTTPJPEGConfig{CameraID: "cam-bad-url", URL: "://missing-scheme"},
		},
		{
			name: "connection refused",
			cfg:  HTTPJPEGConfig{CameraID: "cam-refused", URL: closedURL},
		},
		{
			name: "http 500",
			cfg:  HTTPJPEGConfig{CameraID: "cam-500", URL: srv500.URL},
		},
		{
			name: "wrong content type",
			cfg:  HTTPJPEGConfig{CameraID: "cam-ct", URL: srvBadType.URL},
		},
		{
			name:    "create segment fails",
			cfg:     HTTPJPEGConfig{CameraID: "cam-create-err", URL: srvStream.URL},
			prepare: func(s *hjStore) { s.createErr = errors.New("disk full") },
		},
		{
			name:    "write frame fails",
			cfg:     HTTPJPEGConfig{CameraID: "cam-write-err", URL: srvStream.URL},
			prepare: func(s *hjStore) { s.writeErr = errors.New("write failed") },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newHJStore(t)
			if tc.prepare != nil {
				tc.prepare(store)
			}
			// Metrics are wired so the recordError/recordBytes helpers are exercised.
			r := NewHTTPJPEGRecorder(tc.cfg, store, metrics.NewMetrics())
			require.NoError(t, r.Start(context.Background()))
			require.Eventually(t, func() bool {
				return r.Status() == model.StatusReconnecting
			}, 5*time.Second, 5*time.Millisecond)
			require.NoError(t, r.Stop())
			require.Equal(t, model.StatusStopped, r.Status())
		})
	}
}

func TestCovHTTPJPEG_RecoversFromPanic(t *testing.T) {
	store := newHJStore(t)
	r := NewHTTPJPEGRecorder(HTTPJPEGConfig{CameraID: "cam-panic", URL: "http://127.0.0.1:1/x"}, store)
	r.client = &http.Client{Transport: hjPanicTransport{}}

	err, connected := r.connectAndStream(context.Background())
	require.NoError(t, err, "panic is recovered, so no error is returned")
	require.False(t, connected)
}

func TestCovHTTPJPEG_ReconnectsAndPublishesRecoveryEvents(t *testing.T) {
	const boundary = "reconnect"
	jpeg := generateTestJPEG()
	store := newHJStore(t)
	db := &hjDB{}
	bus := event.NewEventBus(32)
	reconnCh := make(chan event.Event, 8)
	segCh := make(chan event.Event, 8)
	require.NoError(t, bus.Subscribe(event.TopicRecorderReconnected, reconnCh, 8))
	require.NoError(t, bus.Subscribe(event.TopicSegmentCompleted, segCh, 8))

	var reqs atomic.Int32
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqs.Add(1)
		w.Header().Set("Content-Type", "multipart/x-mixed-replace;boundary="+boundary)
		w.WriteHeader(http.StatusOK)
		hjWriteMultipartFrame(w, boundary, jpeg, true)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if n == 1 {
			// Drop the connection immediately to force a reconnect.
			return
		}
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	defer func() {
		close(done)
		srv.Close()
	}()

	r := NewHTTPJPEGRecorder(HTTPJPEGConfig{
		CameraID:   "cam-reconnect",
		URL:        srv.URL,
		SegmentDur: time.Hour,
		DB:         db,
		EventBus:   bus,
	}, store, metrics.NewMetrics())

	require.NoError(t, r.Start(context.Background()))
	require.Eventually(t, func() bool {
		return r.Status() == model.StatusReconnecting
	}, 5*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool {
		return reqs.Load() >= 2 && r.Status() == model.StatusRecording
	}, 10*time.Second, 10*time.Millisecond)

	require.NoError(t, r.Stop())

	select {
	case ev := <-reconnCh:
		rc, ok := ev.Data.(event.RecorderReconnected)
		require.True(t, ok)
		require.Equal(t, "cam-reconnect", rc.CameraID)
		require.NotEmpty(t, rc.DisconnectedAt)
		require.NotEmpty(t, rc.ReconnectedAt)
		require.NotEmpty(t, rc.Downtime)
		require.NotEmpty(t, rc.RecordingID)
		require.Equal(t, "connection_lost", rc.GapReason)
		require.GreaterOrEqual(t, rc.RetryCount, 1)
	case <-time.After(2 * time.Second):
		t.Fatal("expected recorder.reconnected event")
	}

	select {
	case <-segCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected segment.completed event")
	}

	_, recs := db.snapshot()
	require.NotEmpty(t, recs)
	require.False(t, recs[0].ReconnectedAt.IsZero(), "first segment after recovery carries reconnect metadata")
	require.Equal(t, "connection_lost", recs[0].GapReason)
}

// --- segment closing / DB / event edge cases ---

func TestCovHTTPJPEG_CloseCurrentSegmentEdgeCases(t *testing.T) {
	t.Run("no open segment", func(t *testing.T) {
		r := NewHTTPJPEGRecorder(HTTPJPEGConfig{CameraID: "cam-idle"}, newHJStore(t))
		r.closeCurrentSegment()
		require.Empty(t, r.curTempPath)
	})

	t.Run("close error without DB", func(t *testing.T) {
		store := newHJStore(t)
		store.closeErr = errors.New("rename failed")
		r := NewHTTPJPEGRecorder(HTTPJPEGConfig{CameraID: "cam-close-err"}, store)
		r.curTempPath = "/tmp/missing-tmp"
		r.curFinalPath = "/tmp/missing-final"
		r.closeCurrentSegment()
		require.Empty(t, r.curTempPath)
		require.Empty(t, r.curFinalPath)
	})

	t.Run("db insert error still resets state", func(t *testing.T) {
		store := newHJStore(t)
		db := &hjDB{err: errors.New("db down")}
		r := NewHTTPJPEGRecorder(HTTPJPEGConfig{CameraID: "cam-db-err", DB: db}, store, metrics.NewMetrics())
		r.curTempPath = "/tmp/tmp-path"
		r.curFinalPath = filepath.Join(t.TempDir(), "final")
		r.frameCount = 2
		r.segStart = time.Now().Add(-time.Second)
		r.closeCurrentSegment()

		calls, recs := db.snapshot()
		require.Equal(t, 1, calls)
		require.Empty(t, recs)
		require.Empty(t, r.curTempPath)
		require.Zero(t, r.frameCount)
	})

	t.Run("segment without frames skips DB", func(t *testing.T) {
		store := newHJStore(t)
		db := &hjDB{}
		r := NewHTTPJPEGRecorder(HTTPJPEGConfig{CameraID: "cam-noframes", DB: db}, store)
		r.curTempPath = "/tmp/tmp-path"
		r.curFinalPath = "/tmp/final-path"
		r.closeCurrentSegment()
		calls, _ := db.snapshot()
		require.Zero(t, calls)
	})
}

// --- parser unit tests ---

func TestCovHTTPJPEG_ExtractBoundary(t *testing.T) {
	tests := []struct {
		ct   string
		want string
	}{
		{"multipart/x-mixed-replace;boundary=abc123", "abc123"},
		{`multipart/x-mixed-replace; boundary="quoted-value"`, "quoted-value"},
		{"multipart/x-mixed-replace;boundary=frame; charset=binary", "frame"},
		{"multipart/x-mixed-replace", "frame"},
		{"multipart/x-mixed-replace;boundary=", ""},
	}
	for _, tc := range tests {
		require.Equal(t, tc.want, extractBoundary(tc.ct), "content-type %q", tc.ct)
	}
}

func TestCovHTTPJPEG_SkipToBoundary(t *testing.T) {
	r := NewHTTPJPEGRecorder(HTTPJPEGConfig{CameraID: "cam-parse"}, nil)

	t.Run("skips preamble", func(t *testing.T) {
		reader := bufio.NewReader(bytes.NewBufferString("preamble line\r\n--b\r\nrest\n"))
		require.NoError(t, r.skipToBoundary(reader, "b"))
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, "rest\n", line)
	})

	t.Run("eof before boundary", func(t *testing.T) {
		reader := bufio.NewReader(bytes.NewBufferString("no boundary here\r\n"))
		require.ErrorIs(t, r.skipToBoundary(reader, "b"), io.EOF)
	})
}

func TestCovHTTPJPEG_ReadPartHeaders(t *testing.T) {
	r := NewHTTPJPEGRecorder(HTTPJPEGConfig{CameraID: "cam-parse"}, nil)

	t.Run("content length", func(t *testing.T) {
		reader := bufio.NewReader(bytes.NewBufferString("Content-Type: image/jpeg\r\nContent-Length: 42\r\n\r\nbody"))
		n, err := r.readPartHeaders(reader)
		require.NoError(t, err)
		require.Equal(t, 42, n)
	})

	t.Run("case insensitive and spaces", func(t *testing.T) {
		reader := bufio.NewReader(bytes.NewBufferString("content-length:   7  \r\n\r\n"))
		n, err := r.readPartHeaders(reader)
		require.NoError(t, err)
		require.Equal(t, 7, n)
	})

	t.Run("missing content length", func(t *testing.T) {
		reader := bufio.NewReader(bytes.NewBufferString("Content-Type: image/jpeg\r\n\r\n"))
		n, err := r.readPartHeaders(reader)
		require.NoError(t, err)
		require.Zero(t, n)
	})

	t.Run("invalid content length", func(t *testing.T) {
		reader := bufio.NewReader(bytes.NewBufferString("Content-Length: not-a-number\r\n\r\n"))
		_, err := r.readPartHeaders(reader)
		require.ErrorContains(t, err, "invalid content-length")
	})

	t.Run("eof before blank line", func(t *testing.T) {
		reader := bufio.NewReader(bytes.NewBufferString("Content-Length: 3\r\n"))
		_, err := r.readPartHeaders(reader)
		require.Error(t, err)
	})
}

func TestCovHTTPJPEG_ReadUntilBoundary(t *testing.T) {
	t.Run("stops at boundary", func(t *testing.T) {
		reader := bufio.NewReader(bytes.NewBufferString("payload\r\n--b--b"))
		var buf bytes.Buffer
		data, err := readUntilBoundary(reader, &buf, []byte("--b"))
		require.NoError(t, err)
		require.Equal(t, "payload", string(data))
	})

	t.Run("eof before boundary", func(t *testing.T) {
		reader := bufio.NewReader(bytes.NewBufferString("payload"))
		var buf bytes.Buffer
		_, err := readUntilBoundary(reader, &buf, []byte("--b"))
		require.ErrorIs(t, err, io.EOF)
	})
}
