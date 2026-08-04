package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

const (
	DefaultLalmaxHTTPPort = 12090
	DefaultLalmaxHTTPAddr = "http://127.0.0.1:12090"
	DefaultLalRTMPPort    = 11935
	DefaultLalRTSPPort    = 15544
	DefaultLalHTTPPort    = 18080
	DefaultSRTPort        = 19000
)

type Config struct {
	Server        ServerConfig        `yaml:"server"`
	Storage       StorageConfig       `yaml:"storage"`
	Media         MediaConfig         `yaml:"media"`
	Cameras       []CameraConfig      `yaml:"cameras"`
	Cleanup       CleanupConfig       `yaml:"cleanup"`
	Merge         MergeConfig         `yaml:"merge"`
	Auth          AuthConfig          `yaml:"auth"`
	FTP           FTPConfig           `yaml:"ftp"`
	MQTT          MQTTConfig          `yaml:"mqtt"`
	WebDAV        WebDAVConfig        `yaml:"webdav"`
	HLS           HLSConfig           `yaml:"hls"`
	Streaming     StreamingConfig     `yaml:"streaming"`
	Observability ObservabilityConfig `yaml:"observability"`
	Xiaomi        XiaomiConfig        `yaml:"xiaomi"`
	RTMP          RTMPConfig          `yaml:"rtmp"`
	SRT           SRTConfig           `yaml:"srt"`
	GB28181       GB28181Config       `yaml:"gb28181"`
	Health        HealthConfig        `yaml:"health"`
	RemoteLog     RemoteLogConfig     `yaml:"remote_log"`
	WebSocket     WebSocketConfig     `yaml:"websocket"`
	AI            AIConfig            `yaml:"ai"`
	MetricsAuth   MetricsAuthConfig   `yaml:"metrics_auth"`
	Snapshot      SnapshotConfig      `yaml:"snapshot"`
	Version       string              `yaml:"version"`
}

type SnapshotConfig struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`   // Enable periodic snapshots
	Interval string `yaml:"interval" json:"interval"` // Snapshot interval, default "5m", min "1m"
	Quality  int    `yaml:"quality" json:"quality"`   // JPEG quality 1-100, default 80
	MaxAge   string `yaml:"max_age" json:"max_age"`   // Max age before cleanup, default "24h"
}

type ServerConfig struct {
	Listen string `yaml:"listen"` // default ":9090"
}

type StorageConfig struct {
	RootDir         string `yaml:"root_dir"`         // default "/mnt/data/nvr"
	SegmentDuration string `yaml:"segment_duration"` // default "30s"
}

type MediaConfig struct {
	Mode             string `yaml:"mode"`
	LalmaxHTTPAddr   string `yaml:"lalmax_http_addr"`
	LalmaxPublicURL  string `yaml:"lalmax_public_url"`
	LalmaxConfigPath string `yaml:"lalmax_config_path"`
	// Lal protocol ports (used when mode=http)
	RTMPPort int `yaml:"rtmp_port,omitempty"`
	RTSPPort int `yaml:"rtsp_port,omitempty"`
	HTTPPort int `yaml:"http_port,omitempty"` // HTTP-FLV port
	// RTSP server authentication
	RTSPAuthEnable bool   `yaml:"rtsp_auth_enable,omitempty"` // Enable RTSP server auth (default false)
	RTSPAuthMethod int    `yaml:"rtsp_auth_method,omitempty"` // 0=Basic, 1=Digest (default 0)
	RTSPUsername   string `yaml:"rtsp_username,omitempty"`    // RTSP server username
	RTSPPassword   string `yaml:"rtsp_password,omitempty"`    // RTSP server password
}

type CameraConfig struct {
	ID                   string                 `yaml:"id"`
	Name                 string                 `yaml:"name"`
	Protocol             string                 `yaml:"protocol"`                 // rtsp_h264, rtsp_mjpeg, http_jpeg
	Encoding             string                 `yaml:"encoding"`                 // h264, h265, mjpeg, jpeg (independent of protocol)
	RTSPTransport        string                 `yaml:"rtsp_transport,omitempty"` // tcp or udp; default tcp
	URL                  string                 `yaml:"url"`
	Username             string                 `yaml:"username"`
	Password             string                 `yaml:"password"`
	ONVIFEndpoint        string                 `yaml:"onvif_endpoint"`
	ProfileToken         string                 `yaml:"profile_token"`
	StreamEncoding       string                 `yaml:"stream_encoding"` // H264 or H265, for ONVIF cameras. Empty = auto-detect.
	Enabled              bool                   `yaml:"enabled"`
	SubStreamURL         string                 `yaml:"sub_stream_url"`
	SnapshotURL          string                 `yaml:"snapshot_url"`
	SampleInterval       int                    `yaml:"sample_interval"`
	HLSMaxFPS            int                    `yaml:"hls_max_fps"`
	Merge                *MergeConfig           `yaml:"merge"`
	Timelapse            *CameraTimelapseConfig `yaml:"timelapse,omitempty" json:"timelapse,omitempty"`
	AudioEnabled         bool                   `yaml:"audio_enabled"`
	SourceType           string                 `yaml:"source_type,omitempty" json:"source_type,omitempty"`
	HealthOverrides      HealthOverrides        `yaml:"health_overrides,omitempty"`
	FrameWatchdogTimeout string                 `yaml:"frame_watchdog_timeout,omitempty"` // default "30s" (per-camera frame watchdog)
	PullRetryNum         int                    `yaml:"pull_retry_num,omitempty"`         // -1=forever, 0=never, >0=limited (default -1 for pull cameras)

	// Xiaomi-specific camera fields (only used when protocol is "xiaomi")
	DID    string `yaml:"did,omitempty"`    // Xiaomi Device ID
	Vendor string `yaml:"vendor,omitempty"` // Transport vendor: "cs2" (default)
}

// HealthOverrides allows per-camera health monitoring threshold overrides.
// When set, non-zero values take precedence over global health config.
type HealthOverrides struct {
	MaxIDRInterval         string  `yaml:"max_idr_interval,omitempty"`
	BitrateChangeThreshold float64 `yaml:"bitrate_change_threshold,omitempty"`
	MinFPS                 int     `yaml:"min_fps,omitempty"`
	OfflineThreshold       string  `yaml:"offline_threshold,omitempty"`
	FreezeTimeout          string  `yaml:"freeze_timeout,omitempty"`
}

// CameraSupportsAudioRecording reports whether a camera can record AAC/G.711 into MP4 segments.
func CameraSupportsAudioRecording(cam CameraConfig) bool {
	enc := strings.ToLower(strings.TrimSpace(cam.Encoding))
	switch cam.Protocol {
	case "rtsp", "onvif", "xiaomi":
	default:
		return false
	}
	switch enc {
	case "h264", "h265":
		return true
	case "", "mjpeg", "jpeg":
		return false
	default:
		return false
	}
}

// ApplyCameraAudioDefault enables audio recording for supported codecs unless explicitly disabled.
func ApplyCameraAudioDefault(cam *CameraConfig) {
	if cam == nil {
		return
	}
	if cam.AudioEnabled && (cam.Encoding == "jpeg" || cam.Encoding == "mjpeg") {
		slog.Warn("audio_enabled not supported for MJPEG/HTTP-JPEG cameras, disabling", "camera_id", cam.ID)
		cam.AudioEnabled = false
		return
	}
	if CameraSupportsAudioRecording(*cam) {
		cam.AudioEnabled = true
	}
}

func NormalizeRTSPTransport(transport string) string {
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "udp":
		return "udp"
	default:
		return "tcp"
	}
}

func IsValidRTSPTransport(transport string) bool {
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "", "tcp", "udp":
		return true
	default:
		return false
	}
}

type CleanupConfig struct {
	RetentionDays        int    `yaml:"retention_days"`         // default 30
	CheckInterval        string `yaml:"check_interval"`         // default "1h"
	DiskThresholdPercent int    `yaml:"disk_threshold_percent"` // default 95
}

type MergeConfig struct {
	Enabled            bool   `yaml:"enabled"`
	CheckInterval      string `yaml:"check_interval"`
	WindowSize         string `yaml:"window_size"`
	BatchLimit         int    `yaml:"batch_limit"`
	MinSegmentAge      string `yaml:"min_segment_age"`
	MinSegmentsToMerge int    `yaml:"min_segments_to_merge"`
}

type CameraTimelapseConfig struct {
	Enabled        bool   `yaml:"enabled" json:"enabled"`                                     // default false
	Interval       string `yaml:"interval,omitempty" json:"interval,omitempty"`               // snapshot interval, default "30s", min 1s
	OutputFPS      int    `yaml:"output_fps,omitempty" json:"output_fps,omitempty"`           // output framerate, default 30, range 1-60
	VideoCodec     string `yaml:"video_codec,omitempty" json:"video_codec,omitempty"`         // h264 or h265, default h264
	DeleteOriginal bool   `yaml:"delete_original,omitempty" json:"delete_original,omitempty"` // remove original segments after timelapse, default false
}

type AuthConfig struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
	Password     string `yaml:"password"`
}

type FTPConfig struct {
	Enabled          *bool  `yaml:"enabled"`            // default true
	Port             int    `yaml:"port"`               // default 2121
	PassivePortRange string `yaml:"passive_port_range"` // default "2122-2140"
}

type MQTTConfig struct {
	Enabled  bool   `yaml:"enabled"` // default false
	Broker   string `yaml:"broker"`
	Topic    string `yaml:"topic"`
	ClientID string `yaml:"client_id"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type WebDAVConfig struct {
	Enabled    *bool  `yaml:"enabled"`     // default true
	PathPrefix string `yaml:"path_prefix"` // default "/dav"
	ReadWrite  bool   `yaml:"read_write"`  // default false
}

