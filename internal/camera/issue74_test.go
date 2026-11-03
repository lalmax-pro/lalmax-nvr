package camera

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/lalmax-pro/lalmax-nvr/internal/recorder"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// The embedded interface makes unexpected persistence calls fail immediately.
type syncFailureRepository struct {
	storage.CameraSyncRepository
	fail     string
	calls    []string
	saved    config.CameraConfig
	streamID string
	loaded   []config.CameraConfig
	err      error
}

func (s *syncFailureRepository) step(name string) error {
	s.calls = append(s.calls, name)
	if s.fail == name {
		return s.err
	}
	return nil
}
func (s *syncFailureRepository) UpsertCamera(_ context.Context, _, _, _, _, _, _, _ string, _ bool, _, _, _ string, extras ...string) error {
	if len(extras) != 1 || extras[0] != "udp" {
		panic("transport was not normalized")
	}
	return s.step("upsert")
}
func (s *syncFailureRepository) SaveCameraExtras(_ context.Context, cam config.CameraConfig) error {
	s.saved = cam
	return s.step("extras")
}
func (s *syncFailureRepository) BindStreamToCamera(_ context.Context, streamID, _ string) error {
	s.streamID = streamID
	return s.step("bind")
}
func (s *syncFailureRepository) ListCameraConfigs(context.Context) ([]config.CameraConfig, error) {
	return s.loaded, s.step("list")
}

func TestIssue74SyncFailuresAndStreamIdentity(t *testing.T) {
	ctx := context.Background()
	require.NoError(t, SyncCamerasFromStorage(ctx, nil, nil, ""))
	require.NoError(t, SyncCamerasFromStorage(ctx, &config.Config{}, nil, ""))
	for i, stage := range []string{"upsert", "extras", "bind", "list"} {
		t.Run(stage, func(t *testing.T) {
			sentinel := errors.New("injected " + stage)
			repo := &syncFailureRepository{fail: stage, err: sentinel}
			original := config.CameraConfig{ID: "cam", RTSPTransport: " UDP "}
			cfg := &config.Config{Cameras: []config.CameraConfig{original}}
			require.ErrorIs(t, SyncCamerasFromStorage(ctx, cfg, repo, ""), sentinel)
			require.Equal(t, []string{"upsert", "extras", "bind", "list"}[:i+1], repo.calls)
			require.Equal(t, []config.CameraConfig{original}, cfg.Cameras, "a failed migration must not replace config cameras")
		})
	}
	for _, streamID := range []string{"", "separate-stream"} {
		t.Run("stream="+streamID, func(t *testing.T) {
			repo := &syncFailureRepository{loaded: []config.CameraConfig{{ID: "from-db"}}}
			cfg := &config.Config{Cameras: []config.CameraConfig{{ID: "cam", StreamID: streamID, RTSPTransport: "udp"}}}
			require.NoError(t, SyncCamerasFromStorage(ctx, cfg, repo, ""))
			expected := streamID
			if expected == "" {
				expected = "cam"
			}
			require.Equal(t, expected, repo.saved.StreamID)
			require.Equal(t, expected, repo.streamID)
			require.Equal(t, repo.loaded, cfg.Cameras)
		})
	}
	t.Run("save failure after loading", func(t *testing.T) {
		repo := &syncFailureRepository{loaded: []config.CameraConfig{{ID: "from-db"}}}
		cfg := &config.Config{Cameras: []config.CameraConfig{{ID: "cam", RTSPTransport: "udp"}}}
		require.Error(t, SyncCamerasFromStorage(ctx, cfg, repo, t.TempDir()))
		require.Equal(t, repo.loaded, cfg.Cameras)
	})
	t.Run("database only", func(t *testing.T) {
		repo := &syncFailureRepository{loaded: []config.CameraConfig{{ID: "from-db"}}}
		cfg := &config.Config{}
		require.NoError(t, SyncCamerasFromStorage(ctx, cfg, repo, filepath.Join(t.TempDir(), "unused.yaml")))
		require.Equal(t, []string{"list"}, repo.calls)
		require.Equal(t, repo.loaded, cfg.Cameras)
	})
}

