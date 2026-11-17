package onvif

import (
	"time"
)

// Service represents an ONVIF service.
type Service struct {
	Namespace string
	XAddr     string
	Version   string
}

// Capabilities represents ONVIF device capabilities.
type Capabilities struct {
	Device    *DeviceCapabilities
	Media     *MediaCapabilities
	Media2    *MediaCapabilities
	Recording *RecordingCapabilities
	Search    *SearchCapabilities
	Replay    *ReplayCapabilities
	PTZ       *PTZCapabilities
	Imaging   *ImagingCapabilities
	Events    *EventsCapabilities
}

// DeviceCapabilities represents device service capabilities.
type DeviceCapabilities struct {
	XAddr string
}

// MediaCapabilities represents media service capabilities.
type MediaCapabilities struct {
	XAddr string
}

// RecordingCapabilities represents recording service capabilities.
type RecordingCapabilities struct {
	XAddr string
}

// SearchCapabilities represents search service capabilities.
type SearchCapabilities struct {
	XAddr string
}

// ReplayCapabilities represents replay service capabilities.
type ReplayCapabilities struct {
	XAddr string
}

// PTZCapabilities represents PTZ service capabilities.
type PTZCapabilities struct {
	XAddr string
}

// ImagingCapabilities represents imaging service capabilities.
type ImagingCapabilities struct {
	XAddr string
}

// EventsCapabilities represents events service capabilities.
type EventsCapabilities struct {
	XAddr string
}

// DeviceInfo represents device information.
type DeviceInfo struct {
	Manufacturer    string `json:"manufacturer"`
	Model           string `json:"model"`
	FirmwareVersion string `json:"firmware_version"`
	SerialNumber    string `json:"serial_number"`
	HardwareId      string `json:"hardware_id"`
}

// MediaProfile represents a media profile.
type MediaProfile struct {
	Token            string     `json:"token"`
	Name             string     `json:"name"`
	VideoSource      string     `json:"video_source"` // Video source configuration token.
	VideoSourceToken string     `json:"video_source_token,omitempty"`
	VideoEncoder     string     `json:"video_encoder"`
	AudioSource      string     `json:"audio_source"`
	AudioEncoder     string     `json:"audio_encoder"`
	Resolution       Resolution `json:"resolution"`
	Encoding         string     `json:"encoding"`
	Framerate        float64    `json:"framerate"`
	Bitrate          int        `json:"bitrate"`
}

// Resolution represents video resolution.
type Resolution struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Range represents a min/max range for a numeric parameter.
type Range struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// ImagingOptions represents supported ranges for imaging parameters.
type ImagingOptions struct {
	Brightness  *Range `json:"brightness,omitempty"`
	Contrast    *Range `json:"contrast,omitempty"`
	Saturation  *Range `json:"saturation,omitempty"`
	Sharpness   *Range `json:"sharpness,omitempty"`
}

// Recording represents a recording on an ONVIF device.
type Recording struct {
	Token       string          `json:"token"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Source      RecordingSource `json:"source"`
	StartTime   time.Time       `json:"start_time"`
	EndTime     time.Time       `json:"end_time"`
	Status      string          `json:"status"`
	Tracks      []Track         `json:"tracks"`
}

// RecordingSource represents the source of a recording.
type RecordingSource struct {
	SourceID    string `json:"source_id"`
	Name        string `json:"name"`
	Location    string `json:"location"`
	Description string `json:"description"`
}

// Track represents a recording track.
type Track struct {
	Token       string             `json:"token"`
	TrackType   string             `json:"track_type"`
	Description string             `json:"description"`
	Segments    []RecordingSegment `json:"segments,omitempty"`
}

// RecordingSegment represents a segment of a recording.
type RecordingSegment struct {
	Token           string    `json:"token"`
	RecordingToken  string    `json:"recording_token"`
	StartTime       time.Time `json:"start_time"`
	EndTime         time.Time `json:"end_time"`
	FilePath        string    `json:"file_path"`
	Duration        int64     `json:"duration"`                   // seconds
	Size            int64     `json:"size"`                       // bytes
	RecordingBounds bool      `json:"recording_bounds,omitempty"` // The overall recording bounds may contain gaps.
}

// SearchFilter represents search criteria for recordings.
type SearchFilter struct {
	RecordingToken string
	StartTime      time.Time
	EndTime        time.Time
	MaxResults     int
	SearchToken    string // For pagination continuation
}

// SearchResult represents a search result with pagination support.
type SearchResult struct {
	Segments    []RecordingSegment `json:"segments"`
	SearchToken string             `json:"search_token"`
	SearchState string             `json:"search_state"` // Completed, MoreResults, etc.
	HasMore     bool               `json:"has_more"`
}

// PTZStatus represents PTZ status.
type PTZStatus struct {
	Position PTZPosition `json:"position"`
	Moving   bool        `json:"moving"`
	Error    string      `json:"error"`
}

// PTZPosition represents PTZ position.
type PTZPosition struct {
	PanTilt Vector2D `json:"pan_tilt"`
	Zoom   Vector1D `json:"zoom"`
}

// Vector2D represents a 2D vector (pan/tilt).
type Vector2D struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Vector1D represents a 1D vector (zoom).
type Vector1D struct {
	X float64 `json:"x"`
}

// PTZPreset represents a PTZ preset.
type PTZPreset struct {
	Token    string      `json:"token"`
	Name     string      `json:"name"`
	Position PTZPosition `json:"position"`
}

// PTZVelocity represents PTZ velocity.
type PTZVelocity struct {
	PanTilt Vector2D
	Zoom    Vector1D
}

// Event represents an ONVIF event.
type Event struct {
	Topic       string                 `json:"topic"`
	Timestamp   time.Time              `json:"timestamp"`
	Source      map[string]interface{} `json:"source"`
	Data        map[string]interface{} `json:"data"`
	PropertyKey string                 `json:"property_key"`
}

// ImagingSettings represents imaging settings.
type ImagingSettings struct {
	Brightness      float64 `json:"brightness"`
	Contrast        float64 `json:"contrast"`
	ColorSaturation float64 `json:"color_saturation"`
	Sharpness       float64 `json:"sharpness"`
	WhiteBalance    string  `json:"white_balance"`
	FocusMode       string  `json:"focus_mode"`
	ExposureMode    string  `json:"exposure_mode"`
}
