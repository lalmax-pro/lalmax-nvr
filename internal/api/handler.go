package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/ai"
	"github.com/lalmax-pro/lalmax-nvr/internal/camera"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/gb28181"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/merge"
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

var logger = slog.Default().With("component", "api")

var appStartTime = time.Now()

// HealthCheck represents the result of a single health check.
type HealthCheck struct {
	Status  string `json:"status"` // "ok" | "warning" | "error"
	Message string `json:"message,omitempty"`
}

// HealthResponse is the response from /api/health.
type HealthResponse struct {
	Status        string                 `json:"status"` // "ok" | "degraded" | "unhealthy"
	Checks        map[string]HealthCheck `json:"checks"`
	Uptime        string                 `json:"uptime"`
	StartTime     time.Time              `json:"start_time"`
	SetupRequired bool                   `json:"setup_required"`
	Cameras       *CameraHealthSummary   `json:"cameras,omitempty"`
}

// CameraHealthSummary provides aggregated camera health in the /api/health response.
type CameraHealthSummary struct {
	Total        int                  `json:"total"`
	Recording    int                  `json:"recording"`
	Reconnecting int                  `json:"reconnecting"`
	Error        int                  `json:"error"`
	Offline      int                  `json:"offline"`
	Details      []CameraHealthDetail `json:"details"`
}

// CameraHealthDetail is a per-camera summary included in /api/health.
type CameraHealthDetail struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// SystemStats is the response from /api/stats/system.
type SystemStats struct {
	CPU       CPUStats     `json:"cpu"`
	Memory    MemoryStats  `json:"memory"`
	Network   NetworkStats `json:"network"`
	System    SystemInfo   `json:"system"`
	Uptime    string       `json:"uptime"`
	Timestamp int64        `json:"timestamp"`
}

type SystemInfo struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	CPUCores int    `json:"cpu_cores"`
}

type CPUStats struct {
	Total uint64 `json:"total"` // cumulative total jiffies
	Idle  uint64 `json:"idle"`  // cumulative idle jiffies
}

type MemoryStats struct {
	Total      uint64 `json:"total"`       // MemTotal bytes
	Available  uint64 `json:"available"`   // MemAvailable bytes
	ProcessRSS uint64 `json:"process_rss"` // NVR process RSS bytes
}

type NetworkStats struct {
	BytesSent uint64 `json:"bytes_sent"`
	BytesRecv uint64 `json:"bytes_recv"`
}

// capabilitiesResponse is the response for GET /api/capabilities.
type capabilitiesResponse struct {
	Ingest ingestCapabilities `json:"ingest"`
}

type ingestCapabilities struct {
	RTMP *protocolCapability `json:"rtmp,omitempty"`
	SRT  *protocolCapability `json:"srt,omitempty"`
}

type protocolCapability struct {
	Enabled bool `json:"enabled"`
	Port    int  `json:"port"`
}
type snapshotCache struct {
	data      []byte
	timestamp time.Time
}

// Handler holds dependencies for the REST API handlers.

type Handler struct {
	db                *storage.DB
	store             *storage.Manager
	authMW            func(http.Handler) http.Handler
	multiUserMW       func(http.Handler) http.Handler
	config            *config.Config
	configWatcher     *config.Watcher
	camMgr            *camera.CameraManager
	mediaEngine       media.Engine
	wsMgr             media.WS
	configPath        string
	snapshotMu        sync.RWMutex
	snapshots         map[string]*snapshotCache // cameraID -> cached snapshot
	mergeMgr          *merge.MergeManager
	healthMgr         HealthManager
	stabilityProvider StabilityProvider
	cloudProxy        CloudAuthProxy
	streamRegistry    *StreamRegistry
	aiManager         *ai.Manager
	onvifDiscover     func(ctx context.Context, timeout time.Duration) *onvif.DiscoveryResult
	onvifProbeDevice  func(ctx context.Context, host string, port int, timeout time.Duration) (*onvif.DiscoveredDevice, error)
	onvifNewClient    func(endpoint, username, password string) onvifDeviceClient
	banMgr            BanManager
	gb28181Svr        GB28181StreamStatus
	gb28181Restarter  GB28181Restarter
	gb28181Server     *gb28181.Server
	snapshotMgr       *camera.SnapshotManager
	// readyzDiskUsage overrides disk probing for /api/readyz (tests only).
	readyzDiskUsage func() (total, used int64, err error)
	// sysMetrics holds the in-memory ring buffer of periodic system metric samples.
	sysMetrics *sysMetricsHistory
	// streamMetrics holds per-stream ring buffers of periodic live metric samples.
	streamMetrics *streamMetricsHistory
}

// GB28181StreamStatus reports active GB28181 play sessions for stream status overlay.
type GB28181StreamStatus interface {
	IsStreamPlaying(streamID string) bool
}

// GB28181Restarter restarts the GB28181 SIP server with new configuration.
type GB28181Restarter interface {
	RestartGB28181(ctx context.Context, cfg *config.GB28181Config) error
}

type GB28181ServerProvider interface {
	CurrentGB28181Server() *gb28181.Server
}

func (h *Handler) SetConfigWatcher(w *config.Watcher) {
	h.configWatcher = w
}

