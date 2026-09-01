package camera

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/health"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/merge"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/lalmax-pro/lalmax-nvr/internal/recorder"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi"
)

var logger = slog.Default().With("component", "camera-manager")

// CameraUpdate holds optional fields for updating a camera.
// Only non-nil fields will be applied.
type CameraUpdate struct {
	Name            *string
	URL             *string
	Protocol        *string
	Encoding        *string
	RTSPTransport   *string
	Username        *string
	Password        *string
	Enabled         *bool
	Description     *string
	Location        *string
	Brand           *string
	Model           *string
	SerialNumber    *string
	RetentionDays   *int
	ONVIFEndpoint   *string
	ProfileToken    *string
	StreamEncoding  *string
	AudioEnabled    *bool
	RecordingMode   *string
	ActivationState *string
	SubStreamURL    *string
	SubProfileToken *string
	SubnetHints     *[]string
	StableID        *string
	Adaptive        *config.CameraAdaptiveConfig
}

type CameraManager struct {
	cfg                  *config.Config
	store                *storage.Manager
	db                   *storage.DB
	configPath           string
	recorders            map[string]model.Recorder // camera_id → Recorder
	metrics              *metrics.Metrics
	mergeMgr             *merge.MergeManager // segment merge manager (nil = no merge)
	healthMgr            *health.Manager     // health monitoring (nil when disabled)
	eventBus             *event.EventBus
	mediaEngine          media.Engine
	onvifProfileResolver func(context.Context, config.CameraConfig) ([]onvif.DeviceProfile, error)
	onvifStreamResolver  func(context.Context, config.CameraConfig) (string, error)
	mu                   sync.RWMutex
	onvifClients         map[string]*onvif.Client            // camera_id → cached ONVIF client
	onvifMu              sync.Mutex                          // protects onvifClients
	errorDetails         map[string]*model.CameraErrorDetail // cameraID → latest error detail
	eventSubscribers     map[string]onvif.EventSubscriber    // camera_id → event subscriber
	frameSampleCounter   uint64                              // atomic: 1/100 sampling for frame processing duration
	pausedRecorders      map[string]bool                     // camera IDs with paused recording
	eventMgr             *recorder.EventManager
}

func NewCameraManager(cfg *config.Config, store *storage.Manager, db *storage.DB, configPath string, opts ...interface{}) *CameraManager {
	var m *metrics.Metrics
	var mm *merge.MergeManager
	for _, opt := range opts {
		switch v := opt.(type) {
		case *metrics.Metrics:
			m = v
		case *merge.MergeManager:
			mm = v
		}
	}
	return &CameraManager{
		cfg:              cfg,
		store:            store,
		db:               db,
		configPath:       configPath,
		recorders:        make(map[string]model.Recorder),
		metrics:          m,
		mergeMgr:         mm,
		errorDetails:     make(map[string]*model.CameraErrorDetail),
		onvifClients:     make(map[string]*onvif.Client),
		eventSubscribers: make(map[string]onvif.EventSubscriber),
		pausedRecorders:  make(map[string]bool),
	}
}

// SetEventBus injects the application event bus used by recorders.
func (cm *CameraManager) SetEventBus(bus *event.EventBus) {
	cm.eventBus = bus
}

func cameraRTSPTransport(cam config.CameraConfig) string {
	return config.NormalizeRTSPTransport(cam.RTSPTransport)
}

// SetHealthManager sets the health manager for camera health monitoring.
// Can be called with nil to disable health monitoring.
func (cm *CameraManager) SetHealthManager(m *health.Manager) {
	cm.healthMgr = m
	if m != nil {
		m.SetStatusFunc(func() map[string]string {
			cm.mu.RLock()
			defer cm.mu.RUnlock()
			result := make(map[string]string, len(cm.recorders))
			for id, rec := range cm.recorders {
				result[id] = string(rec.Status())
			}
			return result
		})
	}
}

// SetMediaEngine sets the lal/lalmax-backed media engine used for relay pulls.
func (cm *CameraManager) SetMediaEngine(engine media.Engine) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.mediaEngine = engine
}

func (cm *CameraManager) SetEventManager(m *recorder.EventManager) {
	cm.eventMgr = m
}