// ObservabilityConfig defines observability settings
type ObservabilityConfig struct {
	LogLevel    string     `yaml:"log_level"`    // default "info"
	LogFormat   string     `yaml:"log_format"`   // default "text"
	EnablePprof bool       `yaml:"enable_pprof"` // default false
	OTLP        OTLPConfig `yaml:"otlp"`
}

// OTLPConfig defines export to an external OTLP/HTTP collector.
// Pointer booleans preserve an explicitly configured false while allowing
// traces and metrics to default to enabled.
type OTLPConfig struct {
	Enabled               bool              `yaml:"enabled"`
	Endpoint              string            `yaml:"endpoint"`
	ServiceName           string            `yaml:"service_name"`
	TracesEnabled         *bool             `yaml:"traces_enabled"`
	MetricsEnabled        *bool             `yaml:"metrics_enabled"`
	Headers               map[string]string `yaml:"headers,omitempty"`
	Timeout               string            `yaml:"timeout"`
	MetricsExportInterval string            `yaml:"metrics_export_interval"`
}

func (c OTLPConfig) ExportTraces() bool {
	return c.TracesEnabled == nil || *c.TracesEnabled
}

func (c OTLPConfig) ExportMetrics() bool {
	return c.MetricsEnabled == nil || *c.MetricsEnabled
}

type HLSConfig struct {
	Enabled          *bool  `yaml:"enabled"`             // default false
	OnDemand         *bool  `yaml:"on_demand"`           // default true: slice only when HLS is accessed
	IdleTimeout      string `yaml:"idle_timeout"`        // stop slicing after no HLS access (default "60s")
	WriteBufferSize  int    `yaml:"write_buffer_size"`   // async frame buffer per stream (default 100)
	SegmentMaxSizeMB int    `yaml:"segment_max_size_mb"` // HLS segment max size in MB (default 10)
	SegmentCount     int    `yaml:"segment_count"`       // HLS segment count per stream (default 7, range [3,10])
	MaxStreams       int    `yaml:"max_streams"`         // default 4 (RPi constraint)
	LowLatency       bool   `yaml:"low_latency"`         // enable Low-Latency HLS (gohlslib MuxerVariantLowLatency)
	PartMinDuration  string `yaml:"part_min_duration"`   // LL-HLS partial segment duration (default "200ms", range [100ms-1s])

	// lal (TS-based HLS) settings
	LalFragmentDurationMs int    `yaml:"lal_fragment_duration_ms,omitempty"` // TS fragment duration in ms (default 3000)
	LalFragmentNum        int    `yaml:"lal_fragment_num,omitempty"`         // Number of live playlist entries (default 6)
	LalCleanupMode        int    `yaml:"lal_cleanup_mode,omitempty"`         // 0=never, 1=end, 2=ASAP (default 1)
	LalUseMemory          bool   `yaml:"lal_use_memory,omitempty"`           // Use in-memory storage for TS segments
	LalTempDir            string `yaml:"lal_temp_dir,omitempty"`             // HLS TS鏂囦欢涓存椂鐩綍 (default "hls-temp")

	// lalmax (fMP4/LL-HLS) settings
	LalmaxSegmentDuration int `yaml:"lalmax_segment_duration,omitempty"` // fMP4 segment duration in seconds (default 1)
	LalmaxPartDuration    int `yaml:"lalmax_part_duration,omitempty"`    // LL-HLS partial segment duration in ms (default 200)
}

// StreamingConfig configures streaming protocol options (WebRTC, FLV, etc.)
type StreamingConfig struct {
	DefaultProtocol string       `yaml:"default_protocol"` // webrtc | flv | ws-flv | hls | ll-hls (default "webrtc")
	WebRTC          WebRTCConfig `yaml:"webrtc"`
	FLV             FLVConfig    `yaml:"flv"`
	// AutoStopNoViewSec is the seconds to wait before stopping a stream pull when no viewers.
	// Default: 300 (5 minutes). Set to 0 to disable auto-stop.
	AutoStopNoViewSec int `yaml:"auto_stop_no_view_sec,omitempty"`
}

// WebRTCConfig configures WebRTC WHEP streaming
type WebRTCConfig struct {
	Enabled     *bool  `yaml:"enabled"`      // default true
	MaxViewers  int    `yaml:"max_viewers"`  // default 2, range [1,10]
	IdleTimeout string `yaml:"idle_timeout"` // default "60s"
}

// FLVConfig configures HTTP-FLV streaming
type FLVConfig struct {
	Enabled      *bool  `yaml:"enabled"`        // default true
	MaxViewers   int    `yaml:"max_viewers"`    // default 10, range [1,50]
	IdleTimeout  string `yaml:"idle_timeout"`   // default "60s"
	GOPCacheSize int    `yaml:"gop_cache_size"` // default 1
}

// XiaomiConfig holds Xiaomi cloud authentication settings.
type XiaomiConfig struct {
	UserID string `yaml:"user_id"` // Xiaomi account user ID (from auth response)
	Token  string `yaml:"token"`   // Xiaomi passToken for API access
	Region string `yaml:"region"`  // Region code (e.g. "cn", "sg", "de")
}

// SRTConfig configures the SRT ingest server.
// SRT is served by lalmax on its default port (:19000).
// Camera pushes should use streamid format: #!::h=<camera_id>,m=publish
type SRTConfig struct {
	Enabled *bool `yaml:"enabled"` // default false
	Port    int   `yaml:"port,omitempty"`
}