func NewHandler(db *storage.DB, store *storage.Manager, authMW func(http.Handler) http.Handler, cfg *config.Config, camMgr *camera.CameraManager, configPath string, mergeMgr *merge.MergeManager, cloudProxy CloudAuthProxy) *Handler {
	h := &Handler{
		db:               db,
		store:            store,
		authMW:           authMW,
		config:           cfg,
		camMgr:           camMgr,
		configPath:       configPath,
		snapshots:        make(map[string]*snapshotCache),
		mergeMgr:         mergeMgr,
		cloudProxy:       cloudProxy,
		sysMetrics:       newSysMetricsHistory(),
		streamMetrics:    newStreamMetricsHistory(),
		onvifDiscover:    onvif.Discover,
		onvifProbeDevice: onvif.ProbeDevice,
		onvifNewClient: func(endpoint, username, password string) onvifDeviceClient {
			return onvif.NewClient(endpoint, username, password)
		},
	}
	// Default multi-user auth: wraps authMW and injects a super_admin user
	// so RequireOperatePermission works without explicit SetMultiUserAuthMW.
	h.SetMultiUserAuthMW(func(next http.Handler) http.Handler {
		return authMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, _, _ := r.BasicAuth()
			if user == "" {
				if tok := r.URL.Query().Get("token"); tok != "" {
					if decoded, err := base64.StdEncoding.DecodeString(tok); err == nil {
						if parts := strings.SplitN(string(decoded), ":", 2); len(parts) == 2 {
							user = parts[0]
						}
					}
				}
			}
			if user == "" {
				user = "admin"
			}
			ctx := context.WithValue(r.Context(), middleware.UserContextKey, &model.User{
				ID:       1,
				Username: user,
				Role:     model.RoleSuperAdmin,
				Enabled:  true,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		}))
	})
	return h
}

func (h *Handler) SetMultiUserAuthMW(mw func(http.Handler) http.Handler) {
	h.multiUserMW = mw
}

// SetAIManager sets the AI Manager on the handler.
func (h *Handler) SetAIManager(mgr *ai.Manager) {
	h.aiManager = mgr
	if h.aiManager != nil && h.db != nil {
		h.aiManager.SetStore(h.db)
	}
}