// createRecorder creates a recorder for the given camera config.
// Returns nil for unknown protocols.
func (cm *CameraManager) createRecorder(cam config.CameraConfig, segDur time.Duration) model.Recorder {
	var rec model.Recorder
	recordingSourceURL := cm.recordingSourceURL(cam)
	switch cam.Protocol {
	case "xiaomi":
		if cm.mediaEngine != nil {
			rec = new(xiaomi.XiaomiPlugin).NewRecorderWithMediaEngine(cam, cm.store, cm.db, cm.mediaEngine, cm.metrics)
		} else {
			rec = new(xiaomi.XiaomiPlugin).NewRecorder(cam, cm.store, cm.db, cm.metrics)
		}
		// Wire ErrorReporter for TUTK vendor error detection
		if xr, ok := rec.(*xiaomi.XiaomiRecorder); ok {
			xr.SetErrorReporter(cm)
		}
	case "gb28181":
		// GB28181 streams are pulled via RTSP from lalmax
		if recordingSourceURL != "" {
			logger.Info("GB28181 recording via lalmax relay", "camera_id", cam.ID, "source_url", recordingSourceURL)
		} else {
			logger.Info("GB28181 recording via direct RTSP pull", "camera_id", cam.ID)
		}
		// Auto-detect encoding via RTSP probe (handles device-side encoding changes)
		probeURL := firstNonEmpty(recordingSourceURL, cam.URL)
		if probeURL != "" {
			detectedEncoding := cm.probeGB28181Encoding(probeURL, cam)
			if detectedEncoding != "" && detectedEncoding != cam.Encoding {
				logger.Warn("GB28181 encoding mismatch detected, using actual encoding",
					"camera_id", cam.ID,
					"configured", cam.Encoding,
					"detected", detectedEncoding)
				cam.Encoding = detectedEncoding
				// Update database with correct encoding
				if cm.db != nil {
					if err := cm.db.UpsertCamera(context.Background(), cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.Enabled, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding, cameraRTSPTransport(cam)); err != nil {
						logger.Error("failed to update camera encoding in database", "camera_id", cam.ID, "error", err)
					}
				}
			}
		}
		switch cam.Encoding {
		case string(model.FormatH264), "": // Default to H264 if encoding unknown
			h264Cfg := recorder.H264Config{
				CameraID:      cam.ID,
				RTSPURL:       firstNonEmpty(recordingSourceURL, cam.URL),
				RTSPTransport: cameraRTSPTransport(cam),
				Username:      cam.Username,
				Password:      cam.Password,
				SegmentDur:    segDur,
				DB:            cm.db,
				AudioEnabled:  cam.AudioEnabled,
				EventBus:      cm.eventBus,
			}
			if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
				h264Cfg.FrameWatchdogTimeout = d
			}
			rec = cm.newH264Recorder(cam, h264Cfg)
		case string(model.FormatH265):
			h265Cfg := recorder.H265Config{
				CameraID:      cam.ID,
				RTSPURL:       firstNonEmpty(recordingSourceURL, cam.URL),
				RTSPTransport: cameraRTSPTransport(cam),
				Username:      cam.Username,
				Password:      cam.Password,
				SegmentDur:    segDur,
				DB:            cm.db,
				AudioEnabled:  cam.AudioEnabled,
				EventBus:      cm.eventBus,
			}
			if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
				h265Cfg.FrameWatchdogTimeout = d
			}
			rec = cm.newH265Recorder(cam, h265Cfg)
		default:
			logger.Warn("unsupported encoding for GB28181 recording", "camera_id", cam.ID, "encoding", cam.Encoding)
			return nil
		}
	case string(model.ProtoRTSP):
		// Auto-detect encoding if configured encoding might be wrong
		detectedEncoding := cm.probeRTSPEncoding(cam)
		if detectedEncoding != "" && detectedEncoding != cam.Encoding {
			logger.Warn("encoding mismatch detected, using actual encoding",
				"camera_id", cam.ID,
				"configured", cam.Encoding,
				"detected", detectedEncoding)
			cam.Encoding = detectedEncoding
			// Update database with correct encoding
			if cm.db != nil {
				if err := cm.db.UpsertCamera(context.Background(), cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.Enabled, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding, cameraRTSPTransport(cam)); err != nil {
					logger.Error("failed to update camera encoding in database", "camera_id", cam.ID, "error", err)
				}
			}
		}

		switch cam.Encoding {
		case string(model.FormatH264):
			if recordingSourceURL != "" {
				logger.Info("recording via lalmax relay", "camera_id", cam.ID, "source_url", recordingSourceURL)
			} else {
				logger.Info("recording via direct camera pull", "camera_id", cam.ID)
			}
			h264Cfg := recorder.H264Config{
				CameraID:      cam.ID,
				RTSPURL:       firstNonEmpty(recordingSourceURL, cam.URL),
				RTSPTransport: cameraRTSPTransport(cam),
				Username:      cam.Username,
				Password:      cam.Password,
				SegmentDur:    segDur,
				DB:            cm.db,
				AudioEnabled:  cam.AudioEnabled,
				EventBus:      cm.eventBus,
			}
			if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
				h264Cfg.FrameWatchdogTimeout = d
			}
			rec = cm.newH264Recorder(cam, h264Cfg)
		case string(model.FormatH265):
			if recordingSourceURL != "" {
				logger.Info("recording via lalmax relay", "camera_id", cam.ID, "source_url", recordingSourceURL)
			} else {
				logger.Info("recording via direct camera pull", "camera_id", cam.ID)
			}
			h265Cfg := recorder.H265Config{
				CameraID:      cam.ID,
				RTSPURL:       firstNonEmpty(recordingSourceURL, cam.URL),
				RTSPTransport: cameraRTSPTransport(cam),
				Username:      cam.Username,
				Password:      cam.Password,
				SegmentDur:    segDur,
				DB:            cm.db,
				AudioEnabled:  cam.AudioEnabled,
				EventBus:      cm.eventBus,
			}
			if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
				h265Cfg.FrameWatchdogTimeout = d
			}
			rec = cm.newH265Recorder(cam, h265Cfg)
		case string(model.FormatMJPEG):
			logger.Warn("MJPEG recording uses direct camera pull (lalmax does not relay MJPEG)",
				"camera_id", cam.ID)
			mjpegCfg := recorder.MJPEGConfig{
				CameraID:       cam.ID,
				RTSPURL:        cam.URL,
				SegmentDur:     segDur,
				SampleInterval: cam.SampleInterval,
				DB:             cm.db,
				EventBus:       cm.eventBus,
			}
			rec = recorder.NewMJPEGRecorder(mjpegCfg, cm.store, cm.metrics)
		default:
			return nil
		}
	case string(model.ProtoHTTP):
		if cam.Encoding != string(model.EncJPEG) {
			return nil
		}
		logger.Warn("HTTP/JPEG recording uses direct camera pull (lalmax does not relay HTTP/JPEG)",
			"camera_id", cam.ID)
		httpJpegCfg := recorder.HTTPJPEGConfig{
			CameraID:   cam.ID,
			URL:        cam.URL,
			SegmentDur: segDur,
			Username:   cam.Username,
			Password:   cam.Password,
			DB:         cm.db,
			EventBus:   cm.eventBus,
		}
		rec = recorder.NewHTTPJPEGRecorder(httpJpegCfg, cm.store, cm.metrics)
	case string(model.ProtoONVIF):
		if recordingSourceURL != "" {
			if detectedEncoding := cm.probeStreamEncoding(recordingSourceURL, cam); detectedEncoding != "" && detectedEncoding != cm.normalizedRecordingEncoding(cam) {
				logger.Warn("ONVIF relay encoding mismatch detected, using actual encoding",
					"camera_id", cam.ID,
					"configured", cm.normalizedRecordingEncoding(cam),
					"detected", detectedEncoding)
				cam.Encoding = detectedEncoding
				cam.StreamEncoding = strings.ToUpper(detectedEncoding)
				if err := cm.applyPreparedCameraState(context.Background(), cam); err != nil {
					logger.Error("failed to update ONVIF camera encoding", "camera_id", cam.ID, "error", err)
				}
			}
			switch cm.normalizedRecordingEncoding(cam) {
			case string(model.FormatH264):
				h264Cfg := recorder.H264Config{
					CameraID:      cam.ID,
					RTSPURL:       recordingSourceURL,
					RTSPTransport: cameraRTSPTransport(cam),
					SegmentDur:    segDur,
					DB:            cm.db,
					AudioEnabled:  cam.AudioEnabled,
					EventBus:      cm.eventBus,
				}
				if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
					h264Cfg.FrameWatchdogTimeout = d
				}
				rec = cm.newH264Recorder(cam, h264Cfg)
				break
			case string(model.FormatH265):
				h265Cfg := recorder.H265Config{
					CameraID:      cam.ID,
					RTSPURL:       recordingSourceURL,
					RTSPTransport: cameraRTSPTransport(cam),
					SegmentDur:    segDur,
					DB:            cm.db,
					AudioEnabled:  cam.AudioEnabled,
					EventBus:      cm.eventBus,
				}
				if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
					h265Cfg.FrameWatchdogTimeout = d
				}
				rec = cm.newH265Recorder(cam, h265Cfg)
				break
			}
			if rec != nil {
				break
			}
		}
		if cm.mediaEngine != nil {
			// Encoding unknown — try to probe ONVIF device to detect encoding,
			// then create an H264/H265Recorder backed by the lalmax relay.
			if probed, probeURL := cm.probeONVIFEncodingAndBuildURL(context.Background(), cam, segDur); probed != nil {
				rec = probed
				recordingSourceURL = probeURL
				break
			}
			logger.Warn("onvif encoding unknown and probe failed, skipping recording",
				"camera_id", cam.ID,
				"hint", "set encoding manually or check ONVIF device connectivity")
			return nil
		}
		logger.Warn("using legacy ONVIFRecorder (media engine disabled)",
			"camera_id", cam.ID,
			"hint", "enable media engine for lalmax-based recording")
		onvifEndpoint := cam.ONVIFEndpoint
		if onvifEndpoint == "" {
			onvifEndpoint = cam.URL
		}
		onvifClient := onvif.NewClient(onvifEndpoint, cam.Username, cam.Password)
		onvifCfg := recorder.ONVIFConfig{
			CameraID:       cam.ID,
			ProfileToken:   cam.ProfileToken,
			StreamEncoding: cam.StreamEncoding,
			RTSPTransport:  cameraRTSPTransport(cam),
			Username:       cam.Username,
			Password:       cam.Password,
			SegmentDur:     segDur,
			DB:             cm.db,
			AudioEnabled:   cam.AudioEnabled,
			EventBus:       cm.eventBus,
		}
		if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
			onvifCfg.FrameWatchdogTimeout = d
		}
		rec = recorder.NewONVIFRecorder(onvifCfg, onvifClient, cm.store, cm.metrics)
	case "timelapse":
		tlCfg := recorder.TimelapseRecorderConfig{
			CameraID: cam.ID,
			DB:       cm.db,
			Metrics:  cm.metrics,
		}
		if cam.Timelapse != nil {
			if d, err := time.ParseDuration(cam.Timelapse.Interval); err == nil && d >= time.Millisecond {
				tlCfg.Interval = d
			}
			if cam.Timelapse.OutputFPS > 0 {
				tlCfg.OutputFPS = cam.Timelapse.OutputFPS
			}
			if cam.Timelapse.VideoCodec != "" {
				tlCfg.VideoCodec = cam.Timelapse.VideoCodec
			}
		}
		rec = recorder.NewTimelapseRecorder(tlCfg, cm.store)
	case "rtmp-pull", "http-flv-pull":
		// These protocols pull via lalmax relay, then record via lalmax's RTSP output
		if recordingSourceURL == "" {
			logger.Warn("relay pull protocol requires media engine", "camera_id", cam.ID, "protocol", cam.Protocol)
			return nil
		}
		logger.Info("recording via relay pull", "camera_id", cam.ID, "protocol", cam.Protocol, "source_url", recordingSourceURL)
		switch cam.Encoding {
		case string(model.FormatH264), "": // Default to H264
			h264Cfg := recorder.H264Config{
				CameraID:      cam.ID,
				RTSPURL:       recordingSourceURL,
				RTSPTransport: "tcp",
				SegmentDur:    segDur,
				DB:            cm.db,
				AudioEnabled:  cam.AudioEnabled,
				EventBus:      cm.eventBus,
			}
			if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
				h264Cfg.FrameWatchdogTimeout = d
			}
			rec = cm.newH264Recorder(cam, h264Cfg)
		case string(model.FormatH265):
			h265Cfg := recorder.H265Config{
				CameraID:      cam.ID,
				RTSPURL:       recordingSourceURL,
				RTSPTransport: "tcp",
				SegmentDur:    segDur,
				DB:            cm.db,
				AudioEnabled:  cam.AudioEnabled,
				EventBus:      cm.eventBus,
			}
			if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
				h265Cfg.FrameWatchdogTimeout = d
			}
			rec = cm.newH265Recorder(cam, h265Cfg)
		default:
			logger.Warn("unsupported encoding for relay pull recording", "camera_id", cam.ID, "encoding", cam.Encoding)
			return nil
		}
	default:
		return nil
	}

	// Initialize StreamHub for frame fan-out on all recorders
	initStreamHub(rec, cam.ID, cam.Protocol, &cm.frameSampleCounter, cm.metrics)
	return rec
}

func (cm *CameraManager) recordingSourceURL(cam config.CameraConfig) string {
	if cm.mediaEngine == nil {
		return ""
	}
	switch cam.Protocol {
	case string(model.ProtoRTSP):
		if cam.Encoding != string(model.FormatH264) && cam.Encoding != string(model.FormatH265) {
			return ""
		}
	case string(model.ProtoONVIF):
		if enc := cm.normalizedRecordingEncoding(cam); enc != string(model.FormatH264) && enc != string(model.FormatH265) {
			return ""
		}
	case "rtmp-pull", "http-flv-pull":
		// Relay pull protocols always use lalmax RTSP output for recording
		if cam.Encoding != string(model.FormatH264) && cam.Encoding != string(model.FormatH265) && cam.Encoding != "" {
			return ""
		}
	case "xiaomi":
		// Xiaomi streams are served via lal, use RTSP for recording
		if cam.Encoding != string(model.FormatH264) && cam.Encoding != string(model.FormatH265) {
			return ""
		}
	default:
		return ""
	}
	playURL, err := cm.mediaEngine.BuildPlayURL(context.Background(), media.PlayURLRequest{
		StreamID: cam.ID,
		AppName:  "live",
		Protocol: "rtsp",
	})
	if err != nil || playURL == nil {
		return ""
	}
	return playURL.URL
}

