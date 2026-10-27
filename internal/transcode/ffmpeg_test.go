package transcode

import (
	"slices"
	"strings"
	"testing"
)

func testSpec(t *testing.T) JobSpec {
	t.Helper()
	return JobSpec{Profile: testProfile(t), InputURL: "rtsp://127.0.0.1:15544/live/source?token=secret", OutputURL: "rtmp://127.0.0.1:11935/live/derived?token=secret", VideoCodec: "H265", AudioCodec: "AAC", Threads: 2}
}
func TestFFmpegArgs(t *testing.T) {
	s := testSpec(t)
	args, err := FFmpegArgs(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{s.InputURL, s.OutputURL, "libx264", "aac", "1500k", "yuv420p"} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing argument %q", want)
		}
	}
	if args[len(args)-1] != s.OutputURL {
		t.Fatal("output must be last")
	}
	for _, arg := range args {
		if strings.Contains(arg, "scale=") && (!strings.Contains(arg, "min(iw,1280)") || !strings.Contains(arg, "force_divisible_by=2")) {
			t.Fatal("unsafe scale")
		}
	}
	s.AudioCodec = ""
	args, err = FFmpegArgs(s)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "-an") || slices.Contains(args, "-c:a") {
		t.Fatal("missing audio must not create an audio encoder")
	}
	s.VideoCodec = "h264"
	s.Profile.Video = VideoSpec{Codec: "copy"}
	args, err = FFmpegArgs(s)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, "-vf") {
		t.Fatal("copy must not filter")
	}
}
func TestFFmpegRejectsUnsafeSpec(t *testing.T) {
	for _, change := range []func(*JobSpec){
		func(s *JobSpec) { s.InputURL = "file:///etc/passwd" },
		func(s *JobSpec) { s.OutputURL = "/tmp/output.flv" },
		func(s *JobSpec) { s.OutputURL = "rtmp://localhost/live/x\n-y" },
		func(s *JobSpec) { s.Threads = 0 },
		func(s *JobSpec) { s.Profile.Backend = "gstreamer" },
	} {
		s := testSpec(t)
		change(&s)
		if _, err := FFmpegArgs(s); err == nil {
			t.Fatal("accepted invalid execution spec")
		}
	}
}
