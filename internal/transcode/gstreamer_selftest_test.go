package transcode

import (
	"context"
	"slices"
	"testing"
)

func TestGStreamerSelfTestHasFiniteSources(t *testing.T) {
	p := DefaultProfile("gstreamer")
	args, err := gstreamerSelfTestArgs(p, "/tmp/probe with spaces.flv")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"num-buffers=15", "num-buffers=24", "mux.video", "mux.audio", gstProperty("location", "/tmp/probe with spaces.flv")} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing %s", want)
		}
	}
	p.Audio = AudioSpec{Codec: "none"}
	args, err = gstreamerSelfTestArgs(p, "/tmp/output.flv")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, "mux.audio") {
		t.Fatal("video-only test created audio branch")
	}
}
func TestGStreamerSelfTestCannotPassEmptyOutput(t *testing.T) {
	p := DefaultProfile("gstreamer")
	report, err := ProbeGStreamer(context.Background(), (Config{}).Tools(), p, fakeProbeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if report.CanEnable {
		t.Fatal("tool execution without valid media passed")
	}
	found := false
	for _, check := range report.Checks {
		if check.Name == "media_self_test" && check.Status == CheckFailed {
			found = true
		}
	}
	if !found {
		t.Fatal("invalid probe media not marked failed")
	}
}