// Routes returns a chi.Router with all routes registered.
// It also starts the background system-metrics sampler (once per handler instance).
func (h *Handler) Routes() http.Handler {
	h.startMetricsSampler(context.Background())
	h.startStreamMetricsSampler(context.Background())
	r := chi.NewRouter()

	// Public routes with rate limiting on health/readyz
	r.Group(func(r chi.Router) {
		rl := middleware.NewRateLimiter(middleware.RateLimiterConfig{
			MaxRequests: 60,
			Window:      time.Minute,
		})
		r.Use(rl)
		r.Get("/api/health", h.handleHealth)
		r.Get("/api/health/cameras", h.handleHealthCameras)
		r.Get("/api/readyz", h.handleReadyz)
		r.Get("/api/capabilities", h.handleCapabilities)
	})
	r.Post("/api/auth/login", h.handleLogin)
	r.Post("/api/setup", h.handleSetup)

	// Protected routes
	r.Group(func(r chi.Router) {
		if h.multiUserMW != nil {
			r.Use(h.multiUserMW)
		} else {
			r.Use(h.authMW)
		}
		r.Route("/api/recordings", func(r chi.Router) {
			r.Get("/", h.handleListRecordings)
			r.With(middleware.RequireOperatePermission()).Post("/batch-delete", h.handleBatchDeleteRecordings)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", h.handleGetRecording)
				r.With(middleware.RequireOperatePermission()).Delete("/", h.handleDeleteRecording)
				r.Get("/download", h.handleDownloadRecording)
				r.Get("/frames", h.handleListFrames)
			})
		})
		r.Route("/api/events", func(r chi.Router) {
			r.Get("/", h.handleListEvents)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", h.handleGetEvent)
				r.With(middleware.RequireOperatePermission()).Delete("/", h.handleDeleteEvent)
				r.Post("/ack", h.handleAcknowledgeEvent)
			})
		})
		r.Route("/api/cameras", func(r chi.Router) {
			r.Get("/", h.handleListCameras)
			r.With(middleware.RequireOperatePermission()).Post("/", h.handleCreateCamera)
			r.With(middleware.RequireOperatePermission()).Post("/test-connection", h.handleTestConnection)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", h.handleGetCamera)
				r.With(middleware.RequireOperatePermission()).Put("/", h.handleUpdateCamera)
				r.With(middleware.RequireOperatePermission()).Delete("/", h.handleDeleteCamera)
				r.With(middleware.RequireOperatePermission()).Delete("/permanent", h.handlePermanentDeleteCamera)
				// WebSocket stream (must be before HLS catch-all /stream/*)
				r.Get("/stream/ws", h.handleStreamWS)
				// HTTP fMP4 stream (must be before HLS catch-all /stream/*)
				r.Get("/stream.m4s", h.handleFMP4Stream)
				r.Get("/stream/*", h.handleHLSStream)
				r.Delete("/stream", h.handleStopHLSStream)
				// WebRTC WHEP endpoints
				r.Post("/stream/webrtc", h.handleCreateWHEPSession)
				r.Delete("/stream/webrtc/{session}", h.handleDeleteWHEPSession)
				// HTTP-FLV stream
				r.Get("/stream.flv", h.handleFLVStream)
				// Per-camera protocols
				r.Get("/protocols", h.handleCameraProtocols)
				r.Get("/onvif/profiles", h.handleONVIFCameraProfiles)
				r.Get("/onvif/capabilities", h.handleONVIFCapabilities)
				r.Post("/ptz/move", h.handlePTZMove)
				r.Post("/ptz/stop", h.handlePTZStop)
				r.Get("/ptz/status", h.handlePTZStatus)
				r.Get("/ptz/presets", h.handlePTZGetPresets)
				r.With(middleware.RequireOperatePermission()).Post("/ptz/presets", h.handlePTZCreatePreset)
				r.With(middleware.RequireOperatePermission()).Post("/ptz/presets/{token}/goto", h.handlePTZGoToPreset)
				r.With(middleware.RequireOperatePermission()).Delete("/ptz/presets/{token}", h.handlePTZDeletePreset)
				r.Get("/snapshot/uri", h.handleSnapshotGetUri)
				r.Get("/imaging/settings", h.handleImagingGetSettings)
				r.With(middleware.RequireOperatePermission()).Put("/imaging/settings", h.handleImagingSetSettings)
				r.Get("/imaging/options", h.handleImagingGetOptions)
				// Device management
				r.With(middleware.RequireOperatePermission()).Post("/onvif/reboot", h.handleONVIFReboot)
				r.Get("/onvif/network", h.handleONVIFGetNetwork)
				r.With(middleware.RequireOperatePermission()).Put("/onvif/network", h.handleONVIFSetNetwork)
				r.Get("/onvif/users", h.handleONVIFGetUsers)
				r.With(middleware.RequireOperatePermission()).Post("/onvif/users", h.handleONVIFCreateUsers)
				r.With(middleware.RequireOperatePermission()).Delete("/onvif/users", h.handleONVIFDeleteUsers)
				r.With(middleware.RequireOperatePermission()).Put("/onvif/users/{username}", h.handleONVIFSetUser)
				// ONVIF Recording
				r.Get("/onvif/recordings", h.handleGetONVIFRecordings)
				r.Get("/onvif/recordings/{token}", h.handleGetONVIFRecordingInformation)
				r.Get("/onvif/recordings/search", h.handleSearchONVIFRecordings)
				r.Get("/onvif/replay/{token}", h.handleGetONVIFReplayURI)
				r.Get("/snapshot", h.handleSnapshot)
				r.With(middleware.RequireOperatePermission()).Put("/merge-config", h.handleUpdateCameraMergeConfig)
				r.With(middleware.RequireOperatePermission()).Delete("/merge-config", h.handleDeleteCameraMergeConfig)
				r.Get("/stats", h.handleCameraRecordingStats)
				r.Get("/recordings/days", h.handleCameraRecordingDays)
				// Per-camera timelapse configuration
				r.Get("/timelapse", h.handleGetCameraTimelapse)
				r.With(middleware.RequireOperatePermission()).Put("/timelapse", h.handlePutCameraTimelapse)
				// Per-camera recording schedule (used when recording_mode = scheduled)
				r.Get("/recording-schedule", h.handleGetRecordingSchedule)
				r.With(middleware.RequireOperatePermission()).Put("/recording-schedule", h.handleSetRecordingSchedule)
				r.With(middleware.RequireOperatePermission()).Post("/start", h.handleStartCamera)
				r.With(middleware.RequireOperatePermission()).Post("/stop", h.handleStopCamera)
				r.With(middleware.RequireOperatePermission()).Post("/pause-recording", h.handlePauseRecording)
				r.With(middleware.RequireOperatePermission()).Post("/resume-recording", h.handleResumeRecording)
			})
		})
		r.Get("/api/stats", h.handleStats)
		r.Get("/api/stats/system", h.handleSystemStats)
		r.Get("/api/stats/trends", h.handleStatsTrends)
		r.Get("/api/stats/system/history", h.handleSystemHistory)
		r.Get("/api/stats/hourly", h.handleHourlyStats)
		r.Get("/api/stats/camera-uptime", h.handleCameraUptimeStats)
		r.Get("/api/network", h.handleGetNetworkInterfaces)
		r.Get("/api/streams", h.handleListStreams)
		r.Get("/api/streams/{stream_id}", h.handleGetStream)
		r.Get("/api/streams/{stream_id}/metrics/history", h.handleStreamMetricsHistory)
		r.With(middleware.RequireOperatePermission()).Post("/api/streams/{stream_id}/bind-camera", h.handleBindCamera)
		r.With(middleware.RequireOperatePermission()).Post("/api/streams/{stream_id}/unbind-camera", h.handleUnbindCamera)
		r.With(middleware.RequireOperatePermission()).Post("/api/streams/{stream_id}/promote", h.handlePromoteStream)
		r.With(middleware.RequireOperatePermission()).Delete("/api/streams/{stream_id}", h.handleDeleteStream)
		r.With(middleware.RequireOperatePermission()).Post("/api/streams/{stream_id}/kick-publisher", h.handleKickPublisher)
		r.With(middleware.RequireOperatePermission()).Post("/api/streams/{stream_id}/ban", h.handleBanStream)
		r.With(middleware.RequireOperatePermission()).Delete("/api/streams/{stream_id}/ban", h.handleUnbanStream)
		r.Get("/api/streams/bans", h.handleListBans)
		r.Get("/api/streams/history", h.handleListStreamHistory)
		r.Delete("/api/streams/history/{stream_id}", h.handleDeleteStreamHistory)
		r.Get("/api/settings", h.handleGetSettings)
		r.With(middleware.RequireOperatePermission()).Put("/api/settings", h.handleUpdateSettings)
		r.Get("/api/settings/merge", h.handleGetMergeSettings)
		r.With(middleware.RequireOperatePermission()).Put("/api/settings/merge", h.handleUpdateMergeSettings)
		r.Get("/api/settings/streaming", h.handleGetStreamingSettings)
		r.With(middleware.RequireOperatePermission()).Put("/api/settings/streaming", h.handleUpdateStreamingSettings)
		r.Get("/api/settings/gb28181", h.handleGetGB28181Settings)
		r.With(middleware.RequireOperatePermission()).Put("/api/settings/gb28181", h.handleUpdateGB28181Settings)
		r.Get("/api/settings/hls", h.handleGetHLSSettings)
		r.With(middleware.RequireOperatePermission()).Put("/api/settings/hls", h.handleUpdateHLSSettings)
		r.Get("/api/settings/ai", h.handleGetAISettings)
		r.With(middleware.RequireOperatePermission()).Put("/api/settings/ai", h.handleUpdateAISettings)
		r.With(middleware.RequireOperatePermission()).Post("/api/settings/lalmax/regenerate", h.handleRegenerateLalmaxConfig)
		r.With(middleware.RequireOperatePermission()).Post("/api/config/reload", h.handleReloadConfig)
		r.Get("/api/config/check", h.handleCheckConfigChange)
		r.With(middleware.RequireOperatePermission()).Post("/api/backup", h.handleBackup)
		r.Get("/api/backups", h.handleListBackups)
		r.Post("/api/onvif/discover", h.handleONVIFDiscover)
		r.Get("/api/onvif/discover/{ip}", h.handleONVIFDeviceDetail)
		r.Post("/api/onvif/probe", h.handleONVIFProbe)
		r.Get("/api/merge/status", h.handleMergeStatus)
		r.Get("/api/merge/pending", h.handleMergePending)
		r.Get("/api/protocols", h.handleProtocols)
		r.Get("/api/features", h.handleGetFeatures)
		r.With(middleware.RequireOperatePermission()).Put("/api/features", h.handleUpdateFeatures)
		// User management (super_admin only)
		r.Route("/api/users", func(r chi.Router) {
			r.Use(middleware.RequireOperatePermission())
			r.Get("/", h.handleListUsers)
			r.Post("/", h.handleCreateUser)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", h.handleGetUser)
				r.Put("/", h.handleUpdateUser)
				r.Delete("/", h.handleDeleteUser)
			})
		})
		// Archive endpoints
		r.Route("/api/archives", func(r chi.Router) {
			r.Get("/", h.handleListArchives)
			r.With(middleware.RequireOperatePermission()).Post("/{cameraID}/restore", h.handleRestoreArchiveGroup)
			r.Get("/{cameraID}/recordings", h.handleListArchiveRecordings)
			r.With(middleware.RequireOperatePermission()).Delete("/{cameraID}", h.handleDeleteArchiveGroup)
			r.With(middleware.RequireOperatePermission()).Delete("/{cameraID}/recordings/{recordingID}", h.handleDeleteArchiveRecording)
			r.With(middleware.RequireOperatePermission()).Put("/{cameraID}/retention", h.handleSetArchiveRetention)
		})
		// Xiaomi cloud auth and device discovery
		r.Route("/api/xiaomi", func(r chi.Router) {
			r.Post("/auth", h.handleXiaomiAuth)
			r.Post("/captcha", h.handleXiaomiCaptcha)
			r.Post("/verify", h.handleXiaomiVerify)
			r.Get("/devices", h.handleXiaomiDevices)
			r.Post("/sync", h.handleXiaomiSync)
			r.Get("/check-vendor", h.handleCheckVendor)
			r.Get("/talk/ws", h.handleXiaomiTalkWS)
		})
		// Health monitoring endpoints
		r.Get("/api/health/status", h.handleGetHealthStatus)
		r.Get("/api/health/events", h.handleGetHealthEvents)
		r.Get("/api/health/stability", h.handleGetStability)
		r.Get("/api/health/stability/{camera_id}", h.handleGetCameraStability)
		r.Get("/api/cameras/{id}/health", h.handleGetCameraHealth)
		// AI Detection routes (Webhook mode only - external ai-detector pushes results)
		r.Route("/api/ai", func(r chi.Router) {
			r.Get("/status", h.handleGetAIStatus)
			r.Get("/events", h.handleAIEvents)
			r.Get("/detections", h.handleListAIDetections)
			r.Get("/analyses", h.handleListAIAnalyses)
			r.Post("/webhook", h.handleAIWebhook)
			// Multimodal analysis routes
			r.Get("/multimodal/status", h.handleAIMultimodalStatus)
			r.Get("/multimodal/events", h.handleAIMultimodalEvents)
		})
		// Snapshot endpoints
		r.Route("/api/snapshots", func(r chi.Router) {
			r.Get("/{camera_id}", h.handleGetSnapshot)
			r.Get("/{camera_id}/latest", h.handleGetLatestSnapshot)
			r.Post("/{camera_id}/take", h.handleTakeSnapshot)
		})
		// Telemetry
		r.With(telemetryRateLimiter()).Post("/api/telemetry", h.HandleTelemetry)
		// Device Groups
		r.Route("/api/groups", func(r chi.Router) {
			r.Get("/", h.handleListGroups)
			r.Post("/", h.handleCreateGroup)
			r.Get("/tree", h.handleGetGroupTree)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", h.handleGetGroup)
				r.Put("/", h.handleUpdateGroup)
				r.Delete("/", h.handleDeleteGroup)
				r.Get("/channels", h.handleListGroupChannels)
				r.Post("/channels", h.handleAddGroupChannel)
				r.Delete("/channels", h.handleRemoveGroupChannel)
			})
		})
		// GB28181 API routes
		r.Route("/api/gb28181", func(r chi.Router) {
			r.Get("/devices", h.handleGB28181ListDevices)
			r.Post("/play", h.handleGB28181Play)
			r.Post("/stop", h.handleGB28181StopPlay)
			r.Post("/record_info", h.handleGB28181RecordInfo)
			r.Post("/playback", h.handleGB28181Playback)
			r.Post("/playback/speed", h.handleGB28181PlaySpeed)
			r.Post("/playback/seek", h.handleGB28181PlaySeek)
			r.Post("/playback/pause", h.handleGB28181PlayPause)
			r.Post("/playback/resume", h.handleGB28181PlayResume)
			r.Get("/platforms", h.handleGB28181ListPlatforms)
			r.Post("/platforms", h.handleGB28181AddPlatform)
			r.Delete("/platforms", h.handleGB28181DeletePlatform)
			r.Post("/broadcast/start", h.handleGB28181StartBroadcast)
			r.Post("/broadcast/stop", h.handleGB28181StopBroadcast)
			r.Get("/talk/ws", h.handleGB28181TalkWS)
			r.Post("/talk/start", h.handleGB28181StartTalkWhip)
			r.Post("/talk/stop", h.handleGB28181StopTalkWhip)
			r.Get("/alarms", h.handleGB28181ListAlarms)
			r.Get("/platform/events", h.handleGB28181ListPlatformEvents)
			r.Get("/platform/status", h.handleGB28181GetPlatformStatus)
			r.Post("/download/start", h.handleGB28181StartDownload)
			r.Post("/download/batch", h.handleGB28181BatchDownload)
			r.Post("/download/stop", h.handleGB28181StopDownload)
			r.Get("/downloads", h.handleGB28181ListDownloads)

			// 国标目录: 行政区划 / 业务分组
			r.Get("/regions/tree", h.handleGBRegionTree)
			r.Get("/regions/candidates", h.handleGBCivilCodeCandidates)
			r.Get("/regions/description", h.handleGBCivilCodeDescription)
			r.With(middleware.RequireOperatePermission()).Post("/regions", h.handleGBRegionCreate)
			r.With(middleware.RequireOperatePermission()).Put("/regions/{id}", h.handleGBRegionUpdate)
			r.With(middleware.RequireOperatePermission()).Delete("/regions/{id}", h.handleGBRegionDelete)
			r.With(middleware.RequireOperatePermission()).Post("/regions/sync", h.handleGBRegionSync)
			r.With(middleware.RequireOperatePermission()).Post("/regions/by-civil-code", h.handleGBRegionAddByCivilCode)

			r.Get("/groups/tree", h.handleGBGroupTree)
			r.With(middleware.RequireOperatePermission()).Post("/groups", h.handleGBGroupCreate)
			r.With(middleware.RequireOperatePermission()).Put("/groups/{id}", h.handleGBGroupUpdate)
			r.With(middleware.RequireOperatePermission()).Delete("/groups/{id}", h.handleGBGroupDelete)

			r.Get("/catalog/channels", h.handleGBCatalogChannels)
			r.With(middleware.RequireOperatePermission()).Post("/channels/region", h.handleGBChannelAttachRegion)
			r.With(middleware.RequireOperatePermission()).Delete("/channels/region", h.handleGBChannelDetachRegion)
			r.With(middleware.RequireOperatePermission()).Post("/channels/group", h.handleGBChannelAttachGroup)
			r.With(middleware.RequireOperatePermission()).Delete("/channels/group", h.handleGBChannelDetachGroup)
			r.Route("/devices/{deviceID}/channels/{channelID}", func(r chi.Router) {
				// PTZ控制
				r.Post("/ptz", h.handleGB28181PTZControl)
				r.Post("/ptz/position", h.handleGB28181PTZPositionControl)
				r.Get("/ptz/position", h.handleGB28181PTZQueryPosition)

				// 预置位管理
				r.Post("/preset/{presetID}", h.handleGB28181PresetSet)
				r.Post("/preset/{presetID}/call", h.handleGB28181PresetCall)
				r.Delete("/preset/{presetID}", h.handleGB28181PresetDelete)

				// 巡航控制
				r.Post("/cruise/point", h.handleGB28181CruiseAddPoint)
				r.Delete("/cruise/point", h.handleGB28181CruiseDeletePoint)
				r.Post("/cruise/{cruiseID}/start", h.handleGB28181CruiseStart)
				r.Post("/cruise/{cruiseID}/stop", h.handleGB28181CruiseStop)

				// 图像抓拍
				r.Post("/snapshot", h.handleGB28181Snapshot)

				// 设备控制
				r.Post("/reset", h.handleGB28181DeviceReset)
				r.Post("/record", h.handleGB28181RecordControl)
				r.Post("/home-position", h.handleGB28181HomePosition)

				// 设备查询
				r.Get("/info", h.handleGB28181DeviceInfoQuery)
				r.Get("/status", h.handleGB28181DeviceStatusQuery)
				r.Get("/config", h.handleGB28181DeviceConfigQuery)
			})
		})
	})

	return r
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeAPIError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error(), "code": model.ErrorCode(err)})
}

