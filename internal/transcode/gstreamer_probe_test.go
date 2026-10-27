package transcode

import (
	"context"
	"os"
	"testing"
	"time"
)

// Discovery diagnoses the actual installation, including a plugin scanner that
// hangs. Missing/broken tools are recorded as failed checks, not test skips.
func TestGStreamerRealDiscovery(t *testing.T) {
	if os.Getenv("NVR_GSTREAMER_MEDIA_TEST") != "1" {
		t.Skip("set NVR_GSTREAMER_MEDIA_TEST=1 to inspect installed GStreamer")
	}
	launch, err := ResolveTool("", "gst-launch-1.0")
	if err != nil {
		t.Fatal(err)
	}
	inspect, err := ResolveTool("", "gst-inspect-1.0")
	if err != nil {
		t.Fatal(err)
	}
	p := testProfile(t)
	p.Backend = "gstreamer"
	p.Video.Encoder = "x264enc"
	started := time.Now()
	report, err := Discover(context.Background(), ToolPaths{GSTLaunch: launch, GSTInspect: inspect, FFprobe: "ffprobe"}, p, CommandProbeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("checks: %+v", report.Checks)
	if time.Since(started) > 20*time.Second {
		t.Fatal("probe exceeded total time budget")
	}
	if report.CanEnable {
		t.Fatal("discovery cannot claim executable pipeline support")
	}
}
