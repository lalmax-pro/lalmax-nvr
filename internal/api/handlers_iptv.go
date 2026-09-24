package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/iptv"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

type iptvImportRequest struct {
	Name         string            `json:"name"`
	PlaylistURL  string            `json:"playlist_url"`
	PlaylistText string            `json:"playlist_text"`
	Headers      map[string]string `json:"headers"`
}

type iptvCommitRequest struct {
	ItemIDs []string `json:"item_ids"`
}

type iptvChannelUpdateRequest struct {
	Name           string `json:"name"`
	Enabled        *bool  `json:"enabled"`
	Favorite       *bool  `json:"favorite"`
	PublishEnabled *bool  `json:"publish_enabled"`
}

func (h *Handler) handleIPTVCreateImport(w http.ResponseWriter, r *http.Request) {
	if h.iptvSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "iptv module unavailable")
		return
	}
	var req iptvImportRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.PlaylistText) != "" {
		created, err := h.iptvSvc.ImportFromReader(r.Context(), req.Name, strings.NewReader(req.PlaylistText))
		if err != nil {
			writeIPTVError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, created)
		return
	}
	created, err := h.iptvSvc.ImportFromURL(r.Context(), iptv.ImportRequest{
		Name:        req.Name,
		PlaylistURL: req.PlaylistURL,
		Headers:     req.Headers,
	})
	if err != nil {
		writeIPTVError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, created)
}

func (h *Handler) handleIPTVGetImport(w http.ResponseWriter, r *http.Request) {
	if h.iptvSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "iptv module unavailable")
		return
	}
	job, err := h.iptvSvc.GetImport(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load import")
		return
	}
	if job == nil {
		writeError(w, http.StatusNotFound, "import not found")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *Handler) handleIPTVListImportItems(w http.ResponseWriter, r *http.Request) {
	if h.iptvSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "iptv module unavailable")
		return
	}
	items, err := h.iptvSvc.ListImportItems(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("status"), r.URL.Query().Get("q"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list import items")
		return
	}
	if items == nil {
		items = []storage.IPTVImportItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (h *Handler) handleIPTVProbeImportItem(w http.ResponseWriter, r *http.Request) {
	if h.iptvSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "iptv module unavailable")
		return
	}
	item, err := h.iptvSvc.ProbeItem(r.Context(), chi.URLParam(r, "item_id"))
	if err != nil {
		writeIPTVError(w, err)
		return
	}
	if item == nil {
		writeError(w, http.StatusNotFound, "import item not found")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) handleIPTVCommitImport(w http.ResponseWriter, r *http.Request) {
	if h.iptvSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "iptv module unavailable")
		return
	}
	var req iptvCommitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	src, n, err := h.iptvSvc.Commit(r.Context(), chi.URLParam(r, "id"), req.ItemIDs)
	if err != nil {
		writeIPTVError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": src, "channel_count": n})
}

func (h *Handler) handleIPTVListSources(w http.ResponseWriter, r *http.Request) {
	if h.iptvSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "iptv module unavailable")
		return
	}
	sources, err := h.iptvSvc.ListSources(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list iptv sources")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": sources})
}

func (h *Handler) handleIPTVDeleteSource(w http.ResponseWriter, r *http.Request) {
	if h.iptvSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "iptv module unavailable")
		return
	}
	if err := h.iptvSvc.DeleteSource(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete iptv source")
		return
	}
	h.refreshRecordingPlans(r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) handleIPTVListGroups(w http.ResponseWriter, r *http.Request) {
	if h.iptvSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "iptv module unavailable")
		return
	}
	groups, err := h.iptvSvc.ListGroups(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list iptv groups")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (h *Handler) handleIPTVListChannels(w http.ResponseWriter, r *http.Request) {
	if h.iptvSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "iptv module unavailable")
		return
	}
	var favorite *bool
	if v := r.URL.Query().Get("favorite"); v != "" {
		f := v == "true" || v == "1"
		favorite = &f
	}
	channels, err := h.iptvSvc.ListChannels(r.Context(), r.URL.Query().Get("source_id"), r.URL.Query().Get("group"), r.URL.Query().Get("q"), favorite)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list iptv channels")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": channels, "total": len(channels)})
}

