package config

import (
	"gopkg.in/yaml.v3"
	"testing"
)

func TestTranscodingConfigDefaultsAndValidation(t *testing.T) {
	var cfg Config
	cfg.ApplyDefaults()
	if cfg.Transcoding.Enabled {
		t.Fatal("transcoding must remain opt-in")
	}
	if cfg.Transcoding.MaxConcurrent != 2 || cfg.Transcoding.ThreadsPerJob != 2 {
		t.Fatal("missing bounded defaults")
	}
	// Missing executables must not make validation fail: probing diagnoses these.
	cfg.Transcoding.FFmpegPath = "/missing/ffmpeg"
	if err := cfg.Transcoding.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Transcoding.MaxConcurrent = 33
	if err := Validate(&cfg); err == nil {
		t.Fatal("unbounded concurrency accepted")
	}
}

func TestTranscodingYAMLPathsRoundTrip(t *testing.T) {
	var cfg Config
	input := []byte("transcoding:\n  enabled: false\n  ffmpeg_path: /opt/media/ffmpeg\n  gst_launch_path: /opt/media/gst-launch-1.0\n  max_concurrent: 3\n  worker_enabled: true\n")
	if err := yaml.Unmarshal(input, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.ApplyDefaults()
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var restored Config
	if err := yaml.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Transcoding != cfg.Transcoding {
		t.Fatal("transcoding settings lost on save")
	}
	if !restored.Transcoding.WorkerEnabled { t.Fatal("worker mode lost during YAML roundtrip") }
	if restored.Transcoding.Tools().GSTLaunch != "/opt/media/gst-launch-1.0" {
		t.Fatal("configured path ignored")
	}
}