// RTMPConfig configures the RTMP ingest server.
// RTMP is served by lalmax on its default port (:11935).
type RTMPConfig struct {
	Enabled    *bool             `yaml:"enabled"` // default false
	Port       int               `yaml:"port,omitempty"`
	StreamKeys map[string]string `yaml:"stream_keys"` // camera_id 鈫?stream_key
}

// GB28181Config configures the GB28181 SIP signaling server.
type GB28181Config struct {
	Enabled         *bool  `yaml:"enabled"`
	Host            string `yaml:"host"`             // SIP listen host (empty = auto-detect from media_ip)
	Port            int    `yaml:"port"`             // SIP listen port (default 5060)
	ID              string `yaml:"id"`               // 20-digit platform SIP ID
	Password        string `yaml:"password"`         // Global device registration password
	MediaIP         string `yaml:"media_ip"`         // IP address for SDP media reception
	MediaPort       int    `yaml:"media_port"`       // RTP media port (0=auto/multi-port, >0=single port mode)
	StandardVersion string `yaml:"standard_version"` // GB28181 standard version: 2016 or 2022
}

// HealthConfig configures the camera health monitoring system.
type HealthConfig struct {
	Enabled         bool                        `yaml:"enabled"`
	EventsRetention string                      `yaml:"events_retention"`
	Alerts          HealthAlertsConfig          `yaml:"alerts"`
	Layer1          HealthLayer1Config          `yaml:"layer1"`
	Layer2          HealthLayer2Config          `yaml:"layer2"`
	Layer2_5        HealthLayer2_5Config        `yaml:"layer2_5"`
	AutoRemediation HealthAutoRemediationConfig `yaml:"auto_remediation"`
}

type HealthAlertsConfig struct {
	Cooldown string `yaml:"cooldown"`
	MQTT     bool   `yaml:"mqtt"`
}

type HealthLayer1Config struct {
	OfflineThreshold string `yaml:"offline_threshold"`
}

type HealthLayer2Config struct {
	BitrateChangeThreshold float64 `yaml:"bitrate_change_threshold"`
	MinFPS                 int     `yaml:"min_fps"`
	MaxIDRInterval         string  `yaml:"max_idr_interval"`
}

type HealthLayer2_5Config struct {
	FreezeTimeout string `yaml:"freeze_timeout"`
}

type HealthAutoRemediationConfig struct {
	Enabled            bool `yaml:"enabled"`
	MaxRestartsPerHour int  `yaml:"max_restarts_per_hour"`
	CooldownMinutes    int  `yaml:"cooldown_minutes"`
	BlacklistHours     int  `yaml:"blacklist_hours"`
	GlobalMaxPerMin    int  `yaml:"global_max_per_min"`
	// OfflineRestartSeconds is how long a recorder may stay reconnecting/offline
	// before auto-remediation restarts it. This catches "stuck" streams that the
	// recorder's own reconnect loop can never recover — e.g. when the source video
	// encoding changes mid-stream and the codec-locked recorder can't reattach.
	// A full restart re-probes the encoding and resumes recording with the new
	// codec. Set high enough that normal transient reconnects and brief camera
	// reboots heal on their own. Default 90.
	OfflineRestartSeconds int `yaml:"offline_restart_seconds"`
}

// RemoteLogConfig defines remote log shipping settings (e.g. VictoriaLogs).
type RemoteLogConfig struct {
	Enabled  bool   `yaml:"enabled"`  // default false
	Endpoint string `yaml:"endpoint"` // VictoriaLogs URL, e.g. "http://localhost:9428/insert/jsonline"
	Format   string `yaml:"format"`   // "jsonline" (default) or "loki"
}

// MetricsAuthConfig defines optional independent authentication for the /metrics endpoint.
// When username and password (or password_hash) are non-empty, /metrics requires BasicAuth.
// When empty, /metrics stays public (backward compatible).
type MetricsAuthConfig struct {
	Username     string `yaml:"username"`
	Password     string `yaml:"password"`
	PasswordHash string `yaml:"password_hash"`
}
type WebSocketConfig struct {
	MaxViewers   int           `yaml:"max_viewers" json:"maxViewers"`
	WriteBufSize int           `yaml:"write_buf_size" json:"writeBufSize"`
	IdleTimeout  time.Duration `yaml:"idle_timeout" json:"idleTimeout"`
}

type AIConfig struct {
	Enabled             bool                `yaml:"enabled" json:"enabled"`
	Backend             string              `yaml:"backend" json:"backend"` // "webhook" or "disabled"
	FrameSkipRate       int                 `yaml:"frame_skip_rate" json:"frameSkipRate"`
	ConfidenceThreshold float64             `yaml:"confidence_threshold" json:"confidenceThreshold"`
	InferenceTimeoutMs  int                 `yaml:"inference_timeout_ms" json:"inferenceTimeoutMs"`
	Webhook             *AIWebhookConfig    `yaml:"webhook,omitempty" json:"webhook,omitempty"`
	Multimodal          *AIMultimodalConfig `yaml:"multimodal,omitempty" json:"multimodal,omitempty"`
}

type AIMultimodalConfig struct {
	Enabled          bool                        `yaml:"enabled" json:"enabled"`
	Provider         string                      `yaml:"provider" json:"provider"` // "deepseek", "openai", "qwen", etc.
	Providers        map[string]AIProviderConfig `yaml:"providers" json:"providers"`
	AnalysisPrompt   string                      `yaml:"analysis_prompt" json:"analysisPrompt"`
	AnalysisInterval string                      `yaml:"analysis_interval" json:"analysisInterval"` // e.g., "5m", "1h"
	SaveResults      bool                        `yaml:"save_results" json:"saveResults"`
	MaxResults       int                         `yaml:"max_results" json:"maxResults"` // Max results to keep per camera
}

type AIProviderConfig struct {
	Provider    string  `yaml:"provider" json:"provider"` // Provider type
	APIKey      string  `yaml:"api_key" json:"apiKey"`
	Endpoint    string  `yaml:"endpoint" json:"endpoint"`
	Model       string  `yaml:"model" json:"model"`
	VisionModel string  `yaml:"vision_model" json:"visionModel"` // Vision-specific model
	MaxTokens   int     `yaml:"max_tokens" json:"maxTokens"`
	Temperature float32 `yaml:"temperature" json:"temperature"`
	Timeout     int     `yaml:"timeout" json:"timeout"` // seconds
}

type AIWebhookConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
}

// IsConfigured returns true if both username and a password (or hash) are set.
func (c MetricsAuthConfig) IsConfigured() bool {
	return strings.TrimSpace(c.Username) != "" &&
		(strings.TrimSpace(c.Password) != "" || strings.TrimSpace(c.PasswordHash) != "")
}