// probeONVIFEncodingAndBuildURL probes an ONVIF device to detect stream encoding
// when the encoding is unknown. If detection succeeds, it updates the camera config,
// restarts the media pull with the correct stream, and returns the recorder + lalmax URL.
// Returns (nil, "") if detection fails or encoding is unsupported.
func (cm *CameraManager) probeONVIFEncodingAndBuildURL(ctx context.Context, cam config.CameraConfig, segDur time.Duration) (model.Recorder, string) {
	profiles, err := cm.loadONVIFProfiles(ctx, cam)
	if err != nil || len(profiles) == 0 {
		logger.Debug("onvif encoding probe: no profiles", "camera_id", cam.ID, "error", err)
		return nil, ""
	}

	// Find first profile with H264 or H265 encoding
	var detectedEncoding string
	var detectedProfileToken string
	for _, p := range profiles {
		enc := strings.ToLower(p.Encoding)
		if enc == "h264" || enc == "h265" {
			detectedEncoding = enc
			detectedProfileToken = p.Token
			break
		}
	}
	if detectedEncoding == "" {
		logger.Debug("onvif encoding probe: no H264/H265 profile found", "camera_id", cam.ID)
		return nil, ""
	}

	logger.Info("probed ONVIF encoding, switching to lalmax relay",
		"camera_id", cam.ID,
		"encoding", detectedEncoding,
		"profile_token", detectedProfileToken)

	// Persist detected encoding
	cam.Encoding = detectedEncoding
	cam.StreamEncoding = strings.ToUpper(detectedEncoding)
	if cam.ProfileToken == "" {
		cam.ProfileToken = detectedProfileToken
	}
	_ = cm.applyPreparedCameraState(ctx, cam)

	// Restart media pull with the detected encoding
	_ = cm.stopMediaPullLocked(ctx, cam.ID)
	if err := cm.startMediaPullLocked(ctx, cam); err != nil {
		logger.Warn("onvif encoding probe: failed to restart media pull",
			"camera_id", cam.ID, "error", err)
		return nil, ""
	}

	// Build lalmax play URL
	playURL, err := cm.mediaEngine.BuildPlayURL(ctx, media.PlayURLRequest{
		StreamID: cam.ID,
		AppName:  "live",
		Protocol: "rtsp",
	})
	if err != nil || playURL == nil {
		return nil, ""
	}

	// Create the appropriate recorder
	switch detectedEncoding {
	case string(model.FormatH264):
		h264Cfg := recorder.H264Config{
			CameraID:     cam.ID,
			RTSPURL:      playURL.URL,
			SegmentDur:   segDur,
			DB:           cm.db,
			AudioEnabled: cam.AudioEnabled,
			EventBus:     cm.eventBus,
		}
		if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
			h264Cfg.FrameWatchdogTimeout = d
		}
		return cm.newH264Recorder(cam, h264Cfg), playURL.URL
	case string(model.FormatH265):
		h265Cfg := recorder.H265Config{
			CameraID:     cam.ID,
			RTSPURL:      playURL.URL,
			SegmentDur:   segDur,
			DB:           cm.db,
			AudioEnabled: cam.AudioEnabled,
			EventBus:     cm.eventBus,
		}
		if d, err := time.ParseDuration(cam.FrameWatchdogTimeout); err == nil && d > 0 {
			h265Cfg.FrameWatchdogTimeout = d
		}
		return cm.newH265Recorder(cam, h265Cfg), playURL.URL
	}
	return nil, ""
}

func (cm *CameraManager) normalizedRecordingEncoding(cam config.CameraConfig) string {
	if cam.Encoding != "" {
		return strings.ToLower(cam.Encoding)
	}
	if cam.StreamEncoding != "" {
		return strings.ToLower(cam.StreamEncoding)
	}
	return ""
}

func (cm *CameraManager) prepareCameraForStart(ctx context.Context, cam config.CameraConfig) (config.CameraConfig, error) {
	if cam.Protocol != string(model.ProtoONVIF) || cm.mediaEngine == nil {
		return cam, nil
	}

	profiles, err := cm.loadONVIFProfiles(ctx, cam)
	if err != nil {
		return cam, err
	}
	if len(profiles) == 0 {
		return cam, fmt.Errorf("onvif device has no media profiles")
	}

	selected := selectONVIFProfile(cam.ProfileToken, profiles)
	if selected == nil {
		return cam, fmt.Errorf("failed to select onvif profile")
	}
	if cam.ProfileToken == "" {
		cam.ProfileToken = selected.Token
	}
	if selected.Encoding != "" {
		selectedEncoding := strings.ToLower(selected.Encoding)
		if selectedEncoding == string(model.FormatH264) || selectedEncoding == string(model.FormatH265) {
			if cam.Encoding != selectedEncoding {
				logger.Info("corrected ONVIF encoding from selected profile",
					"camera_id", cam.ID,
					"configured", cam.Encoding,
					"detected", selectedEncoding,
					"profile_token", selected.Token)
				cam.Encoding = selectedEncoding
			}
			cam.StreamEncoding = strings.ToUpper(selectedEncoding)
		} else if cam.StreamEncoding == "" {
			cam.StreamEncoding = strings.ToUpper(selected.Encoding)
		}
	} else {
		// Profile doesn't have encoding info (common with Dahua cameras)
		// Try to get encoding from VideoEncoderConfigurations
		encoding := cm.probeEncodingFromVideoEncoderConfigs(ctx, cam, selected.Token)
		if encoding != "" {
			logger.Info("detected ONVIF encoding from VideoEncoderConfigurations",
				"camera_id", cam.ID,
				"encoding", encoding,
				"profile_token", selected.Token)
			cam.Encoding = encoding
			cam.StreamEncoding = strings.ToUpper(encoding)
		}
	}
	return cam, nil
}

// probeEncodingFromVideoEncoderConfigs tries to get encoding info from VideoEncoderConfigurations
// when the profile doesn't include encoding info (common with Dahua cameras).
// It matches the profile token with the video encoder configuration token.
func (cm *CameraManager) probeEncodingFromVideoEncoderConfigs(ctx context.Context, cam config.CameraConfig, profileToken string) string {
	endpoint := cam.ONVIFEndpoint
	if endpoint == "" {
		endpoint = cam.URL
	}
	client := onvif.NewClient(endpoint, cam.Username, cam.Password)
	if err := client.Connect(ctx); err != nil {
		return ""
	}

	mediaService := client.GetMediaService()
	if mediaService == nil {
		return ""
	}
	configs, err := mediaService.GetVideoEncoderConfigurations(ctx)
	if err != nil {
		return ""
	}

	// Extract encoder config token from profile token
	// Dahua pattern: MediaProfile00000 -> encoder config token 00000
	encoderToken := strings.TrimPrefix(profileToken, "MediaProfile")
	if encoderToken == profileToken {
		// No prefix found, try matching by index
		// Profile tokens: MediaProfile00000, MediaProfile00001, etc.
		// Config tokens: 00000, 00001, etc.
		for i, p := range profileToken {
			if p >= '0' && p <= '9' {
				encoderToken = profileToken[i:]
				break
			}
		}
	}

	// Try to find matching config by token
	for _, c := range configs {
		if c.Token == encoderToken {
			encoding := strings.ToLower(c.Encoding)
			if encoding == string(model.FormatH264) || encoding == string(model.FormatH265) {
				return encoding
			}
		}
	}

	// Fallback: if only one config, use it
	if len(configs) == 1 {
		encoding := strings.ToLower(configs[0].Encoding)
		if encoding == string(model.FormatH264) || encoding == string(model.FormatH265) {
			return encoding
		}
	}

	return ""
}

func (cm *CameraManager) loadONVIFProfiles(ctx context.Context, cam config.CameraConfig) ([]onvif.DeviceProfile, error) {
	if cm.onvifProfileResolver != nil {
		return cm.onvifProfileResolver(ctx, cam)
	}
	endpoint := cam.ONVIFEndpoint
	if endpoint == "" {
		endpoint = cam.URL
	}
	client := onvif.NewClient(endpoint, cam.Username, cam.Password)
	if err := client.Connect(ctx); err != nil {
		return nil, fmt.Errorf("onvif connect: %w", err)
	}
	profiles, err := client.GetProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("onvif get profiles: %w", err)
	}
	return profiles, nil
}

func selectONVIFProfile(token string, profiles []onvif.DeviceProfile) *onvif.DeviceProfile {
	if token != "" {
		for i := range profiles {
			if profiles[i].Token == token {
				return &profiles[i]
			}
		}
	}
	for i := range profiles {
		enc := strings.ToUpper(profiles[i].Encoding)
		if enc == "H264" || enc == "H265" {
			return &profiles[i]
		}
	}
	if len(profiles) == 0 {
		return nil
	}
	return &profiles[0]
}

// initStreamHub sets a new StreamHub on the recorder if it has a Hub field.
// It also sets the cameraID for structured logging and wires up the OnBroadcast callback.
func initStreamHub(rec model.Recorder, cameraID string, protocol string, sampleCounter *uint64, m *metrics.Metrics) {
	var hub *model.StreamHub
	switch r := rec.(type) {
	case *recorder.H264Recorder:
		hub = model.NewStreamHub()
		r.Hub = hub
	case *recorder.H265Recorder:
		hub = model.NewStreamHub()
		r.Hub = hub
	case *recorder.ONVIFRecorder:
		hub = model.NewStreamHub()
		r.Hub = hub
	case *recorder.MJPEGRecorder:
		hub = model.NewStreamHub()
		r.Hub = hub
	case *recorder.HTTPJPEGRecorder:
		hub = model.NewStreamHub()
		r.Hub = hub
	case *xiaomi.XiaomiRecorder:
		hub = model.NewStreamHub()
		r.Hub = hub
	}
	if hub != nil {
		hub.SetCameraID(cameraID)
		if m != nil {
			hub.OnBroadcast = func(cid string, isIDR bool) {
				m.StreamHubFramesInTotal.WithLabelValues(cid).Inc()

				// 1/100 sampling: measure frame processing duration
				if sampleCounter != nil {
					count := atomic.AddUint64(sampleCounter, 1)
					if count%100 == 0 {
						start := time.Now()
						m.FrameProcessingDurationSeconds.WithLabelValues(cid, protocol).Observe(time.Since(start).Seconds())
					}
				}
			}
			hub.OnDrop = func(consumerID string) {
				m.StreamHubFramesDropped.WithLabelValues(cameraID, consumerID, "false").Inc()
			}
			hub.OnBufferDepth = func(cid, consumerID string, depth int) {
				m.StreamHubBufferDepth.WithLabelValues(cid, consumerID).Set(float64(depth))
			}
			hub.OnJitterBufferDepth = func(cid string, depth int) {
				m.JitterBufferDepth.WithLabelValues(cid).Set(float64(depth))
			}
			hub.OnJitterReorder = func(cid string) {
				m.JitterBufferReordersTotal.WithLabelValues(cid).Inc()
			}
		}
	}
}

