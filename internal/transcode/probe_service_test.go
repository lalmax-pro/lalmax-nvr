package transcode

import (
	"context"
	"sync"
	"testing"
	"time"
)

type blockingProbe struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (b *blockingProbe) Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestCanceledReprobeRevokesOldApproval(t *testing.T) {
	runner := &blockingProbe{entered: make(chan struct{}), release: make(chan struct{})}
	service := NewProbeService(runner)
	profile := testProfile(t)
	paths := (Config{}).Tools()
	fingerprint, err := ProbeFingerprint(paths, profile)
	if err != nil {
		t.Fatal(err)
	}
	service.reports[profile.ID] = CapabilityReport{ProfileID: profile.ID, CanEnable: true, Fingerprint: fingerprint, CheckedAt: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := service.Probe(ctx, paths, profile); done <- err }()
	<-runner.entered
	if service.Allows(profile.ID, fingerprint) {
		t.Error("old approval survived active reprobe")
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("canceled check returned success")
	}
	if service.Allows(profile.ID, fingerprint) {
		t.Fatal("canceled check restored old approval")
	}
}

func TestProbeServiceRejectsConcurrentChecks(t *testing.T) {
	r := &blockingProbe{entered: make(chan struct{}), release: make(chan struct{})}
	s := NewProbeService(r)
	p := testProfile(t)
	p.Backend = "gstreamer"
	p.Video.Encoder = "x264enc"
	paths := ToolPaths{GSTLaunch: "gst", GSTInspect: "inspect"}
	done := make(chan error, 1)
	go func() { _, err := s.Probe(context.Background(), paths, p); done <- err }()
	<-r.entered
	if _, err := s.Probe(context.Background(), paths, p); err == nil {
		t.Error("parallel probe accepted")
	}
	close(r.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	report, ok := s.Result(p.ID)
	if !ok {
		t.Fatal("result not saved")
	}
	report.Checks[0].Detail = "mutated"
	original, _ := s.Result(p.ID)
	if original.Checks[0].Detail == "mutated" {
		t.Fatal("cache aliases returned slices")
	}
	if s.Allows(p.ID, report.Fingerprint) {
		t.Fatal("discovery cannot grant activation")
	}
}
func TestCurrentResultInvalidatesChangedConfiguration(t *testing.T) {
	s := NewProbeService(nil)
	p := testProfile(t)
	paths := (Config{}).Tools()
	fingerprint, err := ProbeFingerprint(paths, p)
	if err != nil {
		t.Fatal(err)
	}
	s.reports[p.ID] = CapabilityReport{CanEnable: true, Fingerprint: fingerprint, CheckedAt: time.Now()}
	report, ok := s.CurrentResult(paths, p)
	if !ok || report.Stale || !report.CanEnable {
		t.Fatal("fresh result invalidated")
	}
	paths.FFmpeg = "/other/ffmpeg"
	report, ok = s.CurrentResult(paths, p)
	if !ok || !report.Stale || report.CanEnable {
		t.Fatal("changed executable retained activation")
	}
}

func TestProbeServiceRequiresFreshMatchingResult(t *testing.T) {
	s := NewProbeService(nil)
	s.reports["p"] = CapabilityReport{CanEnable: true, Fingerprint: "current", CheckedAt: time.Now()}
	if !s.Allows("p", "current") {
		t.Fatal("fresh result rejected")
	}
	if s.Allows("p", "other") {
		t.Fatal("stale config accepted")
	}
	report := s.reports["p"]
	report.CheckedAt = time.Now().Add(-11 * time.Minute)
	s.reports["p"] = report
	if s.Allows("p", "current") {
		t.Fatal("expired result accepted")
	}
}
