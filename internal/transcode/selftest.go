package transcode

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// A small synthetic H.264/AAC fixture allows copy paths to be tested without
// requiring an H.264 encoder merely to construct the probe input.
//
//go:embed testdata/h264-aac.flv
var ffmpegCopyFixture []byte

// ProbeFFmpeg verifies a bounded synthetic encode, ffprobe inspection and decode.
// It never connects to a camera, publishes media or starts recording.
func ProbeFFmpeg(ctx context.Context, paths ToolPaths, p Profile, runner ProbeRunner) (CapabilityReport, error) {
	if p.Backend != "ffmpeg" {
		return CapabilityReport{}, invalid("FFmpeg self-test requires an FFmpeg profile")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	report, err := Discover(ctx, paths, p, runner)
	if err != nil {
		return report, err
	}
	setResult := func(status CheckStatus, detail string) {
		for i := range report.Checks {
			if report.Checks[i].Name == "media_self_test" {
				report.Checks[i].Status = status
				report.Checks[i].Detail = detail
			}
		}
	}
	for _, check := range report.Checks {
		if check.Status == CheckFailed {
			setResult(NotTested, "Fix failed environment checks before testing media")
			return report, nil
		}
	}
	dir, err := os.MkdirTemp("", "nvr-transcode-probe-")
	if err != nil {
		return report, &Error{Code: "probe_storage_failed", Message: "temporary probe storage is unavailable"}
	}
	// dir is the exact absolute path allocated by MkdirTemp, never user supplied.
	defer os.RemoveAll(dir)
	output := filepath.Join(dir, "probe.flv")
	usesFixture := p.Video.Codec == "copy" || p.Audio.Codec == "copy"
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	if usesFixture {
		fixture := filepath.Join(dir, "input.flv")
		if err := os.WriteFile(fixture, ffmpegCopyFixture, 0600); err != nil {
			return report, &Error{Code: "probe_storage_failed", Message: "temporary probe storage is unavailable"}
		}
		args = append(args, "-i", fixture)
	} else {
		args = append(args, "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=15")
	}
	if (p.Audio.Codec == "aac" || p.Audio.Codec == "g711a") && !usesFixture {
		args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000")
	}
	args = append(args, "-t", "0.5", "-map", "0:v:0")
	args = append(args, ffmpegVideoArgs(p.Video, "1")...)
	if p.Audio.Codec == "aac" || p.Audio.Codec == "g711a" {
		audioInput := "1:a:0"
		if usesFixture {
			audioInput = "0:a:0"
		}
		if p.Audio.Codec == "g711a" {
			args = append(args, "-map", audioInput, "-c:a", "pcm_alaw", "-ar", "8000", "-ac", "1")
		} else {
			args = append(args, "-map", audioInput, "-c:a", "aac", "-ar", strconv.Itoa(p.Audio.SampleRate), "-ac", strconv.Itoa(p.Audio.Channels), "-b:a", strconv.Itoa(p.Audio.BitrateKbps)+"k")
		}
	} else if p.Audio.Codec == "copy" {
		args = append(args, "-map", "0:a:0", "-c:a", "copy")
	} else {
		args = append(args, "-an")
	}
	args = append(args, "-f", "flv", output)
	if _, err := runner.Run(ctx, paths.FFmpeg, args...); err != nil {
		setResult(CheckFailed, "Synthetic encoding failed; check encoder initialization and filters")
		return report, nil
	}
	data, err := runner.Run(ctx, paths.FFprobe, "-v", "error", "-show_streams", "-of", "json", output)
	if err != nil {
		setResult(CheckFailed, "Encoded output could not be inspected")
		return report, nil
	}
	if !validProbeMedia(data, p) {
		setResult(CheckFailed, "Encoded output does not match the required video/audio specification")
		return report, nil
	}
	if _, err := runner.Run(ctx, paths.FFmpeg, "-hide_banner", "-nostdin", "-v", "error", "-xerror", "-i", output, "-f", "null", "-"); err != nil {
		setResult(CheckFailed, "Encoded output failed actual decoding")
		return report, nil
	}
	setResult(Passed, "Synthetic media processing, inspection and decode passed; engine roundtrip and production capacity remain unverified")
	report.CanEnable = true
	return report, nil
}

func validProbeMedia(data []byte, p Profile) bool {
	var media struct {
		Streams []struct {
			CodecType  string `json:"codec_type"`
			CodecName  string `json:"codec_name"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
		} `json:"streams"`
	}
	if json.Unmarshal(data, &media) != nil {
		return false
	}
	video, audio := false, false
	for _, s := range media.Streams {
		switch s.CodecType {
		case "video":
			video = s.CodecName == "h264" && s.Width > 0 && s.Height > 0 && s.Width%2 == 0 && s.Height%2 == 0
			if p.Video.Codec != "copy" {
				video = video && s.Width <= p.Video.MaxWidth && s.Height <= p.Video.MaxHeight
			}
		case "audio":
			if p.Audio.Codec == "g711a" {
				audio = s.CodecName == "pcm_alaw" && s.Channels == 1 && s.SampleRate == "8000"
			} else if p.Audio.Codec == "copy" {
				audio = s.CodecName == "aac" && s.Channels == 1 && s.SampleRate == "48000"
			} else {
				audio = s.CodecName == "aac" && s.Channels == p.Audio.Channels && s.SampleRate == strconv.Itoa(p.Audio.SampleRate)
			}
		}
	}
	return video && (p.Audio.Codec == "none" || audio)
}
