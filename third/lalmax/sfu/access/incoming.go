package access

import (
	"context"
	"errors"
	"strings"

	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/webrtcbridge"
)

var ErrCodecMismatch = errors.New("sfu access: incoming track does not match publication")

type incomingBinding struct{ cancel context.CancelFunc }

// StartIncoming binds an authenticated session's existing publication to one
// Pion remote track. The trusted caller must map OnTrack to the right published
// ID, enforce SDP/transport policy and own the track exclusively. Policy is
// checked again at binding time, but active media is not automatically stopped
// when external authorization changes. Cancel ctx or close the session to stop.
// The buffered result reports the forwarder's termination; it is then closed.
func (s *Session) StartIncoming(ctx context.Context, publicationID string, remote *webrtc.TrackRemote, maxSubscribers, queueSize int) (<-chan error, error) {
	if ctx == nil || remote == nil || publicationID == "" {
		return nil, ErrInvalid
	}
	var publication sfu.Publication
	for _, p := range s.room.Snapshot().Publications {
		if p.ID == publicationID && p.ParticipantID == s.scope.ParticipantID {
			publication = p
			break
		}
	}
	if publication.ID == "" {
		return nil, sfu.ErrPublicationNotFound
	}
	kind := remote.Kind()
	if (publication.Kind == "audio" && kind != webrtc.RTPCodecTypeAudio) || (publication.Kind == "video" && kind != webrtc.RTPCodecTypeVideo) || !strings.EqualFold(remote.Codec().MimeType, publication.Codec) {
		return nil, ErrCodecMismatch
	}
	result := make(chan error, 1)
	err := s.withPolicy(ctx, Request{Operation: Publish, PublicationID: publicationID, Kind: publication.Kind, Source: publication.Source, Codec: publication.Codec}, func() error {
		if err := s.room.EnablePublicationRTP(s.scope.ParticipantID, s.generation, publicationID, maxSubscribers, queueSize); err != nil {
			return err
		}
		bindingCtx, cancel := context.WithCancel(ctx)
		binding := &incomingBinding{cancel: cancel}
		if s.incoming == nil {
			s.incoming = make(map[string]*incomingBinding)
		}
		s.incoming[publicationID] = binding
		go func() {
			err := webrtcbridge.ForwardInbound(bindingCtx, remote, s.room, s.scope.ParticipantID, s.generation, publicationID)
			cancel()
			s.mu.Lock()
			if s.incoming[publicationID] == binding {
				delete(s.incoming, publicationID)
				_ = s.room.UnpublishSession(s.scope.ParticipantID, s.generation, publicationID)
			}
			s.mu.Unlock()
			result <- err
			close(result)
		}()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
