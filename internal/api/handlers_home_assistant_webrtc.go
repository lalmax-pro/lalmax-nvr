package api

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/pion/sdp/v3"
)

type homeAssistantWHEPSession struct {
	StreamID   string `json:"stream_id"`
	InstanceID string `json:"instance_id"`
	AppName    string `json:"app_name"`
	Derived    bool   `json:"derived,omitempty"`
	URL        string `json:"url"`
	Expires    int64  `json:"expires"`
}

func homeAssistantWebRTCCodec(codec string) string {
	switch strings.ToLower(codec) {
	case "h264":
		return "H264"
	case "h265", "hevc":
		return "H265"
	default:
		return ""
	}
}

func homeAssistantOfferSupportsCodec(offer []byte, codec string) bool {
	var description sdp.SessionDescription
	if description.Unmarshal(offer) != nil {
		return false
	}
	for _, m := range description.MediaDescriptions {
		if m.MediaName.Media != "video" || m.MediaName.Port.Value == 0 {
			continue
		}
		if _, inactive := m.Attribute("inactive"); inactive {
			continue
		}
		if _, sendonly := m.Attribute("sendonly"); sendonly {
			continue
		}
		for _, a := range m.Attributes {
			fields := strings.Fields(a.Value)
			if a.Key == "rtpmap" && len(fields) == 2 && slices.Contains(m.MediaName.Formats, fields[0]) && strings.EqualFold(fields[1], codec+"/90000") {
				return true
			}
		}
	}
	return false
}

