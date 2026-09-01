package camera

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/lalmax-pro/lalmax-nvr/internal/recorder"
	"github.com/lalmax-pro/lalmax-nvr/internal/rediscovery"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

type adaptiveTriggerer interface {
	TriggerAdaptive(hold time.Duration)
}

func (cm *CameraManager) recordingModeOf(cameraID string) string {
	if cm.db != nil {
		row, err := cm.db.GetCamera(context.Background(), cameraID)
		if err == nil && row != nil && row.RecordingMode != "" {
			return row.RecordingMode
		}
	}
	// No manager lock: callers may already hold cm.mu.
	if cm.cfg != nil {
		for _, cam := range cm.cfg.Cameras {
			if cam.ID == cameraID && cam.RecordingMode != "" {
				return cam.RecordingMode
			}
		}
	}
	return storage.RecordingModeContinuous
}

func (cm *CameraManager) pauseRecordingLockedIfNeeded(cameraID string) {
	switch cm.recordingModeOf(cameraID) {
	case storage.RecordingModeEvent, storage.RecordingModeOff:
	default:
		return
	}
	rec, ok := cm.recorders[cameraID]
	if !ok || cm.pausedRecorders[cameraID] {
		return
	}
	if pausable, ok := rec.(model.PausableRecorder); ok {
		pausable.Pause()
	}
	cm.pausedRecorders[cameraID] = true
}

func (cm *CameraManager) IsEventRecordingCamera(cameraID string) bool {
	return cm.recordingModeOf(cameraID) == storage.RecordingModeEvent
}

func (cm *CameraManager) IsPendingActivation(cam config.CameraConfig) bool {
	if cam.ActivationState == config.ActivationPending {
		return true
	}
	if cm.db == nil {
		return false
	}
	row, err := cm.db.GetCamera(context.Background(), cam.ID)
	return err == nil && row != nil && row.ActivationState == config.ActivationPending
}

func (cm *CameraManager) maybePauseOnStart(ctx context.Context, cameraID string) {
	switch cm.recordingModeOf(cameraID) {
	case storage.RecordingModeEvent, storage.RecordingModeOff:
		if err := cm.PauseRecording(ctx, cameraID); err != nil {
			logger.Debug("initial pause skipped", "camera_id", cameraID, "error", err)
		}
	}
}

func (cm *CameraManager) attachAdaptiveGate(cam config.CameraConfig, h264 *recorder.H264Config, h265 *recorder.H265Config) {
	if cm.recordingModeOf(cam.ID) != storage.RecordingModeAdaptive {
		return
	}
	interval := cam.Adaptive.TimelapseIntervalDuration()
	gate := recorder.NewAdaptiveGate(interval)
	if h264 != nil {
		h264.Adaptive = gate
	}
	if h265 != nil {
		h265.Adaptive = gate
	}
}

func (cm *CameraManager) newH264Recorder(cam config.CameraConfig, cfg recorder.H264Config) model.Recorder {
	cm.attachAdaptiveGate(cam, &cfg, nil)
	return recorder.NewH264Recorder(cfg, cm.store, cm.metrics)
}

func (cm *CameraManager) newH265Recorder(cam config.CameraConfig, cfg recorder.H265Config) model.Recorder {
	cm.attachAdaptiveGate(cam, nil, &cfg)
	return recorder.NewH265Recorder(cfg, cm.store, cm.metrics)
}

// afterRecorderStartLocked pauses event/off cameras and schedules ONVIF follow-up
// after the caller releases cm.mu (via unlocked).
func (cm *CameraManager) afterRecorderStartLocked(cam config.CameraConfig, unlocked <-chan struct{}) {
	cm.pauseRecordingLockedIfNeeded(cam.ID)
	go func() {
		if unlocked != nil {
			<-unlocked
		}
		cm.ensureStableID(context.Background(), cam)
		cm.subscribeMotionIfNeeded(context.Background(), cam.ID)
	}()
}

func (cm *CameraManager) TriggerAdaptive(cameraID string, hold time.Duration) {
	cm.mu.RLock()
	rec := cm.recorders[cameraID]
	cm.mu.RUnlock()
	if t, ok := rec.(adaptiveTriggerer); ok {
		t.TriggerAdaptive(hold)
	}
}