// startRecorder creates and starts a recorder for the given camera config.
// The caller must hold cm.mu (or at least a write lock) if cm.recorders is being modified.
// If the recorder is created, it will be registered in cm.recorders.
func (cm *CameraManager) startRecorder(ctx context.Context, cam config.CameraConfig, segDur time.Duration) error {
	preparedCam, err := cm.prepareCameraForStart(ctx, cam)
	if err != nil {
		return fmt.Errorf("camera %q: failed to prepare camera: %w", cam.ID, err)
	}
	cam = preparedCam
	if err := cm.applyPreparedCameraState(ctx, cam); err != nil {
		return fmt.Errorf("camera %q: failed to persist prepared camera state: %w", cam.ID, err)
	}
	if err := cm.startMediaPullLocked(ctx, cam); err != nil {
		return fmt.Errorf("camera %q: failed to start media pull: %w", cam.ID, err)
	}
	rec := cm.createRecorder(cam, segDur)
	if rec == nil {
		_ = cm.stopMediaPullLocked(ctx, cam.ID)
		return fmt.Errorf("camera %q: protocol %q does not support recording", cam.ID, cam.Protocol)
	}
	cm.recorders[cam.ID] = rec
	// Recorders derive their run context from context.Background() internally,
	// so their lifecycle is independent of this ctx (e.g. HTTP request context).
	// The ctx is only used for short initial setup (e.g. ONVIF device probe).
	if err := rec.Start(ctx); err != nil {
		delete(cm.recorders, cam.ID)
		delete(cm.pausedRecorders, cam.ID)
		_ = cm.stopMediaPullLocked(ctx, cam.ID)
		// Record connection error metric
		if cm.metrics != nil {
			cm.metrics.CameraConnectionErrorsTotal.WithLabelValues(cam.ID, classifyError(err)).Inc()
		}
		return fmt.Errorf("camera %q: failed to start recorder: %w", cam.ID, err)
	}
	cm.errorDetails[cam.ID] = nil
	if cm.metrics != nil {
		cm.metrics.ActiveCameras.Inc()
	}
	// Notify health manager of new camera with per-camera overrides
	var overrides *config.ResolvedHealthOverrides
	if cm.cfg.Health.Enabled {
		resolved := config.ResolveHealthOverrides(cm.cfg.Health, cam.HealthOverrides)
		overrides = &resolved
	}
	cm.healthMgr.OnCameraAdded(cam.ID, rec, overrides)
	logger.Info("started recorder for camera", "camera_id", cam.ID)
	return nil
}

func (cm *CameraManager) applyPreparedCameraState(ctx context.Context, prepared config.CameraConfig) error {
	changed := false
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID != prepared.ID {
			continue
		}
		if cm.cfg.Cameras[i].Encoding != prepared.Encoding {
			cm.cfg.Cameras[i].Encoding = prepared.Encoding
			changed = true
		}
		if cm.cfg.Cameras[i].ProfileToken != prepared.ProfileToken {
			cm.cfg.Cameras[i].ProfileToken = prepared.ProfileToken
			changed = true
		}
		if cm.cfg.Cameras[i].StreamEncoding != prepared.StreamEncoding {
			cm.cfg.Cameras[i].StreamEncoding = prepared.StreamEncoding
			changed = true
		}
		break
	}
	if !changed {
		return nil
	}
	if cm.db != nil {
		if err := cm.db.UpsertCamera(ctx, prepared.ID, prepared.Name, string(prepared.Protocol), prepared.Encoding, prepared.URL, prepared.Username, prepared.Password, prepared.Enabled, prepared.ONVIFEndpoint, prepared.ProfileToken, prepared.StreamEncoding, cameraRTSPTransport(prepared)); err != nil {
			return err
		}
	}
	return cm.persistConfig()
}

// persistConfig saves the current config to disk if configPath is set.
func (cm *CameraManager) persistConfig() error {
	if cm.configPath != "" {
		if err := config.Save(cm.configPath, cm.cfg); err != nil {
			return fmt.Errorf("camera manager: failed to save config: %w", err)
		}
	}
	return nil
}

// Start creates and starts recorders for all enabled cameras in the config.
// If a single camera fails to start, it logs the error and continues with the rest.
func (cm *CameraManager) Start(ctx context.Context) error {
	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		return fmt.Errorf("camera manager: invalid segment duration %q: %w", cm.cfg.Storage.SegmentDuration, err)
	}

	for _, cam := range cm.cfg.Cameras {
		// Insert camera record into database
		if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.Enabled, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding, cameraRTSPTransport(cam)); err != nil {
			logger.Error("failed to insert camera record", "camera_id", cam.ID, "error", err)
		} else if err := cm.db.SaveCameraExtras(ctx, cam); err != nil {
			logger.Error("failed to save camera extras", "camera_id", cam.ID, "error", err)
		} else {
			logger.Info("inserted camera record", "camera_id", cam.ID)
		}

		if !cam.Enabled {
			logger.Info("camera disabled, skipping", "camera_id", cam.ID, "protocol", cam.Protocol)
			continue
		}

		if cam.Protocol == string(model.ProtoGB28181) {
			logger.Info("gb28181 camera managed via GB28181 API, skipping recorder", "camera_id", cam.ID)
			continue
		}

		if cam.ActivationState == config.ActivationPending {
			logger.Info("camera pending activation, skipping recorder", "camera_id", cam.ID)
			continue
		}

		if err := cm.startRecorder(ctx, cam, segDur); err != nil {
			logger.Error("failed to start recorder", "camera_id", cam.ID, "protocol", cam.Protocol, "error", err)
		} else {
			logger.Info("started recorder", "camera_id", cam.ID, "protocol", cam.Protocol, "encoding", cam.Encoding)
			cm.maybePauseOnStart(ctx, cam.ID)
			go cm.ensureStableID(ctx, cam)
			cm.subscribeMotionIfNeeded(ctx, cam.ID)
		}
	}
	return nil
}

// Stop stops all running recorders and waits for them to complete.
func (cm *CameraManager) Stop() error {
	cm.mu.RLock()
	recs := make([]model.Recorder, 0, len(cm.recorders))
	for _, rec := range cm.recorders {
		recs = append(recs, rec)
	}
	cm.mu.RUnlock()

	var errs []error
	for _, rec := range recs {
		if err := rec.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("camera manager: %d recorder(s) failed to stop", len(errs))
	}

	cm.closeAllONVIFClients()

	return nil
}

// Status returns the status of all managed recorders.
func (cm *CameraManager) Status() map[string]model.RecorderStatus {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	result := make(map[string]model.RecorderStatus, len(cm.recorders))
	for id, rec := range cm.recorders {
		st := rec.Status()
		if cm.pausedRecorders[id] {
			st = model.StatusPaused
		}
		result[id] = st
	}
	return result
}

// CameraStatus returns the status of a single camera recorder.
func (cm *CameraManager) CameraStatus(cameraID string) model.RecorderStatus {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	rec, ok := cm.recorders[cameraID]
	if !ok {
		return model.StatusError
	}
	return rec.Status()
}

// SetErrorDetail sets the error detail for a camera. Thread-safe.
func (cm *CameraManager) SetErrorDetail(cameraID string, detail *model.CameraErrorDetail) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.errorDetails[cameraID] = detail
}

// GetErrorDetail returns the error detail for a camera, or nil if none. Thread-safe.
func (cm *CameraManager) GetErrorDetail(cameraID string) *model.CameraErrorDetail {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.errorDetails[cameraID]
}

// RecorderCount returns the number of managed recorders.
func (cm *CameraManager) RecorderCount() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.recorders)
}

// GetRecorder returns the recorder for the given camera ID, or nil if not found.
func (cm *CameraManager) GetRecorder(cameraID string) model.Recorder {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.recorders[cameraID]
}

// GetCameraConfig returns the config for the given camera ID, or nil if not found.
func (cm *CameraManager) GetCameraConfig(cameraID string) *config.CameraConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			return &cm.cfg.Cameras[i]
		}
	}
	return nil
}

// AddCamera adds a new camera to the manager and starts its recorder if enabled.
// If cam.ID is empty, a new ID is generated automatically.
// Returns the camera ID.
func (cm *CameraManager) AddCamera(ctx context.Context, cam config.CameraConfig) (string, error) {
	if cam.ID == "" {
		cam.ID = GenerateCameraID()
	}
	cam.RTSPTransport = cameraRTSPTransport(cam)

	cm.mu.Lock()
	unlocked := make(chan struct{})
	defer func() {
		cm.mu.Unlock()
		close(unlocked)
	}()

	// Check for duplicate ID
	for _, existing := range cm.cfg.Cameras {
		if existing.ID == cam.ID {
			return "", &model.CameraAlreadyExistsError{CameraID: cam.ID}
		}
	}

	// For ONVIF cameras, try to get profile name if not provided
	var profileName string
	if cam.Protocol == "onvif" && cam.ONVIFEndpoint != "" {
		profileName = cm.getONVIFProfileName(ctx, cam)
	}

	// Append to config
	cm.cfg.Cameras = append(cm.cfg.Cameras, cam)

	// Persist to database
	if cm.db != nil {
		if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.Enabled, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding, cameraRTSPTransport(cam)); err != nil {
			logger.Error("failed to upsert camera record", "camera_id", cam.ID, "error", err)
		} else {
			// Save profile name if available
			if profileName != "" {
				if err := cm.db.UpdateCameraProfileName(ctx, cam.ID, profileName); err != nil {
					logger.Warn("failed to save profile name", "camera_id", cam.ID, "error", err)
				}
			}
			if err := cm.db.SaveCameraExtras(ctx, cam); err != nil {
				logger.Error("failed to save camera extras", "camera_id", cam.ID, "error", err)
			}
		}
	}

	// Start recorder if enabled and protocol supports it
	if cam.Enabled && cam.ActivationState != config.ActivationPending {
		segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
		if err != nil {
			segDur = recorder.DefaultSegmentDur
		}
		if err := cm.startRecorder(ctx, cam, segDur); err != nil {
			logger.Error("failed to start recorder", "error", err)
		} else {
			cm.afterRecorderStartLocked(cam, unlocked)
		}
	}

	if cam.ActivationState != "" && cm.db != nil {
		_ = cm.db.UpdateCameraActivation(ctx, cam.ID, cam.ActivationState)
	}
	if cam.StableID != "" && cm.db != nil {
		_ = cm.db.UpdateCameraStableID(ctx, cam.ID, cam.StableID)
	}
	if cam.RecordingMode != "" && cm.db != nil {
		_ = cm.db.UpdateCameraRecordingMode(ctx, cam.ID, cam.RecordingMode)
	}

	return cam.ID, nil
}

