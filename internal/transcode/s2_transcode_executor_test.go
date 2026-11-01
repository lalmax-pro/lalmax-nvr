package transcode

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// s2ProbeRunner returns canned probe output.
type s2ProbeRunner struct {
	data []byte
	err  error
	args []string
}

func (r *s2ProbeRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.args = append([]string(nil), args...)
	return r.data, r.err
}

// s2MediaAdapter is a scriptable MediaAdapter.
type s2MediaAdapter struct {
	input        ResolvedInput
	resolveErr   error
	reserveErr   error
	observeErr   error
	observe      OutputObservation
	releaseErr   error
	releaseCalls int
	observed     int
	reserved     []string
	mu           sync.Mutex
}

func (a *s2MediaAdapter) Resolve(context.Context, SourceRef) (ResolvedInput, error) {
	return a.input, a.resolveErr
}

func (a *s2MediaAdapter) Reserve(_ context.Context, _ SourceRef, id string) (string, error) {
	a.mu.Lock()
	a.reserved = append(a.reserved, id)
	a.mu.Unlock()
	if a.reserveErr != nil {
		return "", a.reserveErr
	}
	return "rtmp://127.0.0.1/internal/" + id, nil
}

func (a *s2MediaAdapter) Observe(context.Context, string) (OutputObservation, error) {
	a.mu.Lock()
	a.observed++
	a.mu.Unlock()
	return a.observe, a.observeErr
}

func (a *s2MediaAdapter) Release(context.Context, string) error {
	a.mu.Lock()
	a.releaseCalls++
	a.mu.Unlock()
	return a.releaseErr
}

func (a *s2MediaAdapter) releaseCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.releaseCalls
}

func TestS2ProbeInputDimensions(t *testing.T) {
	ctx := context.Background()

	// Only trusted RTSP endpoints are probed.
	if _, _, err := ProbeInputDimensions(ctx, "ffprobe", "http://camera/stream", &s2ProbeRunner{}); err == nil {
		t.Fatal("expected non-RTSP input to be rejected")
	}
	if _, _, err := ProbeInputDimensions(ctx, "ffprobe", "rtsp://", &s2ProbeRunner{}); err == nil {
		t.Fatal("expected missing host to be rejected")
	}
	if _, _, err := ProbeInputDimensions(ctx, "ffprobe", "rtsp://camera/stream", nil); err == nil {
		t.Fatal("expected missing runner to be rejected")
	}

	// Runner failures never leak tool output.
	if _, _, err := ProbeInputDimensions(ctx, "ffprobe", "rtsp://cam/stream", &s2ProbeRunner{err: errors.New("rtsp://user:pass@cam")}); !isCode(err, "source_probe_failed") {
		t.Fatalf("expected source_probe_failed, got %v", err)
	}

	// A single well-formed video stream is accepted.
	runner := &s2ProbeRunner{data: []byte(`{"streams":[{"width":1280,"height":720}]}`)}
	width, height, err := ProbeInputDimensions(ctx, "ffprobe", "rtsp://cam/stream", runner)
	if err != nil || width != 1280 || height != 720 {
		t.Fatalf("unexpected dimensions %dx%d (%v)", width, height, err)
	}
	if len(runner.args) == 0 || runner.args[len(runner.args)-1] != "rtsp://cam/stream" {
		t.Fatalf("unexpected probe args: %v", runner.args)
	}
}

