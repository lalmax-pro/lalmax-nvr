package camera

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 40, B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}))
	return buf.Bytes()
}

func TestDefaultSnapshotConfig(t *testing.T) {
	cfg := DefaultSnapshotConfig()
	require.False(t, cfg.Enabled)
	require.Equal(t, "5m", cfg.Interval)
	require.Equal(t, 80, cfg.Quality)
	require.Equal(t, "24h", cfg.MaxAge)
}

func TestNewSnapshotManagerNormalizesInvalidSettings(t *testing.T) {
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	sm, err := NewSnapshotManager(SnapshotConfig{Interval: "bad", MaxAge: "also-bad", Quality: 0}, store, nil)
	require.NoError(t, err)
	require.Equal(t, 5*time.Minute, sm.interval)
	require.Equal(t, 24*time.Hour, sm.maxAge)
	require.Equal(t, 80, sm.quality)

	sm, err = NewSnapshotManager(SnapshotConfig{Interval: "10s", MaxAge: "2h", Quality: 101}, store, nil)
	require.NoError(t, err)
	require.Equal(t, time.Minute, sm.interval)
	require.Equal(t, 2*time.Hour, sm.maxAge)
	require.Equal(t, 80, sm.quality)
}

func TestSnapshotManagerStartStopWithoutCameras(t *testing.T) {
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	disabled, err := NewSnapshotManager(DefaultSnapshotConfig(), store, nil)
	require.NoError(t, err)
	require.NoError(t, disabled.Start(context.Background()))
	disabled.Stop()

	enabled, err := NewSnapshotManager(SnapshotConfig{Enabled: true, Interval: "1m", Quality: 70, MaxAge: "1h"}, store, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, enabled.Start(ctx))
	require.NoError(t, enabled.Start(ctx))
	enabled.Stop()
	enabled.Stop()
}

func TestSanitizeFilenameAndSnapshotURL(t *testing.T) {
	require.Equal(t, "cam_1_front", sanitizeFilename(`cam/1:front`))
	require.Equal(t, "a_b_c_d_e_f_g", sanitizeFilename(`a\b:c*d?e"f<g`))
	require.Equal(t, "keep", replaceAll("keep", "", "x"))

	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	sm, err := NewSnapshotManager(DefaultSnapshotConfig(), store, nil)
	require.NoError(t, err)
	require.Equal(t, "/api/snapshots/cam", sm.GetSnapshotURL("cam"))
	require.Empty(t, sm.GetSnapshotPath("cam"))
}

func TestTakeURLSnapshotAndSaveCleanup(t *testing.T) {
	payload := jpegBytes(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.jpg":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
		case "/fail.jpg":
			http.Error(w, "nope", http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	store, err := storage.NewManager(root)
	require.NoError(t, err)
	sm, err := NewSnapshotManager(SnapshotConfig{Quality: 50, MaxAge: "1h"}, store, nil)
	require.NoError(t, err)

	_, err = sm.takeURLSnapshot(server.URL + "/fail.jpg")
	require.ErrorContains(t, err, "HTTP 404")
	data, err := sm.takeURLSnapshot(server.URL + "/ok.jpg")
	require.NoError(t, err)
	require.Equal(t, payload, data)

	require.NoError(t, sm.saveSnapshot("cam/front", data, "url"))
	latest := sm.GetSnapshotPath("cam/front")
	require.FileExists(t, latest)

	snapshotDir := filepath.Join(root, "snapshots")
	stale := filepath.Join(snapshotDir, "cam_front_url_old.jpg")
	require.NoError(t, os.WriteFile(stale, payload, 0o644))
	past := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(stale, past, past))
	sm.cleanupOldSnapshots(context.Background())
	require.NoFileExists(t, stale)
	require.FileExists(t, latest)
}

func TestTakeCameraSnapshotFallsBackToURL(t *testing.T) {
	payload := jpegBytes(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)

	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	sm, err := NewSnapshotManager(SnapshotConfig{Quality: 80, MaxAge: "24h"}, store, nil)
	require.NoError(t, err)
	sm.takeCameraSnapshot(context.Background(), config.CameraConfig{
		ID:          "cam1",
		SnapshotURL: server.URL + "/snap.jpg",
	})
	require.FileExists(t, sm.GetSnapshotPath("cam1"))
}

func TestTakeStreamSnapshotWithoutEngine(t *testing.T) {
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	sm, err := NewSnapshotManager(DefaultSnapshotConfig(), store, nil)
	require.NoError(t, err)
	_, err = sm.takeStreamSnapshot(context.Background(), "cam")
	require.ErrorContains(t, err, "media engine not available")
}

func TestOptimizeImageRejectsInvalidJPEG(t *testing.T) {
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	sm, err := NewSnapshotManager(DefaultSnapshotConfig(), store, nil)
	require.NoError(t, err)
	_, err = sm.optimizeImage([]byte("not-a-jpeg"))
	require.Error(t, err)
}
