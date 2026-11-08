package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gofrs/uuid"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/sfubridge"
	"github.com/pion/sdp/v3"
)

func sfuOfferSupportsOpus(offer []byte) bool {
	return sfuSDPSupports(offer, "audio", "opus/48000/2", false)
}

func sfuSDPSupports(offer []byte, kind, codec string, answer bool) bool {
	var description sdp.SessionDescription
	if description.Unmarshal(offer) != nil {
		return false
	}
	for _, media := range description.MediaDescriptions {
		if media.MediaName.Media != kind || media.MediaName.Port.Value == 0 {
			continue
		}
		if _, inactive := media.Attribute("inactive"); inactive {
			continue
		}
		if _, sendonly := media.Attribute("sendonly"); sendonly && !answer {
			continue
		}
		if _, recvonly := media.Attribute("recvonly"); recvonly && answer {
			continue
		}
		for _, a := range media.Attributes {
			fields := strings.Fields(a.Value)
			if a.Key != "rtpmap" || len(fields) != 2 || !strings.EqualFold(fields[1], codec) {
				continue
			}
			for _, format := range media.MediaName.Formats {
				if format == fields[0] {
					return true
				}
			}
		}
	}
	return false
}

type sfuWHEPSession struct {
	Principal string `json:"p"`
	BridgeID  string `json:"b"`
	StreamID  string `json:"s"`
	ID        string `json:"i"`
	Expires   int64  `json:"e"`
}