// isImageFile checks if a filename has an image extension (jpg/jpeg/png).
func isImageFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".png")
}

// validateURL checks that a URL has a valid scheme and non-empty host.
// This is a basic sanity check — specific protocol validation is handled separately.
func validateURL(rawURL string) bool {
	if rawURL == "" {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	return true
}

// validateIP checks that a string is a valid IPv4 or IPv6 address, supporting ip:port format.
func validateIP(ip string) bool {
	// Support ip:port format (e.g., "192.168.63.162:8080")
	if host, _, err := net.SplitHostPort(ip); err == nil {
		return net.ParseIP(host) != nil
	}
	return net.ParseIP(ip) != nil
}

// noopAuthMW is a middleware that passes all requests through (no auth).
func noopAuthMW() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return next
	}
}

// noopHandler is a helper for creating a Handler without real auth.
func noopHandler(db *storage.DB, store *storage.Manager) *Handler {
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.readyzDiskUsage = func() (int64, int64, error) {
		return 1_000_000_000_000, 100_000_000_000, nil
	}
	// Inject a super_admin user so RequireOperatePermission works in tests
	h.SetMultiUserAuthMW(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.UserContextKey, &model.User{
				ID:       1,
				Username: "admin",
				Role:     model.RoleSuperAdmin,
				Enabled:  true,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	return h
}

// --- Test helper exported for handler_test.go ---

// TestHandler creates a Handler with a no-op auth middleware for testing.
func TestHandler(db *storage.DB, store *storage.Manager) *Handler {
	return noopHandler(db, store)
}

// TestHandlerWithAuth creates a Handler with real auth middleware for testing.
func TestHandlerWithAuth(db *storage.DB, store *storage.Manager, username, passwordHash string) *Handler {
	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{
		GetUsername: func() string { return username },
		GetHash:     func() string { return passwordHash },
	}, "")
	h := NewHandler(db, store, authMW, nil, nil, "", nil, nil)
	// Set up multi-user middleware that injects a super_admin user after auth succeeds
	h.SetMultiUserAuthMW(func(next http.Handler) http.Handler {
		return authMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Auth already passed (middleware returned 200), inject super_admin user
			ctx := context.WithValue(r.Context(), middleware.UserContextKey, &model.User{
				ID:       1,
				Username: username,
				Role:     model.RoleSuperAdmin,
				Enabled:  true,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		}))
	})
	return h
}

