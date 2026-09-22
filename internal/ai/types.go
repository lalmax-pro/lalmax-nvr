package ai

import "context"

// Detection represents a single object detection result.
type Detection struct {
	Label      string     `json:"label"`
	Confidence float32    `json:"confidence"`
	Box        [4]float32 `json:"box"`                // [x, y, width, height] in normalized coordinates
	TrackID    *int       `json:"track_id,omitempty"` // Object tracking ID (from ByteTrack/supervision)
	ZoneID     string     `json:"zone_id,omitempty"`  // Zone identifier for region-based detection
}

// DetectionResult is a complete detection event for a camera frame.
type DetectionResult struct {
	CameraID   string      `json:"camera_id"`
	PTS        int64       `json:"pts"`
	Timestamp  int64       `json:"timestamp"`
	ImageURL   string      `json:"image_url,omitempty"`
	Detections []Detection `json:"detections"`
}

// Status represents the current state of the AI subsystem.
type Status struct {
	Backend   string `json:"backend"`   // "webhook", "disabled"
	Available bool   `json:"available"` // backend is ready
	Reason    string `json:"reason"`    // human-readable status explanation
}

// CallbackFunc is invoked when a detection event occurs.
type CallbackFunc func(result DetectionResult)

// Store persists AI events for history views.
type Store interface {
	InsertAIDetection(ctx context.Context, result DetectionResult) error
	InsertAIAnalysis(ctx context.Context, result interface{}) error
}