// getONVIFProfileName connects to the ONVIF device and retrieves the profile name.
func (cm *CameraManager) getONVIFProfileName(ctx context.Context, cam config.CameraConfig) string {
	endpoint := cam.ONVIFEndpoint
	if endpoint == "" {
		endpoint = cam.URL
	}
	if endpoint == "" {
		return ""
	}

	client := onvif.NewClient(endpoint, cam.Username, cam.Password)

	if err := client.Connect(ctx); err != nil {
		logger.Debug("failed to connect to ONVIF device for profile name", "error", err)
		return ""
	}

	profiles, err := client.GetProfiles(ctx)
	if err != nil || len(profiles) == 0 {
		logger.Debug("failed to get ONVIF profiles for profile name", "error", err)
		return ""
	}

	// Find the matching profile
	for _, p := range profiles {
		if p.Token == cam.ProfileToken {
			return p.Name
		}
	}

	// If no match, return first profile name
	return profiles[0].Name
}

// RemoveCamera removes a camera from the manager, stops its recorder, and removes it from config.
// Does NOT delete the camera record from the database.
func (cm *CameraManager) RemoveCamera(ctx context.Context, cameraID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Find camera index
	idx := -1
	for i, cam := range cm.cfg.Cameras {
		if cam.ID == cameraID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return &model.CameraNotFoundError{CameraID: cameraID}
	}

	// Stop and remove recorder if running
	if rec, ok := cm.recorders[cameraID]; ok {
		if err := rec.Stop(); err != nil {
			logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
		}
		// Notify health manager of camera removal
		cm.healthMgr.OnCameraRemoved(cameraID, rec)
		delete(cm.recorders, cameraID)
		delete(cm.pausedRecorders, cameraID)
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
	}
	_ = cm.stopMediaPullLocked(ctx, cameraID)

	// Remove from config slice
	cm.cfg.Cameras = append(cm.cfg.Cameras[:idx], cm.cfg.Cameras[idx+1:]...)

	// Persist config to disk
	if err := cm.persistConfig(); err != nil {
		logger.Error("failed to persist config", "error", err)
	}

	return nil
}

// ArchiveCamera archives a camera: stops recorder, merges segments, marks archived in DB,
// marks all recordings archived, and removes from config YAML.
// The camera row and recordings are preserved in the database.
// Merge failure is non-blocking (logged but continues).
func (cm *CameraManager) ArchiveCamera(ctx context.Context, cameraID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Verify camera exists in config
	idx := -1
	for i, cam := range cm.cfg.Cameras {
		if cam.ID == cameraID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("camera %q not found", cameraID)
	}

	// 1. Stop recorder if running
	if rec, ok := cm.recorders[cameraID]; ok {
		if err := rec.Stop(); err != nil {
			logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
		}
		// Notify health manager of camera removal
		cm.healthMgr.OnCameraRemoved(cameraID, rec)
		delete(cm.recorders, cameraID)
		delete(cm.pausedRecorders, cameraID)
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
	}
	_ = cm.stopMediaPullLocked(ctx, cameraID)

	// 2. Merge segments (non-blocking — failure is logged but does not stop archival)
	if cm.mergeMgr != nil {
		if err := cm.mergeMgr.MergeCamera(ctx, cameraID); err != nil {
			logger.Warn("merge before archive failed", "camera_id", cameraID, "error", err)
		}
	}

	// 3. Mark camera archived in DB
	if err := cm.db.ArchiveCameraDB(ctx, cameraID); err != nil {
		return fmt.Errorf("failed to archive camera in DB: %w", err)
	}

	// 4. Mark all recordings archived in DB
	affected, err := cm.db.ArchiveAllRecordings(ctx, cameraID)
	if err != nil {
		logger.Warn("failed to archive recordings", "camera_id", cameraID, "error", err)
	} else {
		logger.Info("archived recordings", "camera_id", cameraID, "count", affected)
	}

	// 5. Remove from in-memory config slice and persist
	cm.cfg.Cameras = append(cm.cfg.Cameras[:idx], cm.cfg.Cameras[idx+1:]...)
	if err := cm.persistConfig(); err != nil {
		logger.Error("failed to persist config after archive", "camera_id", cameraID, "error", err)
	}

	logger.Info("archived camera", "camera_id", cameraID)
	return nil
}

// RestoreArchivedCamera restores an archived camera back into the active config.
// It also marks the camera and its recordings as active again in the database.
func (cm *CameraManager) RestoreArchivedCamera(ctx context.Context, row *storage.CameraRow) error {
	if row == nil {
		return fmt.Errorf("archived camera not found")
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	for _, existing := range cm.cfg.Cameras {
		if existing.ID == row.ID {
			return &model.CameraAlreadyExistsError{CameraID: row.ID}
		}
	}

	cam := config.CameraConfig{
		ID:             row.ID,
		Name:           row.Name,
		Protocol:       row.Protocol,
		Encoding:       row.Encoding,
		URL:            row.URL,
		RTSPTransport:  row.RTSPTransport,
		Username:       row.Username,
		Enabled:        row.Enabled,
		ONVIFEndpoint:  row.ONVIFEndpoint,
		ProfileToken:   row.ProfileToken,
		StreamEncoding: row.StreamEncoding,
	}
	cam.RTSPTransport = cameraRTSPTransport(cam)

	cm.cfg.Cameras = append(cm.cfg.Cameras, cam)

	if cm.db != nil {
		if err := cm.db.UnarchiveCameraDB(ctx, row.ID); err != nil {
			return fmt.Errorf("failed to restore camera in DB: %w", err)
		}
		if _, err := cm.db.UnarchiveAllRecordings(ctx, row.ID); err != nil {
			logger.Warn("failed to restore archived recordings", "camera_id", row.ID, "error", err)
		}
	}

	if cam.Enabled {
		segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
		if err != nil {
			segDur = recorder.DefaultSegmentDur
		}
		if err := cm.startRecorder(ctx, cam, segDur); err != nil {
			logger.Error("failed to start restored camera", "camera_id", cam.ID, "error", err)
		}
	}

	if err := cm.persistConfig(); err != nil {
		logger.Error("failed to persist config after restore", "camera_id", cam.ID, "error", err)
	}

	logger.Info("restored archived camera", "camera_id", cam.ID)
	return nil
}

// UpdateCamera applies partial updates to an existing camera.
// Returns the updated CameraConfig.
func (cm *CameraManager) UpdateCamera(ctx context.Context, cameraID string, updates CameraUpdate) (*config.CameraConfig, error) {
	cm.mu.Lock()
	unlocked := make(chan struct{})
	defer func() {
		cm.mu.Unlock()
		close(unlocked)
	}()

	// Find camera
	idx := -1
	var cam *config.CameraConfig
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			idx = i
			cam = &cm.cfg.Cameras[i]
			break
		}
	}
	if idx == -1 {
		return nil, &model.CameraNotFoundError{CameraID: cameraID}
	}

	// Determine if recorder needs restart
	needsRestart := false
	if updates.URL != nil && *updates.URL != cam.URL {
		needsRestart = true
	}
	if updates.Protocol != nil && *updates.Protocol != cam.Protocol {
		needsRestart = true
	}
	if updates.RTSPTransport != nil && config.NormalizeRTSPTransport(*updates.RTSPTransport) != cameraRTSPTransport(*cam) {
		needsRestart = true
	}
	if updates.Username != nil && *updates.Username != cam.Username {
		needsRestart = true
	}
	if updates.Password != nil && *updates.Password != cam.Password {
		needsRestart = true
	}
	if updates.ONVIFEndpoint != nil && *updates.ONVIFEndpoint != cam.ONVIFEndpoint {
		needsRestart = true
	}

	// Apply updates
	if updates.Name != nil {
		cam.Name = *updates.Name
	}
	if updates.URL != nil {
		cam.URL = *updates.URL
	}
	if updates.Protocol != nil {
		cam.Protocol = *updates.Protocol
	}
	if updates.RTSPTransport != nil {
		cam.RTSPTransport = config.NormalizeRTSPTransport(*updates.RTSPTransport)
	}
	if updates.Encoding != nil {
		if *updates.Encoding != cam.Encoding {
			needsRestart = true
		}
		cam.Encoding = *updates.Encoding
	}
	if updates.Username != nil {
		cam.Username = *updates.Username
	}
	if updates.Password != nil {
		cam.Password = *updates.Password
	}
	if updates.ONVIFEndpoint != nil {
		cam.ONVIFEndpoint = *updates.ONVIFEndpoint
	}
	if updates.ProfileToken != nil {
		cam.ProfileToken = *updates.ProfileToken
	}
	if updates.StreamEncoding != nil {
		if *updates.StreamEncoding != cam.StreamEncoding {
			needsRestart = true
		}
		cam.StreamEncoding = *updates.StreamEncoding
	}
	if updates.AudioEnabled != nil && *updates.AudioEnabled != cam.AudioEnabled {
		needsRestart = true
		cam.AudioEnabled = *updates.AudioEnabled
	}
	if updates.SubStreamURL != nil {
		cam.SubStreamURL = *updates.SubStreamURL
	}
	if updates.SubProfileToken != nil {
		cam.SubProfileToken = *updates.SubProfileToken
	}
	if updates.SubnetHints != nil {
		cam.SubnetHints = *updates.SubnetHints
	}
	if updates.StableID != nil {
		cam.StableID = *updates.StableID
	}
	if updates.ActivationState != nil {
		cam.ActivationState = *updates.ActivationState
	}
	if updates.Adaptive != nil {
		cam.Adaptive = updates.Adaptive
	}

	// Handle enabled state changes
	enabledChanged := updates.Enabled != nil && *updates.Enabled != cam.Enabled
	if updates.Enabled != nil {
		cam.Enabled = *updates.Enabled
	}

	// Persist to database
	if cm.db != nil {
		if err := cm.db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.Enabled, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding, cameraRTSPTransport(*cam)); err != nil {
			logger.Error("failed to upsert camera record", "camera_id", cam.ID, "error", err)
		} else if err := cm.db.SaveCameraExtras(ctx, *cam); err != nil {
			logger.Error("failed to save camera extras", "camera_id", cam.ID, "error", err)
		}
		// Persist DB-only metadata fields
		if updates.Description != nil || updates.Location != nil || updates.Brand != nil || updates.Model != nil || updates.SerialNumber != nil || updates.RetentionDays != nil {
			desc := strPtrOrEmpty(updates.Description)
			loc := strPtrOrEmpty(updates.Location)
			br := strPtrOrEmpty(updates.Brand)
			mo := strPtrOrEmpty(updates.Model)
			sn := strPtrOrEmpty(updates.SerialNumber)
			rd := intPtrOrZero(updates.RetentionDays)
			if err := cm.db.UpdateCameraMetadata(ctx, cam.ID, desc, loc, br, mo, sn, rd); err != nil {
				logger.Error("failed to update camera metadata", "camera_id", cam.ID, "error", err)
			}
		}
		// Persist recording mode (the scheduler reconciles the recorder on its next tick)
		if updates.RecordingMode != nil {
			if err := cm.db.UpdateCameraRecordingMode(ctx, cam.ID, *updates.RecordingMode); err != nil {
				logger.Error("failed to update recording mode", "camera_id", cam.ID, "error", err)
			}
			cam.RecordingMode = *updates.RecordingMode
		}
		if updates.ActivationState != nil {
			_ = cm.db.UpdateCameraActivation(ctx, cam.ID, *updates.ActivationState)
		}
		if updates.StableID != nil {
			_ = cm.db.UpdateCameraStableID(ctx, cam.ID, *updates.StableID)
		}
	}

	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}

	// Stop existing recorder if needs restart
	if needsRestart {
		if rec, ok := cm.recorders[cam.ID]; ok {
			if err := rec.Stop(); err != nil {
				logger.Warn("failed to stop recorder", "camera_id", cam.ID, "error", err)
			}
			delete(cm.recorders, cam.ID)
			delete(cm.pausedRecorders, cam.ID)
		}
		_ = cm.stopMediaPullLocked(ctx, cam.ID)
	}

	// Start recorder if newly enabled or protocol changed to a recordable one
	if cam.Enabled {
		if needsRestart || enabledChanged {
			// Only start if we don't already have a recorder (needsRestart cleared it, or was never running)
			if _, exists := cm.recorders[cam.ID]; !exists {
				if cam.ActivationState == config.ActivationPending {
					logger.Info("camera pending activation, skipping recorder", "camera_id", cam.ID)
				} else if err := cm.startRecorder(ctx, *cam, segDur); err != nil {
					logger.Error("failed to start recorder", "error", err)
				} else {
					cm.afterRecorderStartLocked(*cam, unlocked)
				}
			}
		}
	}

	// If disabled, stop recorder
	if !cam.Enabled && enabledChanged {
		if rec, ok := cm.recorders[cam.ID]; ok {
			if err := rec.Stop(); err != nil {
				logger.Warn("failed to stop recorder", "camera_id", cam.ID, "error", err)
			}
			delete(cm.recorders, cam.ID)
			delete(cm.pausedRecorders, cam.ID)
			if cm.metrics != nil {
				cm.metrics.ActiveCameras.Dec()
			}
		}
		_ = cm.stopMediaPullLocked(ctx, cam.ID)
	}

	return cam, nil
}

