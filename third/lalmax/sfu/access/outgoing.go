package access

import (
	"context"
	"errors"
	"strings"

	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/webrtcbridge"
)

var ErrOutgoingCodecMismatch = errors.New("sfu access: outgoing track does not match publication")

type outgoingBinding struct{ cancel context.CancelFunc }

// StartOutgoing forwards an existing authorized Room subscription into a
// Pion local track. The caller must first add the track to the negotiated PC,
// validate SDP fmtp/profile and bind the track to the correct publication.
// The current subscriber session and Subscribe policy are checked again.
// External policy revocation does not automatically stop existing media.
// On stop, the binding releases its subscription, unless the caller has
// already removed and replaced it. The returned buffered channel reports exit.
func (s *Session) StartOutgoing(ctx context.Context, publicationID string, local *webrtc.TrackLocalStaticRTP) (<-chan error, error) {
	if ctx == nil || local == nil || publicationID == "" {
		return nil, ErrInvalid
	}
	var publication sfu.Publication
	for _, p := range s.room.Snapshot().Publications {
		if p.ID == publicationID {
			publication = p
			break
		}
	}
	if publication.ID == "" {
		return nil, sfu.ErrPublicationNotFound
	}
	kind := local.Kind()
	if (publication.Kind == "audio" && kind != webrtc.RTPCodecTypeAudio) || (publication.Kind == "video" && kind != webrtc.RTPCodecTypeVideo) || !strings.EqualFold(local.Codec().MimeType, publication.Codec) {
		return nil, ErrOutgoingCodecMismatch
	}
	result := make(chan error, 1)
	err := s.withPolicy(ctx, Request{Operation: Subscribe, PublicationID: publicationID, Kind: publication.Kind, Source: publication.Source, Codec: publication.Codec}, func() error {
		sub, err := s.room.OpenSubscriptionRTP(s.scope.ParticipantID, s.generation, publicationID)
		if err != nil {
			return err
		}
		bindingCtx, cancel := context.WithCancel(ctx)
		binding := &outgoingBinding{cancel: cancel}
		if s.outgoing == nil {
			s.outgoing = make(map[string]*outgoingBinding)
		}
		s.outgoing[publicationID] = binding
		go func() {
			err := webrtcbridge.ForwardOutbound(bindingCtx, sub, local)
			cancel()
			s.mu.Lock()
			if s.outgoing[publicationID] == binding {
				delete(s.outgoing, publicationID)
				_ = s.room.UnsubscribeSession(s.scope.ParticipantID, s.generation, publicationID)
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
