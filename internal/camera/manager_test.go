package camera

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/lalmax-pro/lalmax-nvr/internal/recorder"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

type stubMediaEngine struct {
	startPulls   []media.StartPullRequest
	stopPulls    []string
	startErr     error
	stopErr      error
	activeStreams map[string]bool
}

func (s *stubMediaEngine) Start(context.Context) error    { return nil }
func (s *stubMediaEngine) Shutdown(context.Context) error { return nil }
func (s *stubMediaEngine) Ready(context.Context) error    { return nil }
func (s *stubMediaEngine) StartPull(_ context.Context, req media.StartPullRequest) (*media.StreamSession, error) {
	if s.startErr != nil {
		return nil, s.startErr
	}
	s.startPulls = append(s.startPulls, req)
	return &media.StreamSession{SessionID: "sess-" + req.StreamID, StreamID: req.StreamID}, nil
}
func (s *stubMediaEngine) StopPull(_ context.Context, streamID string) error {
	s.stopPulls = append(s.stopPulls, streamID)
	return s.stopErr
}
func (s *stubMediaEngine) StartRTPReceive(context.Context, media.StartRTPReceiveRequest) (*media.StreamSession, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) StopRTPReceive(context.Context, string) error {
	return errors.New("not implemented")
}
func (s *stubMediaEngine) KickSession(context.Context, string) error {
	return errors.New("not implemented")
}
func (s *stubMediaEngine) GetStream(_ context.Context, streamID string) (*media.StreamInfo, error) {
	if s.activeStreams != nil && s.activeStreams[streamID] {
		return &media.StreamInfo{StreamID: streamID, AppName: "live", Active: true}, nil
	}
	return nil, nil
}
func (s *stubMediaEngine) ListStreams(context.Context) ([]media.StreamInfo, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) BuildPlayURL(_ context.Context, req media.PlayURLRequest) (*media.PlayURL, error) {
	if req.Protocol == "rtsp" {
		return &media.PlayURL{URL: "rtsp://127.0.0.1/live/" + req.StreamID, Protocol: "rtsp"}, nil
	}
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) SubscribeEvents(context.Context, media.EventFilter) (<-chan media.Event, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) SubscribeRTMPEvents(context.Context) (<-chan media.RTMPEvent, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) SubscribeSRTEvents(context.Context) (<-chan media.SRTEvent, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) SubscribeWHIPEvents(context.Context) (<-chan media.WHIPEvent, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) AddCustomizePubSession(_ context.Context, _ string) (media.CustomizePubSession, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) DelCustomizePubSession(_ context.Context, _ media.CustomizePubSession) error {
	return errors.New("not implemented")
}

func testConfig() *config.Config {
	return &config.Config{
		Storage: config.StorageConfig{
			RootDir:         "/tmp/lalmax-nvr-test-camera",
			SegmentDuration: "1m",
		},
		Cameras: []config.CameraConfig{
			{
				ID:       "cam-h264",
				Name:     "H264 Camera",
				Protocol: "rtsp",
				Encoding: "h264",
				URL:      "rtsp://127.0.0.1:1/stream",
				Enabled:  true,
			},
			{
				ID:       "cam-mjpeg",
				Name:     "MJPEG Camera",
				Protocol: "rtsp",
				Encoding: "mjpeg",
				URL:      "rtsp://127.0.0.1:1/stream",
				Enabled:  true,
			},
			{
				ID:       "cam-disabled",
				Name:     "Disabled Camera",
				Protocol: "rtsp",
				Encoding: "h264",
				URL:      "rtsp://127.0.0.1:1/stream",
				Enabled:  false,
			},
			{
				ID:       "cam-jpeg",
				Name:     "JPEG Camera",
				Protocol: "http",
				Encoding: "jpeg",
				URL:      "http://192.168.1.13/jpg",
				Enabled:  true,
			},
		},
	}
}

func newTestManager(t *testing.T) (*CameraManager, *storage.Manager, *storage.DB, string) {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := testConfig()
	cfg.Storage.RootDir = filepath.Join(tmpDir, "storage")
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))
	require.NoError(t, config.Save(configPath, cfg))

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	require.NoError(t, db.Init(ctx))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	t.Cleanup(func() { store.CleanupTempFiles() })

	mgr := NewCameraManager(cfg, store, db, configPath)
	return mgr, store, db, configPath
}

func TestNewCameraManager(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig()
	cfg.Storage.RootDir = filepath.Join(tmpDir, "storage")
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")
	assert.NotNil(t, mgr)
	assert.Equal(t, 0, mgr.RecorderCount())
}

func TestStart_EnabledCameras(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.Start(ctx)
	require.NoError(t, err)

	// Should have created recorders for h264, mjpeg, and http_jpeg cameras
	// (disabled camera is skipped)
	assert.Equal(t, 3, mgr.RecorderCount())

	statuses := mgr.Status()
	assert.Len(t, statuses, 3)
	_, hasH264 := statuses["cam-h264"]
	_, hasMJPEG := statuses["cam-mjpeg"]
	assert.True(t, hasH264, "should have h264 recorder")
	assert.True(t, hasMJPEG, "should have mjpeg recorder")
	_, hasDisabled := statuses["cam-disabled"]
	assert.False(t, hasDisabled, "should not have disabled recorder")
	_, hasJPEG := statuses["cam-jpeg"]
	assert.True(t, hasJPEG, "should have http_jpeg recorder")
}