// RestartRecorder stops and recreates the recorder for the given camera.
// The camera must be enabled.
func (cm *CameraManager) RestartRecorder(ctx context.Context, cameraID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Find camera config
	var cam *config.CameraConfig
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			cam = &cm.cfg.Cameras[i]
			break
		}
	}
	if cam == nil {
		return &model.CameraNotFoundError{CameraID: cameraID}
	}
	if !cam.Enabled {
		return &model.CameraDisabledError{CameraID: cameraID}
	}

	// Stop existing recorder
	if rec, ok := cm.recorders[cameraID]; ok {
		if err := rec.Stop(); err != nil {
			logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
		}
		delete(cm.recorders, cameraID)
		delete(cm.pausedRecorders, cameraID)
	}
	_ = cm.stopMediaPullLocked(ctx, cameraID)
	// Record reconnect attempt
	if cm.metrics != nil {
		cm.metrics.CameraReconnectAttemptsTotal.WithLabelValues(cameraID).Inc()
	}

	// Create and start new recorder
	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}
	return cm.startRecorder(ctx, *cam, segDur)
}

// StartCamera manually starts the recorder for the given camera.
func (cm *CameraManager) StartCamera(ctx context.Context, cameraID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Find camera config
	var cam *config.CameraConfig
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == cameraID {
			cam = &cm.cfg.Cameras[i]
			break
		}
	}
	if cam == nil {
		return &model.CameraNotFoundError{CameraID: cameraID}
	}
	if !cam.Enabled {
		return &model.CameraDisabledError{CameraID: cameraID}
	}
	if cam.ActivationState == config.ActivationPending {
		return fmt.Errorf("camera %q is pending activation", cameraID)
	}

	// Check if already running — stale recorders (error/stopped) can be restarted
	if rec, ok := cm.recorders[cameraID]; ok {
		status := rec.Status()
		if status == model.StatusRecording || status == model.StatusReconnecting {
			return &model.CameraAlreadyRunningError{CameraID: cameraID}
		}
		// Stale recorder — stop and remove so we can start fresh
		if err := rec.Stop(); err != nil {
			logger.Warn("failed to stop stale recorder", "camera_id", cameraID, "error", err)
		}
		delete(cm.recorders, cameraID)
		delete(cm.pausedRecorders, cameraID)
		_ = cm.stopMediaPullLocked(ctx, cameraID)
		if cm.metrics != nil {
			cm.metrics.ActiveCameras.Dec()
		}
	}

	segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
	if err != nil {
		segDur = recorder.DefaultSegmentDur
	}
	return cm.startRecorder(ctx, *cam, segDur)
}

// StopCamera manually stops the recorder for the given camera.
func (cm *CameraManager) StopCamera(_ context.Context, cameraID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	rec, ok := cm.recorders[cameraID]
	if !ok {
		return fmt.Errorf("camera %q not found", cameraID)
	}

	if err := rec.Stop(); err != nil {
		logger.Warn("failed to stop recorder", "camera_id", cameraID, "error", err)
	}
	delete(cm.recorders, cameraID)
	delete(cm.pausedRecorders, cameraID)
	_ = cm.stopMediaPullLocked(context.Background(), cameraID)
	if cm.metrics != nil {
		cm.metrics.ActiveCameras.Dec()
	}
	logger.Info("stopped recorder for camera", "camera_id", cameraID)
	return nil
}

// PauseRecording stops the recorder for the given camera but keeps the media pull
// connection alive. This allows live streaming to continue while recording is paused.
func (cm *CameraManager) PauseRecording(_ context.Context, cameraID string) error {
	cm.mu.Lock()
	rec, ok := cm.recorders[cameraID]
	if !ok {
		cm.mu.Unlock()
		return &model.CameraNotFoundError{CameraID: cameraID}
	}
	if cm.pausedRecorders[cameraID] {
		cm.mu.Unlock()
		return nil
	}
	cm.mu.Unlock()

	if pausable, ok := rec.(model.PausableRecorder); ok {
		pausable.Pause()
	} else if err := rec.Stop(); err != nil {
		logger.Warn("failed to stop recorder for pause", "camera_id", cameraID, "error", err)
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.pausedRecorders[cameraID] = true
	if cm.metrics != nil {
		cm.metrics.ActiveCameras.Dec()
	}
	logger.Info("paused recording for camera", "camera_id", cameraID)
	return nil
}

// ResumeRecording restarts the recorder for a paused camera.
// The media pull connection is still active, so we just create a new recorder.
func (cm *CameraManager) ResumeRecording(ctx context.Context, cameraID string) error {
	cm.mu.Lock()
	if !cm.pausedRecorders[cameraID] {
		cm.mu.Unlock()
		return fmt.Errorf("camera %q recording is not paused", cameraID)
	}

	rec, ok := cm.recorders[cameraID]
	if !ok {
		cm.mu.Unlock()
		return &model.CameraNotFoundError{CameraID: cameraID}
	}
	cm.mu.Unlock()

	// Check if recorder supports resuming
	if pausable, ok := rec.(model.PausableRecorder); ok {
		pausable.Resume()
	} else {
		// Fallback: create a new recorder (requires holding cm.mu)
		cm.mu.Lock()
		cam := cm.getCameraConfigByID(cameraID)
		if cam == nil {
			cm.mu.Unlock()
			return &model.CameraNotFoundError{CameraID: cameraID}
		}
		delete(cm.recorders, cameraID)
		segDur, err := time.ParseDuration(cm.cfg.Storage.SegmentDuration)
		if err != nil {
			segDur = recorder.DefaultSegmentDur
		}
		newRec := cm.createRecorder(*cam, segDur)
		if newRec == nil {
			cm.mu.Unlock()
			return fmt.Errorf("camera %q: protocol %q does not support recording", cameraID, cam.Protocol)
		}
		cm.mu.Unlock()
		if err := newRec.Start(ctx); err != nil {
			return fmt.Errorf("failed to resume recording: %w", err)
		}
		cm.mu.Lock()
		cm.recorders[cameraID] = newRec
		cm.mu.Unlock()
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.pausedRecorders, cameraID)
	if cm.metrics != nil {
		cm.metrics.ActiveCameras.Inc()
	}
	logger.Info("resumed recording for camera", "camera_id", cameraID)
	return nil
}

// RecordingPaused returns true if the camera's recording is paused.
func (cm *CameraManager) RecordingPaused(cameraID string) bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.pausedRecorders[cameraID]
}