type adaptivePauseRecorder struct {
	stubRecorder
	pauses int
	hold   time.Duration
}

func (s *adaptivePauseRecorder) Pause()                             { s.pauses++ }
func (s *adaptivePauseRecorder) Resume()                            {}
func (s *adaptivePauseRecorder) IsPaused() bool                     { return s.pauses > 0 }
func (s *adaptivePauseRecorder) TriggerAdaptive(hold time.Duration) { s.hold = hold }

func TestIssue74RecordingModesAndAdaptiveGate(t *testing.T) {
	cam := config.CameraConfig{ID: "cam", StreamID: "ingest"}
	mgr := NewCameraManager(&config.Config{Cameras: []config.CameraConfig{cam}}, nil, nil, "")
	require.Equal(t, storage.RecordingModeContinuous, mgr.recordingModeOf("cam"))
	require.False(t, mgr.shouldRecordCamera("missing"))
	for _, mode := range []string{storage.RecordingModeContinuous, storage.RecordingModeEvent, storage.RecordingModeOff, storage.RecordingModeAdaptive} {
		t.Run(mode, func(t *testing.T) {
			mgr.SetRecordingModeSource(func(id string) (string, bool) { require.Equal(t, "ingest", id); return mode, true })
			require.Equal(t, mode, mgr.recordingModeOf("cam"))
			require.Equal(t, mode == storage.RecordingModeEvent, mgr.IsEventRecordingCamera("cam"))
			require.Equal(t, mode != storage.RecordingModeEvent && mode != storage.RecordingModeOff, mgr.shouldRecordCamera("cam"))
			h264, h265 := recorder.H264Config{}, recorder.H265Config{}
			mgr.attachAdaptiveGate(cam, &h264, &h265)
			if mode == storage.RecordingModeAdaptive {
				require.NotNil(t, h264.Adaptive)
				require.Same(t, h264.Adaptive, h265.Adaptive)
			} else {
				require.Nil(t, h264.Adaptive)
				require.Nil(t, h265.Adaptive)
			}
		})
	}
	require.Equal(t, storage.RecordingModeOff, mgr.recordingModeOf("missing"))
	mgr.SetRecordingModeSource(func(string) (string, bool) { return "", false })
	require.Equal(t, storage.RecordingModeOff, mgr.recordingModeOf("cam"))
	mgr.SetRecordingDecision(func(string) (bool, bool) { return true, true })
	require.True(t, mgr.shouldRecordCamera("cam"), "known plan decision overrides off mode")
	mgr.SetRecordingDecision(nil)
	rec := &adaptivePauseRecorder{}
	mgr.recorders["cam"] = rec
	mgr.pauseRecordingLockedIfNeeded("cam")
	mgr.pauseRecordingLockedIfNeeded("cam")
	mgr.pauseRecordingLockedIfNeeded("missing")
	require.Equal(t, 1, rec.pauses)
	mgr.maybePauseOnStart(context.Background(), "missing")
	mgr.SetRecordingModeSource(func(string) (string, bool) { return storage.RecordingModeAdaptive, true })
	mgr.HandleActivity("cam", "mqtt")
	require.Equal(t, mgr.cfg.Event.PostRollDuration(), rec.hold)
	mgr.TriggerAdaptive("cam", 17*time.Second)
	require.Equal(t, 17*time.Second, rec.hold)
	mgr.TriggerAdaptive("missing", time.Second)
	mgr.SetRecordingModeSource(func(string) (string, bool) { return storage.RecordingModeEvent, true })
	mgr.HandleActivity("cam", "onvif")                       // No event manager is a supported embedded configuration.
	mgr.subscribeMotionIfNeeded(context.Background(), "cam") // Not ONVIF: no network.
}

