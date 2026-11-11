package access

import (
	"context"
	"errors"
	"strings"

	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
)

var (
	ErrTrackIDInUse   = errors.New("sfu access: track ID already mapped")
	ErrTrackNotMapped = errors.New("sfu access: incoming track is not mapped")
)

// PublishMapped atomically publishes and binds a client-declared WebRTC track
// ID within this participant session. Simulcast RIDs sharing a WebRTC track ID
// map to this publication and remain distinct media layers. IDs are unique per
// session, not global. This does not authenticate the client or validate SDP.
func (s *Session) PublishMapped(ctx context.Context, kind, source, codec, trackID string) (sfu.Publication, error) {
	if trackID == "" || len(trackID) > 256 || strings.TrimSpace(trackID) != trackID || strings.ContainsAny(trackID, "\r\n\x00") {
		return sfu.Publication{}, ErrInvalid
	}
	var publication sfu.Publication
	err := s.withPolicy(ctx, Request{Operation: Publish, Kind: kind, Source: source, Codec: codec}, func() error {
		if _, exists := s.trackMappings[trackID]; exists {
			return ErrTrackIDInUse
		}
		p, err := s.room.PublishSession(s.scope.ParticipantID, s.generation, kind, source, codec)
		if err != nil {
			return err
		}
		if s.trackMappings == nil {
			s.trackMappings = make(map[string]string)
		}
		s.trackMappings[trackID] = p.ID
		publication = p
		return nil
	})
	return publication, err
}

// StartMappedIncoming resolves a TrackRemote ID within this session and calls
// StartIncoming, which rechecks authorization, session generation and codec.
// A missing or blank TrackRemote ID is rejected rather than guessed.
func (s *Session) StartMappedIncoming(ctx context.Context, remote *webrtc.TrackRemote, maxSubscribers, queueSize int) (<-chan error, error) {
	_, done, _, err := s.StartMappedIncomingWithPublication(ctx, remote, maxSubscribers, queueSize)
	return done, err
}

// StartMappedIncomingWithPublication also returns the server-assigned publication
// ID and binding cancellation signal. It is intended for trusted media plumbing.
func (s *Session) StartMappedIncomingWithPublication(ctx context.Context, remote *webrtc.TrackRemote, maxSubscribers, queueSize int) (string, <-chan error, <-chan struct{}, error) {
	if ctx == nil || remote == nil {
		return "", nil, nil, ErrInvalid
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", nil, nil, ErrClosed
	}
	id, ok := s.trackMappings[remote.ID()]
	s.mu.Unlock()
	if !ok {
		return "", nil, nil, ErrTrackNotMapped
	}
	done, stopped, err := s.StartIncomingWithLifetime(ctx, id, remote, maxSubscribers, queueSize)
	if err != nil {
		return "", nil, nil, err
	}
	return id, done, stopped, nil
}

// forgetPublicationMappingLocked is called with s.mu held.
func (s *Session) forgetPublicationMappingLocked(id string) {
	for trackID, pubID := range s.trackMappings {
		if pubID == id {
			delete(s.trackMappings, trackID)
		}
	}
}
