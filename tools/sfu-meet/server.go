package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
	"github.com/q191201771/lalmax/sfu/signaling"
)

const cookieName = "sfu_demo_session"

var demoRoom = sfu.Key{Namespace: "local", ID: "demo"}

type localPolicy struct{}

func (localPolicy) Authorize(_ context.Context, r access.Request) error {
	if r.Room != demoRoom || r.Principal == "" || r.ParticipantID == "" {
		return errors.New("unauthorized")
	}
	switch r.Operation {
	case access.Join, access.Subscribe, access.Unsubscribe, access.Unpublish:
		return nil
	case access.Publish:
		if r.Kind == "audio" && r.Source == "microphone" && strings.EqualFold(r.Codec, "audio/opus") {
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
	_, err = rooms.CreateWithLimits(demoRoom, sfu.RoomLimits{MaxParticipants: 8, MaxPublicationsPerParticipant: 2, MaxSubscriptions: 48, MaxWatchers: 8})
	if err != nil {
		return nil, err
	}
	sameOrigin := func(r *http.Request) bool { return r.Host == u.Host && r.Header.Get("Origin") == origin }
	factory := &signaling.RoomFactory{
		NewPeer: func(_ context.Context, _ signaling.Identity) (*webrtc.PeerConnection, error) {
			engine := webrtc.SettingEngine{}
			engine.SetIncludeLoopbackCandidate(true)
			return webrtc.NewAPI(webrtc.WithSettingEngine(engine)).NewPeerConnection(webrtc.Configuration{})
		},
		Codec: func(p sfu.Publication) (webrtc.RTPCodecCapability, error) {
			if p.Kind != "audio" || !strings.EqualFold(p.Codec, "audio/opus") {
				return webrtc.RTPCodecCapability{}, errors.New("unsupported codec")
			}
			return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, nil
		}, MaxSubscribers: 8, QueueSize: 64,
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
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "SFU local demo signaling is running. Web UI is not included yet.")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != u.Host {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}