// Load reads a YAML config file and returns a Config with defaults applied.
func Load(path string) (*Config, error) {
	if path == "" {
		return nil, fmt.Errorf("path must be provided")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	// apply defaults
	cfg.ApplyDefaults()

	// Decrypt sensitive fields if encryption key is available
	if key := GetEncryptionKey(); key != nil {
		decryptConfig(&cfg, key)
	}

	return &cfg, nil
}

// Save writes the Config to path as YAML using atomic write (temp file + rename).
// If an encryption key is available, sensitive fields are encrypted before writing
// and restored to plaintext in memory after the write completes.
func Save(path string, cfg *Config) error {
	if path == "" {
		return fmt.Errorf("path must be provided")
	}
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	// Cameras are stored in SQLite, not in the YAML config file.
	saveCfg := *cfg
	saveCfg.Cameras = nil

	// Snapshot and encrypt sensitive fields if key is available
	key := GetEncryptionKey()
	if key != nil {
		snap := snapshotSensitive(&saveCfg)
		encryptConfig(&saveCfg, key)
		defer snap.restore(&saveCfg)
	}

	data, err := yaml.Marshal(&saveCfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	// Temp file in same directory to ensure same filesystem for rename.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".lalmax-nvr.yaml.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

// Validate ensures required fields and basic constraints.
func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	// cameras must have id and url
	seen := make(map[string]int)
	for i, c := range cfg.Cameras {
		if strings.TrimSpace(c.ID) == "" {
			return fmt.Errorf("camera[%d].id is required", i)
		}
		if j, ok := seen[c.ID]; ok {
			return fmt.Errorf("camera[%d] and camera[%d] have duplicate id %q", j, i, c.ID)
		}
		seen[c.ID] = i
		if strings.TrimSpace(c.URL) == "" && c.Protocol != "onvif" && c.Protocol != "xiaomi" && c.Protocol != string(model.ProtoGB28181) {
			return fmt.Errorf("camera[%d].url is required", i)
		}
		// Validate URL format if set
		if c.URL != "" {
			parsed, err := url.Parse(c.URL)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return fmt.Errorf("camera[%d].url has invalid format: %s", i, c.URL)
			}
		}
		if (c.Protocol == "onvif" || c.Protocol == string(model.ProtoONVIF)) && strings.TrimSpace(c.ONVIFEndpoint) == "" && strings.TrimSpace(c.URL) == "" {
			return fmt.Errorf("camera[%d].url or onvif_endpoint is required for ONVIF cameras", i)
		}
		// Auto-populate: if url is set but onvif_endpoint is empty, copy url to onvif_endpoint
		if (c.Protocol == "onvif" || c.Protocol == string(model.ProtoONVIF)) && strings.TrimSpace(c.ONVIFEndpoint) == "" && strings.TrimSpace(c.URL) != "" {
			c.ONVIFEndpoint = c.URL
		}
		// Validate ONVIF endpoint URL format if set
		if c.ONVIFEndpoint != "" {
			parsed, err := url.Parse(c.ONVIFEndpoint)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return fmt.Errorf("camera[%d].onvif_endpoint has invalid format: %s", i, c.ONVIFEndpoint)
			}
		}
		// Accept both old combined format and new separate format
		proto := c.Protocol
		enc := c.Encoding
		if strings.Contains(proto, "_") {
			// Old combined format like "rtsp_h264" 鈥?parse and validate
			parsedProto, parsedEnc, err := model.ParseLegacyProtocol(proto)
			if err != nil {
				return fmt.Errorf("camera[%d].protocol invalid: %s", i, proto)
			}
			proto = parsedProto
			enc = parsedEnc
		}
		if err := model.ValidateProtocolEncoding(proto, enc); err != nil {
			return fmt.Errorf("camera[%d].%w", i, err)
		}
		if !IsValidRTSPTransport(c.RTSPTransport) {
			return fmt.Errorf("camera[%d].rtsp_transport invalid: %s (must be tcp or udp)", i, c.RTSPTransport)
		}

		// Validate per-camera health overrides
		if c.HealthOverrides.MaxIDRInterval != "" {
			if _, err := time.ParseDuration(c.HealthOverrides.MaxIDRInterval); err != nil {
				return fmt.Errorf("camera[%d].health_overrides.max_idr_interval invalid duration: %w", i, err)
			}
		}
		if c.HealthOverrides.OfflineThreshold != "" {
			if _, err := time.ParseDuration(c.HealthOverrides.OfflineThreshold); err != nil {
				return fmt.Errorf("camera[%d].health_overrides.offline_threshold invalid duration: %w", i, err)
			}
		}
		if c.HealthOverrides.FreezeTimeout != "" {
			if _, err := time.ParseDuration(c.HealthOverrides.FreezeTimeout); err != nil {
				return fmt.Errorf("camera[%d].health_overrides.freeze_timeout invalid duration: %w", i, err)
			}
		}
		if c.HealthOverrides.BitrateChangeThreshold < 0 || c.HealthOverrides.BitrateChangeThreshold > 1 {
			return fmt.Errorf("camera[%d].health_overrides.bitrate_change_threshold must be between 0 and 1", i)
		}
		if c.HealthOverrides.MinFPS < 0 {
			return fmt.Errorf("camera[%d].health_overrides.min_fps must be >= 0", i)
		}
	}
	// Validate Xiaomi configuration
	for _, cam := range cfg.Cameras {
		if cam.Protocol == "xiaomi" && strings.TrimSpace(cfg.Xiaomi.Token) == "" {
			return fmt.Errorf("xiaomi camera %q requires xiaomi.token in config", cam.ID)
		}
	}
	// port ranges
	if cfg.FTP.Port < 1 || cfg.FTP.Port > 65535 {
		return fmt.Errorf("ftp port out of range: %d", cfg.FTP.Port)
	}
	// Validate segment_duration
	if dur, err := time.ParseDuration(cfg.Storage.SegmentDuration); err != nil {
		return fmt.Errorf("storage.segment_duration invalid: %w", err)
	} else if dur > 30*time.Second {
		return fmt.Errorf("storage.segment_duration must be <= 30s on RPi 3B, got %s", cfg.Storage.SegmentDuration)
	}
	if cfg.Media.Mode != "embedded" && cfg.Media.Mode != "http" {
		return fmt.Errorf("media.mode invalid: %s (must be embedded/http)", cfg.Media.Mode)
	}
	if strings.TrimSpace(cfg.Media.LalmaxHTTPAddr) == "" {
		return fmt.Errorf("media.lalmax_http_addr is required")
	}
	if _, err := url.Parse(cfg.Media.LalmaxHTTPAddr); err != nil {
		return fmt.Errorf("media.lalmax_http_addr invalid: %w", err)
	}
	// Validate retention_days
	if cfg.Cleanup.RetentionDays < 1 || cfg.Cleanup.RetentionDays > 3650 {
		return fmt.Errorf("cleanup.retention_days must be between 1 and 3650, got %d", cfg.Cleanup.RetentionDays)
	}
	// Validate disk_threshold_percent
	if cfg.Cleanup.DiskThresholdPercent < 50 || cfg.Cleanup.DiskThresholdPercent > 99 {
		return fmt.Errorf("cleanup.disk_threshold_percent must be between 50 and 99, got %d", cfg.Cleanup.DiskThresholdPercent)
	}
	// Validate observability.log_level
	if cfg.Observability.LogLevel != "debug" && cfg.Observability.LogLevel != "info" && cfg.Observability.LogLevel != "warn" && cfg.Observability.LogLevel != "error" {
		return fmt.Errorf("observability.log_level invalid: %s (must be debug/info/warn/error)", cfg.Observability.LogLevel)
	}
	// Validate observability.log_format
	if cfg.Observability.LogFormat != "json" && cfg.Observability.LogFormat != "text" {
		return fmt.Errorf("observability.log_format invalid: %s (must be json/text)", cfg.Observability.LogFormat)
	}
	if cfg.Observability.OTLP.Enabled {
		otlp := cfg.Observability.OTLP
		endpoint, err := url.Parse(otlp.Endpoint)
		if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
			return fmt.Errorf("observability.otlp.endpoint invalid: must be an http or https base URL")
		}
		if endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return fmt.Errorf("observability.otlp.endpoint must not contain a query or fragment")
		}
		if !otlp.ExportTraces() && !otlp.ExportMetrics() {
			return fmt.Errorf("observability.otlp requires traces_enabled or metrics_enabled")
		}
		if strings.TrimSpace(otlp.ServiceName) == "" {
			return fmt.Errorf("observability.otlp.service_name is required")
		}
		if timeout, err := time.ParseDuration(otlp.Timeout); err != nil || timeout <= 0 {
			return fmt.Errorf("observability.otlp.timeout must be a positive duration")
		}
		if interval, err := time.ParseDuration(otlp.MetricsExportInterval); err != nil || interval <= 0 {
			return fmt.Errorf("observability.otlp.metrics_export_interval must be a positive duration")
		}
		for name, value := range otlp.Headers {
			if strings.TrimSpace(name) == "" || strings.ContainsAny(name+value, "\r\n") {
				return fmt.Errorf("observability.otlp.headers contains an invalid header")
			}
		}
	}

	// Validate remote_log
	if cfg.RemoteLog.Enabled {
		if strings.TrimSpace(cfg.RemoteLog.Endpoint) == "" {
			return fmt.Errorf("remote_log.endpoint is required when remote_log.enabled=true")
		}
		if cfg.RemoteLog.Format != "jsonline" && cfg.RemoteLog.Format != "loki" {
			return fmt.Errorf("remote_log.format must be \"jsonline\" or \"loki\", got %q", cfg.RemoteLog.Format)
		}
	}
	if cfg.Merge.Enabled {
		if _, err := time.ParseDuration(cfg.Merge.CheckInterval); err != nil {
			return fmt.Errorf("invalid merge check_interval: %w", err)
		}
		if _, err := time.ParseDuration(cfg.Merge.WindowSize); err != nil {
			return fmt.Errorf("invalid merge window_size: %w", err)
		}
		if cfg.Merge.BatchLimit <= 0 {
			return fmt.Errorf("merge batch_limit must be positive")
		}
		if _, err := time.ParseDuration(cfg.Merge.MinSegmentAge); err != nil {
			return fmt.Errorf("invalid merge min_segment_age: %w", err)
		}
		if cfg.Merge.MinSegmentsToMerge < 2 {
			return fmt.Errorf("merge min_segments_to_merge must be at least 2")
		}
	}

	// Validate per-camera timelapse configuration
	for _, cam := range cfg.Cameras {
		if cam.Timelapse == nil {
			continue
		}
		if cam.Timelapse.Interval != "" {
			dur, err := time.ParseDuration(cam.Timelapse.Interval)
			if err != nil {
				return fmt.Errorf("cameras.%s.timelapse.interval invalid duration: %w", cam.ID, err)
			}
			if dur < time.Second {
				return fmt.Errorf("cameras.%s.timelapse.interval must be at least 1s, got %s", cam.ID, cam.Timelapse.Interval)
			}
		}
		if cam.Timelapse.OutputFPS < 1 || cam.Timelapse.OutputFPS > 60 {
			return fmt.Errorf("cameras.%s.timelapse.output_fps must be between 1 and 60, got %d", cam.ID, cam.Timelapse.OutputFPS)
		}
		if cam.Timelapse.VideoCodec != "" && cam.Timelapse.VideoCodec != "h264" && cam.Timelapse.VideoCodec != "h265" {
			return fmt.Errorf("cameras.%s.timelapse.video_codec must be h264 or h265, got %q", cam.ID, cam.Timelapse.VideoCodec)
		}
	}
	// Validate hls.segment_count
	if cfg.HLS.SegmentCount < 3 || cfg.HLS.SegmentCount > 10 {
		return fmt.Errorf("hls.segment_count must be between 3 and 10, got %d", cfg.HLS.SegmentCount)
	}
	// Validate hls.max_streams
	if cfg.HLS.MaxStreams < 1 || cfg.HLS.MaxStreams > 20 {
		return fmt.Errorf("hls.max_streams must be between 1 and 20, got %d", cfg.HLS.MaxStreams)
	}
	// Validate HLS TS temp directory.
	// Note: ApplyDefaults sets a default ("hls-temp"), so this check only triggers
	// if Validate is called independently (without ApplyDefaults first).
	if strings.TrimSpace(cfg.HLS.LalTempDir) == "" {
		return fmt.Errorf("hls.lal_temp_dir is required")
	}
	// Validate LL-HLS configuration
	if cfg.HLS.LowLatency {
		if cfg.HLS.SegmentCount < 7 {
			slog.Warn("auto-fixing hls.segment_count", "old", cfg.HLS.SegmentCount, "new", 7, "reason", "low_latency requires >= 7")
			cfg.HLS.SegmentCount = 7
		}
	}
	// Validate hls.part_min_duration
	if partDur, err := time.ParseDuration(cfg.HLS.PartMinDuration); err != nil {
		return fmt.Errorf("hls.part_min_duration invalid: %w", err)
	} else if partDur < 100*time.Millisecond || partDur > 1*time.Second {
		return fmt.Errorf("hls.part_min_duration must be between 100ms and 1s, got %s", cfg.HLS.PartMinDuration)
	}
	if idle, err := time.ParseDuration(cfg.HLS.IdleTimeout); err != nil {
		return fmt.Errorf("hls.idle_timeout invalid: %w", err)
	} else if idle <= 0 {
		return fmt.Errorf("hls.idle_timeout must be > 0, got %s", cfg.HLS.IdleTimeout)
	}

	// Validate streaming configuration
	if cfg.Streaming.DefaultProtocol != "webrtc" && cfg.Streaming.DefaultProtocol != "flv" && cfg.Streaming.DefaultProtocol != "ws-flv" && cfg.Streaming.DefaultProtocol != "hls" && cfg.Streaming.DefaultProtocol != "ll-hls" {
		return fmt.Errorf("streaming.default_protocol invalid: %s (must be webrtc/flv/ws-flv/hls/ll-hls)", cfg.Streaming.DefaultProtocol)
	}
	if cfg.Streaming.WebRTC.MaxViewers < 1 || cfg.Streaming.WebRTC.MaxViewers > 10 {
		return fmt.Errorf("streaming.webrtc.max_viewers must be between 1 and 10, got %d", cfg.Streaming.WebRTC.MaxViewers)
	}
	if cfg.Streaming.FLV.MaxViewers < 1 || cfg.Streaming.FLV.MaxViewers > 50 {
		return fmt.Errorf("streaming.flv.max_viewers must be between 1 and 50, got %d", cfg.Streaming.FLV.MaxViewers)
	}
	if cfg.Streaming.FLV.GOPCacheSize < 0 {
		return fmt.Errorf("streaming.flv.gop_cache_size must be >= 0, got %d", cfg.Streaming.FLV.GOPCacheSize)
	}
	if _, err := time.ParseDuration(cfg.Streaming.WebRTC.IdleTimeout); err != nil {
		return fmt.Errorf("streaming.webrtc.idle_timeout invalid: %w", err)
	}
	if _, err := time.ParseDuration(cfg.Streaming.FLV.IdleTimeout); err != nil {
		return fmt.Errorf("streaming.flv.idle_timeout invalid: %w", err)
	}
	// Validate WebSocket configuration
	if cfg.WebSocket.MaxViewers <= 0 {
		return fmt.Errorf("websocket.max_viewers must be > 0, got %d", cfg.WebSocket.MaxViewers)
	}
	if cfg.WebSocket.WriteBufSize <= 0 {
		return fmt.Errorf("websocket.write_buf_size must be > 0, got %d", cfg.WebSocket.WriteBufSize)
	}
	if cfg.WebSocket.IdleTimeout <= 0 {
		return fmt.Errorf("websocket.idle_timeout must be > 0, got %s", cfg.WebSocket.IdleTimeout)
	}

	// Validate health configuration
	if cfg.Health.Enabled {
		if _, err := time.ParseDuration(cfg.Health.EventsRetention); err != nil {
			return fmt.Errorf("health.events_retention invalid duration: %w", err)
		}
		if _, err := time.ParseDuration(cfg.Health.Alerts.Cooldown); err != nil {
			return fmt.Errorf("health.alerts.cooldown invalid duration: %w", err)
		}
		if _, err := time.ParseDuration(cfg.Health.Layer1.OfflineThreshold); err != nil {
			return fmt.Errorf("health.layer1.offline_threshold invalid duration: %w", err)
		}
		if cfg.Health.Layer2.BitrateChangeThreshold <= 0 || cfg.Health.Layer2.BitrateChangeThreshold > 1 {
			return fmt.Errorf("health.layer2.bitrate_change_threshold must be between 0 and 1")
		}
		if cfg.Health.Layer2.MinFPS < 1 {
			return fmt.Errorf("health.layer2.min_fps must be >= 1")
		}
		if _, err := time.ParseDuration(cfg.Health.Layer2.MaxIDRInterval); err != nil {
			return fmt.Errorf("health.layer2.max_idr_interval invalid duration: %w", err)
		}
		if _, err := time.ParseDuration(cfg.Health.Layer2_5.FreezeTimeout); err != nil {
			return fmt.Errorf("health.layer2_5.freeze_timeout invalid duration: %w", err)
		}
		if cfg.Health.AutoRemediation.Enabled {
			if cfg.Health.AutoRemediation.MaxRestartsPerHour <= 0 {
				return fmt.Errorf("health.auto_remediation.max_restarts_per_hour must be > 0")
			}
			if cfg.Health.AutoRemediation.CooldownMinutes < 1 {
				return fmt.Errorf("health.auto_remediation.cooldown_minutes must be >= 1")
			}
			if cfg.Health.AutoRemediation.OfflineRestartSeconds < 1 {
				return fmt.Errorf("health.auto_remediation.offline_restart_seconds must be >= 1")
			}
		}
	}
	return nil
}