// extractDIDFromURL parses the DID from a xiaomi:// URL.
func extractDIDFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// strPtr returns a pointer to the given string.
func strPtr(s string) *string {
	return &s
}

// SetWSManager sets the WebSocket stream manager on the handler.
func (h *Handler) SetWSManager(mgr media.WS) {
	h.wsMgr = mgr
}

// SetMediaEngine sets the lal/lalmax-backed media engine on the handler.
func (h *Handler) SetMediaEngine(engine media.Engine) {
	h.mediaEngine = engine
}

// SetHealthManager sets the health manager on the handler.
func (h *Handler) SetHealthManager(mgr HealthManager) {
	h.healthMgr = mgr
}

// SetStabilityProvider sets the stability data provider on the handler.
func (h *Handler) SetStabilityProvider(p StabilityProvider) {
	h.stabilityProvider = p
}

// BanManager provides stream ban operations.
type BanManager interface {
	Ban(ctx context.Context, streamID, reason string, expiresAt *time.Time) error
	Unban(ctx context.Context, streamID string) error
	ListBans(ctx context.Context) ([]storage.StreamBan, error)
}

func (h *Handler) SetGB28181Server(svr GB28181StreamStatus) {
	h.gb28181Svr = svr
}

func (h *Handler) SetGB28181ServerInstance(svr *gb28181.Server) {
	h.gb28181Server = svr
}