func sameWHEPOrigin(a, b *url.URL) bool {
	return a != nil && b != nil && (a.Scheme == "http" || a.Scheme == "https") && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// WHEP session locations can contain internal playback credentials. Use
// authenticated encryption, never signed-but-readable Base64 JSON.
func (h *Handler) homeAssistantSessionAEAD() (cipher.AEAD, error) {
    key := sha256.Sum256(h.homeAssistantWHEPKey)
    block, err := aes.NewCipher(key[:])
    if err != nil { return nil,err }
    return cipher.NewGCM(block)
}

func (h *Handler) sealHomeAssistantSession(session homeAssistantWHEPSession) string {
    data,err:=json.Marshal(session)
    if err!=nil { return "" }
    aead,err:=h.homeAssistantSessionAEAD()
    if err!=nil { return "" }
    nonce:=make([]byte,aead.NonceSize())
    if _,err=rand.Read(nonce);err!=nil{return ""}
    sealed:=aead.Seal(nonce,nonce,data,[]byte("nvr/ha/whep/session/v1"))
    return base64.RawURLEncoding.EncodeToString(sealed)
}

func (h *Handler) openHomeAssistantSession(token string) (homeAssistantWHEPSession, error) {
    var session homeAssistantWHEPSession
    if token=="" || len(token)>8192 {return session,fmt.Errorf("invalid session")}
    ciphertext,err:=base64.RawURLEncoding.DecodeString(token)
    if err!=nil{return session,fmt.Errorf("invalid session encoding")}
    aead,err:=h.homeAssistantSessionAEAD()
    if err!=nil{return session,err}
    if len(ciphertext)<aead.NonceSize()+aead.Overhead() {
        return session,fmt.Errorf("invalid session length")
    }
    nonce,encrypted:=ciphertext[:aead.NonceSize()],ciphertext[aead.NonceSize():]
    data,err:=aead.Open(nil,nonce,encrypted,[]byte("nvr/ha/whep/session/v1"))
    if err!=nil{return session,fmt.Errorf("invalid session authentication")}
    if err=json.Unmarshal(data,&session);err!=nil{return session,err}
    if session.Expires < time.Now().Unix() {return session,fmt.Errorf("expired session")}
    return session,nil
}

func (h *Handler) homeAssistantWHEPURL(ctx context.Context, sid, app string, cleanup bool) (*url.URL, error) {
	if h.mediaEngine == nil {
		return nil, fmt.Errorf("media engine unavailable")
	}
	token := ""
	if h.config != nil {
		token = h.config.Auth.PlaybackTokens[sid]
		if len(h.config.Auth.PlaybackTokens) > 0 && token == "" && !cleanup {
			return nil, fmt.Errorf("playback token missing")
		}
	}
	play, err := h.mediaEngine.BuildPlayURL(ctx, media.PlayURLRequest{StreamID: sid, AppName: app, Protocol: "webrtc", Token: token})
	if err != nil || play == nil {
		return nil, fmt.Errorf("WHEP unavailable")
	}
	u, err := url.Parse(play.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid WHEP URL")
	}
	return u, nil
}

func (h *Handler) requestHomeAssistantWHEP(r *http.Request, method string, target *url.URL, body io.Reader, allowCallerAuth bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(r.Context(), method, target.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/sdp")
	// The configured engine is trusted, but never forward cookies or follow a
	// redirect carrying NVR account credentials to another authority.
	if allowCallerAuth && target.User == nil {
		// The embedded media HTTP surface may require the same operator
		// authorization as the NVR API even when the playback token is
		// scoped to a derived physical stream. The target was resolved from
		// the trusted configured engine and redirects are disabled below.
		req.Header.Set("Authorization", r.Header.Get("Authorization"))
	}
	client := *h.playProxyClient()
	client.Timeout = 20 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client.Do(req)
}

func (h *Handler) handleHomeAssistantWHEP(w http.ResponseWriter, r *http.Request) {
	if !h.requireHomeAssistantPublication(w, r) {
		return
	}
	if r.Header.Get("Content-Type") != "application/sdp" {
		writeError(w, 415, "Content-Type must be application/sdp")
		return
	}
	sid := streamIDFromRequest(r)
	if h.config != nil && h.config.Streaming.WebRTC.Enabled != nil && !*h.config.Streaming.WebRTC.Enabled {
		writeError(w, 503, "WebRTC is disabled")
		return
	}
	if h.mediaEngine == nil {
		writeError(w, 503, "media engine unavailable")
		return
	}
	info, err := h.mediaEngine.GetStream(r.Context(), sid)
	if err != nil || info == nil || !info.Active {
		writeError(w, 503, "published stream is offline")
		return
	}
	codec := homeAssistantWebRTCCodec(info.VideoCodec)
	cfg, err := h.db.GetHomeAssistantSettings(r.Context())
	if err != nil {
		writeError(w, 500, "failed to load publications")
		return
	}
	i := slices.IndexFunc(cfg.Streams, func(p storage.HomeAssistantPublication) bool { return p.StreamID == sid })
	if !cfg.Enabled || i < 0 {
		writeError(w, 404, "stream is not published to Home Assistant")
		return
	}
	app := info.AppName
	if app == "" {
		app = cfg.Streams[i].AppName
	}
	if app == "" {
		app = "live"
	}
	var upstream *url.URL
	var requiredAudio string
	selectedJob := cfg.Streams[i].TranscodeJobID
	if selectedJob != "" {
		derived, resolveErr := h.resolveDerivedPlayback(r.Context(), sid, selectedJob, "webrtc")
		if resolveErr != nil {
			writeDerivedPlaybackError(w, resolveErr, "derived WebRTC output unavailable")
			return
		}
		codec = homeAssistantWebRTCCodec(derived.Stream.VideoCodec)
		switch strings.ToLower(derived.Stream.AudioCodec) {
		case "g711a", "pcma": requiredAudio = "PCMA/8000"
		case "g711u", "pcmu": requiredAudio = "PCMU/8000"
		case "opus": requiredAudio = "opus/48000/2"
		default:
			writeError(w, 422, "derived WebRTC audio requires PCMA, PCMU or Opus; select a G711A output profile")
			return
		}
		if codec == "" {
			writeError(w, 422, "derived WebRTC output requires H264 or H265")
			return
		}
		upstream, err = url.Parse(derived.URL.URL)
		if err != nil || upstream.Host == "" || (upstream.Scheme != "http" && upstream.Scheme != "https") {
			writeError(w, 503, "invalid derived WebRTC playback endpoint")
			return
		}
	} else {
		if codec == "" {
			writeError(w, 400, "WebRTC requires H.264 or H.265; use HLS or RTSP for this codec")
			return
		}
		upstream, err = h.homeAssistantWHEPURL(r.Context(), sid, app, false)
		if err != nil {
			writeError(w, 503, "WebRTC playback unavailable")
			return
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || len(body) == 0 {
		writeError(w, 400, "invalid SDP offer")
		return
	}
	if !homeAssistantOfferSupportsCodec(body, codec) {
		writeError(w, 400, "WebRTC offer does not support the stream codec; use HLS or RTSP")
		return
	}
	if requiredAudio != "" && !homeAssistantOfferSupportsAudio(body, requiredAudio) {
		writeError(w, 400, "WebRTC offer must accept the derived audio codec")
		return
	}
	resp, err := h.requestHomeAssistantWHEP(r, "POST", upstream, strings.NewReader(string(body)), true)
	if err != nil {
		writeError(w, 502, "WebRTC negotiation failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		writeError(w, 502, "media engine rejected WebRTC offer")
		return
	}
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.Header.Get("Location") == "" {
		writeError(w, 502, "invalid WebRTC session location")
		return
	}
	target := upstream.ResolveReference(location)
	if !sameWHEPOrigin(upstream, target) {
		writeError(w, 502, "invalid WebRTC session origin")
		return
	}
	if selectedJob != "" {
		// Media engines commonly return a relative WHEP Location without
		// repeating the playback token. Bind session cleanup to the same
		// one-time scoped token; reject a different token in the response.
		scoped := upstream.Query().Get("token")
		if scoped == "" || (target.Query().Get("token") != "" && target.Query().Get("token") != scoped) {
			writeError(w, 502, "invalid derived WebRTC session credential")
			return
		}
		query := target.Query()
		query.Set("token", scoped)
		target.RawQuery = query.Encode()
	}
	target.User, target.Fragment = nil, ""
	delivered := false
	defer func() {
		if delivered {
			return
		}
		cleanup := *target
		cleanup.User = upstream.User
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		if response, err := h.requestHomeAssistantWHEP(r.WithContext(cleanupCtx), "DELETE", &cleanup, nil, selectedJob == ""); err == nil {
			response.Body.Close()
		}
	}()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(answer) > 1<<20 || !strings.HasPrefix(string(answer), "v=0") {
		writeError(w, 502, "invalid SDP answer")
		return
	}
	token := h.sealHomeAssistantSession(homeAssistantWHEPSession{StreamID: sid, InstanceID: cfg.InstanceID, AppName: app, URL: target.String(), Derived: selectedJob != "", Expires: time.Now().Add(24 * time.Hour).Unix()})
	if token == "" {
        writeError(w, 503, "could not secure WebRTC session")
        return
    }
	w.Header().Set("Content-Type", "application/sdp")
	w.Header().Set("Location", "/api/integrations/home-assistant/streams/"+url.PathEscape(sid)+"/webrtc/"+token)
	w.WriteHeader(201)
	_, err = w.Write(answer)
	delivered = err == nil
}

func (h *Handler) handleHomeAssistantCloseWHEP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	session, err := h.openHomeAssistantSession(chi.URLParam(r, "session"))
	if err != nil || session.StreamID != streamIDFromRequest(r) {
		writeError(w, 400, "invalid WebRTC session")
		return
	}
	cfg, err := h.db.GetHomeAssistantSettings(r.Context())
	if err != nil || cfg.InstanceID != session.InstanceID {
		writeError(w, 400, "invalid NVR instance")
		return
	}
	target, err := url.Parse(session.URL)
	if err != nil {
		writeError(w, 400, "invalid session target")
		return
	}
	base, err := h.homeAssistantWHEPURL(r.Context(), session.StreamID, session.AppName, true)
	if err != nil || !sameWHEPOrigin(base, target) {
		writeError(w, 503, "session engine unavailable")
		return
	}
	if session.Derived {
		// A signed derived session carries its own scoped media token. Never
		// substitute the original camera playback or caller credentials.
		if target.Query().Get("token") == "" {
			writeError(w, 400, "derived session has no scoped playback token")
			return
		}
		target.User = nil
	} else {
		target.User = base.User
	}
	// Cleanup remains allowed after unpublication; the signed token can only
	// delete a session minted for this instance and stream, never create one.
	// The embedded WHEP HTTP route is protected by the trusted operator
	// middleware in addition to the scoped playback token. The token binds
	// the derived stream/session; the local operator auth authorizes the
	// server-to-server DELETE without exposing the token to the caller.
	resp, err := h.requestHomeAssistantWHEP(r, "DELETE", target, nil, true)
	if err != nil {
		writeError(w, 502, "failed to close WebRTC session")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 204 && resp.StatusCode != 404 {
		writeError(w, 502, "media engine rejected session cleanup")
		return
	}
	w.WriteHeader(204)
}