// IsHLSEnabled reports whether HLS/LL-HLS streaming is enabled.
func (cfg *Config) IsHLSEnabled() bool {
	return cfg.HLS.Enabled != nil && *cfg.HLS.Enabled
}

// IsHLSOnDemand reports whether HLS slicing starts only on viewer access.
func (cfg *Config) IsHLSOnDemand() bool {
	if cfg.HLS.OnDemand == nil {
		return true
	}
	return *cfg.HLS.OnDemand
}

// HLSIdleTimeout returns how long to keep slicing after the last HLS request.
func (cfg *Config) HLSIdleTimeout() time.Duration {
	raw := strings.TrimSpace(cfg.HLS.IdleTimeout)
	if raw == "" {
		return 60 * time.Second
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 60 * time.Second
	}
	return d
}

func (cfg *Config) ApplyDefaults() {
	// Server
	if strings.TrimSpace(cfg.Server.Listen) == "" {
		cfg.Server.Listen = ":9090"
	}
	// Storage
	if strings.TrimSpace(cfg.Storage.RootDir) == "" {
		cfg.Storage.RootDir = "/var/lib/lalmax-nvr"
	}
	if strings.TrimSpace(cfg.Storage.SegmentDuration) == "" {
		cfg.Storage.SegmentDuration = "30s"
	}
	if strings.TrimSpace(cfg.Media.Mode) == "" {
		cfg.Media.Mode = "embedded"
	}
	if strings.TrimSpace(cfg.Media.LalmaxHTTPAddr) == "" {
		cfg.Media.LalmaxHTTPAddr = DefaultLalmaxHTTPAddr
	}
	if strings.TrimSpace(cfg.Media.LalmaxPublicURL) == "" {
		cfg.Media.LalmaxPublicURL = cfg.Media.LalmaxHTTPAddr
	}
	// lalmax config path: always default to config/ directory under storage root
	if strings.TrimSpace(cfg.Media.LalmaxConfigPath) == "" {
		cfg.Media.LalmaxConfigPath = filepath.Join(cfg.Storage.RootDir, "config", "lalmax.conf.json")
	}
	// Cameras: nothing heavy, but ensure at least enable false
	// Cleanup
	if cfg.Cleanup.RetentionDays == 0 {
		cfg.Cleanup.RetentionDays = 30
	}
	if strings.TrimSpace(cfg.Cleanup.CheckInterval) == "" {
		cfg.Cleanup.CheckInterval = "1h"
	}
	if cfg.Cleanup.DiskThresholdPercent == 0 {
		cfg.Cleanup.DiskThresholdPercent = 95
	}
	// Auth - no defaults
	// FTP
	if cfg.FTP.Enabled == nil {
		// set default to true only if not configured by user
		cfg.FTP.Enabled = new(bool)
		*cfg.FTP.Enabled = true
	}
	if cfg.FTP.Port == 0 {
		cfg.FTP.Port = 2121
	}
	if strings.TrimSpace(cfg.FTP.PassivePortRange) == "" {
		cfg.FTP.PassivePortRange = "2122-2140"
	}
	// MQTT
	// default false already
	// WebDAV
	if cfg.WebDAV.Enabled == nil {
		// set default to true only if not configured by user
		cfg.WebDAV.Enabled = new(bool)
		*cfg.WebDAV.Enabled = true
	}
	if strings.TrimSpace(cfg.WebDAV.PathPrefix) == "" {
		cfg.WebDAV.PathPrefix = "/dav"
	}
	// Xiaomi
	if cfg.Xiaomi.Region == "" {
		cfg.Xiaomi.Region = "cn"
	}
	// Observability
	if strings.TrimSpace(cfg.Observability.LogLevel) == "" {
		cfg.Observability.LogLevel = "info"
	}
	if strings.TrimSpace(cfg.Observability.LogFormat) == "" {
		cfg.Observability.LogFormat = "text"
	}
	// EnablePprof defaults to false (zero value)
	if strings.TrimSpace(cfg.Observability.OTLP.Endpoint) == "" {
		cfg.Observability.OTLP.Endpoint = "http://127.0.0.1:4318"
	}
	if strings.TrimSpace(cfg.Observability.OTLP.ServiceName) == "" {
		cfg.Observability.OTLP.ServiceName = "lalmax-nvr"
	}
	if cfg.Observability.OTLP.TracesEnabled == nil {
		cfg.Observability.OTLP.TracesEnabled = new(bool)
		*cfg.Observability.OTLP.TracesEnabled = true
	}
	if cfg.Observability.OTLP.MetricsEnabled == nil {
		cfg.Observability.OTLP.MetricsEnabled = new(bool)
		*cfg.Observability.OTLP.MetricsEnabled = true
	}
	if strings.TrimSpace(cfg.Observability.OTLP.Timeout) == "" {
		cfg.Observability.OTLP.Timeout = "10s"
	}
	if strings.TrimSpace(cfg.Observability.OTLP.MetricsExportInterval) == "" {
		cfg.Observability.OTLP.MetricsExportInterval = "30s"
	}
	// Version
	// HLS defaults
	if cfg.HLS.WriteBufferSize <= 0 {
		cfg.HLS.WriteBufferSize = 100
	}
	if cfg.HLS.SegmentMaxSizeMB <= 0 {
		cfg.HLS.SegmentMaxSizeMB = 10
	}
	if cfg.HLS.SegmentCount <= 0 {
		cfg.HLS.SegmentCount = 7
	}
	if cfg.HLS.MaxStreams <= 0 {
		cfg.HLS.MaxStreams = 4
	}
	// LL-HLS: low_latency defaults to true
	if !cfg.HLS.LowLatency {
		cfg.HLS.LowLatency = true
	}
	if strings.TrimSpace(cfg.HLS.PartMinDuration) == "" {
		cfg.HLS.PartMinDuration = "200ms"
	}
	// lal (TS HLS) defaults
	if cfg.HLS.LalFragmentDurationMs <= 0 {
		cfg.HLS.LalFragmentDurationMs = 3000
	}
	if cfg.HLS.LalFragmentNum <= 0 {
		cfg.HLS.LalFragmentNum = 6
	}
	if strings.TrimSpace(cfg.HLS.LalTempDir) == "" {
		cfg.HLS.LalTempDir = "hls-temp"
	}
	// Force LalCleanupMode=2 (ASAP) to immediately delete TS segments when they
	// leave the playlist, minimizing disk usage.
	if cfg.HLS.LalCleanupMode != 2 {
		slog.Warn("overriding hls.lal_cleanup_mode", "user_value", cfg.HLS.LalCleanupMode, "forced", 2, "reason", "immediate cleanup when segments leave playlist")
	}
	cfg.HLS.LalCleanupMode = 2
	// lalmax (fMP4/LL-HLS) defaults
	if cfg.HLS.LalmaxSegmentDuration <= 0 {
		cfg.HLS.LalmaxSegmentDuration = 1
	}
	if cfg.HLS.LalmaxPartDuration <= 0 {
		cfg.HLS.LalmaxPartDuration = 200
	}

	// HLS enabled defaults to false to avoid unnecessary disk usage.
	if cfg.HLS.Enabled == nil {
		cfg.HLS.Enabled = new(bool)
		*cfg.HLS.Enabled = false
	}
	if cfg.HLS.OnDemand == nil {
		cfg.HLS.OnDemand = new(bool)
		*cfg.HLS.OnDemand = true
	}
	if strings.TrimSpace(cfg.HLS.IdleTimeout) == "" {
		cfg.HLS.IdleTimeout = "60s"
	}

	// Streaming defaults
	if strings.TrimSpace(cfg.Streaming.DefaultProtocol) == "" {
		cfg.Streaming.DefaultProtocol = "webrtc"
	}
	if cfg.Streaming.WebRTC.Enabled == nil {
		cfg.Streaming.WebRTC.Enabled = new(bool)
		*cfg.Streaming.WebRTC.Enabled = true
	}
	if cfg.Streaming.WebRTC.MaxViewers <= 0 {
		cfg.Streaming.WebRTC.MaxViewers = 2
	}
	if strings.TrimSpace(cfg.Streaming.WebRTC.IdleTimeout) == "" {
		cfg.Streaming.WebRTC.IdleTimeout = "60s"
	}
	if cfg.Streaming.FLV.Enabled == nil {
		cfg.Streaming.FLV.Enabled = new(bool)
		*cfg.Streaming.FLV.Enabled = true
	}
	if cfg.Streaming.FLV.MaxViewers <= 0 {
		cfg.Streaming.FLV.MaxViewers = 10
	}
	if strings.TrimSpace(cfg.Streaming.FLV.IdleTimeout) == "" {
		cfg.Streaming.FLV.IdleTimeout = "60s"
	}
	if cfg.Streaming.FLV.GOPCacheSize <= 0 {
		cfg.Streaming.FLV.GOPCacheSize = 1
	}
	// AutoStopNoViewSec: default 300 (5 minutes)
	if cfg.Streaming.AutoStopNoViewSec == 0 {
		cfg.Streaming.AutoStopNoViewSec = 300
	}
	// If HLS is disabled, fall back when default protocol is HLS-based.
	if !cfg.IsHLSEnabled() {
		switch cfg.Streaming.DefaultProtocol {
		case "hls", "ll-hls":
			cfg.Streaming.DefaultProtocol = "webrtc"
		}
	}
	if strings.TrimSpace(cfg.Version) == "" {
		cfg.Version = "1.0"
	}
	// Merge defaults
	if cfg.Merge.BatchLimit <= 0 {
		cfg.Merge.BatchLimit = 200
	}
	if cfg.Merge.CheckInterval == "" {
		cfg.Merge.CheckInterval = "1h"
	}
	if cfg.Merge.WindowSize == "" {
		cfg.Merge.WindowSize = "1h"
	}
	if cfg.Merge.MinSegmentAge == "" {
		cfg.Merge.MinSegmentAge = "10m"
	}
	if cfg.Merge.MinSegmentsToMerge <= 0 {
		cfg.Merge.MinSegmentsToMerge = 3
	}
	// RTMP defaults
	if cfg.RTMP.Enabled == nil {
		cfg.RTMP.Enabled = new(bool)
		*cfg.RTMP.Enabled = false
	}
	if cfg.RTMP.Port == 0 {
		cfg.RTMP.Port = DefaultLalRTMPPort
	}
	if cfg.RTMP.StreamKeys == nil {
		cfg.RTMP.StreamKeys = make(map[string]string)
	}
	// WebSocket defaults
	if cfg.WebSocket.MaxViewers <= 0 {
		cfg.WebSocket.MaxViewers = 10
	}
	if cfg.WebSocket.WriteBufSize <= 0 {
		cfg.WebSocket.WriteBufSize = 100
	}
	if cfg.WebSocket.IdleTimeout <= 0 {
		cfg.WebSocket.IdleTimeout = 60 * time.Second
	}

	// SRT defaults
	if cfg.SRT.Enabled == nil {
		cfg.SRT.Enabled = new(bool)
		*cfg.SRT.Enabled = false
	}
	if cfg.SRT.Port == 0 {
		cfg.SRT.Port = DefaultSRTPort
	}

	// GB28181 defaults
	if cfg.GB28181.Enabled == nil {
		cfg.GB28181.Enabled = new(bool)
		*cfg.GB28181.Enabled = false
	}
	if cfg.GB28181.Port == 0 {
		cfg.GB28181.Port = 5060
	}
	if cfg.GB28181.ID == "" {
		cfg.GB28181.ID = "34020000002000000001"
	}
	if cfg.GB28181.Password == "" {
		cfg.GB28181.Password = "12345678"
	}
	if cfg.GB28181.StandardVersion == "" {
		cfg.GB28181.StandardVersion = "2016"
	}

	// Health defaults
	if cfg.Health.EventsRetention == "" {
		cfg.Health.EventsRetention = "720h" // 30 days
	}
	if cfg.Health.Alerts.Cooldown == "" {
		cfg.Health.Alerts.Cooldown = "5m"
	}
	if cfg.Health.Layer1.OfflineThreshold == "" {
		cfg.Health.Layer1.OfflineThreshold = "30s"
	}
	if cfg.Health.Layer2.BitrateChangeThreshold == 0 {
		cfg.Health.Layer2.BitrateChangeThreshold = 0.5
	}
	if cfg.Health.Layer2.MinFPS == 0 {
		cfg.Health.Layer2.MinFPS = 5
	}
	if cfg.Health.Layer2.MaxIDRInterval == "" {
		cfg.Health.Layer2.MaxIDRInterval = "60s"
	}
	if cfg.Health.Layer2_5.FreezeTimeout == "" {
		cfg.Health.Layer2_5.FreezeTimeout = "10s"
	}

	// Auto-remediation defaults
	if cfg.Health.AutoRemediation.MaxRestartsPerHour == 0 {
		cfg.Health.AutoRemediation.MaxRestartsPerHour = 3
	}
	if cfg.Health.AutoRemediation.CooldownMinutes == 0 {
		cfg.Health.AutoRemediation.CooldownMinutes = 5
	}
	if cfg.Health.AutoRemediation.BlacklistHours == 0 {
		cfg.Health.AutoRemediation.BlacklistHours = 1
	}
	if cfg.Health.AutoRemediation.GlobalMaxPerMin == 0 {
		cfg.Health.AutoRemediation.GlobalMaxPerMin = 10
	}
	if cfg.Health.AutoRemediation.OfflineRestartSeconds == 0 {
		cfg.Health.AutoRemediation.OfflineRestartSeconds = 90
	}

	// Remote log defaults
	if cfg.RemoteLog.Format == "" {
		cfg.RemoteLog.Format = "jsonline"
	}
	// AI defaults
	if cfg.AI.Backend == "" {
		cfg.AI.Backend = "disabled"
	}
	if cfg.AI.FrameSkipRate <= 0 {
		cfg.AI.FrameSkipRate = 3
	}
	if cfg.AI.ConfidenceThreshold <= 0 {
		cfg.AI.ConfidenceThreshold = 0.3
	}
	if cfg.AI.InferenceTimeoutMs <= 0 {
		cfg.AI.InferenceTimeoutMs = 30000
	}
	// Multimodal AI defaults
	if cfg.AI.Multimodal != nil && cfg.AI.Multimodal.Enabled {
		if cfg.AI.Multimodal.Provider == "" {
			cfg.AI.Multimodal.Provider = "deepseek"
		}
		if cfg.AI.Multimodal.AnalysisInterval == "" {
			cfg.AI.Multimodal.AnalysisInterval = "5m"
		}
		if cfg.AI.Multimodal.MaxResults <= 0 {
			cfg.AI.Multimodal.MaxResults = 1000
		}
		// Set defaults for each provider
		for name, provider := range cfg.AI.Multimodal.Providers {
			if provider.Timeout <= 0 {
				provider.Timeout = 60
			}
			if provider.MaxTokens <= 0 {
				provider.MaxTokens = 2000
			}
			if provider.Temperature <= 0 {
				provider.Temperature = 0.7
			}
			cfg.AI.Multimodal.Providers[name] = provider
		}
	}
	// Camera protocol/encoding normalization (backward compat with old combined protocol strings)
	for i := range cfg.Cameras {
		cam := &cfg.Cameras[i]
		// If encoding is empty but protocol looks like old combined format (e.g. "rtsp_h264")
		if cam.Encoding == "" && strings.Contains(cam.Protocol, "_") {
			proto, enc, err := model.ParseLegacyProtocol(cam.Protocol)
			if err == nil {
				cam.Protocol = proto
				cam.Encoding = enc
			}
		}
		// If encoding is still empty for known transport-only protocols, set sensible defaults
		if cam.Encoding == "" {
			switch cam.Protocol {
			case "rtsp":
				cam.Encoding = "h264"
			case "http":
				cam.Encoding = "jpeg"
			case "onvif":
				cam.Encoding = "" // ONVIF auto-detects
			case string(model.ProtoGB28181):
				cam.Encoding = "h264"
			}
		}
		ApplyCameraAudioDefault(cam)

		// Timelapse defaults
		if cam.Timelapse != nil {
			if cam.Timelapse.Interval == "" {
				cam.Timelapse.Interval = "30s"
			}
			if cam.Timelapse.OutputFPS == 0 {
				cam.Timelapse.OutputFPS = 30
			}
			if cam.Timelapse.VideoCodec == "" {
				cam.Timelapse.VideoCodec = "h264"
			}
			// DeleteOriginal defaults to false (zero value)
		}
	}
}

