package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
)

// SetTranscodeService must be called during bootstrap, before serving requests.
func (h *Handler) SetTranscodeService(service *transcode.Service) { h.transcodeService = service }

// SetInternalMediaStreamCheck installs the authoritative runtime registry check.
func (h *Handler) SetInternalMediaStreamCheck(check func(string) bool) {
	h.isInternalMediaStream = check
}

// TranscodeProbeService shares administrator self-tests with activation gates.
func (h *Handler) TranscodeProbeService() *transcode.ProbeService { return h.transcodeProbe }
func (h *Handler) handleTranscodeJobs(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.transcodeRepository(w)
	if !ok {
		return
	}
	jobs, err := repo.ListTranscodeJobs(r.Context())
	if err != nil {
		transcodeAPIError(w, err)
		return
	}
	variants := []transcode.Variant{}
	if h.transcodeService != nil {
		variants, err = h.transcodeService.Variants(r.Context(), "")
		if err != nil {
			transcodeAPIError(w, err)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"jobs": jobs, "variants": variants, "runtime_available": h.transcodeService != nil})
}
func (h *Handler) requireTranscodeService(w http.ResponseWriter) bool {
	if h.transcodeService == nil {
		writeError(w, 503, "transcoding runtime is not configured")
		return false
	}
	return true
}
func (h *Handler) handleSaveTranscodeJob(w http.ResponseWriter, r *http.Request) {
	if !h.requireTranscodeService(w) {
		return
	}
	var job transcode.Job
	if decodeTranscodeBody(w, r, &job) != nil {
		writeError(w, 400, "invalid job")
		return
	}
	expected := job.Generation
	if r.Method == http.MethodPost {
		if expected != 0 {
			writeError(w, 400, "new job generation must be zero")
			return
		}
	} else if expected == 0 || job.ID != chi.URLParam(r, "id") {
		writeError(w, 400, "job id and generation are required")
		return
	}
	// Resolve source existence through the configured engine; arbitrary addresses
	// cannot be supplied as input. Engine identity remains server-controlled.
	if job.Source.EngineID != "embedded" || h.mediaEngine == nil {
		writeError(w, 400, "a configured embedded media source is required")
		return
	}
	if h.config != nil && h.config.Media.Mode != "" && h.config.Media.Mode != "embedded" {
		writeError(w, 400, "external media engines are not supported for transcoding yet")
		return
	}
	info, err := h.mediaEngine.GetStream(r.Context(), job.Source.StreamID)
	if err != nil || info == nil {
		writeError(w, 400, "source stream not found")
		return
	}
	app := info.AppName
	if app == "" {
		app = "live"
	}
	if job.Source.AppName != app {
		writeError(w, 400, "source application does not match")
		return
	}
	saved, err := h.transcodeService.Save(r.Context(), job, expected)
	if err != nil {
		transcodeAPIError(w, err)
		return
	}
	status := 200
	if r.Method == http.MethodPost {
		status = 201
	}
	writeJSON(w, status, saved)
}
func transcodeGeneration(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	generation, err := strconv.ParseUint(r.URL.Query().Get("generation"), 10, 64)
	if err != nil || generation == 0 {
		writeError(w, 400, "generation is required")
		return 0, false
	}
	return generation, true
}
func (h *Handler) handleStartTranscodeJob(w http.ResponseWriter, r *http.Request) {
	if !h.requireTranscodeService(w) {
		return
	}
	generation, ok := transcodeGeneration(w, r)
	if !ok {
		return
	}
	runtime, err := h.transcodeService.Start(r.Context(), chi.URLParam(r, "id"), generation)
	if err != nil {
		transcodeAPIError(w, err)
		return
	}
	writeJSON(w, 202, runtime)
}
func (h *Handler) handleStopTranscodeJob(w http.ResponseWriter, r *http.Request) {
	if !h.requireTranscodeService(w) {
		return
	}
	generation, ok := transcodeGeneration(w, r)
	if !ok {
		return
	}
	if err := h.transcodeService.Stop(r.Context(), chi.URLParam(r, "id"), generation); err != nil {
		transcodeAPIError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) handleDeleteTranscodeJob(w http.ResponseWriter, r *http.Request) {
	if !h.requireTranscodeService(w) {
		return
	}
	generation, ok := transcodeGeneration(w, r)
	if !ok {
		return
	}
	if err := h.transcodeService.Delete(r.Context(), chi.URLParam(r, "id"), generation); err != nil {
		transcodeAPIError(w, err)
		return
	}
	w.WriteHeader(204)
}
