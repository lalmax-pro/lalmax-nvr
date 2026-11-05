// Package signaling provides an opt-in WebSocket handler for SFU rooms.
package signaling

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
)

// Identity must come from trusted authentication, never directly from JSON.
type Identity struct {
	Principal   string
	Participant sfu.Participant
	Room        sfu.Key
}
type Authenticate func(*http.Request) (Identity, error)

// Filter must authorize visibility and return only data this principal may see.
// It is called for the initial snapshot and every subsequent update.
type Filter func(context.Context, Identity, sfu.Snapshot) (sfu.Snapshot, error)

// Negotiator is owned by one WebSocket session and must be closed on exit.
// Implementations are trusted to enforce the server media policy.
type Negotiator interface {
	Answer(context.Context, string) (string, error)
	Close() error
}
type NegotiatorFactory func(context.Context, Identity) (Negotiator, error)

// SessionNegotiatorFactory may bind media through the authorized session.
type SessionNegotiatorFactory func(context.Context, Identity, *access.Session) (Negotiator, error)

type Handler struct {
	Rooms                *sfu.Manager
	Policy               access.Authorizer
	Authenticate         Authenticate
	Filter               Filter
	CheckOrigin          func(*http.Request) bool
	NewNegotiator        NegotiatorFactory
	NewSessionNegotiator SessionNegotiatorFactory
}
type command struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	Kind          string `json:"kind"`
	Source        string `json:"source"`
	Codec         string `json:"codec"`
	PublicationID string `json:"publication_id"`
	TrackID       string `json:"track_id"`
	SDP           string `json:"sdp"`
}
type response struct {
	Type        string           `json:"type"`
	ID          string           `json:"id,omitempty"`
	Error       string           `json:"error,omitempty"`
	Publication *sfu.Publication `json:"publication,omitempty"`
	Snapshot    *sfu.Snapshot    `json:"snapshot,omitempty"`
	SDP         string           `json:"sdp,omitempty"`
}

// ServeHTTP requires explicit authentication, authorization, visibility filter
// and origin policy. It never creates rooms. SDP negotiation is opt-in.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Rooms == nil || h.Policy == nil || h.Authenticate == nil || h.Filter == nil || h.CheckOrigin == nil {
		http.Error(w, "signaling unavailable", http.StatusServiceUnavailable)
		return
	}
	if !h.CheckOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	identity, err := h.Authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	room, ok := h.Rooms.Get(identity.Room)
	if !ok {
		http.Error(w, "room not found", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	session, err := access.JoinRoom(ctx, room, h.Policy, identity.Principal, identity.Participant)
	if err != nil {
		http.Error(w, "join rejected", http.StatusForbidden)
		return
	}
	defer session.Close()
	snapshot, events, stop, err := room.Watch(32)
	if err != nil {
		http.Error(w, "room unavailable", http.StatusServiceUnavailable)
		return
	}
	defer stop()
	initial, err := h.Filter(ctx, identity, snapshot)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// Sequence and generation belong to Room, never to the filter.
	initial.Sequence = snapshot.Sequence
	initial.Generation = snapshot.Generation
	initial.Key = snapshot.Key
	upgrader := websocket.Upgrader{CheckOrigin: h.CheckOrigin}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(131072)
	var negotiator Negotiator
	defer func() {
		if negotiator != nil {
			_ = negotiator.Close()
		}
	}()
	responses := make(chan response, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer conn.Close()
		defer cancel()
		write := func(v response) error {
			if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return err
			}
			return conn.WriteJSON(v)
		}
		if write(response{Type: "snapshot", Snapshot: &initial}) != nil {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case reply := <-responses:
				if write(reply) != nil {
					return
				}
			case event, open := <-events:
				if !open {
					return
				}
				visible, err := h.Filter(ctx, identity, event.Snapshot)
				if err != nil {
					return
				}
				visible.Sequence = event.Snapshot.Sequence
				visible.Generation = event.Snapshot.Generation
				visible.Key = event.Snapshot.Key
				if write(response{Type: "snapshot", Snapshot: &visible}) != nil {
					return
				}
			}
		}
	}()
	for {
		var c command
		if err := conn.ReadJSON(&c); err != nil {
			return
		}
		reply := response{Type: "result", ID: c.ID}
		if c.ID == "" || len(c.ID) > 128 {
			reply.Error = "invalid_request"
		} else {
			switch c.Type {
			case "offer":
				if h.NewNegotiator == nil && h.NewSessionNegotiator == nil {
					reply.Error = "unsupported_command"
					break
				}
				if c.SDP == "" || len(c.SDP) > 65536 {
					reply.Error = "invalid_request"
					break
				}
				if negotiator == nil {
					if h.NewSessionNegotiator != nil {
						negotiator, err = h.NewSessionNegotiator(ctx, identity, session)
					} else {
						negotiator, err = h.NewNegotiator(ctx, identity)
					}
					if err == nil && negotiator == nil {
						err = errors.New("nil negotiator")
					}
				}
				if err == nil {
					offerCtx, stop := context.WithTimeout(ctx, 10*time.Second)
					reply.SDP, err = negotiator.Answer(offerCtx, c.SDP)
					stop()
				}
			case "publish":
				var p sfu.Publication
				var e error
				if c.TrackID != "" {
					p, e = session.PublishMapped(ctx, c.Kind, c.Source, c.Codec, c.TrackID)
				} else {
					p, e = session.Publish(ctx, c.Kind, c.Source, c.Codec)
				}
				err = e
				if err == nil {
					reply.Publication = &p
				}
			case "unpublish":
				err = session.Unpublish(ctx, c.PublicationID)
			case "subscribe":
				err = session.Subscribe(ctx, c.PublicationID)
			case "unsubscribe":
				err = session.Unsubscribe(ctx, c.PublicationID)
			default:
				reply.Error = "unsupported_command"
			}
			if err != nil {
				if errors.Is(err, access.ErrDenied) {
					reply.Error = "forbidden"
				} else {
					reply.Error = "operation_failed"
				}
				err = nil
			}
		}
		select {
		case responses <- reply:
		case <-done:
			return
		case <-ctx.Done():
			return
		}
	}
}
