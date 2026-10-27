// Package transcode manages derived media outputs independently of their consumers.
package transcode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
)

// Error contains only safe, actionable information suitable for API responses.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string     { return e.Code + ": " + e.Message }
func invalid(message string) error { return &Error{Code: "invalid_spec", Message: message} }

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// Profile intentionally has no arbitrary command, URL or pipeline fields.
// Only the fixed software encoding paths are accepted in the first version.
type Profile struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Revision uint64    `json:"revision"`
	Backend  string    `json:"backend"`
	Video    VideoSpec `json:"video"`
	Audio    AudioSpec `json:"audio"`
}
type VideoSpec struct {
	Codec       string `json:"codec"`
	Encoder     string `json:"encoder"`
	MaxWidth    int    `json:"max_width"`
	MaxHeight   int    `json:"max_height"`
	FPS         int    `json:"fps"`
	BitrateKbps int    `json:"bitrate_kbps"`
	GOPFrames   int    `json:"gop_frames"`
}
type AudioSpec struct {
	Codec       string `json:"codec"`
	SampleRate  int    `json:"sample_rate"`
	Channels    int    `json:"channels"`
	BitrateKbps int    `json:"bitrate_kbps"`
}

func (p Profile) Validate() error {
	if !identifier.MatchString(p.ID) {
		return invalid("profile id must contain 1–64 letters, digits, underscores or hyphens and start with a letter or digit")
	}
	if len([]rune(p.Name)) > 128 {
		return invalid("profile name is too long")
	}
	if p.Backend != "ffmpeg" && p.Backend != "gstreamer" {
		return invalid("backend must be ffmpeg or gstreamer")
	}
	v := p.Video
	switch v.Codec {
	case "copy":
		if v.Encoder != "" || v.MaxWidth != 0 || v.MaxHeight != 0 || v.FPS != 0 || v.BitrateKbps != 0 || v.GOPFrames != 0 {
			return invalid("video copy cannot apply encoder, resizing, frame rate, bitrate or GOP settings")
		}
	case "h264":
		encoder := "libx264"
		if p.Backend == "gstreamer" {
			encoder = "x264enc"
		}
		if v.Encoder != encoder {
			return invalid("unsupported video encoder for selected backend")
		}
		if v.MaxWidth < 2 || v.MaxWidth > 3840 || v.MaxWidth%2 != 0 || v.MaxHeight < 2 || v.MaxHeight > 2160 || v.MaxHeight%2 != 0 {
			return invalid("maximum dimensions must be even and within 3840x2160")
		}
		if v.FPS < 1 || v.FPS > 60 || v.BitrateKbps < 64 || v.BitrateKbps > 20000 || v.GOPFrames < 1 || v.GOPFrames > 600 {
			return invalid("video rate, bitrate or GOP is outside supported bounds")
		}
	default:
		return invalid("video codec must be h264 or copy")
	}
	a := p.Audio
	switch a.Codec {
	case "none", "copy":
		if a.SampleRate != 0 || a.Channels != 0 || a.BitrateKbps != 0 {
			return invalid("audio none/copy cannot apply encoding settings")
		}
	case "aac":
		if (a.SampleRate != 44100 && a.SampleRate != 48000) || a.Channels < 1 || a.Channels > 2 || a.BitrateKbps < 32 || a.BitrateKbps > 320 {
			return invalid("unsupported AAC sample rate, channels or bitrate")
		}
	default:
		return invalid("audio codec must be aac, copy or none")
	}
	return nil
}

// EffectiveHash excludes display metadata and revision: equivalent media work is
// shared even when independently named profiles describe the same output.
func (p Profile) EffectiveHash() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(struct {
		Backend string
		Video   VideoSpec
		Audio   AudioSpec
	}{p.Backend, p.Video, p.Audio})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

type SourceRef struct {
	EngineID string `json:"engine_id"`
	StreamID string `json:"stream_id"`
	AppName  string `json:"app_name"`
}

// Validate rejects source identifiers that could change a generated media URL.
func (s SourceRef) Validate() error {
	if !identifier.MatchString(s.EngineID) {
		return invalid("invalid media engine identity")
	}
	for _, value := range []string{s.StreamID, s.AppName} {
		if strings.TrimSpace(value) == "" || len(value) > 512 || strings.ContainsAny(value, "/?#\\\r\n\x00") {
			return invalid("source stream and application must be nonempty media identifiers without URL delimiters")
		}
	}
	return nil
}

// WorkKey includes application and engine to prevent cross-source collisions.
func WorkKey(source SourceRef, profile Profile) (string, error) {
	if err := source.Validate(); err != nil {
		return "", err
	}
	hash, err := profile.EffectiveHash()
	if err != nil {
		return "", err
	}
	data, err := json.Marshal([]string{source.EngineID, source.AppName, source.StreamID, hash})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

type RunPolicy string

const (
	Always RunPolicy = "always"
	Manual RunPolicy = "manual"
)

type Job struct {
	ID              string    `json:"id"`
	Source          SourceRef `json:"source"`
	ProfileID       string    `json:"profile_id"`
	ProfileRevision uint64    `json:"profile_revision"`
	Enabled         bool      `json:"enabled"`
	RunPolicy       RunPolicy `json:"run_policy"`
	Generation      uint64    `json:"generation"`
	StoppedByUser   bool      `json:"stopped_by_user,omitempty"`
}

func (j Job) Validate() error {
	if !identifier.MatchString(j.ID) || !identifier.MatchString(j.ProfileID) {
		return invalid("invalid job or profile id")
	}
	if err := j.Source.Validate(); err != nil {
		return err
	}
	if j.RunPolicy != Always && j.RunPolicy != Manual {
		return invalid("only always and manual policies are supported")
	}
	if j.ProfileRevision == 0 {
		return invalid("profile revision is required")
	}
	return nil
}

type State string

const (
	Disabled        State = "disabled"
	Stopped         State = "stopped"
	WaitingSource   State = "waiting_source"
	PendingCapacity State = "pending_capacity"
	Starting        State = "starting"
	Running         State = "running"
	Backoff         State = "backoff"
	Stopping        State = "stopping"
	Failed          State = "failed"
)

// CheckMedia validates copy modes against observed input codecs. Copy is not a
// promise that any input is compatible with the fixed H.264/AAC FLV output path.
func (p Profile) CheckMedia(videoCodec, audioCodec string) error {
	videoCodec = strings.ToLower(strings.TrimSpace(videoCodec))
	audioCodec = strings.ToLower(strings.TrimSpace(audioCodec))
	if err := p.Validate(); err != nil {
		return err
	}
	if videoCodec != "h264" && videoCodec != "h265" && videoCodec != "hevc" {
		return &Error{Code: "unsupported_input", Message: "input video must be H.264 or H.265"}
	}
	if p.Video.Codec == "copy" && videoCodec != "h264" {
		return &Error{Code: "unsupported_input", Message: "video copy requires H.264 for this output"}
	}
	if p.Audio.Codec == "copy" && audioCodec != "" && audioCodec != "aac" {
		return &Error{Code: "unsupported_input", Message: "audio copy requires AAC for this output"}
	}
	if p.Audio.Codec == "aac" && audioCodec != "" && audioCodec != "aac" && audioCodec != "g711a" && audioCodec != "g711u" {
		return &Error{Code: "unsupported_input", Message: "audio codec is not supported by the fixed template"}
	}
	return nil
}