func (cm *CameraManager) startMediaPullLocked(ctx context.Context, cam config.CameraConfig) error {
	if !cm.shouldStartMediaPull(cam) {
		return nil
	}
	sourceURL, err := cm.resolveMediaSourceURL(ctx, cam)
	if err != nil {
		return err
	}
	if sourceURL == "" {
		return fmt.Errorf("empty media source URL")
	}
	// Convert AutoStopNoViewSec from config to duration
	autoStopNoView := time.Duration(cm.cfg.Streaming.AutoStopNoViewSec) * time.Second
	_, err = cm.mediaEngine.StartPull(ctx, media.StartPullRequest{
		StreamID:       cam.ID,
		AppName:        "live",
		SourceURL:      sourceURL,
		Transport:      cameraRTSPTransport(cam),
		RetryForever:   cam.PullRetryNum < 0,
		PullRetryNum:   cam.PullRetryNum,
		AutoStopNoView: autoStopNoView,
	})
	if err != nil {
		// If stream already exists, it's not an error - the recorder can connect to the existing stream
		if isDupInStreamError(err) {
			logger.Info("stream already exists, skipping pull start", "camera_id", cam.ID)
			return nil
		}
		return err
	}
	return nil
}

// isDupInStreamError checks if the error is a duplicate in-stream error from lalmax.
func isDupInStreamError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "in stream already exist at group")
}

func (cm *CameraManager) stopMediaPullLocked(ctx context.Context, cameraID string) error {
	if cm.mediaEngine == nil || cameraID == "" {
		return nil
	}
	if err := cm.mediaEngine.StopPull(ctx, cameraID); err != nil && !isMediaStreamNotFound(err) {
		return err
	}
	return nil
}

func (cm *CameraManager) shouldStartMediaPull(cam config.CameraConfig) bool {
	if cm.mediaEngine == nil {
		return false
	}
	switch cam.Protocol {
	case string(model.ProtoONVIF):
		return true
	case string(model.ProtoRTSP):
		// Skip relay pull when the stream already lives in lalmax (RTMP/SRT push, bind, promote).
		// The recorder connects to lal's RTSP output directly; a second pull would duplicate the in-stream.
		if cm.hasExistingLalmaxStream(context.Background(), cam) {
			return false
		}
		return cam.Encoding == string(model.FormatH264) || cam.Encoding == string(model.FormatH265)
	case "rtmp-pull", "http-flv-pull":
		// These protocols are pure relay pulls — always start media pull
		return true
	default:
		return false
	}
}

func (cm *CameraManager) hasExistingLalmaxStream(ctx context.Context, cam config.CameraConfig) bool {
	if cm.mediaEngine == nil {
		return false
	}
	if cm.db != nil {
		if binding, _ := cm.db.GetBindingByCameraID(ctx, cam.ID); binding != nil {
			return true
		}
	}
	if info, err := cm.mediaEngine.GetStream(ctx, cam.ID); err == nil && info != nil && info.Active {
		return true
	}
	// Promoted push streams store lal's RTSP play URL as cam.URL.
	playURL, err := cm.mediaEngine.BuildPlayURL(ctx, media.PlayURLRequest{
		StreamID: cam.ID,
		AppName:  "live",
		Protocol: "rtsp",
	})
	if err == nil && playURL != nil && playURL.URL != "" && cam.URL != "" {
		if sameMediaURLPath(cam.URL, playURL.URL) {
			return true
		}
	}
	return false
}

func sameMediaURLPath(a, b string) bool {
	if a == b {
		return true
	}
	au, errA := url.Parse(a)
	bu, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return false
	}
	return au.Scheme == bu.Scheme && au.Path == bu.Path
}

func (cm *CameraManager) resolveMediaSourceURL(ctx context.Context, cam config.CameraConfig) (string, error) {
	switch cam.Protocol {
	case string(model.ProtoRTSP):
		return applySourceCredentials(cam.URL, cam.Username, cam.Password)
	case string(model.ProtoONVIF):
		return cm.resolveONVIFStreamURL(ctx, cam)
	default:
		return "", nil
	}
}

func (cm *CameraManager) resolveONVIFStreamURL(ctx context.Context, cam config.CameraConfig) (string, error) {
	if cm.onvifStreamResolver != nil {
		return cm.onvifStreamResolver(ctx, cam)
	}
	endpoint := cam.ONVIFEndpoint
	if endpoint == "" {
		endpoint = cam.URL
	}
	client := onvif.NewClient(endpoint, cam.Username, cam.Password)
	if err := client.Connect(ctx); err != nil {
		return "", fmt.Errorf("onvif connect: %w", err)
	}
	profileToken := cam.ProfileToken
	if profileToken == "" {
		profiles, err := client.GetProfiles(ctx)
		if err != nil {
			return "", fmt.Errorf("onvif get profiles: %w", err)
		}
		if len(profiles) == 0 {
			return "", fmt.Errorf("onvif device has no media profiles")
		}
		profileToken = profiles[0].Token
	}
	streamInfo, err := client.GetStreamURI(ctx, profileToken)
	if err != nil {
		return "", fmt.Errorf("onvif get stream URI: %w", err)
	}
	if streamInfo.URI == "" {
		return "", fmt.Errorf("onvif device returned empty stream URI")
	}
	return applySourceCredentials(streamInfo.URI, cam.Username, cam.Password)
}

func applySourceCredentials(rawURL, username, password string) (string, error) {
	if rawURL == "" {
		return "", nil
	}
	if username == "" {
		return rawURL, nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.User != nil && u.User.Username() != "" {
		return rawURL, nil
	}
	u.User = url.UserPassword(username, password)
	return u.String(), nil
}

func isMediaStreamNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "404") || strings.Contains(msg, "not found")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// getOrCreateONVIFClient returns a cached ONVIF client for the given camera,
// creating one if it doesn't exist in the cache.
// Camera config lookup is done OUTSIDE the onvifMu lock to avoid deadlock with cm.mu.
func (cm *CameraManager) getOrCreateONVIFClient(ctx context.Context, cameraID string) (*onvif.Client, error) {
	cam := cm.GetCameraConfig(cameraID)
	if cam == nil {
		return nil, &model.CameraNotFoundError{CameraID: cameraID}
	}
	if cam.Protocol != string(model.ProtoONVIF) {
		return nil, &model.ONVIFNotCameraError{CameraID: cameraID}
	}
	endpoint := cam.ONVIFEndpoint
	if endpoint == "" {
		endpoint = cam.URL
	}

	cm.onvifMu.Lock()
	defer cm.onvifMu.Unlock()

	if cached, ok := cm.onvifClients[cameraID]; ok {
		return cached, nil
	}

	client := onvif.NewClient(endpoint, cam.Username, cam.Password)
	if err := client.Connect(ctx); err != nil {
		return nil, &model.ONVIFConnectionError{CameraID: cameraID, Err: err}
	}
	cm.onvifClients[cameraID] = client
	return client, nil
}

// CloseONVIFClient removes a cached ONVIF client for the given camera.
func (cm *CameraManager) CloseONVIFClient(cameraID string) {
	cm.onvifMu.Lock()
	defer cm.onvifMu.Unlock()
	delete(cm.onvifClients, cameraID)
}

// GetONVIFClient returns a cached ONVIF client for the given camera.
// Returns error if camera is not found, not ONVIF, or client creation fails.
func (cm *CameraManager) GetONVIFClient(ctx context.Context, cameraID string) (*onvif.Client, error) {
	return cm.getOrCreateONVIFClient(ctx, cameraID)
}

// closeAllONVIFClients clears the entire ONVIF client cache.
func (cm *CameraManager) closeAllONVIFClients() {
	cm.onvifMu.Lock()
	defer cm.onvifMu.Unlock()
	cm.onvifClients = make(map[string]*onvif.Client)
}

// GetONVIFPTZController returns a PTZController for the given ONVIF camera.
// Returns error if camera is not found, not ONVIF, or client creation fails.
func (cm *CameraManager) GetONVIFPTZController(ctx context.Context, cameraID string) (onvif.PTZController, error) {
	cam := cm.GetCameraConfig(cameraID)
	if cam == nil {
		return nil, &model.CameraNotFoundError{CameraID: cameraID}
	}
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		return nil, err
	}
	profiles, err := client.GetProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("get profiles for camera %q: %w", cameraID, err)
	}
	selected := selectONVIFProfile(cam.ProfileToken, profiles)
	if selected == nil {
		return nil, &model.ONVIFNoProfilesError{CameraID: cameraID}
	}
	return client.NewPTZController(selected.Token), nil
}

// GetImagingController returns an ImagingController for the given ONVIF camera.
// Returns error if camera is not found, not ONVIF, or client creation fails.
func (cm *CameraManager) GetImagingController(ctx context.Context, cameraID string) (onvif.ImagingController, error) {
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		return nil, err
	}
	profiles, err := client.GetProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("get profiles for camera %q: %w", cameraID, err)
	}
	if len(profiles) == 0 {
		return nil, &model.ONVIFNoProfilesError{CameraID: cameraID}
	}
	// Use VideoSource token (not Profile token) for Imaging service
	videoSourceToken := profiles[0].VideoSource
	if videoSourceToken == "" {
		videoSourceToken = profiles[0].Token // Fallback to profile token
	}
	ctrl := client.NewImagingController(videoSourceToken)
	if ctrl == nil {
		return nil, fmt.Errorf("failed to create imaging controller for camera %q", cameraID)
	}
	// Use device endpoint as imaging service base — most cameras serve imaging
	// on the same host with /onvif/imaging_service path.
	endpoint := client.GetEndpoint()
	imgEndpoint := strings.TrimSuffix(endpoint, "/device_service") + "/imaging_service"
	ctrl.SetImagingEndpoint(imgEndpoint)
	return ctrl, nil
}

// GetSnapshotProvider returns a SnapshotProvider for the given ONVIF camera.
// Returns error if camera is not found, not ONVIF, or client creation fails.
func (cm *CameraManager) GetSnapshotProvider(ctx context.Context, cameraID string) (onvif.SnapshotProvider, error) {
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		return nil, err
	}
	profiles, err := client.GetProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("get profiles for camera %q: %w", cameraID, err)
	}
	if len(profiles) == 0 {
		return nil, &model.ONVIFNoProfilesError{CameraID: cameraID}
	}
	return client.NewSnapshotProvider(profiles[0].Token), nil
}

