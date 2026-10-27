package transcode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

type CheckStatus string

const (
	Passed      CheckStatus = "passed"
	CheckFailed CheckStatus = "failed"
	NotTested   CheckStatus = "not_tested"
	Unsupported CheckStatus = "unsupported"
)

type CapabilityCheck struct {
	Name   string      `json:"name"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail"`
}
type CapabilityReport struct {
	Backend     string            `json:"backend"`
	ProfileID   string            `json:"profile_id"`
	Fingerprint string            `json:"fingerprint"`
	CheckedAt   time.Time         `json:"checked_at"`
	Checks      []CapabilityCheck `json:"checks"`
	// CanEnable is true only after real media self-test, never just discovery.
	CanEnable bool `json:"can_enable"`
	Stale     bool `json:"stale"`
}
type ToolPaths struct {
	FFmpeg     string `json:"ffmpeg"`
	FFprobe    string `json:"ffprobe"`
	GSTLaunch  string `json:"gst_launch"`
	GSTInspect string `json:"gst_inspect"`
}

type ProbeRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

// Discover checks tool execution and declared capabilities only. It deliberately
// does not claim that encoding, decoding or media-engine playback has passed.
func Discover(ctx context.Context, paths ToolPaths, profile Profile, runner ProbeRunner) (CapabilityReport, error) {
	if err := profile.Validate(); err != nil {
		return CapabilityReport{}, err
	}
	if runner == nil {
		return CapabilityReport{}, invalid("probe runner is required")
	}
	fingerprint, err := ProbeFingerprint(paths, profile)
	if err != nil {
		return CapabilityReport{}, err
	}
	report := CapabilityReport{Backend: profile.Backend, ProfileID: profile.ID, Fingerprint: fingerprint, CheckedAt: time.Now().UTC(), Checks: []CapabilityCheck{}}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	check := func(name, path string, args ...string) ([]byte, bool) {
		if path == "" {
			report.Checks = append(report.Checks, CapabilityCheck{name, CheckFailed, "Executable path is not configured"})
			return nil, false
		}
		if ctx.Err() != nil {
			report.Checks = append(report.Checks, CapabilityCheck{name, NotTested, "Probe deadline exceeded or request canceled"})
			return nil, false
		}
		out, err := runner.Run(ctx, path, args...)
		if err != nil {
			report.Checks = append(report.Checks, CapabilityCheck{name, CheckFailed, "Check failed; verify executable, dependencies and permissions"})
			return nil, false
		}
		report.Checks = append(report.Checks, CapabilityCheck{name, Passed, "Executable check completed"})
		return out, true
	}
	if profile.Backend == "ffmpeg" {
		check("ffmpeg", paths.FFmpeg, "-version")
		check("ffprobe", paths.FFprobe, "-version")
		out, ok := check("encoders", paths.FFmpeg, "-hide_banner", "-encoders")
		if ok {
			for _, encoder := range []string{profile.Video.Encoder, func() string {
				if profile.Audio.Codec == "aac" {
					return "aac"
				}
				return ""
			}()} {
				if encoder == "" {
					continue
				}
				status := CheckFailed
				detail := "Required encoder is not available"
				if encoderListed(string(out), encoder) {
					status = Passed
					detail = "Encoder is listed; runtime test still required"
				}
				report.Checks = append(report.Checks, CapabilityCheck{"encoder:" + encoder, status, detail})
			}
		}
	} else {
		check("gstreamer", paths.GSTLaunch, "--version")
		check("gst-inspect", paths.GSTInspect, "--version")
		elements := []string{"rtspsrc", "rtph264depay", "rtph265depay", "h264parse", "h265parse", "flvmux", "rtmpsink", "queue"}
		if profile.Video.Codec != "copy" {
			check("ffprobe_dimensions", paths.FFprobe, "-version")
			elements = append(elements, "avdec_h264", "avdec_h265", "videoconvert", "videoscale", "videorate", "x264enc")
		}
		if profile.Audio.Codec != "none" {
			elements = append(elements, "rtpmp4gdepay", "aacparse")
		}
		if profile.Audio.Codec == "aac" {
			elements = append(elements, "audioconvert", "audioresample", "avenc_aac", "avdec_aac", "rtppcmadepay", "rtppcmudepay", "alawdec", "mulawdec")
		}
		for _, element := range elements {
			check("element:"+element, paths.GSTInspect, element)
		}
	}
	report.Checks = append(report.Checks,
		CapabilityCheck{"media_self_test", NotTested, "Discovery alone does not verify actual encoding"},
		CapabilityCheck{"engine_roundtrip", NotTested, "Media engine publish and playback have not been tested"},
	)
	return report, nil
}

// ProbeFingerprint can be computed without executing external programs.
func ProbeFingerprint(paths ToolPaths, profile Profile) (string, error) {
	hash, err := profile.EffectiveHash()
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(struct {
		Paths   ToolPaths
		Profile string
	}{paths, hash})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func encoderListed(output, name string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == name {
			return true
		}
	}
	return false
}

// ResolveTool honors an explicit path without falling back to a different tool.
func ResolveTool(configured, fallback string) (string, error) {
	name := configured
	if name == "" {
		name = fallback
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", &Error{Code: "tool_unavailable", Message: "media tool is not executable or not installed"}
	}
	return path, nil
}
