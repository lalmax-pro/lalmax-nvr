package transcode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// ProbeGStreamer executes a bounded local capability self-test, not a source
// integration test. Real-source roundtrip remains separately not_tested.
func ProbeGStreamer(ctx context.Context, paths ToolPaths, p Profile, runner ProbeRunner) (CapabilityReport, error) {
	if p.Backend != "gstreamer" {
		return CapabilityReport{}, invalid("GStreamer profile required")
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	report, err := Discover(ctx, paths, p, runner)
	if err != nil {
		return report, err
	}
	result := func(status CheckStatus, detail string) {
		for i := range report.Checks {
			if report.Checks[i].Name == "media_self_test" {
				report.Checks[i].Status = status
				report.Checks[i].Detail = detail
			}
		}
	}
	for _, check := range report.Checks {
		if check.Status == CheckFailed {
			result(NotTested, "Fix environment checks before media self-test")
			return report, nil
		}
	}
	if p.Video.Codec == "copy" || p.Audio.Codec == "copy" {
		result(NotTested, "Copy mode requires a compatible source fixture")
		return report, nil
	}
	dir, err := os.MkdirTemp("", "nvr-gst-probe-")
	if err != nil {
		return report, &Error{Code: "probe_storage_failed", Message: "temporary storage unavailable"}
	}
	// This exact path is allocated by MkdirTemp, not supplied by the client.
	defer os.RemoveAll(dir)
	output := filepath.Join(dir, "probe.flv")
	args, err := gstreamerSelfTestArgs(p, output)
	if err != nil {
		return report, err
	}
	if _, err = runner.Run(ctx, paths.GSTLaunch, args...); err != nil {
		result(CheckFailed, "GStreamer synthetic encoding failed or timed out")
		return report, nil
	}
	data, err := runner.Run(ctx, paths.FFprobe, "-v", "error", "-show_streams", "-of", "json", output)
	if err != nil || !validProbeMedia(data, p) {
		result(CheckFailed, "GStreamer output failed media inspection")
		return report, nil
	}
	if _, err = runner.Run(ctx, paths.GSTLaunch, "-e", "filesrc", gstProperty("location", output), "!", "decodebin", "name=d", "d.", "!", "queue", "!", "videoconvert", "!", "fakesink", "sync=false"); err != nil {
		result(CheckFailed, "GStreamer output failed decoding")
		return report, nil
	}
	// Decode the audio separately so an unlinked audio track cannot be mistaken
	// for a verified audio decode merely because the video sink received buffers.
	if p.Audio.Codec == "aac" || p.Audio.Codec == "g711a" {
		if _, err = runner.Run(ctx, paths.GSTLaunch, "-e", "filesrc", gstProperty("location", output), "!", "decodebin", "name=d", "d.", "!", "queue", "!", "audioconvert", "!", "fakesink", "sync=false"); err != nil {
			result(CheckFailed, "GStreamer audio output failed decoding")
			return report, nil
		}
	}
	result(Passed, "GStreamer local encode, inspection and decode passed; source roundtrip remains unverified")
	report.CanEnable = true
	return report, nil
}
func gstreamerSelfTestArgs(p Profile, output string) ([]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.Backend != "gstreamer" || p.Video.Codec != "h264" || p.Audio.Codec == "copy" {
		return nil, invalid("unsupported GStreamer self-test template")
	}
	w, h, err := outputDimensions(320, 240, p.Video)
	if err != nil {
		return nil, err
	}
	v := p.Video
	args := []string{"-e", "flvmux", "name=mux", "streamable=true", "!", "filesink", gstProperty("location", output), "videotestsrc", "num-buffers=15", "!", fmt.Sprintf("video/x-raw,format=I420,width=%d,height=%d,framerate=%d/1", w, h, v.FPS), "!", "x264enc", "tune=zerolatency", "speed-preset=veryfast", "threads=1", "bitrate=" + strconv.Itoa(v.BitrateKbps), "key-int-max=" + strconv.Itoa(v.GOPFrames), "!", "h264parse", "!", "video/x-h264,stream-format=avc,alignment=au", "!", "queue", "!", "mux.video"}
	if p.Audio.Codec == "aac" || p.Audio.Codec == "g711a" {
		a := p.Audio
		args = append(args, "audiotestsrc", "num-buffers=24", "!", "audioconvert", "!", "audioresample", "!", fmt.Sprintf("audio/x-raw,rate=%d,channels=%d", a.SampleRate, a.Channels))
		if a.Codec == "g711a" {
			args = append(args, "!", "alawenc")
		} else {
			args = append(args, "!", "avenc_aac", "bitrate="+strconv.Itoa(a.BitrateKbps*1000), "!", "aacparse")
		}
		args = append(args, "!", "queue", "!", "mux.audio")
	}
	return args, nil
}
