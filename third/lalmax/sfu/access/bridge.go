package access

import (
	"context"
	"errors"
	"strings"

	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/bridge"
)

var ErrBridgeCodec = errors.New("sfu access: unsupported bridge track codec")

type bridgeBinding struct{ cancel context.CancelFunc }

// StartH264Bridge binds an existing subscription to a lal publisher. The
// authenticated principal must be authorized both to subscribe and to manage
// this specific target. The caller must validate the actual negotiated H264
// profile, packetization mode and no-B-frame constraint. Use a dedicated system
// participant: a subscription can have only one RTP consumer. The supplied
// context controls the binding lifetime. Startup and runtime errors are sent
// on the returned channel; a successful return only means the binding was queued.
// External policy revocation requires the embedding service to stop the binding.
func (s *Session) StartH264Bridge(ctx context.Context, publicationID, target string, publisher bridge.Publisher) (<-chan error, error) {
	return s.StartH264BridgeWithContexts(ctx, ctx, publicationID, target, publisher)
}

// StartH264BridgeWithContexts separates the short-lived authorization request
// from the bridge lifetime. Both contexts must be valid during setup. The
// caller must cancel lifetimeCtx on shutdown or permission revocation.
func (s *Session) StartH264BridgeWithContexts(authCtx, lifetimeCtx context.Context, publicationID, target string, publisher bridge.Publisher) (<-chan error, error) {
	return s.startBridge(authCtx, lifetimeCtx, publicationID, target, publisher, "video", "video/H264")
}

// StartOpusBridgeWithContexts binds an authorized Opus audio publication to
// its own Group. The caller must validate the negotiated codec and clock.
func (s *Session) StartOpusBridgeWithContexts(authCtx, lifetimeCtx context.Context, publicationID, target string, publisher bridge.Publisher) (<-chan error, error) {
	return s.startBridge(authCtx, lifetimeCtx, publicationID, target, publisher, "audio", "audio/opus")
}

func (s *Session) startBridge(authCtx, lifetimeCtx context.Context, publicationID, target string, publisher bridge.Publisher, kind, codec string) (<-chan error, error) {
	if authCtx == nil || lifetimeCtx == nil || publicationID == "" || target == "" || publisher == nil {
		return nil, ErrInvalid
	}
	if err := lifetimeCtx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	snapshot, err := s.room.SnapshotSession(s.scope.ParticipantID, s.generation)
	if err != nil {
		return nil, err
	}
	var publication sfu.Publication
	for _, p := range snapshot.Publications {
		if p.ID == publicationID {
			publication = p
			break
		}
	}
	if publication.ID == "" {
		return nil, sfu.ErrPublicationNotFound
	}
	if publication.Kind != kind || !strings.EqualFold(publication.Codec, codec) {
		return nil, ErrBridgeCodec
	}
	req := s.scope
	req.PublicationID, req.Kind, req.Source, req.Codec, req.TargetGroup = publication.ID, publication.Kind, publication.Source, publication.Codec, target
	req.Operation = Subscribe
	if err = authorize(authCtx, s.policy, req); err != nil {
		return nil, err
	}
	req.Operation = ManageBridge
	if err = authorize(authCtx, s.policy, req); err != nil {
		return nil, err
	}
	if err = lifetimeCtx.Err(); err != nil {
		return nil, err
	}
	if s.bridges[publicationID] != nil {
		return nil, errors.New("sfu access: bridge already running")
	}
	sub, err := s.room.OpenSubscriptionRTP(s.scope.ParticipantID, s.generation, publicationID)
	if err != nil {
		return nil, err
	}
	bindingCtx, cancel := context.WithCancel(lifetimeCtx)
	binding := &bridgeBinding{cancel: cancel}
	if s.bridges == nil {
		s.bridges = make(map[string]*bridgeBinding)
	}
	s.bridges[publicationID] = binding
	result := make(chan error, 1)
	go func() {
		var err error
		if kind == "audio" {
			err = bridge.RunOpusToLal(bindingCtx, sub, publisher, target)
		} else {
			err = bridge.RunH264ToLal(bindingCtx, sub, publisher, target)
		}
		cancel()
		s.mu.Lock()
		if s.bridges[publicationID] == binding {
			delete(s.bridges, publicationID)
		}
		s.mu.Unlock()
		result <- err
		close(result)
	}()
	return result, nil
}
