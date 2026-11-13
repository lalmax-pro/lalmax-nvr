package api

import (
    "context"
    "net/http"
    "strings"

    "github.com/go-chi/chi/v5"
    "github.com/lalmax-pro/lalmax-nvr/internal/recorder"
    "github.com/lalmax-pro/lalmax-nvr/internal/storage"
    "github.com/lalmax-pro/lalmax-nvr/internal/transcode"
)

// SetDerivedRecorder supplies the existing segmented recording engine. A
// derived output is never recorded automatically; these routes require the
// operator permission and a running, source-bound transcoding job.
func (h *Handler) SetDerivedRecorder(m *recorder.TaskManager) {
    h.derivedRecorder = m
}

func (h *Handler) derivedRecordTarget(w http.ResponseWriter, r *http.Request) (transcode.Job, string, bool) {
    if !h.requireTranscodeService(w) { return transcode.Job{}, "", false }
    if h.derivedRecorder == nil || h.isInternalMediaStream == nil {
        writeError(w, http.StatusServiceUnavailable, "derived recording is unavailable")
        return transcode.Job{}, "", false
    }
    generation, ok := transcodeGeneration(w, r)
    if !ok { return transcode.Job{}, "", false }
    repo, ok := h.transcodeRepository(w)
    if !ok { return transcode.Job{}, "", false }
    jobs, err := repo.ListTranscodeJobs(r.Context())
    if err != nil { transcodeAPIError(w, err); return transcode.Job{}, "", false }
    for _, job := range jobs {
        if job.ID != chi.URLParam(r, "id") { continue }
        if job.Generation != generation {
            transcodeAPIError(w, &transcode.Error{Code:"revision_conflict", Message:"job generation changed"})
            return transcode.Job{}, "", false
        }
        _, outputID, err := h.transcodeService.PlaybackTarget(r.Context(), job.Source.StreamID, job.ID)
        if err != nil { transcodeAPIError(w, err); return transcode.Job{}, "", false }
        if !h.isInternalMediaStream(outputID) {
            writeError(w, http.StatusServiceUnavailable, "derived output is not registered")
            return transcode.Job{}, "", false
        }
        return job, outputID, true
    }
    transcodeAPIError(w, &transcode.Error{Code:"job_not_found", Message:"job does not exist"})
    return transcode.Job{}, "", false
}

func (h *Handler) handleStartDerivedRecording(w http.ResponseWriter, r *http.Request) {
    job, outputID, ok := h.derivedRecordTarget(w, r)
    if !ok { return }
    if h.mediaEngine == nil {
        writeError(w, http.StatusServiceUnavailable, "media engine unavailable")
        return
    }
    actual, err := h.mediaEngine.GetStream(r.Context(), outputID)
    if err != nil || actual == nil || !actual.Active || !strings.EqualFold(actual.VideoCodec, "h264") {
        writeError(w, http.StatusServiceUnavailable, "recordable derived H264 media is not ready")
        return
    }
    provenanceDB, ok := h.db.(interface {
        SaveDerivedRecordingSource(context.Context,storage.DerivedRecordingSource) error
    })
    if !ok {
        writeError(w, http.StatusServiceUnavailable, "derived recording provenance storage is unavailable")
        return
    }
    if err := provenanceDB.SaveDerivedRecordingSource(r.Context(),storage.DerivedRecordingSource{
        PhysicalStreamID: outputID,SourceStreamID:job.Source.StreamID,
        JobID:job.ID,Generation:job.Generation,
        ProfileID:job.ProfileID,ProfileRevision:job.ProfileRevision,
        VideoCodec:strings.ToLower(actual.VideoCodec),AudioCodec:strings.ToLower(actual.AudioCodec),
    }); err != nil {
        writeError(w, http.StatusInternalServerError, "could not persist recording provenance")
        return
    }
    if err := h.derivedRecorder.EnsureDerived(r.Context(), outputID); err != nil {
        writeError(w, http.StatusServiceUnavailable, "derived recording could not be started")
        return
    }
    h.logSuccess(r, "transcode.record.start", "transcode", job.ID, "derived recording started",
        map[string]any{"source_id":job.Source.StreamID, "generation":job.Generation})
    w.Header().Set("Cache-Control", "no-store")
    writeJSON(w, http.StatusCreated, map[string]any{"job_id":job.ID,"source_id":job.Source.StreamID,"generation":job.Generation,"recording":true})
}

func (h *Handler) handleStopDerivedRecording(w http.ResponseWriter, r *http.Request) {
    job, outputID, ok := h.derivedRecordTarget(w, r)
    if !ok { return }
    if err := h.derivedRecorder.Stop(r.Context(), outputID, "derived_recording_stopped"); err != nil {
        writeError(w, http.StatusServiceUnavailable, "derived recording could not be stopped")
        return
    }
    h.logSuccess(r, "transcode.record.stop", "transcode", job.ID, "derived recording stopped",
        map[string]any{"source_id":job.Source.StreamID, "generation":job.Generation})
    w.WriteHeader(http.StatusNoContent)
}