func (h *Handler) SetGB28181Restarter(r GB28181Restarter) {
	h.gb28181Restarter = r
}

func (h *Handler) SetSnapshotManager(mgr *camera.SnapshotManager) {
	h.snapshotMgr = mgr
}

func (h *Handler) SetBanManager(mgr BanManager) {
	h.banMgr = mgr
}

// --- Per-camera streaming protocols endpoint ---

// cameraProtocolsResponse is the response for GET /api/cameras/{id}/protocols.
type cameraProtocolsResponse struct {
	Protocols    []ProtocolDetail    `json:"protocols"`
	Encoding     string              `json:"encoding"`
	Default      string              `json:"default"`
	StreamStatus *cameraStreamStatus `json:"stream_status,omitempty"`
}

type cameraStreamStatus struct {
	Engine      string                `json:"engine"`
	StreamID    string                `json:"stream_id"`
	AppName     string                `json:"app_name,omitempty"`
	Active      bool                  `json:"active"`
	Publisher   *cameraSessionStatus  `json:"publisher,omitempty"`
	Subscribers []cameraSessionStatus `json:"subscribers,omitempty"`
	VideoCodec  string                `json:"video_codec,omitempty"`
	AudioCodec  string                `json:"audio_codec,omitempty"`
	InFPS       float64               `json:"in_fps,omitempty"`
	LastError   string                `json:"last_error,omitempty"`
}