func TestS2ParseInputDimensions(t *testing.T) {
	cases := []struct {
		name string
		data string
		ok   bool
	}{
		{"valid", `{"streams":[{"width":640,"height":360}]}`, true},
		{"invalid json", `{`, false},
		{"no streams", `{"streams":[]}`, false},
		{"two streams", `{"streams":[{"width":640,"height":360},{"width":640,"height":360}]}`, false},
		{"zero width", `{"streams":[{"width":0,"height":360}]}`, false},
		{"one pixel", `{"streams":[{"width":1,"height":360}]}`, false},
		{"too tall", `{"streams":[{"width":640,"height":16385}]}`, false},
	}
	for _, tc := range cases {
		width, height, err := parseInputDimensions([]byte(tc.data))
		if tc.ok {
			if err != nil || width != 640 || height != 360 {
				t.Fatalf("%s: unexpected result %dx%d (%v)", tc.name, width, height, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
	}
}

func TestS2ConfigValidateAndDefaults(t *testing.T) {
	cfg := Config{}
	cfg.ApplyDefaults()
	if cfg.MaxConcurrent != 2 || cfg.ThreadsPerJob != 2 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}

	for _, bad := range []Config{
		{MaxConcurrent: -1},
		{MaxConcurrent: 33},
		{ThreadsPerJob: -1},
		{ThreadsPerJob: 17},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("expected validation error for %+v", bad)
		}
	}

	// Explicit paths win over executable names.
	tools := Config{FFmpegPath: "/opt/ffmpeg", FFprobePath: "/opt/ffprobe", GSTLaunchPath: "/opt/gst", GSTInspectPath: "/opt/inspect"}.Tools()
	if tools.FFmpeg != "/opt/ffmpeg" || tools.FFprobe != "/opt/ffprobe" || tools.GSTLaunch != "/opt/gst" || tools.GSTInspect != "/opt/inspect" {
		t.Fatalf("unexpected tool paths: %+v", tools)
	}
	fallback := Config{}.Tools()
	if fallback.FFmpeg != "ffmpeg" || fallback.FFprobe != "ffprobe" || fallback.GSTLaunch != "gst-launch-1.0" || fallback.GSTInspect != "gst-inspect-1.0" {
		t.Fatalf("unexpected fallback tool paths: %+v", fallback)
	}
}

func TestS2ReservedProcessWaitRunsCleanupOnce(t *testing.T) {
	process := &fakeManagedProcess{done: make(chan struct{})}
	close(process.done)
	cleanups := 0
	reserved := &reservedProcess{Process: process, cleanup: func() { cleanups++ }}
	if err := reserved.Wait(); err != nil {
		t.Fatalf("unexpected wait error: %v", err)
	}
	if cleanups != 1 {
		t.Fatalf("expected exactly one cleanup, got %d", cleanups)
	}
}

func TestS2BackendExecutorStart(t *testing.T) {
	ctx := context.Background()
	p := testProfile(t)
	source := SourceRef{EngineID: "embedded", AppName: "live", StreamID: "camera"}

	// A missing adapter is reported as engine_unavailable.
	executor := &BackendExecutor{Config: Config{FFmpegPath: "/bin/echo"}}
	if _, err := executor.Start(ctx, source, p, "variant"); !isCode(err, "engine_unavailable") {
		t.Fatalf("expected engine_unavailable, got %v", err)
	}

	// Resolution and media compatibility failures propagate.
	adapter := &s2MediaAdapter{resolveErr: errors.New("source offline")}
	executor = &BackendExecutor{Adapter: adapter, Config: Config{FFmpegPath: "/bin/echo"}}
	if _, err := executor.Start(ctx, source, p, "variant"); err == nil {
		t.Fatal("expected resolve error")
	}

	adapter = &s2MediaAdapter{input: ResolvedInput{URL: "rtsp://cam/stream", VideoCodec: "mpeg2"}}
	executor = &BackendExecutor{Adapter: adapter, Config: Config{FFmpegPath: "/bin/echo"}}
	if _, err := executor.Start(ctx, source, p, "variant"); !isCode(err, "unsupported_input") {
		t.Fatalf("expected unsupported_input, got %v", err)
	}

	// A failing reserve aborts without starting a process.
	adapter = &s2MediaAdapter{
		input:      ResolvedInput{URL: "rtsp://cam/stream", VideoCodec: "h264", AudioCodec: "aac"},
		reserveErr: errors.New("reserve exploded"),
	}
	executor = &BackendExecutor{Adapter: adapter, Config: Config{FFmpegPath: "/bin/echo"}}
	if _, err := executor.Start(ctx, source, p, "variant"); err == nil {
		t.Fatal("expected reserve error")
	}

	// Invalid config after reservation triggers the bounded cleanup loop.
	adapter = &s2MediaAdapter{
		input:      ResolvedInput{URL: "rtsp://cam/stream", VideoCodec: "h264", AudioCodec: "aac"},
		releaseErr: errors.New("release exploded"),
	}
	executor = &BackendExecutor{Adapter: adapter, Config: Config{FFmpegPath: "/bin/echo", MaxConcurrent: 99}}
	if _, err := executor.Start(ctx, source, p, "variant"); err == nil {
		t.Fatal("expected config validation error")
	}
	if calls := adapter.releaseCount(); calls != 3 {
		t.Fatalf("expected three cleanup attempts, got %d", calls)
	}

	// A failing process start also cleans up the reservation.
	adapter = &s2MediaAdapter{input: ResolvedInput{URL: "rtsp://cam/stream", VideoCodec: "h264", AudioCodec: "aac"}}
	executor = &BackendExecutor{Adapter: adapter, Config: Config{FFmpegPath: "/nonexistent/ffmpeg"}}
	if _, err := executor.Start(ctx, source, p, "variant"); err == nil {
		t.Fatal("expected start error")
	}
	if calls := adapter.releaseCount(); calls != 1 {
		t.Fatalf("expected one cleanup attempt, got %d", calls)
	}

	// A successful FFmpeg start registers the reservation and cleans up on wait.
	adapter = &s2MediaAdapter{input: ResolvedInput{URL: "rtsp://cam/stream", VideoCodec: "h264", AudioCodec: "aac"}}
	executor = &BackendExecutor{Adapter: adapter, Config: Config{FFmpegPath: "/bin/echo"}}
	process, err := executor.Start(ctx, source, p, "variant")
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	_ = process.Wait()
	executor.mu.Lock()
	_, stillReserved := executor.reserved["variant"]
	executor.mu.Unlock()
	if stillReserved {
		t.Fatal("reservation must be released after the process exits")
	}

	// GStreamer profiles start through gst-launch.
	gstProfile := testProfile(t)
	gstProfile.Backend = "gstreamer"
	gstProfile.Video.Encoder = "x264enc"
	adapter = &s2MediaAdapter{input: ResolvedInput{URL: "rtsp://cam/stream", VideoCodec: "h264", AudioCodec: "aac", Width: 640, Height: 360}}
	executor = &BackendExecutor{Adapter: adapter, Config: Config{GSTLaunchPath: "/bin/echo"}}
	process, err = executor.Start(ctx, source, gstProfile, "variant-gst")
	if err != nil {
		t.Fatalf("gstreamer start failed: %v", err)
	}
	_ = process.Wait()
}

func TestS2StartGStreamer(t *testing.T) {
	p := testProfile(t)
	p.Backend = "gstreamer"
	p.Video.Encoder = "x264enc"
	spec := JobSpec{Profile: p, InputURL: "rtsp://cam/stream", OutputURL: "rtmp://127.0.0.1/live/variant", VideoCodec: "h264", AudioCodec: "aac", Threads: 2}

	process, err := StartGStreamer(context.Background(), "/bin/echo", spec, 640, 360)
	if err != nil {
		t.Fatalf("gstreamer start failed: %v", err)
	}
	if err := process.Wait(); err != nil {
		t.Fatalf("unexpected gstreamer wait error: %v", err)
	}

	// An invalid output cannot be started.
	if _, err := StartGStreamer(context.Background(), "/bin/echo", JobSpec{Profile: p}, 640, 360); err == nil {
		t.Fatal("expected invalid spec error")
	}
}

func TestS2BackendExecutorReady(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	// Without an adapter the output is never ready.
	executor := &BackendExecutor{}
	if executor.Ready(ctx, "variant") {
		t.Fatal("expected not ready without adapter")
	}

	// Observation failures or inactive outputs are not ready.
	adapter := &s2MediaAdapter{observeErr: errors.New("observe exploded")}
	executor = &BackendExecutor{Adapter: adapter}
	if executor.Ready(ctx, "variant") {
		t.Fatal("expected not ready on observe error")
	}

	adapter = &s2MediaAdapter{observe: OutputObservation{Active: false}}
	executor = &BackendExecutor{Adapter: adapter}
	if executor.Ready(ctx, "variant") {
		t.Fatal("expected not ready for inactive output")
	}

	adapter = &s2MediaAdapter{observe: OutputObservation{Active: true, VideoCodec: "h265", LastFrame: now}}
	executor = &BackendExecutor{Adapter: adapter}
	if executor.Ready(ctx, "variant") {
		t.Fatal("expected not ready for non-H264 output")
	}

	// An active H.264 output needs a matching reservation and a fresh frame.
	adapter = &s2MediaAdapter{observe: OutputObservation{Active: true, VideoCodec: "h264", LastFrame: time.Now()}}
	executor = &BackendExecutor{Adapter: adapter}
	if executor.Ready(ctx, "missing-reservation") {
		t.Fatal("expected not ready without a reservation")
	}

	executor = &BackendExecutor{Adapter: adapter, reserved: map[string]time.Time{"variant": time.Now().Add(-time.Second)}}
	if !executor.Ready(ctx, "variant") {
		t.Fatal("expected ready for an active reserved output")
	}

	// A stale last frame is not ready.
	adapter = &s2MediaAdapter{observe: OutputObservation{Active: true, VideoCodec: "h264", LastFrame: time.Now().Add(-time.Minute)}}
	executor = &BackendExecutor{Adapter: adapter, reserved: map[string]time.Time{"variant": time.Now().Add(-time.Minute)}}
	if executor.Ready(ctx, "variant") {
		t.Fatal("expected not ready for a stale frame")
	}

	// A frame older than the reservation is not ready.
	adapter = &s2MediaAdapter{observe: OutputObservation{Active: true, VideoCodec: "h264", LastFrame: time.Now().Add(-10 * time.Second)}}
	executor = &BackendExecutor{Adapter: adapter, reserved: map[string]time.Time{"variant": time.Now()}}
	if executor.Ready(ctx, "variant") {
		t.Fatal("expected not ready for a frame predating the reservation")
	}
}

func TestS2NewManagerDefaultsLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := NewManager(ctx, &fakeExecutor{}, 0)
	if limit := manager.limit; limit != 2 {
		t.Fatalf("expected default limit 2, got %d", limit)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("close failed: %v", err)
	}
}
