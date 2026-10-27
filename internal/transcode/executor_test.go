package transcode

import (
	"context"
	"testing"
	"time"
)

type adapterStub struct {
	observation OutputObservation
	released    int
	reserved    int
}

func (a *adapterStub) Resolve(context.Context, SourceRef) (ResolvedInput, error) {
	return ResolvedInput{URL: "rtsp://localhost/live/source", VideoCodec: "h265", Width: 1920, Height: 1080}, nil
}
func (a *adapterStub) Reserve(context.Context, SourceRef, string) (string, error) {
	a.reserved++
	return "rtmp://localhost/live/variant", nil
}
func (a *adapterStub) Observe(context.Context, string) (OutputObservation, error) {
	return a.observation, nil
}
func (a *adapterStub) Release(context.Context, string) error { a.released++; return nil }
func TestExecutorFailedStartReleasesReservation(t *testing.T) {
	a := &adapterStub{}
	e := &BackendExecutor{Adapter: a, Config: Config{FFmpegPath: "/missing/backend"}}
	_, err := e.Start(context.Background(), SourceRef{EngineID: "embedded", AppName: "live", StreamID: "source"}, testProfile(t), "output")
	if err == nil || a.reserved != 1 || a.released != 1 {
		t.Fatalf("reservation leak: err=%v reserved=%d released=%d", err, a.reserved, a.released)
	}
}
func TestExecutorRequiresFreshOutputMedia(t *testing.T) {
	started := time.Now().Add(-time.Second)
	a := &adapterStub{observation: OutputObservation{Active: true, VideoCodec: "h264", LastFrame: time.Now()}}
	e := &BackendExecutor{Adapter: a, reserved: map[string]time.Time{"output": started}}
	if !e.Ready(context.Background(), "output") {
		t.Fatal("fresh media rejected")
	}
	a.observation.LastFrame = started.Truncate(time.Second)
	if !e.Ready(context.Background(), "output") {
		t.Fatal("second-resolution current media rejected")
	}
	a.observation.LastFrame = started.Add(-time.Second)
	if e.Ready(context.Background(), "output") {
		t.Fatal("previous publisher counted as ready")
	}
	a.observation.LastFrame = time.Now()
	a.observation.VideoCodec = "h265"
	if e.Ready(context.Background(), "output") {
		t.Fatal("wrong codec counted as ready")
	}
}
