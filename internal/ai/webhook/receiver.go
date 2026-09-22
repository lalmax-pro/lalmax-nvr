package webhook

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
)

var logger = slog.Default().With("component", "ai-webhook")

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

// CallbackFunc is invoked when a detection event occurs.
type CallbackFunc func(result DetectionResult)

// DetectRequest is the JSON body pushed by external AI services.
type DetectRequest struct {
	CameraID   string      `json:"camera_id"`
	PTS        int64       `json:"pts"`
	Timestamp  int64       `json:"timestamp"`
	ImageURL   string      `json:"image_url"`
	Detections []Detection `json:"detections"`
}

// Receiver accepts webhook pushes from external AI services.
type Receiver struct {
	mu        sync.RWMutex
	callbacks map[string]CallbackFunc
	counter   int
}

// NewReceiver creates a new webhook receiver.
func NewReceiver() *Receiver {
	return &Receiver{
		callbacks: make(map[string]CallbackFunc),
	}
}

// OnDetection registers a callback for detection events.
func (r *Receiver) OnDetection(cb CallbackFunc) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counter++
	id := fmt.Sprintf("webhook-cb-%d", r.counter)
	r.callbacks[id] = cb
	return id
}

// UnregisterCallback removes a callback.
func (r *Receiver) UnregisterCallback(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.callbacks[id]
	delete(r.callbacks, id)
	return ok
}

// HandleHTTP returns an http.HandlerFunc that accepts webhook pushes.
func (r *Receiver) HandleHTTP() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}

		var detectReq DetectRequest
		if err := json.Unmarshal(body, &detectReq); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		if detectReq.CameraID == "" {
			http.Error(w, "camera_id is required", http.StatusBadRequest)
			return
		}

		result := DetectionResult{
			CameraID:   detectReq.CameraID,
			PTS:        detectReq.PTS,
			Timestamp:  detectReq.Timestamp,
			ImageURL:   detectReq.ImageURL,
			Detections: detectReq.Detections,
		}

		// Dispatch to all registered callbacks
		r.mu.RLock()
		for _, cb := range r.callbacks {
			go cb(result)
		}
		r.mu.RUnlock()

		logger.Info("webhook detection received",
			"camera_id", detectReq.CameraID,
			"detections", len(detectReq.Detections),
		)

		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok"}`)
	}
}