type cameraSessionStatus struct {
	SessionID         string `json:"session_id"`
	Protocol          string `json:"protocol"`
	Remote            string `json:"remote,omitempty"`
	BitrateKbits      int    `json:"bitrate_kbits,omitempty"`
	ReadBitrateKbits  int    `json:"read_bitrate_kbits,omitempty"`
	WriteBitrateKbits int    `json:"write_bitrate_kbits,omitempty"`
}

// getCameraID returns the URL-decoded camera ID from the request.
// This handles IDs with colons (e.g., GB28181 device:channel) that get encoded as %3A.
func getCameraID(r *http.Request) string {
	id := chi.URLParam(r, "id")
	if decoded, err := url.PathUnescape(id); err == nil {
		return decoded
	}
	return id
}

func (h *Handler) mediaPlayURL(ctx context.Context, cameraID, protocol string) (*url.URL, error) {
	if h.mediaEngine == nil {
		return nil, nil
	}
	playURL, err := h.mediaEngine.BuildPlayURL(ctx, media.PlayURLRequest{
		StreamID: cameraID,
		AppName:  "live",
		Protocol: protocol,
	})
	if err != nil || playURL == nil || playURL.URL == "" {
		return nil, err
	}
	u, err := url.Parse(playURL.URL)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (h *Handler) mediaHLSResourceURL(ctx context.Context, cameraID, tail string, rawQuery string) (*url.URL, error) {
	isLLHLS := isLLHLSResource(tail, rawQuery)

	var baseURL *url.URL
	var err error
	if isLLHLS {
		// LL-HLS → lalmax (port 12090)
		baseURL, err = h.mediaPlayURL(ctx, cameraID, "ll-hls")
	} else {
		// Regular HLS → lal (port 8080)
		baseURL, err = h.mediaPlayURL(ctx, cameraID, "hls")
	}
	if err != nil || baseURL == nil {
		return baseURL, err
	}

	resource := *baseURL
	cleanTail := strings.TrimPrefix(tail, "/")
	if cleanTail != "" && cleanTail != "index.m3u8" {
		resource.Path = path.Join(path.Dir(baseURL.Path), cleanTail)
	}
	if rawQuery != "" {
		if resource.RawQuery == "" {
			resource.RawQuery = rawQuery
		} else {
			resource.RawQuery = resource.RawQuery + "&" + rawQuery
		}
	}
	return &resource, nil
}

func isLLHLSResource(tail string, rawQuery string) bool {
	if strings.Contains(rawQuery, "ll-hls=1") {
		return true
	}
	cleanTail := strings.TrimPrefix(tail, "/")
	ext := strings.ToLower(path.Ext(cleanTail))
	if ext == ".m3u8" && cleanTail != "" && cleanTail != "index.m3u8" {
		return true
	}
	return ext == ".mp4" || ext == ".m4s" || ext == ".cmfv" || ext == ".cmfa"
}

func (h *Handler) proxyMediaRequest(w http.ResponseWriter, r *http.Request, upstream *url.URL) error {
	if upstream == nil {
		return fmt.Errorf("upstream URL is nil")
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream.String(), r.Body)
	if err != nil {
		return err
	}
	req.Header = r.Header.Clone()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	copyHeaderFiltered(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	return err
}

func copyHeaderFiltered(dst, src http.Header) {
	for k := range dst {
		dst.Del(k)
	}
	for k, values := range src {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range values {
			dst.Add(k, v)
		}
	}
}

func encodeUpstreamSessionLocation(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeUpstreamSessionLocation(token string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (h *Handler) applyHLSAvailability(protocols []ProtocolDetail) []ProtocolDetail {
	if h.config == nil || h.config.IsHLSEnabled() {
		return protocols
	}
	for i := range protocols {
		switch protocols[i].Protocol {
		case "hls", "ll-hls":
			protocols[i].Available = false
			protocols[i].Reason = "Enable HLS in Settings"
			protocols[i].PlayURL = ""
			protocols[i].Backend = ""
		}
	}
	return protocols
}

// handleCameraProtocols handles GET /api/cameras/{id}/protocols.
// It returns the available streaming protocols for a specific camera
// based on its encoding and the registered stream handlers.
func (h *Handler) handleCameraProtocols(w http.ResponseWriter, r *http.Request) {
	id := getCameraID(r)

	cam, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	encoding := cam.Encoding
	if encoding == "" {
		encoding = cam.StreamEncoding
	}

	// If encoding still unknown (e.g. ONVIF auto-detect), probe the running recorder
	if encoding == "" && h.camMgr != nil {
		if rec := h.camMgr.GetRecorder(id); rec != nil {
			codec, _, _, _ := getCodecParams(rec)
			if codec != "" {
				encoding = string(codec)
			}
		}
	}

	var protocols []ProtocolDetail
	if h.streamRegistry != nil {
		if encoding != "" {
			protocols = h.streamRegistry.ProtocolsDetailForCodec(model.Format(encoding))
		} else {
			// Encoding unknown — show all protocols as potentially available
			protocols = h.streamRegistry.ProtocolsDetailForCodec(model.FormatH264)
		}
	}
	if protocols == nil {
		protocols = []ProtocolDetail{}
	}
	protocols = h.applyHLSAvailability(protocols)

	// Determine default protocol: prefer user-configured default, then fallback order
	defaultProto := ""
	if h.config != nil && h.config.Streaming.DefaultProtocol != "" {
		for _, p := range protocols {
			if p.Protocol == h.config.Streaming.DefaultProtocol && p.Available {
				defaultProto = h.config.Streaming.DefaultProtocol
				break
			}
		}
	}
	if defaultProto == "" {
		for _, preferred := range []string{"webrtc", "flv", "ll-hls", "hls"} {
			for _, p := range protocols {
				if p.Protocol == preferred && p.Available {
					defaultProto = preferred
					break
				}
			}
			if defaultProto != "" {
				break
			}
		}
	}

	protocols = h.attachMediaPlayURLs(r.Context(), id, protocols)

	writeJSON(w, http.StatusOK, cameraProtocolsResponse{
		Protocols:    protocols,
		Encoding:     encoding,
		Default:      defaultProto,
		StreamStatus: h.getCameraStreamStatus(r.Context(), id),
	})
}

func (h *Handler) attachMediaPlayURLs(ctx context.Context, cameraID string, protocols []ProtocolDetail) []ProtocolDetail {
	if len(protocols) == 0 {
		return protocols
	}
	for i := range protocols {
		if !protocols[i].Available {
			continue
		}
		if playURL, backend, ok := h.protocolPlayURL(ctx, cameraID, protocols[i].Protocol); ok {
			protocols[i].PlayURL = playURL
			protocols[i].Backend = backend
			continue
		}
	}
	return protocols
}

func (h *Handler) protocolPlayURL(ctx context.Context, cameraID, protocol string) (string, string, bool) {
	switch protocol {
	case "wasm":
		if h.wsMgr == nil {
			return "", "", false
		}
		return "/api/cameras/" + cameraID + "/stream/ws", "builtin-ws", true
	case "hls", "ll-hls", "webrtc", "flv", "ws-flv", "fmp4":
		if h.mediaEngine != nil {
			playURL, err := h.mediaEngine.BuildPlayURL(ctx, media.PlayURLRequest{
				StreamID: cameraID,
				AppName:  "live",
				Protocol: protocol,
			})
			if err == nil && playURL != nil && playURL.URL != "" {
				return playURL.URL, "lalmax", true
			}
		}
	}

	return "", "", false
}

func (h *Handler) getCameraStreamStatus(ctx context.Context, cameraID string) *cameraStreamStatus {
	if h.mediaEngine == nil {
		return nil
	}
	info, err := h.mediaEngine.GetStream(ctx, cameraID)
	if err != nil {
		return &cameraStreamStatus{
			Engine:    "lalmax",
			StreamID:  cameraID,
			LastError: err.Error(),
		}
	}
	if info == nil {
		return &cameraStreamStatus{
			Engine:   "lalmax",
			StreamID: cameraID,
		}
	}

	status := &cameraStreamStatus{
		Engine:     "lalmax",
		StreamID:   info.StreamID,
		AppName:    info.AppName,
		Active:     info.Active,
		VideoCodec: info.VideoCodec,
		AudioCodec: info.AudioCodec,
		InFPS:      info.InFPS,
	}
	if info.Publisher != nil {
		status.Publisher = &cameraSessionStatus{
			SessionID:         info.Publisher.SessionID,
			Protocol:          info.Publisher.Protocol,
			Remote:            info.Publisher.Remote,
			BitrateKbits:      info.Publisher.BitrateKbits,
			ReadBitrateKbits:  info.Publisher.ReadBitrateKbits,
			WriteBitrateKbits: info.Publisher.WriteBitrateKbits,
		}
	}
	if len(info.Subscribers) > 0 {
		status.Subscribers = make([]cameraSessionStatus, 0, len(info.Subscribers))
		for _, sub := range info.Subscribers {
			status.Subscribers = append(status.Subscribers, cameraSessionStatus{
				SessionID:         sub.SessionID,
				Protocol:          sub.Protocol,
				Remote:            sub.Remote,
				BitrateKbits:      sub.BitrateKbits,
				ReadBitrateKbits:  sub.ReadBitrateKbits,
				WriteBitrateKbits: sub.WriteBitrateKbits,
			})
		}
	}
	return status
}

// handleCapabilities handles GET /api/capabilities.
// It returns the server's ingest capabilities (RTMP, SRT).
func (h *Handler) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	resp := capabilitiesResponse{}

	if h.config.RTMP.Enabled != nil && *h.config.RTMP.Enabled {
		resp.Ingest.RTMP = &protocolCapability{
			Enabled: true,
			Port:    1935, // lalmax default RTMP port
		}
	} else {
		resp.Ingest.RTMP = &protocolCapability{
			Enabled: false,
			Port:    1935,
		}
	}

	if h.config.SRT.Enabled != nil && *h.config.SRT.Enabled {
		resp.Ingest.SRT = &protocolCapability{
			Enabled: true,
			Port:    9000, // lalmax default SRT port
		}
	} else {
		resp.Ingest.SRT = &protocolCapability{
			Enabled: false,
			Port:    9000,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
