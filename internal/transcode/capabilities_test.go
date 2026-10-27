package transcode

import (
	"context"
	"errors"
	"testing"
)

type fakeProbeRunner struct{ fail bool }

func (f fakeProbeRunner) Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	if f.fail {
		return nil, errors.New("rtsp://user:password@host?token=secret")
	}
	return []byte(" V....D libx264 H264 encoder\n A..... aac AAC encoder\n"), nil
}
func TestDiscoverDoesNotClaimReadiness(t *testing.T) {
	p := testProfile(t)
	paths := ToolPaths{FFmpeg: "ffmpeg", FFprobe: "ffprobe"}
	r, err := Discover(context.Background(), paths, p, fakeProbeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if r.CanEnable {
		t.Fatal("discovery cannot enable an untested template")
	}
	for _, c := range r.Checks {
		if c.Name == "media_self_test" && c.Status != NotTested {
			t.Fatal("false self-test success")
		}
	}
	paths.FFmpeg = "another-ffmpeg"
	other, err := Discover(context.Background(), paths, p, fakeProbeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if other.Fingerprint == r.Fingerprint {
		t.Fatal("path changes must invalidate old result")
	}
	r, err = Discover(context.Background(), paths, p, fakeProbeRunner{fail: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Checks {
		if c.Status == Passed {
			t.Fatal("failed check reported passed")
		}
		if c.Detail == "rtsp://user:password@host?token=secret" {
			t.Fatal("credential leak")
		}
	}
}
func TestEncoderListedUsesExactName(t *testing.T) {
	if encoderListed(" V..... libx264rgb description libx264", "libx264") {
		t.Fatal("substring matched unsupported encoder")
	}
	if !encoderListed(" V..... libx264 description", "libx264") {
		t.Fatal("encoder not found")
	}
}
func TestResolveExplicitToolDoesNotFallback(t *testing.T) {
	if _, err := ResolveTool("/not-installed/ffmpeg", "go"); err == nil {
		t.Fatal("invalid explicit path silently accepted")
	}
}
