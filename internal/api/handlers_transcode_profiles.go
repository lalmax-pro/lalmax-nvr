package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
)

func (h *Handler) transcodeRepository(w http.ResponseWriter) (transcode.Repository, bool) {
	repo, ok := h.db.(transcode.Repository)
	if !ok {
		writeError(w, 503, "transcoding repository unavailable")
	}
	return repo, ok
}
func transcodeAPIError(w http.ResponseWriter, err error) {
	var domain *transcode.Error
	if !errors.As(err, &domain) {
		writeError(w, 500, "transcoding operation failed")
		return
	}
	status := 400
	switch domain.Code {
	case "revision_conflict", "profile_in_use", "probe_busy", "job_running", "job_stopping":
		status = 409
	case "backend_unavailable", "engine_unavailable", "service_stopped", "output_unavailable":
		status = 503
	case "probe_required", "transcoding_disabled":
		status = 412
	case "profile_not_found", "job_not_found", "lease_not_found":
		status = 404
	case "demand_unavailable":
		status = 412
	case "capacity_exceeded":
		status = 429
	case "output_timeout":
		status = 504
	}
	writeJSON(w, status, map[string]string{"error": domain.Message, "code": domain.Code})
}
func decodeTranscodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("expected one object")
	}
	return nil
}
func (h *Handler) handleTranscodeProfiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	repo, ok := h.transcodeRepository(w)
	if !ok {
		return
	}
	profiles, err := repo.ListTranscodeProfiles(r.Context())
	if err != nil {
		transcodeAPIError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"profiles": profiles})
}
func (h *Handler) handleSaveTranscodeProfile(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.transcodeRepository(w)
	if !ok {
		return
	}
	var p transcode.Profile
	if decodeTranscodeBody(w, r, &p) != nil {
		writeError(w, 400, "invalid profile")
		return
	}
	expected := p.Revision
	if r.Method == http.MethodPost {
		if expected != 0 {
			writeError(w, 400, "new profile revision must be zero")
			return
		}
	} else if p.ID != chi.URLParam(r, "id") || expected == 0 {
		writeError(w, 400, "profile id and revision are required")
		return
	}
	saved, err := repo.SaveTranscodeProfile(r.Context(), p, expected)
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
func (h *Handler) handleDeleteTranscodeProfile(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.transcodeRepository(w)
	if !ok {
		return
	}
	revision, err := strconv.ParseUint(r.URL.Query().Get("revision"), 10, 64)
	if err != nil || revision == 0 {
		writeError(w, 400, "revision is required")
		return
	}
	if err = repo.DeleteTranscodeProfile(r.Context(), chi.URLParam(r, "id"), revision); err != nil {
		transcodeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) handleProbeTranscodeProfile(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.transcodeRepository(w)
	if !ok {
		return
	}
	if h.transcodeProbe == nil {
		writeError(w, 503, "capability service unavailable")
		return
	}
	profiles, err := repo.ListTranscodeProfiles(r.Context())
	if err != nil {
		transcodeAPIError(w, err)
		return
	}
	for _, p := range profiles {
		if p.ID == chi.URLParam(r, "id") {
			cfg := transcode.Config{}
			if h.config != nil {
				cfg = h.config.Transcoding
			}
			report, err := h.transcodeProbe.Probe(r.Context(), cfg.Tools(), p)
			if err != nil {
				transcodeAPIError(w, err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, 200, report)
			return
		}
	}
	writeError(w, 404, "profile not found")
}