func (h *Handler) sealSFUWHEPSession(session sfuWHEPSession) string {
	data, _ := json.Marshal(session)
	mac := hmac.New(sha256.New, h.sfuWHEPKey)
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (h *Handler) openSFUWHEPSession(token string) (sfuWHEPSession, error) {
	var session sfuWHEPSession
	parts := strings.Split(token, ".")
	if len(token) > 4096 || len(parts) != 2 {
		return session, errors.New("invalid session")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return session, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return session, err
	}
	mac := hmac.New(sha256.New, h.sfuWHEPKey)
	mac.Write(data)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return session, errors.New("invalid signature")
	}
	if err = json.Unmarshal(data, &session); err != nil {
		return session, err
	}
	if session.Expires < time.Now().Unix() {
		return session, errors.New("expired session")
	}
	if _, err = uuid.FromString(session.ID); err != nil {
		return session, errors.New("invalid session ID")
	}
	return session, nil
}
func (h *Handler) sfuWHEPURL(ctx context.Context, stream, token string) (*url.URL, error) {
	if h.mediaEngine == nil {
		return nil, errors.New("media engine unavailable")
	}
	play, err := h.mediaEngine.BuildPlayURL(ctx, media.PlayURLRequest{StreamID: stream, AppName: "live", Protocol: "webrtc", Token: token})
	if err != nil || play == nil {
		return nil, errors.New("playback unavailable")
	}
	target, err := url.Parse(play.URL)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") || target.Path != "/webrtc/whep" || target.Fragment != "" {
		return nil, errors.New("invalid media target")
	}
	q := target.Query()
	q.Set("streamid", stream)
	q.Set("app_name", "live")
	q.Set("token", token)
	target.RawQuery = q.Encode()
	return target, nil
}
func (h *Handler) createSFUBridgeWHEP(w http.ResponseWriter, r *http.Request) {
	registry, principal, ok := h.sfuBridgeRequest(w, r)
	if !ok {
		return
	}
	if r.Header.Get("Content-Type") != "application/sdp" {
		writeError(w, 415, "Content-Type must be application/sdp")
		return
	}
	bridgeID := chi.URLParam(r, "id")
	stream, token, err := registry.Playback(r.Context(), principal, bridgeID)
	if err != nil {
		sfuBridgeError(w, err)
		return
	}
	status, err := registry.Get(r.Context(), principal, bridgeID)
	if err != nil {
		sfuBridgeError(w, err)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || len(body) == 0 || (!homeAssistantOfferSupportsCodec(body, "H264") && !sfuOfferSupportsOpus(body)) {
		writeError(w, 400, "invalid H.264 or Opus SDP offer")
		return
	}
	if status.AudioPublicationID != "" && (!homeAssistantOfferSupportsCodec(body, "H264") || !sfuOfferSupportsOpus(body)) {
		writeError(w, 400, "combined playback requires H.264 and Opus")
		return
	}
	upstream, err := h.sfuWHEPURL(r.Context(), stream, token)
	if err != nil {
		writeError(w, 503, "WebRTC playback unavailable")
		return
	}
	resp, err := h.requestHomeAssistantWHEP(r, http.MethodPost, upstream, strings.NewReader(string(body)))
	if err != nil {
		writeError(w, 502, "WebRTC negotiation failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		if resp.StatusCode == http.StatusNotFound {
			writeError(w, 503, "bridge media not ready")
		} else {
			writeError(w, 502, "media engine rejected WebRTC offer")
		}
		return
	}
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.Header.Get("Location") == "" {
		writeError(w, 502, "invalid WebRTC session")
		return
	}
	target := upstream.ResolveReference(location)
	prefix := "/webrtc/whep/"
	id := strings.TrimPrefix(target.Path, prefix)
	if !sameWHEPOrigin(upstream, target) || !strings.HasPrefix(target.Path, prefix) || strings.Contains(id, "/") || target.Fragment != "" {
		writeError(w, 502, "invalid WebRTC session")
		return
	}
	if _, err = uuid.FromString(id); err != nil {
		writeError(w, 502, "invalid WebRTC session")
		return
	}
	target.User = upstream.User
	target.RawQuery = url.Values{"token": []string{token}}.Encode()
	delivered := false
	defer func() {
		if delivered {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		if response, err := h.requestHomeAssistantWHEP(r.WithContext(cleanup), http.MethodDelete, target, nil); err == nil {
			response.Body.Close()
		}
	}()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(answer) > 1<<20 || !strings.HasPrefix(string(answer), "v=0") {
		writeError(w, 502, "invalid SDP answer")
		return
	}
	if status.AudioPublicationID != "" && (!sfuSDPSupports(answer, "video", "H264/90000", true) || !sfuSDPSupports(answer, "audio", "opus/48000/2", true)) {
		writeError(w, 503, "combined bridge media not ready")
		return
	}
	session := h.sealSFUWHEPSession(sfuWHEPSession{Principal: principal, BridgeID: bridgeID, StreamID: stream, ID: id, Expires: time.Now().Add(24 * time.Hour).Unix()})
	w.Header().Set("Content-Type", "application/sdp")
	w.Header().Set("Location", "/api/sfu/bridges/"+url.PathEscape(bridgeID)+"/whep/"+session)
	w.WriteHeader(http.StatusCreated)
	_, err = w.Write(answer)
	delivered = err == nil
}
func (h *Handler) deleteSFUBridgeWHEP(w http.ResponseWriter, r *http.Request) {
	registry, principal, ok := h.sfuBridgeRequest(w, r)
	if !ok {
		return
	}
	bridgeID := chi.URLParam(r, "id")
	session, err := h.openSFUWHEPSession(chi.URLParam(r, "session"))
	if err != nil || session.BridgeID != bridgeID || session.Principal != principal {
		writeError(w, 404, "WebRTC session not found")
		return
	}
	stream, token, err := registry.Playback(r.Context(), principal, bridgeID)
	if errors.Is(err, sfubridge.ErrNotFound) {
		if _, getErr := registry.Get(r.Context(), principal, bridgeID); getErr == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	if err != nil {
		sfuBridgeError(w, err)
		return
	}
	if stream != session.StreamID {
		writeError(w, 404, "WebRTC session not found")
		return
	}
	target, err := h.sfuWHEPURL(r.Context(), stream, token)
	if err != nil {
		writeError(w, 503, "WebRTC playback unavailable")
		return
	}
	target.Path = fmt.Sprintf("/webrtc/whep/%s", session.ID)
	target.RawQuery = url.Values{"token": []string{token}}.Encode()
	resp, err := h.requestHomeAssistantWHEP(r, http.MethodDelete, target, nil)
	if err != nil {
		writeError(w, 502, "failed to close WebRTC session")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 204 && resp.StatusCode != 404 {
		writeError(w, 502, "media engine rejected session cleanup")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