// GetDeviceManager returns a DeviceManager for the given ONVIF camera.
// Returns error if camera is not found, not ONVIF, or client creation fails.
func (cm *CameraManager) GetDeviceManager(ctx context.Context, cameraID string) (onvif.DeviceManager, error) {
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		return nil, err
	}
	dm := client.NewDeviceManager()
	if dm == nil {
		return nil, fmt.Errorf("failed to create device manager for camera %q", cameraID)
	}
	return dm, nil
}

// strPtrOrEmpty returns the string value of a *string pointer, or empty string if nil.
func strPtrOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// intPtrOrZero returns the int value of a *int pointer, or 0 if nil.
func intPtrOrZero(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}

// SetProtocolEnabled enables or disables a protocol.
// When disabling, stops all cameras using that protocol.
// When enabling, no auto-start (user starts cameras manually).
func (cm *CameraManager) SetProtocolEnabled(protocol string, enabled bool) {
	if !enabled {
		cm.stopCamerasByProtocol(protocol)
	}
}

func (cm *CameraManager) stopCamerasByProtocol(protocol string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for id, rec := range cm.recorders {
		var camProtocol string
		for _, cam := range cm.cfg.Cameras {
			if cam.ID == id {
				camProtocol = cam.Protocol
				break
			}
		}
		if camProtocol == protocol {
			if err := rec.Stop(); err != nil {
				logger.Warn("failed to stop recorder", "camera_id", id, "error", err)
			}
			delete(cm.recorders, id)
			delete(cm.pausedRecorders, id)
			_ = cm.stopMediaPullLocked(context.Background(), id)
			if cm.metrics != nil {
				cm.metrics.ActiveCameras.Dec()
			}
		}
	}
}

// SubscribeONVIFEvents subscribes to PullPoint events for the given camera.
// The eventCallback is invoked when events are received.
// Returns error if camera is not found, not ONVIF, or subscription fails.
func (cm *CameraManager) SubscribeONVIFEvents(ctx context.Context, cameraID string, eventCallback onvif.EventCallback) error {
	client, err := cm.getOrCreateONVIFClient(ctx, cameraID)
	if err != nil {
		return err
	}

	cm.onvifMu.Lock()
	defer cm.onvifMu.Unlock()

	if _, exists := cm.eventSubscribers[cameraID]; exists {
		return nil // Already subscribed
	}

	sub := client.NewEventSubscriber(onvif.WithEventCallback(eventCallback))
	if sub == nil {
		return fmt.Errorf("camera %q: failed to create event subscriber", cameraID)
	}
	if err := sub.Subscribe(ctx, cameraID); err != nil {
		return fmt.Errorf("camera %q: subscribe to events: %w", cameraID, err)
	}
	cm.eventSubscribers[cameraID] = sub
	logger.Info("subscribed to ONVIF events", "camera_id", cameraID)
	return nil
}

// UnsubscribeONVIFEvents unsubscribes from PullPoint events for the given camera.
func (cm *CameraManager) UnsubscribeONVIFEvents(ctx context.Context, cameraID string) error {
	cm.onvifMu.Lock()
	defer cm.onvifMu.Unlock()

	sub, exists := cm.eventSubscribers[cameraID]
	if !exists {
		return nil
	}

	if err := sub.Unsubscribe(ctx, cameraID); err != nil {
		logger.Warn("failed to unsubscribe from events", "camera_id", cameraID, "error", err)
	}
	delete(cm.eventSubscribers, cameraID)
	logger.Info("unsubscribed from ONVIF events", "camera_id", cameraID)
	return nil
}

// StopAllONVIFEvents unsubscribes from all ONVIF event subscriptions.
func (cm *CameraManager) StopAllONVIFEvents(ctx context.Context) {
	cm.onvifMu.Lock()
	for id, sub := range cm.eventSubscribers {
		_ = sub.Unsubscribe(ctx, id)
	}
	cm.eventSubscribers = make(map[string]onvif.EventSubscriber)
	cm.onvifMu.Unlock()
}

// classifyError categorizes a connection error into a Prometheus label value.
// Values: "timeout", "auth", "network", "unknown".
func classifyError(err error) string {
	if err == nil {
		return "unknown"
	}
	msg := err.Error()
	// Check for common error patterns
	switch {
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline"):
		return "timeout"
	case strings.Contains(msg, "401") || strings.Contains(msg, "403") || strings.Contains(msg, "unauthorized") || strings.Contains(msg, "auth"):
		return "auth"
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "network") || strings.Contains(msg, "dial") || strings.Contains(msg, "no such host"):
		return "network"
	default:
		return "unknown"
	}
}

// MonitorStreamEvents subscribes to lalmax stream events and stops/starts recorders
// when streams go offline/online. This is only effective for cameras backed by
// lalmax streams (push or relay pull). Xiaomi and direct RTSP cameras are unaffected.
func (cm *CameraManager) MonitorStreamEvents(ctx context.Context) {
	if cm.mediaEngine == nil {
		return
	}

	// Subscribe to all relevant stream lifecycle events
	events, err := cm.mediaEngine.SubscribeEvents(ctx, media.EventFilter{
		Types: []media.EventType{
			media.EventPublisherStarted,
			media.EventPublisherStopped,
			media.EventRelayPullStarted,
			media.EventRelayPullStopped,
			media.EventStreamActive,
			media.EventStreamStopped,
		},
	})
	if err != nil {
		logger.Error("failed to subscribe to stream events", "error", err)
		return
	}

	logger.Info("stream event monitoring started")

	for {
		select {
		case <-ctx.Done():
			logger.Info("stream event monitoring stopped")
			return
		case ev, ok := <-events:
			if !ok {
				logger.Warn("stream event channel closed")
				return
			}
			cm.handleStreamEvent(ctx, ev)
		}
	}
}

func (cm *CameraManager) handleStreamEvent(ctx context.Context, ev media.Event) {
	// Sub-stream idle stops must not tear down the main camera recorder.
	if media.IsSubStreamID(ev.StreamID) {
		return
	}

	// Only care about cameras that exist in our config
	cm.mu.RLock()
	cam := cm.getCameraConfigByID(ev.StreamID)
	cm.mu.RUnlock()
	if cam == nil {
		return // Not a managed camera, ignore
	}

	// Skip Xiaomi cameras - they have their own lifecycle
	if cam.Protocol == "xiaomi" {
		return
	}

	switch ev.Type {
	case media.EventPublisherStopped, media.EventRelayPullStopped, media.EventStreamStopped:
		logger.Info("stream went offline, stopping recorder", "camera_id", ev.StreamID, "event", ev.Type)
		cm.StopCamera(ctx, ev.StreamID)

	case media.EventPublisherStarted, media.EventRelayPullStarted, media.EventStreamActive:
		// Only start if camera is enabled and recorder is not running
		if !cam.Enabled {
			return
		}
		cm.mu.RLock()
		_, hasRecorder := cm.recorders[ev.StreamID]
		cm.mu.RUnlock()
		if hasRecorder {
			return // Already running
		}
		logger.Info("stream came online, starting recorder", "camera_id", ev.StreamID, "event", ev.Type)
		cm.StartCamera(ctx, ev.StreamID)
	}
}

// getCameraConfigByID returns the camera config for the given ID, or nil if not found.
func (cm *CameraManager) getCameraConfigByID(id string) *config.CameraConfig {
	for i := range cm.cfg.Cameras {
		if cm.cfg.Cameras[i].ID == id {
			return &cm.cfg.Cameras[i]
		}
	}
	return nil
}

// probeRTSPEncoding probes an RTSP stream to detect the actual video encoding.
// Returns the detected encoding (h264, h265, mjpeg) or empty string if detection fails.
// This is used to auto-correct encoding mismatches between config and actual stream.
func (cm *CameraManager) probeRTSPEncoding(cam config.CameraConfig) string {
	// Only probe for RTSP protocol
	if cam.Protocol != string(model.ProtoRTSP) {
		return ""
	}

	// Skip probing if encoding is empty or already correct
	if cam.Encoding == "" {
		return ""
	}

	// Determine the URL to probe
	probeURL := cam.URL
	if probeURL == "" {
		return ""
	}

	// Create probe config
	probeCfg := recorder.RTSPProbeConfig{
		RTSPURL:       probeURL,
		RTSPTransport: cameraRTSPTransport(cam),
		Username:      cam.Username,
		Password:      cam.Password,
		Timeout:       10 * time.Second,
	}

	// Probe the stream
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := recorder.ProbeRTSPEncoding(ctx, probeCfg)
	if err != nil {
		logger.Debug("RTSP encoding probe failed",
			"camera_id", cam.ID,
			"url", probeURL,
			"error", err)
		return ""
	}

	// Return detected encoding if it differs from config
	if result.DetectedEncoding != "" && result.DetectedEncoding != cam.Encoding {
		return result.DetectedEncoding
	}

	return ""
}

// probeGB28181Encoding probes a GB28181 RTSP stream to detect the actual video encoding.
// This handles cases where the device encoding changes (e.g., user switches from H264 to H265 on camera).
func (cm *CameraManager) probeGB28181Encoding(rtspURL string, cam config.CameraConfig) string {
	return cm.probeStreamEncoding(rtspURL, cam)
}

func (cm *CameraManager) probeStreamEncoding(rtspURL string, cam config.CameraConfig) string {
	probeCfg := recorder.RTSPProbeConfig{
		RTSPURL:       rtspURL,
		RTSPTransport: cameraRTSPTransport(cam),
		Username:      cam.Username,
		Password:      cam.Password,
		Timeout:       10 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := recorder.ProbeRTSPEncoding(ctx, probeCfg)
	if err != nil {
		logger.Debug("RTSP stream encoding probe failed",
			"camera_id", cam.ID,
			"url", rtspURL,
			"error", err)
		return ""
	}

	return result.DetectedEncoding
}
