package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
)

func TestSnapshotHonorsExplicitFFmpegPath(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Transcoding.FFmpegPath = executable
	h := &Handler{config: cfg}
	got, err := h.resolveSnapshotFFmpegPath()
	if err != nil || got != executable {
		t.Fatalf("explicit tool not honored: %s %v", got, err)
	}
	cfg.Transcoding.FFmpegPath = filepath.Join(t.TempDir(), "missing-ffmpeg")
	if got, err = h.resolveSnapshotFFmpegPath(); err == nil || got != "" {
		t.Fatal("invalid explicit tool silently fell back to PATH")
	}
}