// UpdateONVIFEndpoint updates a camera's ONVIF endpoint and URL.
func (cm *CameraManager) UpdateONVIFEndpoint(ctx context.Context, cameraID, endpoint string) error {
	ep := strings.TrimSpace(endpoint)
	updates := CameraUpdate{ONVIFEndpoint: &ep, URL: &ep}
	_, err := cm.UpdateCamera(ctx, cameraID, updates)
	return err
}

// ActivateCamera stores credentials and starts a pending camera.
func (cm *CameraManager) ActivateCamera(ctx context.Context, cameraID, username, password string) error {
	active := config.ActivationActive
	enabled := true
	updates := CameraUpdate{
		Username:        &username,
		Password:        &password,
		Enabled:         &enabled,
		ActivationState: &active,
	}
	if _, err := cm.UpdateCamera(ctx, cameraID, updates); err != nil {
		return err
	}
	if cm.db != nil {
		_ = cm.db.UpdateCameraActivation(ctx, cameraID, config.ActivationActive)
	}
	return nil
}

// EnsureSubStream starts an on-demand lalmax pull for the camera sub-stream.
func (cm *CameraManager) EnsureSubStream(ctx context.Context, cameraID string) error {
	if cm.mediaEngine == nil {
		return fmt.Errorf("media engine not available")
	}
	cam := cm.GetCameraConfig(cameraID)
	if cam == nil {
		return fmt.Errorf("camera %q not found", cameraID)
	}
	source, err := cm.resolveSubStreamURL(ctx, *cam)
	if err != nil {
		return err
	}
	if source == "" {
		return fmt.Errorf("no sub-stream configured")
	}
	autoStop := time.Duration(cm.cfg.Streaming.PreviewAutoStopSec) * time.Second
	if autoStop <= 0 {
		autoStop = 60 * time.Second
	}
	_, err = cm.mediaEngine.StartPull(ctx, media.StartPullRequest{
		StreamID:       media.SubStreamID(cameraID),
		AppName:        "live",
		SourceURL:      source,
		Transport:      cameraRTSPTransport(*cam),
		RetryForever:   false,
		PullRetryNum:   3,
		AutoStopNoView: autoStop,
	})
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "already exist") {
		return err
	}
	return nil
}

// HasSubStream reports whether the camera has a configured sub-stream source.
func (cm *CameraManager) HasSubStream(cameraID string) bool {
	cam := cm.GetCameraConfig(cameraID)
	if cam == nil {
		return false
	}
	return strings.TrimSpace(cam.SubStreamURL) != "" || strings.TrimSpace(cam.SubProfileToken) != ""
}

func (cm *CameraManager) resolveSubStreamURL(ctx context.Context, cam config.CameraConfig) (string, error) {
	if strings.TrimSpace(cam.SubStreamURL) != "" {
		return cam.SubStreamURL, nil
	}
	if cam.Protocol != "onvif" || strings.TrimSpace(cam.SubProfileToken) == "" {
		return "", nil
	}
	client, err := cm.getOrCreateONVIFClient(ctx, cam.ID)
	if err != nil {
		return "", err
	}
	info, err := client.GetStreamURI(ctx, cam.SubProfileToken)
	if err != nil || info == nil {
		return "", err
	}
	return info.URI, nil
}

func (cm *CameraManager) ensureStableID(ctx context.Context, cam config.CameraConfig) {
	if cam.Protocol != "onvif" {
		return
	}
	if strings.TrimSpace(cam.StableID) != "" && strings.TrimSpace(camIDSerial(cm, cam.ID)) != "" {
		return
	}
	client, err := cm.getOrCreateONVIFClient(ctx, cam.ID)
	if err != nil {
		return
	}
	info, err := client.GetDeviceInformation(ctx)
	if err != nil || info == nil || strings.TrimSpace(info.SerialNumber) == "" {
		return
	}
	serial := strings.TrimSpace(info.SerialNumber)
	cm.mu.Lock()
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cam.ID {
			cm.cfg.Cameras[i].StableID = serial
			break
		}
	}
	cm.mu.Unlock()
	if cm.db != nil {
		_ = cm.db.UpdateCameraStableID(ctx, cam.ID, serial)
		row, _ := cm.db.GetCamera(ctx, cam.ID)
		if row != nil && row.SerialNumber == "" {
			_ = cm.db.UpdateCameraMetadata(ctx, cam.ID, row.Description, row.Location, firstNonEmpty(info.Manufacturer, row.Brand), firstNonEmpty(info.Model, row.Model), serial, row.RetentionDays)
		}
	}
}

