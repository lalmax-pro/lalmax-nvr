package camera

// cam3* snapshot tests cover the periodic snapshot loop, the ONVIF snapshot
// path, stream fallback and the file-system error branches.

import (
	"context"
	"net"
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

func TestCam3SnapshotRunLoopCleansUp(t *testing.T) {
	root := t.TempDir()
	store, err := storage.NewManager(root)
	require.NoError(t, err)

	sm, err := NewSnapshotManager(SnapshotConfig{Enabled: true, Interval: "1m", Quality: 70, MaxAge: "1h"}, store, nil)
	require.NoError(t, err)
	// The production ticker is minute-granular; a short interval keeps the
	// periodic branch deterministic while the initial timer still applies.
	sm.interval = 10 * time.Millisecond

	snapshotDir := filepath.Join(root, "snapshots")
	require.NoError(t, os.MkdirAll(snapshotDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(snapshotDir, "nested"), 0o755))

	latest := filepath.Join(snapshotDir, "cam3_latest.jpg")
	require.NoError(t, os.WriteFile(latest, []byte("latest"), 0o644))
	stale := filepath.Join(snapshotDir, "cam3_url_old.jpg")
	require.NoError(t, os.WriteFile(stale, []byte("stale"), 0o644))
	past := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(latest, past, past))
	require.NoError(t, os.Chtimes(stale, past, past))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, sm.Start(ctx))
	t.Cleanup(sm.Stop)

	// The stale snapshot is removed by the first periodic cleanup.
	cam3WaitFor(t, 30*time.Second, func() bool {
		_, statErr := os.Stat(stale)
		return os.IsNotExist(statErr)
	})
	sm.Stop()

	require.FileExists(t, latest, "_latest.jpg snapshots are never removed")
	require.DirExists(t, filepath.Join(snapshotDir, "nested"), "directories are skipped")
}

func TestCam3SnapshotRunReturnsOnStopAndContext(t *testing.T) {
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)

	stopped, err := NewSnapshotManager(SnapshotConfig{Enabled: true, Interval: "1m"}, store, nil)
	require.NoError(t, err)
	close(stopped.stopCh)
	// A closed stop channel makes the initial select deterministic.
	stopped.run(context.Background())

	cancelled, err := NewSnapshotManager(SnapshotConfig{Enabled: true, Interval: "1m"}, store, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled.run(ctx)
}

func TestCam3TakeCameraSnapshotFromONVIF(t *testing.T) {
	dev := cam3NewFakeONVIF(t, &cam3FakeONVIF{
		Profiles: []cam3Profile{{Token: "main", Name: "MainStream", Encoding: "h264", VideoSource: "vsrc"}},
		Snapshots: map[string]string{
			"main":    cam3BaseToken + "/snapshot.jpg",
			"token_0": cam3BaseToken + "/snapshot.jpg",
		},
	})
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	sm, err := NewSnapshotManager(SnapshotConfig{Quality: 80, MaxAge: "24h"}, store, nil)
	require.NoError(t, err)

	sm.takeCameraSnapshot(context.Background(), config.CameraConfig{
		ID:            "cam3-onvif-snap",
		ONVIFEndpoint: dev.URL(),
		ProfileToken:  "main",
	})
	require.FileExists(t, sm.GetSnapshotPath("cam3-onvif-snap"))

	// Without a profile token the default profile is used.
	sm.takeCameraSnapshot(context.Background(), config.CameraConfig{
		ID:            "cam3-onvif-default",
		ONVIFEndpoint: dev.URL(),
	})
	require.FileExists(t, sm.GetSnapshotPath("cam3-onvif-default"))
}

func TestCam3TakeCameraSnapshotAllMethodsFail(t *testing.T) {
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	sm, err := NewSnapshotManager(SnapshotConfig{Quality: 80, MaxAge: "24h"}, store, nil)
	require.NoError(t, err)

	// ONVIF connect fails and no media engine exists, so every method fails.
	sm.takeCameraSnapshot(context.Background(), config.CameraConfig{
		ID:            "cam3-no-method",
		ONVIFEndpoint: "http://127.0.0.1:1/onvif/device_service",
	})
	require.Empty(t, sm.GetSnapshotPath("cam3-no-method"))

	// A configured snapshot URL that refuses the connection also falls through.
	sm.takeCameraSnapshot(context.Background(), config.CameraConfig{
		ID:          "cam3-bad-url",
		SnapshotURL: "http://127.0.0.1:1/snap.jpg",
	})
	require.Empty(t, sm.GetSnapshotPath("cam3-bad-url"))

	_, err = sm.takeURLSnapshot("http://127.0.0.1:1/snap.jpg")
	require.Error(t, err)
}