func TestStart_DisabledCamerasSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "1m",
		},
		Cameras: []config.CameraConfig{
			{
				ID:       "cam-1",
				Protocol: "rtsp",
				Encoding: "h264",
				URL:      "rtsp://192.168.1.10:554/stream",
				Enabled:  false,
			},
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = db.Init(ctx)

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, db, "")
	err = mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, mgr.RecorderCount())
}

func TestStart_HTTPJPEGRecorderCreated(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "1m",
		},
		Cameras: []config.CameraConfig{
			{
				ID:       "cam-1",
				Protocol: "http",
				Encoding: "jpeg",
				Enabled:  true,
			},
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = db.Init(ctx)

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, db, "")
	err = mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, mgr.RecorderCount())
	_, hasJPEG := mgr.Status()["cam-1"]
	assert.True(t, hasJPEG, "should have http_jpeg recorder")
}

func TestStart_InvalidSegmentDuration(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "not-a-duration",
		},
		Cameras: []config.CameraConfig{
			{
				ID:       "cam-1",
				Protocol: "rtsp",
				Encoding: "h264",
				Enabled:  true,
			},
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = db.Init(ctx)

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, db, "")
	err = mgr.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid segment duration")
}

func TestStop(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, mgr.RecorderCount())

	// Give recorders a moment to start their goroutines
	time.Sleep(100 * time.Millisecond)

	err = mgr.Stop()
	require.NoError(t, err)

	// After stop, recorders should still be in the map (not removed)
	assert.Equal(t, 3, mgr.RecorderCount())

	// Status should be stopped
	statuses := mgr.Status()
	for _, s := range statuses {
		assert.Equal(t, model.StatusStopped, s)
	}

	time.Sleep(100 * time.Millisecond)

	err = mgr.Stop()
	require.NoError(t, err)

	// After stop, recorders should still be in the map (not removed)
	assert.Equal(t, 3, mgr.RecorderCount())

	// Status should be stopped
}

func TestStop_EmptyManager(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig()
	cfg.Storage.RootDir = filepath.Join(tmpDir, "storage")
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")
	err = mgr.Stop()
	require.NoError(t, err)
}

func TestStatus_EmptyManager(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig()
	cfg.Storage.RootDir = filepath.Join(tmpDir, "storage")
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")
	statuses := mgr.Status()
	assert.NotNil(t, statuses)
	assert.Empty(t, statuses)
}

func TestCameraStatus_Unknown(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig()
	cfg.Storage.RootDir = filepath.Join(tmpDir, "storage")
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")
	status := mgr.CameraStatus("nonexistent")
	assert.Equal(t, model.StatusError, status)
}

func TestCameraStatus_Known(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.Start(ctx)
	require.NoError(t, err)

	status := mgr.CameraStatus("cam-h264")
	// Status will be recording or reconnecting (since no real RTSP server)
	assert.Contains(t, []model.RecorderStatus{
		model.StatusRecording,
		model.StatusReconnecting,
	}, status)
}

func TestGracefulShutdown(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	err := mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, mgr.RecorderCount())

	// Let recorders run briefly
	time.Sleep(100 * time.Millisecond)

	// Cancel context to signal shutdown
	cancel()

	// Stop should complete promptly
	done := make(chan error, 1)
	go func() {
		done <- mgr.Stop()
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not complete in time")
	}

	statuses := mgr.Status()
	for _, s := range statuses {
		assert.Equal(t, model.StatusStopped, s)
	}
}

func TestStart_UnknownProtocol(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "1m",
		},
		Cameras: []config.CameraConfig{
			{
				ID:       "cam-1",
				Protocol: "onvif",
				URL:      "rtsp://192.168.1.10:554/stream",
				Enabled:  true,
			},
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = db.Init(ctx)

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, db, "")
	err = mgr.Start(ctx)
	require.NoError(t, err) // should not fail, just skip unknown protocol
	assert.Equal(t, 0, mgr.RecorderCount())
}

func TestStart_InsertsCameraRecords(t *testing.T) {
	mgr, _, db, _ := newTestManager(t)

	// Initialize database
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start camera manager
	err := mgr.Start(ctx)
	require.NoError(t, err)

	// Check that enabled cameras are in the database
	cameras, err := db.ListCameras(ctx)
	require.NoError(t, err)
	require.Len(t, cameras, 4, "should have 4 cameras in database (including disabled)")

	// Verify camera records exist and have correct data
	cameraMap := make(map[string]storage.CameraRow)
	for _, cam := range cameras {
		cameraMap[cam.ID] = cam
	}

	// Check H264 camera
	h264Cam, exists := cameraMap["cam-h264"]
	require.True(t, exists, "H264 camera should be in database")
	assert.Equal(t, "H264 Camera", h264Cam.Name)
	assert.Equal(t, "rtsp", h264Cam.Protocol)
	assert.True(t, h264Cam.Enabled)

	// Check MJPEG camera
	mjpegCam, exists := cameraMap["cam-mjpeg"]
	require.True(t, exists, "MJPEG camera should be in database")
	assert.Equal(t, "MJPEG Camera", mjpegCam.Name)
	assert.Equal(t, "rtsp", mjpegCam.Protocol)
	assert.True(t, mjpegCam.Enabled)

	// Verify disabled camera IS in database (all cameras are inserted)
	_, exists = cameraMap["cam-disabled"]
	require.True(t, exists, "Disabled camera should be in database")

	// Verify JPEG camera IS in database (all cameras are inserted)
	_, exists = cameraMap["cam-jpeg"]
	require.True(t, exists, "JPEG camera should be in database")
}

