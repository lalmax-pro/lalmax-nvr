package transcode

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// JobSpec is resolved by the trusted media adapter, never accepted from an API.
// URLs may contain credentials and must not be logged or serialized.
type JobSpec struct {
	Profile    Profile
	InputURL   string
	OutputURL  string
	VideoCodec string
	AudioCodec string
	Threads    int
}

func (s JobSpec) validate(backend string) error {
	if s.Profile.Backend != backend {
		return invalid("profile backend mismatch")
	}
	if err := s.Profile.CheckMedia(s.VideoCodec, s.AudioCodec); err != nil {
		return err
	}
	if s.Threads < 1 || s.Threads > 16 {
		return invalid("threads must be between 1 and 16")
	}
	for _, endpoint := range []struct{ raw, scheme string }{{s.InputURL, "rtsp"}, {s.OutputURL, "rtmp"}} {
		u, err := url.Parse(endpoint.raw)
		if err != nil || u.Scheme != endpoint.scheme || u.Hostname() == "" || u.Fragment != "" || strings.ContainsAny(endpoint.raw, "\r\n\x00") {
			return invalid("invalid media endpoint")
		}
	}
	return nil
}

// FFmpegArgs builds the fixed H.264/AAC FLV output path without a shell.
func FFmpegArgs(s JobSpec) ([]string, error) {
	if err := s.validate("ffmpeg"); err != nil {
		return nil, err
	}
	threads := strconv.Itoa(s.Threads)
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "warning", "-threads", threads, "-rtsp_transport", "tcp", "-i", s.InputURL, "-map", "0:v:0"}
	args = append(args, ffmpegVideoArgs(s.Profile.Video, threads)...)
	a := s.Profile.Audio
	if a.Codec == "none" || strings.TrimSpace(s.AudioCodec) == "" {
		args = append(args, "-an")
	} else {
		args = append(args, "-map", "0:a:0?")
		if a.Codec == "copy" {
			args = append(args, "-c:a", "copy")
		} else if a.Codec == "g711a" {
			// A-law is supported by the media engine's WHEP RTP packetizer.
			// No AAC fallback is allowed for WebRTC audio profiles.
			args = append(args, "-c:a", "pcm_alaw", "-ar", "8000", "-ac", "1")
		} else {
			args = append(args, "-c:a", "aac", "-ar", strconv.Itoa(a.SampleRate), "-ac", strconv.Itoa(a.Channels), "-b:a", fmt.Sprintf("%dk", a.BitrateKbps))
		}
	}
	args = append(args, "-f", "flv", s.OutputURL)
	return args, nil
}

func ffmpegVideoArgs(v VideoSpec, threads string) []string {
	if v.Codec == "copy" {
		return []string{"-c:v", "copy"}
	}
	// min(iw/ih, limit) prevents upscaling. Aspect ratio is retained and both
	// dimensions are rounded to multiples of two for yuv420p encoding.
	filter := fmt.Sprintf("scale=w='min(iw,%d)':h='min(ih,%d)':force_original_aspect_ratio=decrease:force_divisible_by=2,setsar=1", v.MaxWidth, v.MaxHeight)
	// Hardware path is named explicitly and cannot silently fall back to
	// libx264. Its availability is checked by the synthetic media probe.
	if v.Encoder == "h264_nvenc" {
		return []string{"-c:v", "h264_nvenc", "-preset", "p4", "-tune", "ll",
			"-rc", "cbr", "-pix_fmt", "yuv420p", "-vf", filter,
			"-r", strconv.Itoa(v.FPS), "-g", strconv.Itoa(v.GOPFrames),
			"-bf", "0", "-b:v", fmt.Sprintf("%dk", v.BitrateKbps),
			"-maxrate", fmt.Sprintf("%dk", v.BitrateKbps),
			"-bufsize", fmt.Sprintf("%dk", v.BitrateKbps*2)}
	}
	return []string{"-c:v", "libx264", "-threads", threads, "-filter_threads", threads, "-preset", "veryfast", "-tune", "zerolatency", "-pix_fmt", "yuv420p", "-vf", filter, "-r", strconv.Itoa(v.FPS), "-g", strconv.Itoa(v.GOPFrames), "-keyint_min", strconv.Itoa(v.GOPFrames), "-sc_threshold", "0", "-bf", "0", "-b:v", fmt.Sprintf("%dk", v.BitrateKbps), "-maxrate", fmt.Sprintf("%dk", v.BitrateKbps), "-bufsize", fmt.Sprintf("%dk", v.BitrateKbps*2)}
}