func TestIssue74EnsureSubStream(t *testing.T) {
	ctx := context.Background()
	mgr := NewCameraManager(&config.Config{}, nil, nil, "")
	require.EqualError(t, mgr.EnsureSubStream(ctx, "cam"), "media engine not available")
	engine := &stubMediaEngine{}
	mgr.SetMediaEngine(engine)
	require.EqualError(t, mgr.EnsureSubStream(ctx, "cam"), `camera "cam" not found`)
	require.False(t, mgr.HasSubStream("missing"))
	mgr.cfg.Cameras = []config.CameraConfig{{ID: "cam", StreamID: "ingest", Protocol: "rtsp", RTSPTransport: "udp"}}
	require.EqualError(t, mgr.EnsureSubStream(ctx, "cam"), "no sub-stream configured")
	require.False(t, mgr.HasSubStream("cam"))
	mgr.cfg.Cameras[0].SubStreamURL = "rtsp://unused/sub"
	require.True(t, mgr.HasSubStream("cam"))
	require.NoError(t, mgr.EnsureSubStream(ctx, "cam"))
	require.Len(t, engine.startPulls, 1)
	require.Equal(t, media.StartPullRequest{StreamID: media.SubStreamID("ingest"), AppName: "live", SourceURL: "rtsp://unused/sub", Transport: "udp", PullRetryNum: 3, AutoStopNoView: time.Minute}, engine.startPulls[0])
	mgr.cfg.Streaming.PreviewAutoStopSec = 12
	require.NoError(t, mgr.EnsureSubStream(ctx, "cam"))
	require.Equal(t, 12*time.Second, engine.startPulls[1].AutoStopNoView)
	sentinel := errors.New("pull rejected")
	engine.startErr = sentinel
	require.ErrorIs(t, mgr.EnsureSubStream(ctx, "cam"), sentinel)
	engine.startErr = errors.New("stream Already Exists")
	require.NoError(t, mgr.EnsureSubStream(ctx, "cam"))
	mgr.cfg.Cameras[0].SubStreamURL = ""
	mgr.cfg.Cameras[0].SubProfileToken = "sub"
	require.True(t, mgr.HasSubStream("cam"))
	mgr.cfg.Cameras[0].Protocol = "onvif"
	// A mismatched lookup exercises the resolver error without connecting a device.
	_, err := mgr.resolveSubStreamURL(ctx, config.CameraConfig{ID: "missing", Protocol: "onvif", SubProfileToken: "sub"})
	require.Error(t, err)
}

func TestIssue74ActivationAndRediscoveryGuards(t *testing.T) {
	ctx := context.Background()
	mgr, _, db, _ := newTestManager(t)
	require.True(t, mgr.IsPendingActivation(config.CameraConfig{ActivationState: config.ActivationPending}))
	require.False(t, mgr.IsPendingActivation(config.CameraConfig{ID: "missing"}))
	require.NoError(t, db.UpsertCamera(ctx, "pending", "Pending", "onvif", "", "", "", "", false, "", "", ""))
	require.NoError(t, db.UpdateCameraActivation(ctx, "pending", config.ActivationPending))
	require.True(t, mgr.IsPendingActivation(config.CameraConfig{ID: "pending"}))
	require.NoError(t, mgr.UpdateONVIFEndpoint(ctx, "cam-disabled", "  http://unused/onvif  "))
	require.Equal(t, "http://unused/onvif", mgr.GetCameraConfig("cam-disabled").ONVIFEndpoint)
	require.Error(t, mgr.ActivateCamera(ctx, "missing", "user", "pass"))
	require.Error(t, mgr.UpdateONVIFEndpoint(ctx, "missing", "http://unused"))
	found, err := mgr.RediscoverAndReconnect(ctx, "missing")
	require.Error(t, err)
	require.False(t, found)
	for _, cam := range []config.CameraConfig{{ID: "rtsp", Protocol: "rtsp"}, {ID: "onvif", Protocol: "onvif"}} {
		mgr.cfg.Cameras = []config.CameraConfig{cam}
		found, err := mgr.RediscoverAndReconnect(ctx, cam.ID)
		require.NoError(t, err)
		require.False(t, found)
	}
	disabled := false
	mgr.cfg.Health.Rediscovery.Enabled = &disabled
	found, err = mgr.RediscoverAndReconnect(ctx, "missing")
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, camIDSerial(mgr, "missing"))
	mgr.db = nil
	require.False(t, mgr.IsPendingActivation(config.CameraConfig{}))
	require.Empty(t, camIDSerial(mgr, "missing"))
	for raw, want := range map[string]string{" HTTP://Camera.LOCAL:80/path ": "camera.local:80", "not-a-url///": "not-a-url", "://bad/": "://bad"} {
		require.Equal(t, want, normalizeURLHost(raw))
	}
}