// --- CRUD lifecycle tests ---

func TestAddCamera_EnabledH264(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	id, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:       "cam-new-h264",
		Name:     "New H264 Camera",
		Protocol: "rtsp",
		Encoding: "h264",
		Enabled:  true,
	})
	require.NoError(t, err)
	assert.Equal(t, "cam-new-h264", id)

	// Recorder should be created
	_, ok := mgr.recorders["cam-new-h264"]
	assert.True(t, ok, "recorder should be created for enabled h264 camera")
	assert.Equal(t, 1, mgr.RecorderCount())

	// Camera should be in config
	assert.Len(t, mgr.cfg.Cameras, 5) // 4 original + 1 new
}

func TestAddCamera_Disabled(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	id, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:       "cam-new-disabled",
		Name:     "Disabled Camera",
		Protocol: "rtsp",
		Encoding: "h264",
		Enabled:  false,
	})
	require.NoError(t, err)
	assert.Equal(t, "cam-new-disabled", id)

	// No recorder should be created
	assert.Equal(t, 0, mgr.RecorderCount())
}

func TestAddCamera_HTTPJPEG(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	id, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:       "cam-new-jpeg",
		Name:     "JPEG Camera",
		Protocol: "http",
		Encoding: "jpeg",
		Enabled:  true,
	})
	require.NoError(t, err)
	assert.Equal(t, "cam-new-jpeg", id)

	// Recorder should be created for http_jpeg
	_, ok := mgr.recorders["cam-new-jpeg"]
	assert.True(t, ok, "recorder should be created for http_jpeg camera")
	assert.Equal(t, 1, mgr.RecorderCount())
}

func TestAddCamera_DuplicateID(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:       "cam-h264", // duplicate
		Name:     "Dup Camera",
		Protocol: "rtsp",
		Encoding: "h264",
		Enabled:  true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestAddCamera_AutoID(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	id, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:       "", // empty → auto-generate
		Name:     "Auto ID Camera",
		Protocol: "rtsp",
		Encoding: "h264",
		Enabled:  false,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, id)
	assert.True(t, len(id) > 4, "auto-generated ID should have cam- prefix")
}

func TestAddCamera_Persists(t *testing.T) {
	mgr, _, db, configPath := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:       "cam-persist",
		Name:     "Persist Camera",
		Protocol: "rtsp",
		Encoding: "h264",
		Enabled:  false,
	})
	require.NoError(t, err)

	_ = configPath
	row, err := db.GetCamera(ctx, "cam-persist")
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, "Persist Camera", row.Name)
}

func TestRemoveCamera_WithRecorder(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the manager to create recorders
	err := mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, mgr.RecorderCount())

	// Remove a camera that has a recorder
	err = mgr.RemoveCamera(ctx, "cam-h264")
	require.NoError(t, err)

	// Recorder should be removed
	assert.Equal(t, 2, mgr.RecorderCount())
	_, ok := mgr.recorders["cam-h264"]
	assert.False(t, ok)

	// Camera should be removed from config
	assert.Len(t, mgr.cfg.Cameras, 3)
}

func TestRemoveCamera_NotFound(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.RemoveCamera(ctx, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRemoveCamera_Persists(t *testing.T) {
	mgr, _, _, configPath := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.RemoveCamera(ctx, "cam-jpeg")
	require.NoError(t, err)

	// Reload config from disk
	loaded, err := config.Load(configPath)
	require.NoError(t, err)
	for _, cam := range loaded.Cameras {
		assert.NotEqual(t, "cam-jpeg", cam.ID, "removed camera should not be in config")
	}
}

func TestUpdateCamera_Name(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	newName := "Updated H264 Camera"
	updated, err := mgr.UpdateCamera(ctx, "cam-h264", CameraUpdate{Name: &newName})
	require.NoError(t, err)
	assert.Equal(t, newName, updated.Name)

	// Recorder count should not change (no restart needed)
	assert.Equal(t, 0, mgr.RecorderCount())
}

func TestUpdateCamera_URL(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start to create recorders
	err := mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, mgr.RecorderCount())

	newURL := "rtsp://127.0.0.1:2/new-stream"
	updated, err := mgr.UpdateCamera(ctx, "cam-h264", CameraUpdate{URL: &newURL})
	require.NoError(t, err)
	assert.Equal(t, newURL, updated.URL)

	// Recorder should still exist (restarted)
	assert.Equal(t, 3, mgr.RecorderCount())
}

func TestUpdateCamera_Disable(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start to create recorders
	err := mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, mgr.RecorderCount())

	disabled := false
	updated, err := mgr.UpdateCamera(ctx, "cam-h264", CameraUpdate{Enabled: &disabled})
	require.NoError(t, err)
	assert.False(t, updated.Enabled)

	// Recorder should be stopped and removed
	assert.Equal(t, 2, mgr.RecorderCount())
	_, ok := mgr.recorders["cam-h264"]
	assert.False(t, ok)
}

