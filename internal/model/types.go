package model

import (
	"context"
	"fmt"
	"time"
)

// Recorder records video from a camera source
type Recorder interface {
	Start(ctx context.Context) error
	Stop() error
	Status() RecorderStatus
}

// PausableRecorder is an optional interface that recorders can implement
// to support pausing recording without stopping the stream connection.
type PausableRecorder interface {
	Recorder
	Pause()
	Resume()
	IsPaused() bool
}

// CodecParamsProvider is an optional interface that recorders can implement
// to expose codec parameter sets (SPS/PPS/VPS). The api handler uses this
// for codec detection when establishing WS/WebRTC streams.
type CodecParamsProvider interface {
	// CodecParams returns the current codec parameters detected from the stream.
	// Returns nil slices if codec info frames have not been received yet.
	CodecParams() (codec Format, sps, pps, vps []byte)
}

// Camera represents a camera source configuration
type Camera struct {
	ID       string
	Name     string
	Protocol Protocol
	Encoding Format
	URL      string
	Username string
	Password string
	Enabled  bool

	CreatedAt time.Time
}

type Recording struct {
	ID            string    `json:"id"`
	CameraID      string    `json:"camera_id"`
	FilePath      string    `json:"file_path"`
	Format        Format    `json:"format"`
	StartedAt     time.Time `json:"started_at"`
	EndedAt       time.Time `json:"ended_at"`
	Duration      float64   `json:"duration"`
	FileSize      int64     `json:"file_size"`
	FrameCount    int       `json:"frame_count"`
	Merged        bool      `json:"merged"`
	MergeStatus   string    `json:"merge_status"`
	Archived      bool      `json:"archived"`
	ReconnectedAt time.Time `json:"reconnected_at,omitempty"`
	GapReason     string    `json:"gap_reason,omitempty"`
	Locked        bool      `json:"locked"`
}

type Segment struct {
	ID         string
	CameraID   string
	FilePath   string
	Format     Format
	StartedAt  time.Time
	TempPath   string
	FrameCount int
}

type SegmentMeta struct {
	CameraID string
	Format   Format
}

type RecordingFilter struct {
	CameraID  string
	StartTime time.Time
	EndTime   time.Time
	Format    Format
	Merged    *bool // nil = all, true = merged only, false = unmerged only
	Search    string
	Limit     int
	Offset    int
	SortBy    string // started_at, duration, file_size, camera_id; default: started_at
	SortOrder string // asc, desc; default: desc
	Archived  *bool  // nil = all, true = archived only, false = not archived
}

