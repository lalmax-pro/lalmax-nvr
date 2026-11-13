package api

import (
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
)

// Administrator-only same-origin preview. Internal bearer credentials are never
// sent to the browser; client query parameters cannot override backend tokens.
func (h *Handler) handleTranscodeFLV(w http.ResponseWriter, r *http.Request) {
	if !h.requireTranscodeService(w) {
		return
	}
	playback, err := h.resolveDerivedPlayback(r.Context(), chi.URLParam(r, "stream_id"), chi.URLParam(r, "job_id"), "flv")
	if err != nil {
		writeDerivedPlaybackError(w, err, "derived preview unavailable")
		return
	}
	upstream, err := url.Parse(playback.URL.URL)
	if err != nil || upstream.Host == "" || (upstream.Scheme != "http" && upstream.Scheme != "https") {
		writeError(w, 503, "invalid preview endpoint")
		return
	}
	h.proxyDerivedFLV(w, r, upstream)
}

// proxyDerivedFLV takes a trusted URL constructed by the runtime registry only.
func (h *Handler) proxyDerivedFLV(w http.ResponseWriter, r *http.Request, upstream *url.URL) {
	w.Header().Set("Cache-Control", "no-store")
	// Prevent a redirect carrying user authorization to an arbitrary authority.
	client := *h.playProxyClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(r.Context(), "GET", upstream.String(), nil)
	if err != nil {
		writeError(w, 502, "preview request failed")
		return
	}
	// The internal media URL carries a narrowly-scoped playback token.
	// Never forward a browser's administrator Basic credentials to the media
	// engine, even when it runs on the same host.
	response, err := client.Do(req)
	if err != nil {
		writeError(w, 502, "preview backend unavailable")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		writeError(w, 502, "preview rejected by media engine")
		return
	}
	w.Header().Set("Content-Type", "video/x-flv")
	w.WriteHeader(200)
	_ = copyBodyFlush(w, response.Body)
}