func TestCam3TakeCameraSnapshotFromStreamLoopback(t *testing.T) {
	payload := cam3JPEGBytes(t)

	listener, err := net.Listen("tcp", "127.0.0.1:1290")
	if err != nil {
		t.Skipf("loopback port 1290 unavailable: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/snap/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/snap/cam3-fail" {
			http.Error(w, "no stream", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/api/snap/cam3-truncated" {
			w.Header().Set("Content-Length", "1024")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("short"))
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(payload)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})

	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	sm, err := NewSnapshotManager(SnapshotConfig{Quality: 80, MaxAge: "24h"}, store, &stubMediaEngine{})
	require.NoError(t, err)

	data, err := sm.takeStreamSnapshot(context.Background(), "cam3-stream")
	require.NoError(t, err)
	require.Equal(t, payload, data)

	_, err = sm.takeStreamSnapshot(context.Background(), "cam3-fail")
	require.ErrorContains(t, err, "HTTP 503")

	_, err = sm.takeStreamSnapshot(context.Background(), "cam3-truncated")
	require.Error(t, err)

	sm.takeCameraSnapshot(context.Background(), config.CameraConfig{ID: "cam3-stream-cam"})
	require.FileExists(t, sm.GetSnapshotPath("cam3-stream-cam"))
}

func TestCam3SnapshotFilesystemErrors(t *testing.T) {
	// A file where the snapshot directory is expected breaks creation and
	// directory listing.
	root := t.TempDir()
	store, err := storage.NewManager(root)
	require.NoError(t, err)
	sm, err := NewSnapshotManager(SnapshotConfig{Quality: 80, MaxAge: "1h"}, store, nil)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "snapshots"), []byte("not a dir"), 0o644))

	require.Error(t, sm.saveSnapshot("cam3-broken", cam3JPEGBytes(t), "url"))
	sm.cleanupOldSnapshots(context.Background())

	// Missing snapshot directory and latest-path-is-a-directory branches.
	missingRoot := t.TempDir()
	missingStore, err := storage.NewManager(missingRoot)
	require.NoError(t, err)
	missingMgr, err := NewSnapshotManager(SnapshotConfig{Quality: 80, MaxAge: "1h"}, missingStore, nil)
	require.NoError(t, err)
	missingMgr.cleanupOldSnapshots(context.Background())

	usableRoot := t.TempDir()
	usableStore, err := storage.NewManager(usableRoot)
	require.NoError(t, err)
	usableMgr, err := NewSnapshotManager(SnapshotConfig{Quality: 80, MaxAge: "1h"}, usableStore, nil)
	require.NoError(t, err)
	// Non-JPEG input is stored as-is instead of being re-encoded.
	require.NoError(t, usableMgr.saveSnapshot("cam3-raw", []byte("raw-bytes"), "url"))
	require.FileExists(t, usableMgr.GetSnapshotPath("cam3-raw"))
	require.NoError(t, os.MkdirAll(filepath.Join(usableRoot, "snapshots", "cam3-dirlatest_latest.jpg"), 0o755))
	require.Error(t, usableMgr.saveSnapshot("cam3-dirlatest", cam3JPEGBytes(t), "url"))

	// A read-only snapshot directory makes removal fail; the error is logged
	// and the walk continues.
	root2 := t.TempDir()
	store2, err := storage.NewManager(root2)
	require.NoError(t, err)
	sm2, err := NewSnapshotManager(SnapshotConfig{Quality: 80, MaxAge: "1h"}, store2, nil)
	require.NoError(t, err)
	dir := filepath.Join(root2, "snapshots")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	stale := filepath.Join(dir, "cam3_url_old.jpg")
	require.NoError(t, os.WriteFile(stale, []byte("old"), 0o644))
	past := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(stale, past, past))
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	sm2.cleanupOldSnapshots(context.Background())
}

func TestCam3TakeURLSnapshotReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("short"))
	}))
	t.Cleanup(server.Close)

	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	sm, err := NewSnapshotManager(SnapshotConfig{Quality: 80}, store, nil)
	require.NoError(t, err)

	_, err = sm.takeURLSnapshot(server.URL + "/truncated.jpg")
	require.Error(t, err)
}
