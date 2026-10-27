package transcode

import (
	"context"
	"os"
	"testing"
)

// Explicit opt-in: this test fails, rather than skips, when the selected local
// GStreamer installation cannot encode/decode the advertised template.
func TestGStreamerActualSelfTest(t *testing.T) {
	if os.Getenv("NVR_GSTREAMER_SELFTEST") != "1" {
		t.Skip("set NVR_GSTREAMER_SELFTEST=1 for installed backend execution")
	}
	paths := (Config{}).Tools()
	for _, audio := range []string{"aac", "none"} {
		t.Run(audio, func(t *testing.T) {
			p := DefaultProfile("gstreamer")
			if audio == "none" {
				p.Audio = AudioSpec{Codec: "none"}
			}
			report, err := ProbeGStreamer(context.Background(), paths, p, CommandProbeRunner{})
			if err != nil {
				t.Fatal(err)
			}
			if !report.CanEnable {
				t.Fatalf("actual backend not usable: %+v", report.Checks)
			}
		})
	}
}