func TestUpdateCamera_Enable(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// cam-disabled has no recorder initially
	assert.Equal(t, 0, mgr.RecorderCount())

	enabled := true
	updated, err := mgr.UpdateCamera(ctx, "cam-disabled", CameraUpdate{Enabled: &enabled})
	require.NoError(t, err)
	assert.True(t, updated.Enabled)

	// Recorder should be created
	assert.Equal(t, 1, mgr.RecorderCount())
	_, ok := mgr.recorders["cam-disabled"]
	assert.True(t, ok)
}

func TestUpdateCamera_NotFound(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	name := "test"
	_, err := mgr.UpdateCamera(ctx, "nonexistent", CameraUpdate{Name: &name})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRestartRecorder(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start to create recorders
	err := mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, mgr.RecorderCount())

	// Restart a recorder
	err = mgr.RestartRecorder(ctx, "cam-h264")
	require.NoError(t, err)

	// Recorder should still be there
	assert.Equal(t, 3, mgr.RecorderCount())
	_, ok := mgr.recorders["cam-h264"]
	assert.True(t, ok)
}

func TestRestartRecorder_Disabled(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.RestartRecorder(ctx, "cam-disabled")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disabled")
}

func TestStartCamera_SkipsMediaPullForBoundLalmaxStream(t *testing.T) {
	mgr, _, db, _ := newTestManager(t)
	engine := &stubMediaEngine{}
	mgr.SetMediaEngine(engine)

	ctx := context.Background()
	require.NoError(t, db.BindStreamToCamera(ctx, "cam-h264", "cam-h264"))

	err := mgr.StartCamera(ctx, "cam-h264")
	require.NoError(t, err)
	assert.Empty(t, engine.startPulls)
}

func TestStartCamera_SkipsMediaPullForActiveLalmaxStream(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	engine := &stubMediaEngine{activeStreams: map[string]bool{"cam-h264": true}}
	mgr.SetMediaEngine(engine)

	ctx := context.Background()
	err := mgr.StartCamera(ctx, "cam-h264")
	require.NoError(t, err)
	assert.Empty(t, engine.startPulls)
}

func TestStartCamera_SkipsMediaPullForLalPlayURL(t *testing.T) {
	mgr, _, _, configPath := newTestManager(t)
	engine := &stubMediaEngine{}
	mgr.SetMediaEngine(engine)

	cfg := testConfig()
	for i := range cfg.Cameras {
		if cfg.Cameras[i].ID == "cam-h264" {
			cfg.Cameras[i].URL = "rtsp://127.0.0.1/live/cam-h264"
			break
		}
	}
	require.NoError(t, config.Save(configPath, cfg))
	mgr.cfg = cfg

	ctx := context.Background()
	err := mgr.StartCamera(ctx, "cam-h264")
	require.NoError(t, err)
	assert.Empty(t, engine.startPulls)
}

func TestStartCamera_StartsMediaPullForRTSP(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	engine := &stubMediaEngine{}
	mgr.SetMediaEngine(engine)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.StartCamera(ctx, "cam-h264")
	require.NoError(t, err)
	require.Len(t, engine.startPulls, 1)
	assert.Equal(t, "cam-h264", engine.startPulls[0].StreamID)
	assert.Equal(t, "live", engine.startPulls[0].AppName)
	assert.Equal(t, "rtsp://127.0.0.1:1/stream", engine.startPulls[0].SourceURL)
}

func TestStopCamera_StopsMediaPull(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	engine := &stubMediaEngine{}
	mgr.SetMediaEngine(engine)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.StartCamera(ctx, "cam-h264")
	require.NoError(t, err)

	err = mgr.StopCamera(ctx, "cam-h264")
	require.NoError(t, err)
	require.Contains(t, engine.stopPulls, "cam-h264")
}

func TestStartCamera_RTSPAddsCredentialsToMediaPull(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	engine := &stubMediaEngine{}
	mgr.SetMediaEngine(engine)

	cam := mgr.GetCameraConfig("cam-h264")
	require.NotNil(t, cam)
	cam.URL = "rtsp://camera.local/live"
	cam.Username = "alice"
	cam.Password = "secret"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.StartCamera(ctx, "cam-h264")
	require.NoError(t, err)
	require.Len(t, engine.startPulls, 1)
	assert.Equal(t, "rtsp://alice:secret@camera.local/live", engine.startPulls[0].SourceURL)
}

func TestCreateRecorder_RTSPUsesMediaEngineRTSPOutput(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	engine := &stubMediaEngine{}
	mgr.SetMediaEngine(engine)

	rec := mgr.createRecorder(config.CameraConfig{
		ID:       "cam-local",
		Protocol: "rtsp",
		Encoding: "h264",
		URL:      "rtsp://origin-camera/live",
		Enabled:  true,
	}, time.Minute)
	require.NotNil(t, rec)

	h264Rec, ok := rec.(*recorder.H264Recorder)
	require.True(t, ok)
	require.Equal(t, "rtsp://127.0.0.1/live/cam-local", h264Rec.SourceURL())
}

