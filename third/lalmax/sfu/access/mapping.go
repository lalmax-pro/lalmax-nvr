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
// ID within this participant session. IDs are unique per session, not global.
// This does not authenticate the client, validate SDP or support Simulcast.
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
	if ctx == nil || remote == nil {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	id, ok := s.trackMappings[remote.ID()]
	s.mu.Unlock()
	if !ok {
		return nil, ErrTrackNotMapped
	}
	return s.StartIncoming(ctx, id, remote, maxSubscribers, queueSize)
}

// forgetPublicationMappingLocked is called with s.mu held.
func (s *Session) forgetPublicationMappingLocked(id string) {
	for trackID, pubID := range s.trackMappings {
		if pubID == id {
			delete(s.trackMappings, trackID)
		}
	}
}