// ResolveMergeConfig returns the effective MergeConfig for a camera.
// If perCamera is nil, the global config is returned unchanged.
// If perCamera is non-nil, only non-zero fields override the global config.
func ResolveMergeConfig(global MergeConfig, perCamera *MergeConfig) MergeConfig {
	if perCamera == nil {
		return global
	}
	result := global
	if perCamera.Enabled {
		result.Enabled = true
	}
	if perCamera.CheckInterval != "" {
		result.CheckInterval = perCamera.CheckInterval
	}
	if perCamera.WindowSize != "" {
		result.WindowSize = perCamera.WindowSize
	}
	if perCamera.BatchLimit > 0 {
		result.BatchLimit = perCamera.BatchLimit
	}
	if perCamera.MinSegmentAge != "" {
		result.MinSegmentAge = perCamera.MinSegmentAge
	}
	if perCamera.MinSegmentsToMerge > 0 {
		result.MinSegmentsToMerge = perCamera.MinSegmentsToMerge
	}
	return result
}

// ResolveHealthOverrides returns the effective health thresholds for a camera.
// Per-camera overrides take precedence over global health config when set.
// Duration strings are left as-is (empty string means "use global").
func ResolveHealthOverrides(global HealthConfig, overrides HealthOverrides) ResolvedHealthOverrides {
	result := ResolvedHealthOverrides{
		MaxIDRInterval:         global.Layer2.MaxIDRInterval,
		BitrateChangeThreshold: global.Layer2.BitrateChangeThreshold,
		MinFPS:                 global.Layer2.MinFPS,
		OfflineThreshold:       global.Layer1.OfflineThreshold,
		FreezeTimeout:          global.Layer2_5.FreezeTimeout,
	}
	if overrides.MaxIDRInterval != "" {
		result.MaxIDRInterval = overrides.MaxIDRInterval
	}
	if overrides.BitrateChangeThreshold > 0 {
		result.BitrateChangeThreshold = overrides.BitrateChangeThreshold
	}
	if overrides.MinFPS > 0 {
		result.MinFPS = overrides.MinFPS
	}
	if overrides.OfflineThreshold != "" {
		result.OfflineThreshold = overrides.OfflineThreshold
	}
	if overrides.FreezeTimeout != "" {
		result.FreezeTimeout = overrides.FreezeTimeout
	}
	return result
}

