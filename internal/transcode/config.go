package transcode

// Config contains trusted operator-controlled runtime settings, not task input.
type Config struct {
	Enabled        bool   `yaml:"enabled" json:"enabled"`
	MaxConcurrent  int    `yaml:"max_concurrent" json:"max_concurrent"`
	ThreadsPerJob  int    `yaml:"threads_per_job" json:"threads_per_job"`
	FFmpegPath     string `yaml:"ffmpeg_path" json:"ffmpeg_path"`
	FFprobePath    string `yaml:"ffprobe_path" json:"ffprobe_path"`
	GSTLaunchPath  string `yaml:"gst_launch_path" json:"gst_launch_path"`
	GSTInspectPath string `yaml:"gst_inspect_path" json:"gst_inspect_path"`
}

func (c Config) Tools() ToolPaths {
	// Leave missing tools as their intended executable names so probing produces
	// per-tool failures rather than preventing startup of the entire NVR.
	choose := func(value, fallback string) string {
		if value != "" {
			return value
		}
		return fallback
	}
	return ToolPaths{FFmpeg: choose(c.FFmpegPath, "ffmpeg"), FFprobe: choose(c.FFprobePath, "ffprobe"), GSTLaunch: choose(c.GSTLaunchPath, "gst-launch-1.0"), GSTInspect: choose(c.GSTInspectPath, "gst-inspect-1.0")}
}

func (c *Config) ApplyDefaults() {
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = 2
	}
	if c.ThreadsPerJob == 0 {
		c.ThreadsPerJob = 2
	}
}

func (c Config) Validate() error {
	if c.MaxConcurrent < 0 || c.MaxConcurrent > 32 {
		return invalid("max_concurrent must be between 1 and 32, or zero for the default")
	}
	if c.ThreadsPerJob < 0 || c.ThreadsPerJob > 16 {
		return invalid("threads_per_job must be between 1 and 16, or zero for the default")
	}
	return nil
}

// DefaultProfile is a safe explicit software template for interactive detection.
func DefaultProfile(backend string) Profile {
	encoder := "libx264"
	if backend == "gstreamer" {
		encoder = "x264enc"
	}
	return Profile{ID: "default_" + backend, Name: "H.264 720p / AAC", Revision: 1, Backend: backend, Video: VideoSpec{Codec: "h264", Encoder: encoder, MaxWidth: 1280, MaxHeight: 720, FPS: 15, BitrateKbps: 1500, GOPFrames: 30}, Audio: AudioSpec{Codec: "aac", SampleRate: 48000, Channels: 1, BitrateKbps: 64}}
}
