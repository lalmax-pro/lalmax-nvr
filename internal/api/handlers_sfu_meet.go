package api

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/sfumeetui"
)

// The page is public like the NVR SPA; all signaling and bridge operations
// require NVR authentication and an issued single-use ticket.
func (h *Handler) serveSFUMeet(w http.ResponseWriter, r *http.Request) {
	names := map[string]string{"/sfu-meet/": "web/index.html", "/sfu-meet/app.js": "web/app.js", "/sfu-meet/style.css": "web/style.css"}
	name, ok := names[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	content, err := sfumeetui.Assets.ReadFile(name)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if name == "web/index.html" {
		content = bytes.Replace(content, []byte(`<html lang="zh-CN">`), []byte(`<html lang="zh-CN" data-mode="nvr">`), 1)
		w.Header().Set("Permissions-Policy", "camera=(self), microphone=(self), geolocation=()")
	}
	if strings.HasSuffix(name, ".js") {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	if strings.HasSuffix(name, ".css") {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss:; media-src 'self' blob:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(content))
}