func TestCreateRecorder_ONVIFUsesMediaEngineRTSPOutputWhenEncodingKnown(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	engine := &stubMediaEngine{}
	mgr.SetMediaEngine(engine)

	rec := mgr.createRecorder(config.CameraConfig{
		ID:             "cam-onvif-local",
		Protocol:       "onvif",
		Encoding:       "h265",
		StreamEncoding: "H265",
		URL:            "http://camera/onvif/device_service",
		Enabled:        true,
	}, time.Minute)
	require.NotNil(t, rec)

	h265Rec, ok := rec.(*recorder.H265Recorder)
	require.True(t, ok)
	require.Equal(t, "rtsp://127.0.0.1/live/cam-onvif-local", h265Rec.SourceURL())
}

func TestPrepareCameraForStart_ONVIFResolvesEncodingViaInternalClient(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	mgr.SetMediaEngine(&stubMediaEngine{})
	mgr.onvifProfileResolver = func(ctx context.Context, cam config.CameraConfig) ([]onvif.DeviceProfile, error) {
		return []onvif.DeviceProfile{
			{Token: "p1", Encoding: "H264", Width: 1920, Height: 1080},
		}, nil
	}

	cam, err := mgr.prepareCameraForStart(context.Background(), config.CameraConfig{
		ID:       "cam-onvif",
		Protocol: "onvif",
		URL:      "http://camera/onvif/device_service",
	})
	require.NoError(t, err)
	require.Equal(t, "p1", cam.ProfileToken)
	require.Equal(t, "H264", cam.StreamEncoding)
	require.Equal(t, "h264", cam.Encoding)
}

func TestPrepareCameraForStart_ONVIFPrefersConfiguredProfileToken(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	mgr.SetMediaEngine(&stubMediaEngine{})
	mgr.onvifProfileResolver = func(ctx context.Context, cam config.CameraConfig) ([]onvif.DeviceProfile, error) {
		return []onvif.DeviceProfile{
			{Token: "main", Encoding: "H264", Width: 1920, Height: 1080},
			{Token: "sub", Encoding: "H265", Width: 1280, Height: 720},
		}, nil
	}

	cam, err := mgr.prepareCameraForStart(context.Background(), config.CameraConfig{
		ID:           "cam-onvif",
		Protocol:     "onvif",
		URL:          "http://camera/onvif/device_service",
		ProfileToken: "sub",
	})
	require.NoError(t, err)
	require.Equal(t, "sub", cam.ProfileToken)
	require.Equal(t, "H265", cam.StreamEncoding)
	require.Equal(t, "h265", cam.Encoding)
}

func TestStartRecorder_PersistsPreparedONVIFState(t *testing.T) {
	mgr, _, db, configPath := newTestManager(t)
	mgr.SetMediaEngine(&stubMediaEngine{})
	mgr.onvifProfileResolver = func(ctx context.Context, cam config.CameraConfig) ([]onvif.DeviceProfile, error) {
		return []onvif.DeviceProfile{
			{Token: "main", Encoding: "H264", Width: 1920, Height: 1080},
		}, nil
	}
	mgr.onvifStreamResolver = func(ctx context.Context, cam config.CameraConfig) (string, error) {
		return "rtsp://origin/onvif-stream", nil
	}

	mgr.cfg.Cameras = append(mgr.cfg.Cameras, config.CameraConfig{
		ID:       "cam-onvif-persist",
		Name:     "ONVIF Persist",
		Protocol: "onvif",
		URL:      "http://camera/onvif/device_service",
		Enabled:  true,
	})
	require.NoError(t, db.UpsertCamera(context.Background(), "cam-onvif-persist", "ONVIF Persist", "onvif", "", "http://camera/onvif/device_service", "", "", true, "", "", ""))

	err := mgr.startRecorder(context.Background(), mgr.cfg.Cameras[len(mgr.cfg.Cameras)-1], time.Minute)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = mgr.StopCamera(context.Background(), "cam-onvif-persist")
	})

	cfgCam := mgr.GetCameraConfig("cam-onvif-persist")
	require.NotNil(t, cfgCam)
	require.Equal(t, "main", cfgCam.ProfileToken)
	require.Equal(t, "h264", cfgCam.Encoding)
	require.Equal(t, "H264", cfgCam.StreamEncoding)

	row, err := db.GetCamera(context.Background(), "cam-onvif-persist")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, "main", row.ProfileToken)
	require.Equal(t, "h264", row.Encoding)
	require.Equal(t, "H264", row.StreamEncoding)

	_ = configPath
	loaded, err := db.ListCameraConfigs(context.Background())
	require.NoError(t, err)
	found := false
	for _, cam := range loaded {
		if cam.ID == "cam-onvif-persist" {
			found = true
			require.Equal(t, "main", cam.ProfileToken)
			require.Equal(t, "h264", cam.Encoding)
			require.Equal(t, "H264", cam.StreamEncoding)
		}
	}
	require.True(t, found)
}