// Event represents a product-level NVR event persisted for search and playback.
type Event struct {
	ID             int64      `json:"id"`
	CameraID       string     `json:"camera_id"`
	Source         string     `json:"source"`
	Type           string     `json:"type"`
	Severity       string     `json:"severity"`
	Status         string     `json:"status"`
	Message        string     `json:"message"`
	Metadata       string     `json:"metadata"`
	RecordingID    string     `json:"recording_id,omitempty"`
	SnapshotPath   string     `json:"snapshot_path,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

const (
	EventSourceHealth   = "health"
	EventSourceRecorder = "recorder"
	EventSourceAI       = "ai"
	EventSourceMQTT     = "mqtt"

	EventSeverityInfo     = "info"
	EventSeverityWarning  = "warning"
	EventSeverityCritical = "critical"

	EventStatusOpen         = "open"
	EventStatusAcknowledged = "acknowledged"

	AlarmActionRecord     = "record"
	AlarmActionWebhook    = "webhook"
	AlarmActionGotoPreset = "goto_preset"
)

type AlarmRule struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Enabled      bool      `json:"enabled"`
	CameraID     string    `json:"camera_id"`
	Source       string    `json:"source"`
	EventType    string    `json:"event_type"`
	Severity     string    `json:"severity"`
	Action       string    `json:"action"`
	ActionTarget string    `json:"action_target"`
	CreatedAt    time.Time `json:"created_at"`
}

type RecorderStatus string

// CameraErrorDetail represents a camera error with type classification and message.
// This is used to provide detailed error info to the frontend (e.g. TUTK errors).
type CameraErrorDetail struct {
	Type       string    `json:"type"`
	Message    string    `json:"message"`
	DetectedAt time.Time `json:"detected_at"`
}

type StorageStats struct {
	TotalBytes     int64 `json:"total_bytes"`
	UsedBytes      int64 `json:"used_bytes"`
	RecordingCount int   `json:"recording_count"`
	CameraCount    int   `json:"camera_count"`
}

// DailyStats represents aggregated recording statistics for a single day.
type DailyStats struct {
	Date         string         `json:"date"`
	Recordings   int            `json:"recordings"`
	TotalSize    int64          `json:"total_size"`
	CameraCounts map[string]int `json:"cameras,omitempty"`
}

type Protocol string

type Format string

// Constants for statuses
const (
	StatusRecording    RecorderStatus = "recording"
	StatusPaused       RecorderStatus = "paused" // Recording paused; stream connection still active
	StatusStopped      RecorderStatus = "stopped"
	StatusError        RecorderStatus = "error"
	StatusReconnecting RecorderStatus = "reconnecting"
	StatusOffline      RecorderStatus = "offline" // Stream source is offline (e.g. lalmax stream idle)
)

// HealthStatus represents the health status of a camera or component.
type HealthStatus string

// Health status constants.
const (
	HealthStatusHealthy HealthStatus = "healthy"
	HealthStatusWarning HealthStatus = "warning"
	HealthStatusError   HealthStatus = "error"
	HealthStatusUnknown HealthStatus = "unknown"
)

// HealthEventType represents the type of a health monitoring event.
type HealthEventType string

// Health event type constants.
const (
	HealthEventConnectionLost     HealthEventType = "connection_lost"
	HealthEventConnectionRestored HealthEventType = "connection_restored"
	HealthEventStreamAnomaly      HealthEventType = "stream_anomaly"
	HealthEventFreezeDetected     HealthEventType = "freeze_detected"
	HealthEventFreezeRecovered    HealthEventType = "freeze_recovered"
)

// HealthReporter is the interface for reporting health events.
// Implementations must be safe for concurrent use.
type HealthReporter interface {
	ReportHealth(cameraID string, event HealthEvent)
}

// Protocol implementations
const (
	ProtoRTSPH264  Protocol = "rtsp_h264"
	ProtoRTSPMJPEG Protocol = "rtsp_mjpeg"
	ProtoHTTPJPEG  Protocol = "http_jpeg"
	ProtoRTSPH265  Protocol = "rtsp_h265"
	ProtoONVIF     Protocol = "onvif"
	ProtoXiaomi    Protocol = "xiaomi"
	ProtoGB28181   Protocol = "gb28181"
)

// Transport-only protocol constants
const (
	ProtoRTSP Protocol = "rtsp"
	ProtoHTTP Protocol = "http"
)

// Encoding constants
const (
	EncJPEG Format = "jpeg"
)

// Formats used for recordings/segments
const (
	FormatH264  Format = "h264"
	FormatMJPEG Format = "mjpeg"
	FormatH265  Format = "h265"
)

// Audio format constants
const (
	FormatAAC  Format = "aac"  // AAC audio
	FormatG711 Format = "g711" // G.711 mu-law/a-law audio
)

// AudioCodec represents the audio codec type for AudioFrame.
type AudioCodec string

const (
	AudioAAC  AudioCodec = "aac"  // AAC audio codec
	AudioG711 AudioCodec = "g711" // G.711 mu-law (PCMU) and a-law (PCMA)
	AudioOpus AudioCodec = "opus" // Opus audio codec
)

// Merge status constants.
const (
	MergeStatusPending = "pending"
	MergeStatusMerged  = "merged"
	MergeStatusFailed  = "failed"
)

// AudioFrame represents a single audio frame for distribution through StreamHub.
type AudioFrame struct {
	PTS   int64      // Presentation timestamp (same clock as video)
	Codec AudioCodec // Audio codec type
	Data  []byte     // Encoded audio data (AAC frames, G.711 samples, etc.)
}

// ValidEncodingsForProtocol maps transport protocol to supported encodings
var ValidEncodingsForProtocol = map[string][]string{
	string(ProtoRTSP):    {string(FormatH264), string(FormatH265), string(FormatMJPEG)},
	string(ProtoHTTP):    {string(EncJPEG)},
	string(ProtoONVIF):   {string(FormatH264), string(FormatH265)},
	string(ProtoXiaomi):  {string(FormatH264), string(FormatH265)},
	string(ProtoGB28181): {string(FormatH264), string(FormatH265)},
}

// ParseLegacyProtocol splits old combined protocol strings (e.g. "rtsp_h264") into separate protocol and encoding
func ParseLegacyProtocol(old string) (protocol, encoding string, err error) {
	switch old {
	case "rtsp_h264":
		return "rtsp", "h264", nil
	case "rtsp_h265":
		return "rtsp", "h265", nil
	case "rtsp_mjpeg":
		return "rtsp", "mjpeg", nil
	case "http_jpeg":
		return "http", "jpeg", nil
	case "onvif":
		return "onvif", "", nil
	default:
		return "", "", fmt.Errorf("unknown legacy protocol: %s", old)
	}
}

// ValidateProtocolEncoding checks if the protocol+encoding combination is valid.
// Empty encoding is allowed for ONVIF (auto-detect).
func ValidateProtocolEncoding(protocol, encoding string) error {
	encodings, ok := ValidEncodingsForProtocol[protocol]
	if !ok {
		return fmt.Errorf("unknown protocol: %s", protocol)
	}
	// ONVIF allows empty encoding (auto-detect)
	if protocol == string(ProtoONVIF) && encoding == "" {
		return nil
	}
	// GB28181 streams are managed via SIP; encoding is informational for stream mapping.
	if protocol == string(ProtoGB28181) && encoding == "" {
		return nil
	}
	for _, e := range encodings {
		if e == encoding {
			return nil
		}
	}
	return fmt.Errorf("encoding %q not valid for protocol %q", encoding, protocol)
}

// HealthEvent represents a single camera health check result stored in camera_health_events.
type HealthEvent struct {
	ID        int64     `json:"id"`
	CameraID  string    `json:"camera_id"`
	EventType string    `json:"event_type"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	Metadata  string    `json:"metadata"`
	CreatedAt time.Time `json:"created_at"`
}

