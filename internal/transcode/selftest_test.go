package transcode

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestProbeMediaValidation(t *testing.T) {
	p := testProfile(t)
	good := []byte(`{"streams":[{"codec_type":"video","codec_name":"h264","width":320,"height":240},{"codec_type":"audio","codec_name":"aac","sample_rate":"48000","channels":1}]}`)
	if !validProbeMedia(good, p) {
		t.Fatal("valid output rejected")
	}
	for _, bad := range []string{`{}`, `{"streams":[]}`, strings.ReplaceAll(string(good), "h264", "hevc"), strings.ReplaceAll(string(good), "48000", "8000"), strings.ReplaceAll(string(good), "320", "9999")} {
		if validProbeMedia([]byte(bad), p) {
			t.Fatal("invalid output accepted")
		}
	}
}
func TestBoundedProbeOutput(t *testing.T) {
	b := &boundedOutput{limit: 4}
	if n, err := b.Write([]byte("123456")); n != 6 || err != nil {
		t.Fatal("must drain entire write")
	}
	_, _ = b.Write([]byte("789"))
	if string(b.Bytes()) != "1234" {
		t.Fatal("output exceeded bound")
	}
}
func TestFFmpegRealSelfTest(t *testing.T) {
	if os.Getenv("NVR_TRANSCODE_MEDIA_TEST") != "1" {
		t.Skip("set NVR_TRANSCODE_MEDIA_TEST=1 to run installed FFmpeg")
	}
	ffmpeg, err := ResolveTool("", "ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ffprobe, err := ResolveTool("", "ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	for _, audio := range []string{"aac", "none"} {
		t.Run(audio, func(t *testing.T) {
			p := testProfile(t)
			if audio == "none" {
				p.Audio = AudioSpec{Codec: "none"}
			}
			report, err := ProbeFFmpeg(context.Background(), ToolPaths{FFmpeg: ffmpeg, FFprobe: ffprobe}, p, CommandProbeRunner{})
			if err != nil {
				t.Fatal(err)
			}
			if !report.CanEnable {
				t.Fatalf("self-test failed: %+v", report.Checks)
			}
			for _, check := range report.Checks {
				if check.Name == "engine_roundtrip" && check.Status != NotTested {
					t.Fatal("self-test must not claim network validation")
				}
			}
		})
	}
}