func TestCreateRecorder_ONVIFReturnsNilWhenMediaEngineEnabledButEncodingUnknown(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	mgr.SetMediaEngine(&stubMediaEngine{})

	// Inject a resolver that returns profiles without H264/H265 encoding
	// so the probe fails and returns nil.
	mgr.onvifProfileResolver = func(ctx context.Context, cam config.CameraConfig) ([]onvif.DeviceProfile, error) {
		return []onvif.DeviceProfile{
			{Token: "profile-mpeg4", Encoding: "MPEG4"},
		}, nil
	}

	rec := mgr.createRecorder(config.CameraConfig{
		ID:       "cam-onvif-unknown",
		Protocol: "onvif",
		URL:      "http://camera/onvif/device_service",
		Enabled:  true,
	}, time.Minute)
	require.Nil(t, rec)
}

func TestCreateRecorder_ONVIF_ProbesEncodingWhenMediaEngineEnabled(t *testing.T) {
	mgr, _, db, configPath := newTestManager(t)
	engine := &stubMediaEngine{}
	mgr.SetMediaEngine(engine)

	// Inject a stub ONVIF profile resolver that returns H265 encoding
	mgr.onvifProfileResolver = func(ctx context.Context, cam config.CameraConfig) ([]onvif.DeviceProfile, error) {
		return []onvif.DeviceProfile{
			{Token: "profile-h265", Encoding: "H265"},
		}, nil
	}
	// Inject a stub ONVIF stream resolver so startMediaPullLocked doesn't hit the network
	mgr.onvifStreamResolver = func(ctx context.Context, cam config.CameraConfig) (string, error) {
		return "rtsp://192.168.1.200/stream", nil
	}

	cam := config.CameraConfig{
		ID:       "cam-onvif-probe",
		Name:     "ONVIF Probe Camera",
		Protocol: "onvif",
		URL:      "http://192.168.1.200/onvif/device_service",
		Username: "admin",
		Password: "pass",
		Enabled:  true,
	}
	// Add camera to config so applyPreparedCameraState can persist it
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, cam)

	segDur, err := time.ParseDuration("1m")
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "should create recorder after encoding probe")

	// Should be an H265Recorder backed by lalmax URL
	_, ok := rec.(*recorder.H265Recorder)
	require.True(t, ok, "expected H265Recorder, got %T", rec)

	// Verify encoding was persisted
	cfgCam := mgr.GetCameraConfig("cam-onvif-probe")
	require.NotNil(t, cfgCam)
	assert.Equal(t, "h265", cfgCam.Encoding)
	assert.Equal(t, "H265", cfgCam.StreamEncoding)
	assert.Equal(t, "profile-h265", cfgCam.ProfileToken)

	// Verify DB persistence
	row, err := db.GetCamera(context.Background(), "cam-onvif-probe")
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, "h265", row.Encoding)
	assert.Equal(t, "H265", row.StreamEncoding)

	_ = configPath
	loaded, err := db.ListCameraConfigs(context.Background())
	require.NoError(t, err)
	found := false
	for _, c := range loaded {
		if c.ID == "cam-onvif-probe" {
			found = true
			assert.Equal(t, "h265", c.Encoding)
		}
	}
	assert.True(t, found, "camera should be persisted to database")
}

func TestStartMediaPullLocked_UsesCameraRTSPTransport(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	engine := &stubMediaEngine{}
	mgr.SetMediaEngine(engine)
	mgr.onvifStreamResolver = func(ctx context.Context, cam config.CameraConfig) (string, error) {
		return "rtsp://192.168.1.200/stream", nil
	}

	cam := config.CameraConfig{
		ID:            "cam-onvif-udp",
		Name:          "ONVIF UDP Camera",
		Protocol:      "onvif",
		ONVIFEndpoint: "http://192.168.1.200/onvif/device_service",
		Encoding:      "h264",
		RTSPTransport: "udp",
		Enabled:       true,
	}

	require.NoError(t, mgr.startMediaPullLocked(context.Background(), cam))
	require.Len(t, engine.startPulls, 1)
	require.Equal(t, "udp", engine.startPulls[0].Transport)
}

func TestCreateRecorder_ONVIF_ProbeFailsReturnsNil(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	mgr.SetMediaEngine(&stubMediaEngine{})

	// Inject a stub that returns no encoding (simulating a device without H264/H265)
	mgr.onvifProfileResolver = func(ctx context.Context, cam config.CameraConfig) ([]onvif.DeviceProfile, error) {
		return []onvif.DeviceProfile{
			{Token: "profile-mpeg4", Encoding: "MPEG4"},
		}, nil
	}

	cam := config.CameraConfig{
		ID:       "cam-onvif-noh26x",
		Protocol: "onvif",
		URL:      "http://192.168.1.200/onvif/device_service",
		Enabled:  true,
	}
	segDur, err := time.ParseDuration("1m")
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.Nil(t, rec, "should return nil when probe finds no H264/H265 encoding")
}

