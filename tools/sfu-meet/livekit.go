package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/livekitsfu"
	"github.com/lalmax-pro/lalmax-nvr/internal/sfumeetui"
)

// newLiveKitDemoHandler is loopback-only. The launch secret is exchanged for
// a room-scoped LiveKit token; the LiveKit API secret never reaches the page.
func newLiveKitDemoHandler(origin, launchSecret, liveKitURL, key, secret string) (http.Handler, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid demo origin")
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || host != "127.0.0.1" || port == "" || len(launchSecret) < 32 {
		return nil, errors.New("demo requires loopback origin and secret")
	}
	issuer, err := livekitsfu.NewTokenIssuer(key, secret)
	if err != nil {
		return nil, err
	}
	liveURL, err := url.Parse(liveKitURL)
	if err != nil || liveURL.Scheme != "http" || liveURL.Hostname() != "127.0.0.1" || liveURL.Port() == "" || liveURL.User != nil || liveURL.Path != "" || liveURL.RawQuery != "" || liveURL.Fragment != "" {
		return nil, errors.New("LiveKit demo requires a loopback URL")
	}
	names := map[string]string{"/": "web/livekit.html", "/app.js": "web/livekit-app.js", "/style.css": "web/style.css", "/livekit-sdk.js": "web/livekit-sdk.js"}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != u.Host {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/api/sfu/livekit/join" {
			w.Header().Set("Cache-Control", "no-store")
			if r.Method != http.MethodPost || r.Header.Get("Origin") != origin {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			username, password, ok := r.BasicAuth()
			if !ok || username != "demo" || subtle.ConstantTimeCompare([]byte(password), []byte(launchSecret)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			var input struct {
				Room string `json:"room"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil || input.Room != "demo" {
				http.Error(w, "invalid room", http.StatusBadRequest)
				return
			}
			if err := decoder.Decode(&struct{}{}); err != io.EOF {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			id, err := randomID()
			if err != nil {
				http.Error(w, "unavailable", http.StatusInternalServerError)
				return
			}
			identity := "demo_" + id[:24]
			token, err := issuer.IssueJoin(livekitsfu.JoinGrant{Room: "demo", Identity: identity, ValidFor: 5 * time.Minute, CanPublish: true, CanSubscribe: true})
			if err != nil {
				http.Error(w, "unavailable", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"url": liveKitURL, "token": token, "room": "demo", "identity": identity, "expires_in": 300})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/licenses" {
			content, err := sfumeetui.LiveKitNotices()
			if err != nil {
				http.Error(w, "unavailable", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			http.ServeContent(w, r, "licenses.txt", time.Time{}, bytes.NewReader(content))
			return
		}
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
		if name == "web/livekit.html" {
			content = bytes.Replace(content, []byte(`<html lang="zh-CN">`), []byte(`<html lang="zh-CN" data-mode="local">`), 1)
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
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' http://"+liveURL.Host+" ws://"+liveURL.Host+"; media-src 'self' blob:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(content))
	}), nil
}