func (h *Handler) handleIPTVGetChannel(w http.ResponseWriter, r *http.Request) {
	if h.iptvSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "iptv module unavailable")
		return
	}
	ch, err := h.iptvSvc.GetChannel(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get iptv channel")
		return
	}
	if ch == nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (h *Handler) handleIPTVGetPlayback(w http.ResponseWriter, r *http.Request) {
	if h.iptvSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "iptv module unavailable")
		return
	}
	playback, err := h.iptvSvc.GetPlaybackDetails(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if strings.Contains(err.Error(), "disabled") {
			writeError(w, http.StatusForbidden, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load channel playback")
		}
		return
	}
	if playback == nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, playback)
}

func (h *Handler) handleIPTVHLSProxy(w http.ResponseWriter, r *http.Request) {
	if h.iptvSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "iptv module unavailable")
		return
	}
	resp, finalURL, err := h.iptvSvc.OpenPlaybackResource(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("url"), r.Header.Get("Range"))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "disabled") {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		writeIPTVError(w, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		copyIPTVProxyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
		return
	}

	if iptv.IsHLSManifest(resp.Header.Get("Content-Type"), finalURL) {
		body, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
		if err != nil {
			writeError(w, http.StatusBadGateway, "failed to read upstream HLS manifest")
			return
		}
		if len(body) > 2<<20 {
			writeError(w, http.StatusBadGateway, "upstream HLS manifest is too large")
			return
		}
		body, err = iptv.RewriteHLSManifest(body, finalURL, chi.URLParam(r, "id"))
		if err != nil {
			writeError(w, http.StatusBadGateway, "upstream HLS manifest contains an invalid resource URL")
			return
		}
		copyIPTVProxyHeaders(w.Header(), resp.Header)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Del("Content-Length")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}
	copyIPTVProxyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func copyIPTVProxyHeaders(dst, src http.Header) {
	for _, name := range []string{"Content-Type", "Cache-Control", "ETag", "Last-Modified", "Accept-Ranges", "Content-Range", "Content-Length"} {
		if value := src.Get(name); value != "" {
			dst.Set(name, value)
		}
	}
}

func (h *Handler) handleIPTVUpdateChannel(w http.ResponseWriter, r *http.Request) {
	if h.iptvSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "iptv module unavailable")
		return
	}
	existing, err := h.iptvSvc.GetChannel(r.Context(), chi.URLParam(r, "id"))
	if err != nil || existing == nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	var req iptvChannelUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	enabled, favorite, publishEnabled := existing.Enabled, existing.Favorite, existing.PublishEnabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if req.Favorite != nil {
		favorite = *req.Favorite
	}
	if req.PublishEnabled != nil {
		publishEnabled = *req.PublishEnabled
	}
	ch, err := h.iptvSvc.UpdateChannel(r.Context(), existing.ID, req.Name, enabled, favorite, publishEnabled)
	if err != nil {
		if errors.Is(err, media.ErrHLSPullEmbeddedOnly) {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writeIPTVError(w, err)
		return
	}
	h.refreshRecordingPlans(r)
	writeJSON(w, http.StatusOK, ch)
}

func (h *Handler) handleIPTVDeleteChannel(w http.ResponseWriter, r *http.Request) {
	if h.iptvSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "iptv module unavailable")
		return
	}
	if err := h.iptvSvc.DeleteChannel(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete channel")
		return
	}
	h.refreshRecordingPlans(r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func writeIPTVError(w http.ResponseWriter, err error) {
	switch {
	case strings.Contains(err.Error(), "not found"):
		writeError(w, http.StatusNotFound, err.Error())
	case strings.Contains(err.Error(), "invalid") ||
		strings.Contains(err.Error(), "not allowed") ||
		strings.Contains(err.Error(), "not supported") ||
		strings.Contains(err.Error(), "disabled") ||
		strings.Contains(err.Error(), "no channels") ||
		strings.Contains(err.Error(), "none of the selected") ||
		strings.Contains(err.Error(), "already committed"):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusBadGateway, err.Error())
	}
}