func TestIssue74PrepareONVIFErrorsAndFallback(t *testing.T) {
	ctx := context.Background()
	mgr := NewCameraManager(&config.Config{}, nil, nil, "")
	mgr.SetMediaEngine(&stubMediaEngine{})
	cam := config.CameraConfig{ID: "cam", Protocol: "onvif"}
	sentinel := errors.New("profiles unavailable")
	mgr.onvifProfileResolver = func(context.Context, config.CameraConfig) ([]onvif.DeviceProfile, error) { return nil, sentinel }
	_, err := mgr.prepareCameraForStart(ctx, cam)
	require.ErrorIs(t, err, sentinel)
	require.ErrorContains(t, mgr.startRecorder(ctx, cam, time.Minute), "failed to prepare camera")
	mgr.onvifProfileResolver = func(context.Context, config.CameraConfig) ([]onvif.DeviceProfile, error) { return nil, nil }
	_, err = mgr.prepareCameraForStart(ctx, cam)
	require.EqualError(t, err, "onvif device has no media profiles")
	require.Nil(t, selectONVIFProfile("", nil))
	profiles := []onvif.DeviceProfile{{Token: "legacy", Encoding: "MPEG4"}}
	require.Equal(t, &profiles[0], selectONVIFProfile("missing", profiles))
	mgr.onvifProfileResolver = func(context.Context, config.CameraConfig) ([]onvif.DeviceProfile, error) { return profiles, nil }
	prepared, err := mgr.prepareCameraForStart(ctx, cam)
	require.NoError(t, err)
	require.Equal(t, "legacy", prepared.ProfileToken)
	require.Equal(t, "MPEG4", prepared.StreamEncoding)
	cam.StreamEncoding = "KEEP"
	prepared, err = mgr.prepareCameraForStart(ctx, cam)
	require.NoError(t, err)
	require.Equal(t, "KEEP", prepared.StreamEncoding)
}

func TestIssue74ManagerStopErrors(t *testing.T) {
	mgr := NewCameraManager(&config.Config{}, nil, nil, "")
	calls := 0
	for _, id := range []string{"a", "b"} {
		mgr.recorders[id] = &stubRecorder{stopFn: func() error { calls++; return errors.New("stop failed") }}
	}
	require.EqualError(t, mgr.Stop(), "camera manager: 2 recorder(s) failed to stop")
	require.Equal(t, 2, calls, "one stop failure must not skip other recorders")
	require.Nil(t, mgr.detachRecorderLocked("missing"))
	mgr.pausedRecorders["a"] = true
	rec := mgr.detachRecorderLocked("a")
	require.NotNil(t, rec)
	require.NotContains(t, mgr.pausedRecorders, "a")
	stopDetachedRecorder("a", rec)
	stopDetachedRecorder("missing", nil)
	require.Equal(t, 3, calls)
	require.Equal(t, model.StatusError, mgr.CameraStatus("missing"))
}