func TestCreateRecorder_ONVIF(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "1m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	cam := config.CameraConfig{
		ID:       "cam-onvif",
		Name:     "ONVIF Camera",
		Protocol: "onvif",
		URL:      "http://192.168.1.100/onvif/device_service",
		Username: "admin",
		Password: "pass",
		Enabled:  true,
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "ONVIF protocol should create a recorder")
	// Verify it's an ONVIFRecorder
	status := rec.Status()
	require.Equal(t, model.StatusStopped, status)
}

func TestCreateRecorder_ONVIF_WithEndpoint(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "30s",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	cam := config.CameraConfig{
		ID:            "cam-onvif-endpoint",
		Name:          "ONVIF Camera",
		Protocol:      "onvif",
		URL:           "http://192.168.1.100/stream",
		ONVIFEndpoint: "http://192.168.1.100:8080/onvif/device_service",
		Username:      "admin",
		Password:      "pass",
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "ONVIF protocol with endpoint should create a recorder")
}

func TestGetONVIFPTZController_NotFound(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "1m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	ctx := context.Background()
	_, err = mgr.GetONVIFPTZController(ctx, "nonexistent")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestGetONVIFPTZController_NotONVIF(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "1m",
		},
		Cameras: []config.CameraConfig{{
			ID:       "cam-h264",
			Name:     "H264 Camera",
			Protocol: "rtsp",
			Encoding: "h264",
			Enabled:  true,
		}},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	ctx := context.Background()
	_, err = mgr.GetONVIFPTZController(ctx, "cam-h264")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not an ONVIF device")
}

func TestCreateRecorder_FallbackToBuiltIn(t *testing.T) {
	t.Helper()

	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "1m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	t.Cleanup(func() { store.CleanupTempFiles() })

	mgr := NewCameraManager(cfg, store, nil, "")

	// Built-in rtsp+h264 should still work (no plugin registered for "rtsp")
	cam := config.CameraConfig{
		ID:       "cam-rtsp-h264",
		Protocol: "rtsp",
		Encoding: "h264",
		URL:      "rtsp://127.0.0.1:1/stream",
		Enabled:  true,
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "built-in rtsp+h264 should still create a recorder")
}

func TestNewCameraManager_WithMetrics(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "30s",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	t.Cleanup(func() { store.CleanupTempFiles() })

	mm := metrics.NewMetrics()

	mgr := NewCameraManager(cfg, store, nil, "", mm)
	assert.NotNil(t, mgr)
	assert.Equal(t, mm, mgr.metrics)
}

func TestNewCameraManager_BackwardCompatOpts(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "30s",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	t.Cleanup(func() { store.CleanupTempFiles() })

	// Old style: just metrics as variadic arg
	mgr := NewCameraManager(cfg, store, nil, "", metrics.NewMetrics())
	assert.NotNil(t, mgr)
	assert.NotNil(t, mgr.metrics)
	assert.Nil(t, mgr.mergeMgr)

	// Old style: no opts at all
	mgr2 := NewCameraManager(cfg, store, nil, "")
	assert.NotNil(t, mgr2)
	assert.Nil(t, mgr2.metrics)
	assert.Nil(t, mgr2.mergeMgr)
}

func TestGetOrCreateONVIFClient_CacheHit(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	assert.NotNil(t, mgr.onvifClients)
	assert.Empty(t, mgr.onvifClients)

	// Seed the cache with a mock client directly
	mockClient := onvif.NewClient("http://192.168.1.100/onvif/device_service", "admin", "pass")
	mgr.onvifClients["test-cam"] = mockClient

	// The getOrCreateONVIFClient can't actually Connect() without a real device,
	// so test the cache lookup path by pre-seeding and verifying CloseONVIFClient removes it.
	assert.Contains(t, mgr.onvifClients, "test-cam")
	mgr.CloseONVIFClient("test-cam")
	assert.NotContains(t, mgr.onvifClients, "test-cam")
}

func TestGetOrCreateONVIFClient_CameraNotFound(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	ctx := context.Background()
	_, err := mgr.getOrCreateONVIFClient(ctx, "nonexistent-camera")
	assert.Error(t, err)
	var notFound *model.CameraNotFoundError
	assert.ErrorAs(t, err, &notFound)
	assert.Equal(t, "nonexistent-camera", notFound.CameraID)
}

func TestGetOrCreateONVIFClient_NonONVIFCamera(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	ctx := context.Background()
	_, err := mgr.getOrCreateONVIFClient(ctx, "cam-h264")
	assert.Error(t, err)
	var notONVIF *model.ONVIFNotCameraError
	assert.ErrorAs(t, err, &notONVIF)
	assert.Equal(t, "cam-h264", notONVIF.CameraID)
}

func TestCloseONVIFClient(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	assert.Empty(t, mgr.onvifClients)

	mockClient := onvif.NewClient("http://192.168.1.100/onvif/device_service", "admin", "pass")
	mgr.onvifClients["cam-to-close"] = mockClient
	assert.Len(t, mgr.onvifClients, 1)

	mgr.CloseONVIFClient("cam-to-close")
	assert.Empty(t, mgr.onvifClients)

	// Closing a non-existent key is a no-op
	mgr.CloseONVIFClient("does-not-exist")
	assert.Empty(t, mgr.onvifClients)
}

