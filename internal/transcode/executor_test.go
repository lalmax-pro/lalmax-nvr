package transcode

import (
	"context"
	"testing"
	"time"
)

type adapterStub struct {
	observation   OutputObservation
	observations  []OutputObservation
	observeIndex  int
	released      int
	reserved      int
}

func (a *adapterStub) Resolve(context.Context, SourceRef) (ResolvedInput, error) {
	return ResolvedInput{URL: "rtsp://localhost/live/source", VideoCodec: "h265", Width: 1920, Height: 1080}, nil
}
func (a *adapterStub) Reserve(context.Context, SourceRef, string) (string, error) {
	a.reserved++
	return "rtmp://localhost/live/variant", nil
}
func (a *adapterStub) Observe(context.Context, string) (OutputObservation, error) {
	if len(a.observations) > 0 {
		index := a.observeIndex
		if index >= len(a.observations) {
			index = len(a.observations) - 1
		}
		a.observeIndex++
		return a.observations[index], nil
	}
	return a.observation, nil
}
func (a *adapterStub) Release(context.Context, string) error { a.released++; return nil }

func newReadyExecutor(a *adapterStub, started time.Time) *BackendExecutor {
	return &BackendExecutor{
		Adapter:  a,
		reserved: map[string]time.Time{"output": started},
		ready:    map[string]*readinessState{},
	}
}

func TestExecutorFailedStartReleasesReservation(t *testing.T) {
	a := &adapterStub{}
	e := &BackendExecutor{Adapter: a, Config: Config{FFmpegPath: "/missing/backend"}}
	_, err := e.Start(context.Background(), SourceRef{EngineID: "embedded", AppName: "live", StreamID: "source"}, testProfile(t), "output")
	if err == nil || a.reserved != 1 || a.released != 1 {
		t.Fatalf("reservation leak: err=%v reserved=%d released=%d", err, a.reserved, a.released)
	}
}

func TestExecutorRejectsMetadataOnlyBytesAndStalledCounter(t *testing.T) {
	a := &adapterStub{observations: []OutputObservation{
		{Active: true, VideoCodec: "h264", MediaBytes: 128},
		{Active: true, VideoCodec: "h264", MediaBytes: 128},
	}}
	e := newReadyExecutor(a, time.Now())
	if e.Ready(context.Background(), "output") {
		t.Fatal("metadata or sequence-header bytes counted as ready")
	}
	if e.Ready(context.Background(), "output") {
		t.Fatal("stalled media counter counted as ready")
	}
}

func TestExecutorRejectsPublisherBytesWithoutVideoTimestamp(t *testing.T) {
	a := &adapterStub{observations: []OutputObservation{
		{Active: true, VideoCodec: "h264", MediaBytes: 128},
		{Active: true, VideoCodec: "h264", MediaBytes: 256},
	}}
	e := newReadyExecutor(a, time.Now())
	if e.Ready(context.Background(), "output") {
		t.Fatal("one media-byte sample counted as ready")
	}
	if e.Ready(context.Background(), "output") {
		t.Fatal("audio or metadata bytes accepted without video-frame evidence")
	}
}

func TestExecutorRejectsOldGenerationAndCounterResetUntilFresh(t *testing.T) {
	started := time.Now()
	a := &adapterStub{observations: []OutputObservation{
		{Active: true, VideoCodec: "h264", LastFrame: started.Add(-2 * time.Second), MediaBytes: 900},
		{Active: true, VideoCodec: "h264", MediaBytes: 100},
		{Active: true, VideoCodec: "h264", MediaBytes: 200},
		{Active: true, VideoCodec: "h264", MediaBytes: 300, LastFrame: time.Now()},
	}}
	e := newReadyExecutor(a, started)
	if e.Ready(context.Background(), "output") {
		t.Fatal("old-generation frame counted as ready")
	}
	if e.Ready(context.Background(), "output") {
		t.Fatal("counter reset counted as ready")
	}
	if e.Ready(context.Background(), "output") {
		t.Fatal("first post-reset progress counted as ready")
	}
	if !e.Ready(context.Background(), "output") {
		t.Fatal("fresh post-reset video frame was not accepted")
	}
}

func TestExecutorRejectsStaleVideoDespitePublisherByteGrowth(t *testing.T) {
    started := time.Now().Add(-2*time.Minute)
    stale := time.Now().Add(-time.Minute)
    a := &adapterStub{observations: []OutputObservation{
        {Active: true, VideoCodec: "h264", LastFrame: stale, MediaBytes: 1000},
        {Active: true, VideoCodec: "h264", LastFrame: stale, MediaBytes: 2000},
    }}
    e := newReadyExecutor(a, started)
    if e.Ready(context.Background(), "output") || e.Ready(context.Background(), "output") {
        t.Fatal("stale video timestamp was treated as live from aggregate bytes")
    }
}

func TestExecutorAcceptsFreshVideoFrame(t *testing.T) {
	started := time.Now().Add(-time.Second)
	a := &adapterStub{observation: OutputObservation{
		Active: true, VideoCodec: "h264", LastFrame: time.Now(),
	}}
	e := newReadyExecutor(a, started)
	if !e.Ready(context.Background(), "output") {
		t.Fatal("fresh video frame rejected")
	}
}

func TestExecutorReadyWithReservedButNoStateMap(t *testing.T) {
	adapter := &adapterStub{observation: OutputObservation{
		Active: true, VideoCodec: "h264", LastFrame: time.Now(),
	}}
	executor := &BackendExecutor{
		Adapter:  adapter,
		reserved: map[string]time.Time{"output": time.Now().Add(-time.Second)},
	}
	if !executor.Ready(context.Background(), "output") {
		t.Fatal("fresh frame should be ready even with an initially nil ready map")
	}
	if executor.ready["output"] == nil {
		t.Fatal("Ready must initialize per-output progress state")
	}
}

func TestExecutorRejectsNoVideoFrameAndExitedProcess(t *testing.T) {
	a := &adapterStub{observations: []OutputObservation{
		{Active: true, VideoCodec: "h264", MediaBytes: 0},
		{Active: false, VideoCodec: "h264", MediaBytes: 512},
	}}
	e := newReadyExecutor(a, time.Now())
	if e.Ready(context.Background(), "output") {
		t.Fatal("no-video-frame observation counted as ready")
	}
	if e.Ready(context.Background(), "output") {
		t.Fatal("exited process counted as ready")
	}
}

func TestExecutorRejectsWrongCodecAndUnreservedOutput(t *testing.T) {
	a := &adapterStub{observation: OutputObservation{Active: true, VideoCodec: "h265", MediaBytes: 512}}
	e := newReadyExecutor(a, time.Now())
	if e.Ready(context.Background(), "output") {
		t.Fatal("wrong codec counted as ready")
	}
	if e.Ready(context.Background(), "missing") {
		t.Fatal("unreserved output counted as ready")
	}
}