// ResolvedHealthOverrides holds fully-resolved health threshold values
// (duration strings ready for time.ParseDuration).
type ResolvedHealthOverrides struct {
	MaxIDRInterval         string
	BitrateChangeThreshold float64
	MinFPS                 int
	OfflineThreshold       string
	FreezeTimeout          string
}

// EncryptConfigFile loads a config file, encrypts all sensitive fields, and saves it back.
// Returns the list of field paths that were encrypted.
// Returns an error if no encryption key is available or if the config cannot be loaded/saved.
func EncryptConfigFile(path string) ([]string, error) {
	key := GetEncryptionKey()
	if key == nil {
		return nil, fmt.Errorf("NVR_ENCRYPTION_KEY environment variable not set (must be 32-byte base64-encoded key)")
	}
	cfg, err := Load(path)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	// Find plaintext fields before encryption
	plaintextFields := SensitiveFieldPaths(cfg)
	if len(plaintextFields) == 0 {
		return nil, nil // nothing to encrypt
	}

	slog.Info("encrypting config fields", "path", path, "fields", plaintextFields)

	// Save will encrypt via the snapshot mechanism
	if err := Save(path, cfg); err != nil {
		return nil, fmt.Errorf("save encrypted config: %w", err)
	}

	return plaintextFields, nil
}
