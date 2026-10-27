package transcode

import (
	"slices"
	"strings"
	"testing"
)

func TestGStreamerFixedPipeline(t *testing.T) {
	s := testSpec(t)
	s.Profile = DefaultProfile("gstreamer")
	args, err := GStreamerArgs(s, 1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	pipeline := strings.Join(args, " ")
	if !strings.Contains(pipeline, "encoding-name=H265 ! queue ! rtph265depay") || !strings.Contains(pipeline, "encoding-name=MPEG4-GENERIC ! queue ! rtpmp4gdepay") {
		t.Fatal("dynamic RTSP branches must be decoupled before depayloading")
	}
	for _, want := range []string{"rtph265depay", "avdec_h265", "x264enc", "avenc_aac", "mux.video", "mux.audio"} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if !strings.Contains(strings.Join(args, " "), "width=1280,height=720") {
		t.Fatal("wrong output dimensions")
	}
	s.AudioCodec = ""
	args, err = GStreamerArgs(s, 640, 480)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, "mux.audio") {
		t.Fatal("absent audio branch can stall pipeline")
	}
	if !strings.Contains(strings.Join(args, " "), "width=640,height=480") {
		t.Fatal("small source was upscaled")
	}
	if _, err = GStreamerArgs(s, 0, 0); err == nil {
		t.Fatal("unknown dimensions silently accepted")
	}
	s.VideoCodec = "h264"
	s.Profile.Video = VideoSpec{Codec: "copy"}
	args, err = GStreamerArgs(s, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, "x264enc") {
		t.Fatal("copy unexpectedly encodes")
	}
}
func TestGStreamerPropertyEscaping(t *testing.T) {
	value := `rtsp://localhost/live/x?token=" ! filesink location=/tmp/unwanted`
	property := gstProperty("location", value)
	if !strings.HasPrefix(property, `location="`) || !strings.HasSuffix(property, `"`) || !strings.Contains(property, `\"`) {
		t.Fatalf("property is not quoted: %s", property)
	}
}
func TestGStreamerDimensions(t *testing.T) {
	v := DefaultProfile("gstreamer").Video
	for _, tc := range []struct{ w, h, wantW, wantH int }{{1920, 1080, 1280, 720}, {1080, 1920, 404, 720}, {320, 240, 320, 240}, {641, 481, 640, 480}} {
		w, h, err := outputDimensions(tc.w, tc.h, v)
		if err != nil {
			t.Fatal(err)
		}
		if w != tc.wantW || h != tc.wantH {
			t.Fatalf("%dx%d -> %dx%d", tc.w, tc.h, w, h)
		}
	}
}