// CameraHealth represents the latest health status summary for a camera.
type CameraHealth struct {
	CameraID      string    `json:"camera_id"`
	LatestStatus  string    `json:"latest_status"`
	LatestEvent   string    `json:"latest_event"`
	LatestMessage string    `json:"latest_message"`
	LastEventAt   time.Time `json:"last_event_at"`
}

// HourlyStats represents recording activity aggregated per hour.
type HourlyStats struct {
	Hour       string `json:"hour"`        // RFC3339, e.g. "2024-01-01T14:00:00Z"
	Recordings int    `json:"recordings"`
	TotalSize  int64  `json:"total_size"`
}

// CameraUptimeStat summarises health-event activity for a single camera over a period.
type CameraUptimeStat struct {
	CameraID           string `json:"camera_id"`
	CameraName         string `json:"camera_name"`
	ConnectionLosses   int    `json:"connection_losses"`
	ConnectionRestores int    `json:"connection_restores"`
	TotalEvents        int    `json:"total_events"`
}

// SystemMetricSample is a single periodic snapshot of system resource usage.
type SystemMetricSample struct {
	Timestamp  int64   `json:"ts"`         // Unix seconds
	CPUPct     float64 `json:"cpu"`        // 0–100
	MemPct     float64 `json:"mem"`        // 0–100
	NetUpBps   float64 `json:"net_up"`     // bytes/sec sent
	NetDnBps   float64 `json:"net_dn"`     // bytes/sec received
	Goroutines int     `json:"goroutines"` // runtime.NumGoroutine()
}

// StreamMetricSample is a single periodic snapshot of a stream's live metrics.
type StreamMetricSample struct {
	Timestamp    int64   `json:"ts"`            // Unix seconds
	InFPS        float64 `json:"in_fps"`        // publisher input frame rate
	BitrateKbits int     `json:"bitrate_kbits"` // publisher bitrate (audio+video combined)
	Subscribers  int     `json:"subscribers"`   // number of active subscribers
}