func camIDSerial(cm *CameraManager, cameraID string) string {
	if cm.db == nil {
		return ""
	}
	row, err := cm.db.GetCamera(context.Background(), cameraID)
	if err != nil || row == nil {
		return ""
	}
	return row.SerialNumber
}

// RediscoverAndReconnect scans for the camera by serial and reconnects if the IP changed.
func (cm *CameraManager) RediscoverAndReconnect(ctx context.Context, cameraID string) (bool, error) {
	if cm.cfg != nil && !cm.cfg.Health.Rediscovery.IsEnabled() {
		return false, nil
	}
	cam := cm.GetCameraConfig(cameraID)
	if cam == nil {
		return false, fmt.Errorf("camera %q not found", cameraID)
	}
	if cam.Protocol != "onvif" {
		return false, nil
	}
	stableID := strings.TrimSpace(cam.StableID)
	if stableID == "" && cm.db != nil {
		if row, err := cm.db.GetCamera(ctx, cameraID); err == nil && row != nil {
			stableID = firstNonEmpty(row.StableID, row.SerialNumber)
		}
	}
	if stableID == "" {
		logger.Info("rediscovery skipped: no stable_id", "camera_id", cameraID)
		return false, nil
	}

	engine := rediscovery.New(cm.cfg.Health.Rediscovery)
	last := firstNonEmpty(cam.ONVIFEndpoint, cam.URL)
	confirm := func(ctx context.Context, endpoint string) (string, error) {
		client := onvif.NewClient(endpoint, cam.Username, cam.Password)
		if err := client.Connect(ctx); err != nil {
			return "", err
		}
		info, err := client.GetDeviceInformation(ctx)
		if err != nil || info == nil {
			return "", err
		}
		return info.SerialNumber, nil
	}
	result, err := engine.DiscoverByStableID(ctx, stableID, last, cam.SubnetHints, confirm)
	if err != nil {
		return false, err
	}
	if !result.Found {
		return false, nil
	}
	newEP := result.Endpoint
	if normalizeURLHost(newEP) == normalizeURLHost(last) {
		return true, nil
	}
	cm.CloseONVIFClient(cameraID)
	if err := cm.UpdateONVIFEndpoint(ctx, cameraID, newEP); err != nil {
		return false, err
	}
	if cm.healthMgr != nil {
		cm.healthMgr.ClearCameraBlacklist(cameraID)
	}
	logger.Info("rediscovered onvif camera", "camera_id", cameraID, "endpoint", newEP)
	return true, nil
}

func (cm *CameraManager) subscribeMotionIfNeeded(ctx context.Context, cameraID string) {
	mode := cm.recordingModeOf(cameraID)
	if mode != storage.RecordingModeEvent && mode != storage.RecordingModeAdaptive {
		return
	}
	cam := cm.GetCameraConfig(cameraID)
	if cam == nil || cam.Protocol != "onvif" {
		return
	}
	cb := func(ev onvif.ONVIFEvent) {
		topic := strings.ToLower(ev.Topic)
		if !strings.Contains(topic, "motion") && !strings.Contains(topic, "ruleengine") && !strings.Contains(topic, "cellmotion") {
			return
		}
		cm.HandleActivity(cameraID, "onvif")
	}
	if err := cm.SubscribeONVIFEvents(ctx, cameraID, cb); err != nil {
		logger.Debug("onvif motion subscribe skipped", "camera_id", cameraID, "error", err)
	}
}

// HandleActivity is invoked by MQTT / ONVIF motion to start event or adaptive recording.
func (cm *CameraManager) HandleActivity(cameraID, source string) {
	switch cm.recordingModeOf(cameraID) {
	case storage.RecordingModeEvent:
		if cm.eventMgr != nil {
			cm.eventMgr.Trigger(cameraID, source)
		}
	case storage.RecordingModeAdaptive:
		hold := 30 * time.Second
		if cm.cfg != nil {
			hold = cm.cfg.Event.PostRollDuration()
		}
		cm.TriggerAdaptive(cameraID, hold)
	}
}

func normalizeURLHost(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	return strings.ToLower(u.Host)
}