func TestCloseAllONVIFClients(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	assert.Empty(t, mgr.onvifClients)

	mockClient1 := onvif.NewClient("http://192.168.1.100/onvif/device_service", "admin", "pass")
	mockClient2 := onvif.NewClient("http://192.168.1.101/onvif/device_service", "admin", "pass")
	mgr.onvifClients["cam-1"] = mockClient1
	mgr.onvifClients["cam-2"] = mockClient2
	assert.Len(t, mgr.onvifClients, 2)

	mgr.closeAllONVIFClients()
	assert.Empty(t, mgr.onvifClients)
}

func TestClassifyError(t *testing.T) {
	t.Helper()
	require.Equal(t, "unknown", classifyError(nil))
	require.Equal(t, "unknown", classifyError(fmt.Errorf("some random error")))
	require.Equal(t, "timeout", classifyError(fmt.Errorf("connection timeout after 10s")))
	require.Equal(t, "timeout", classifyError(fmt.Errorf("deadline exceeded")))
	require.Equal(t, "auth", classifyError(fmt.Errorf("401 unauthorized")))
	require.Equal(t, "auth", classifyError(fmt.Errorf("authentication failed")))
	require.Equal(t, "network", classifyError(fmt.Errorf("connection refused")))
	require.Equal(t, "network", classifyError(fmt.Errorf("dial tcp: no such host")))
}

func TestCameraConnectionErrorMetrics(t *testing.T) {
	t.Helper()
	m := metrics.NewMetrics()
	mgr, store, _, configPath := newTestManager(t)
	defer store.CleanupTempFiles()
	_ = mgr

	// Create a new manager with metrics and a camera that will fail to start
	tmpDir := t.TempDir()
	cfg := testConfig()
	cfg.Storage.RootDir = filepath.Join(tmpDir, "storage")
	// Use an unknown protocol so createRecorder returns nil → startRecorder returns error
	cfg.Cameras[0].Protocol = "unknown_proto"
	cfg.Cameras[0].Enabled = true
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))
	require.NoError(t, config.Save(configPath, cfg))

	mgr2 := NewCameraManager(cfg, store, nil, configPath, m)
	// Call startRecorder directly to trigger the error metric
	segDur, _ := time.ParseDuration(cfg.Storage.SegmentDuration)
	err := mgr2.startRecorder(context.Background(), cfg.Cameras[0], segDur)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not support recording")

	// Verify no metric was recorded (createRecorder returns nil, not a connection error)
	families, _ := m.Registry.Gather()
	for _, f := range families {
		require.NotEqual(t, "nvr_camera_connection_errors_total", f.GetName(), "unknown protocol should not record connection error")
	}
}

func TestFrameProcessingDuration_1in100Sampling(t *testing.T) {
	m := metrics.NewMetrics()
	mgr := NewCameraManager(testConfig(), nil, nil, "", m)

	segDur, err := time.ParseDuration("1m")
	require.NoError(t, err)

	cfg := testConfig()
	rec := mgr.createRecorder(cfg.Cameras[0], segDur)
	require.NotNil(t, rec)

	// Type-assert to H264Recorder to access the Hub
	h264Rec, ok := rec.(*recorder.H264Recorder)
	require.True(t, ok, "expected H264Recorder")
	hub := h264Rec.Hub
	require.NotNil(t, hub)

	// Simulate 500 frames — expect ~5 histogram samples (1/100 sampling)
	for i := 0; i < 500; i++ {
		hub.Broadcast(int64(i), [][]byte{{byte(i)}}, i == 0)
	}

	// Gather metrics and verify sample count
	families, err := m.Registry.Gather()
	require.NoError(t, err)

	var samples int
	for _, f := range families {
		if f.GetName() == "nvr_frame_processing_duration_seconds" {
			for _, metric := range f.GetMetric() {
				samples += int(metric.GetHistogram().GetSampleCount())
			}
		}
	}

	// 500 frames / 100 = 5 samples, allow ±1 for edge cases
	require.InDelta(t, 5, samples, 1, "expected ~5 histogram samples for 500 frames")
}

func TestPauseRecording(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.StartCamera(ctx, "cam-h264")
	require.NoError(t, err)

	assert.False(t, mgr.RecordingPaused("cam-h264"))

	err = mgr.PauseRecording(ctx, "cam-h264")
	require.NoError(t, err)

	assert.True(t, mgr.RecordingPaused("cam-h264"))
	assert.Equal(t, model.StatusPaused, mgr.Status()["cam-h264"])
}

func TestResumeRecording(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.StartCamera(ctx, "cam-h264")
	require.NoError(t, err)

	err = mgr.PauseRecording(ctx, "cam-h264")
	require.NoError(t, err)

	err = mgr.ResumeRecording(ctx, "cam-h264")
	require.NoError(t, err)

	assert.False(t, mgr.RecordingPaused("cam-h264"))
}

func TestPauseRecordingNotFound(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	err := mgr.PauseRecording(context.Background(), "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestResumeRecordingNotPaused(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.StartCamera(ctx, "cam-h264")
	require.NoError(t, err)

	err = mgr.ResumeRecording(ctx, "cam-h264")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not paused")
}
