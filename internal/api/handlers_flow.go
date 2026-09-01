package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

type flowCameraResponse struct {
	CameraID   string            `json:"camera_id"`
	Name       string            `json:"name"`
	Status     string            `json:"status"`
	Protocol   string            `json:"protocol"`
	Encoding   string            `json:"encoding"`
	Error      string            `json:"error,omitempty"`
	Source     flowSource        `json:"source"`
	Engine     *flowEngine       `json:"engine,omitempty"`
	Recording  flowRecording     `json:"recording"`
	Substream  flowSubstream     `json:"substream"`
	Viewers    map[string]int    `json:"viewers_by_protocol"`
}

type flowSource struct {
	Active    bool   `json:"active"`
	URLHost   string `json:"url_host,omitempty"`
	Transport string `json:"transport,omitempty"`
}

type flowEngine struct {
	Active         bool    `json:"active"`
	VideoCodec     string  `json:"video_codec,omitempty"`
	AudioCodec     string  `json:"audio_codec,omitempty"`
	FPS            float64 `json:"fps"`
	LastFrameAgeS  float64 `json:"last_frame_age_s"`
	PublisherProto string  `json:"publisher_protocol,omitempty"`
}

type flowRecording struct {
	Status       string `json:"status"`
	Paused       bool   `json:"paused"`
	MergePending int    `json:"merge_pending"`
}

type flowSubstream struct {
	Active bool `json:"active"`
}

func (h *Handler) handleCameraFlow(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, h.buildFlow(r, cam))
}

func (h *Handler) handleFlowStreams(w http.ResponseWriter, r *http.Request) {
	cams, err := h.db.ListCameras(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list cameras")
		return
	}
	out := make([]flowCameraResponse, 0, len(cams))
	for i := range cams {
		out = append(out, h.buildFlow(r, &cams[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"cameras": out})
}

func (h *Handler) buildFlow(r *http.Request, cam *storage.CameraRow) flowCameraResponse {
	resp := flowCameraResponse{
		CameraID: cam.ID,
		Name:     cam.Name,
		Status:   string(cam.Status),
		Protocol: cam.Protocol,
		Encoding: flowFirstNonEmpty(cam.Encoding, cam.StreamEncoding),
		Viewers:  map[string]int{},
		Source: flowSource{
			Transport: cam.RTSPTransport,
			URLHost:   hostOfURL(cam.URL),
		},
	}
	if cam.ErrorDetail != nil {
		resp.Error = *cam.ErrorDetail
	}

	status := string(cam.Status)
	paused := cam.RecordingPaused
	if h.camMgr != nil {
		if rec := h.camMgr.GetRecorder(cam.ID); rec != nil {
			status = string(rec.Status())
			if p, ok := rec.(model.PausableRecorder); ok {
				paused = p.IsPaused()
			}
		}
	}
	resp.Status = status
	resp.Recording.Status = status
	resp.Recording.Paused = paused
	if h.db != nil {
		if n, err := h.db.CountPendingMerges(r.Context(), cam.ID); err == nil {
			resp.Recording.MergePending = n
		}
	}

	if h.mediaEngine != nil {
		if info, err := h.mediaEngine.GetStream(r.Context(), cam.ID); err == nil && info != nil {
			resp.Source.Active = info.Active
			engine := &flowEngine{
				Active:     info.Active,
				VideoCodec: info.VideoCodec,
				AudioCodec: info.AudioCodec,
				FPS:        info.InFPS,
			}
			if info.Publisher != nil {
				engine.PublisherProto = info.Publisher.Protocol
			}
			if !info.LastFrameTime.IsZero() {
				engine.LastFrameAgeS = time.Since(info.LastFrameTime).Seconds()
			}
			for _, sub := range info.Subscribers {
				proto := strings.ToLower(strings.TrimSpace(sub.Protocol))
				if proto == "" {
					proto = "unknown"
				}
				resp.Viewers[proto]++
			}
			resp.Engine = engine
		}
		if sub, err := h.mediaEngine.GetStream(r.Context(), media.SubStreamID(cam.ID)); err == nil && sub != nil {
			resp.Substream.Active = sub.Active
		}
	}
	return resp
}

func hostOfURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		raw = raw[at+1:]
	}
	if slash := strings.Index(raw, "/"); slash >= 0 {
		raw = raw[:slash]
	}
	return raw
}

func flowFirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
