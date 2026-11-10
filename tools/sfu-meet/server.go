package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/sfumeetui"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
	"github.com/q191201771/lalmax/sfu/signaling"
	"github.com/q191201771/lalmax/sfu/webrtcbridge"
)

const cookieName = "sfu_demo_session"

var webAssets = sfumeetui.Assets

var demoRoom = sfu.Key{Namespace: "local", ID: "demo"}

func demoVideoFeedback() []webrtc.RTCPFeedback {
	return []webrtc.RTCPFeedback{{Type: "nack", Parameter: "pli"}, {Type: "ccm", Parameter: "fir"}}
}

type localPolicy struct{}

func (localPolicy) Authorize(_ context.Context, r access.Request) error {
	if r.Room != demoRoom || r.Principal == "" || r.ParticipantID == "" {
		return errors.New("unauthorized")
	}
	switch r.Operation {
	case access.Join, access.Subscribe, access.Unsubscribe, access.Unpublish:
		return nil
	case access.Publish:
		if r.Kind == "audio" && (r.Source == "microphone" || r.Source == "screen") && strings.EqualFold(r.Codec, "audio/opus") {
			return nil
		}
		if r.Kind == "video" && (r.Source == "camera" || r.Source == "screen") && strings.EqualFold(r.Codec, "video/h264") {
			return nil
		}
	}
	return errors.New("unauthorized")
}
func randomID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func equalSecret(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// newDemoHandler only serves one fixed loopback origin. It is a development
// tool and intentionally has no production authentication or persistent rooms.
func newDemoHandler(origin, secret string) (http.Handler, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid demo origin")
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || host != "127.0.0.1" || port == "" || len(secret) < 32 {
		return nil, errors.New("demo requires loopback origin and secret")
	}
	rooms := sfu.NewManager()
	_, err = rooms.CreateWithLimits(demoRoom, sfu.RoomLimits{MaxParticipants: 8, MaxPublicationsPerParticipant: 4, MaxSubscriptions: 48, MaxWatchers: 8})
	if err != nil {
		return nil, err
	}
	sameOrigin := func(r *http.Request) bool { return r.Host == u.Host && r.Header.Get("Origin") == origin }
	factory := &signaling.RoomFactory{
		NewPeer: func(_ context.Context, _ signaling.Identity) (*webrtc.PeerConnection, error) {
			engine := webrtc.SettingEngine{}
			engine.SetIncludeLoopbackCandidate(true)
			media := &webrtc.MediaEngine{}
			if err := media.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, PayloadType: 111}, webrtc.RTPCodecTypeAudio); err != nil {
				return nil, err
			}
			if err := media.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", RTCPFeedback: demoVideoFeedback()}, PayloadType: 96}, webrtc.RTPCodecTypeVideo); err != nil {
				return nil, err
			}
			registry := &interceptor.Registry{}
			if err := webrtc.ConfigureTWCCHeaderExtensionSender(media, registry); err != nil {
				return nil, err
			}
			// NACK may resend one cached header concurrently; isolate it before TWCC mutates it.
			registry.Add(webrtcbridge.CopyRTPHeaderFactory{})
			if err := webrtc.RegisterDefaultInterceptors(media, registry); err != nil {
				return nil, err
			}
			return webrtc.NewAPI(webrtc.WithSettingEngine(engine), webrtc.WithMediaEngine(media), webrtc.WithInterceptorRegistry(registry)).NewPeerConnection(webrtc.Configuration{})
		},
		Codec: func(p sfu.Publication) (webrtc.RTPCodecCapability, error) {
			if p.Kind == "audio" && strings.EqualFold(p.Codec, "audio/opus") {
				return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, nil
			}
			if p.Kind == "video" && strings.EqualFold(p.Codec, "video/h264") {
				return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", RTCPFeedback: demoVideoFeedback()}, nil
			}
			return webrtc.RTPCodecCapability{}, errors.New("unsupported codec")
		}, MaxSubscribers: 8, QueueSize: 64,
		OnInboundPeer: func(ctx context.Context, _ signaling.Identity, pc *webrtc.PeerConnection, track *webrtc.TrackRemote, done <-chan error, err error) {
			if err != nil || track.Kind() != webrtc.RTPCodecTypeVideo {
				return
			}
			// Retain a periodic fallback for late subscribers and keyframe loss.
			go func() {
				ticker := time.NewTicker(2 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-done:
						return
					case <-ticker.C:
						if pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(track.SSRC())}}) != nil {
							return
						}
					}
				}
			}()
		},
	}
	ws := &signaling.Handler{Rooms: rooms, Policy: localPolicy{}, CheckOrigin: sameOrigin, NewSessionNegotiator: factory.NewNegotiator,
		Authenticate: func(r *http.Request) (signaling.Identity, error) {
			cookie, err := r.Cookie(cookieName)
			if err != nil || !equalSecret(cookie.Value, secret) {
				return signaling.Identity{}, errors.New("unauthorized")
			}
			id, err := randomID()
			if err != nil {
				return signaling.Identity{}, err
			}
			return signaling.Identity{Principal: id, Participant: sfu.Participant{ID: id}, Room: demoRoom}, nil
		},
		Filter: func(_ context.Context, _ signaling.Identity, s sfu.Snapshot) (sfu.Snapshot, error) { return s, nil },
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !sameOrigin(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil || len(body.Token) > 128 || !equalSecret(body.Token, secret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var trailing any
		if err := dec.Decode(&trailing); err != io.EOF {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: secret, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/ws", ws)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		names := map[string]string{"/": "web/index.html", "/app.js": "web/app.js", "/style.css": "web/style.css"}
		name, ok := names[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		data, err := webAssets.ReadFile(name)
		if err != nil {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		if name == "web/app.js" {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		}
		if name == "web/style.css" {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws:; media-src 'self' blob:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != u.Host {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}
