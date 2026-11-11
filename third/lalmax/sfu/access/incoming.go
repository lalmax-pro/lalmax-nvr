package access

import (
	"context"
	"errors"
	"strings"

	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/layerselect"
	"github.com/q191201771/lalmax/sfu/webrtcbridge"
)

var (
	ErrCodecMismatch  = errors.New("sfu access: incoming track does not match publication")
	ErrUnsupportedRID = errors.New("sfu access: unsupported simulcast RID")
)

type incomingBinding struct{ cancel context.CancelFunc }

// StartIncoming binds an authenticated session's existing publication to one
// Pion remote track. The trusted caller must map OnTrack to the right published
// ID, enforce SDP/transport policy and own the track exclusively. Policy is
// checked again at binding time, but active media is not automatically stopped
// when external authorization changes. Cancel ctx or close the session to stop.
// The buffered result reports the forwarder's termination; it is then closed.
func (s *Session) StartIncoming(ctx context.Context, publicationID string, remote *webrtc.TrackRemote, maxSubscribers, queueSize int) (<-chan error, error) {
	done, _, err := s.StartIncomingWithLifetime(ctx, publicationID, remote, maxSubscribers, queueSize)
	return done, err
}

// StartIncomingWithLifetime exposes the source binding's cancellation signal
// to trusted transport plumbing, including on unpublish and session close.
func (s *Session) StartIncomingWithLifetime(ctx context.Context, publicationID string, remote *webrtc.TrackRemote, maxSubscribers, queueSize int) (<-chan error, <-chan struct{}, error) {
	if ctx == nil || remote == nil || publicationID == "" {
		return nil, nil, ErrInvalid
	}
	var publication sfu.Publication
	for _, p := range s.room.Snapshot().Publications {
		if p.ID == publicationID && p.ParticipantID == s.scope.ParticipantID {
			publication = p
			break
		}
	}
	if publication.ID == "" {
		return nil, nil, sfu.ErrPublicationNotFound
	}
	kind := remote.Kind()
	codec := remote.Codec()
	if (publication.Kind == "audio" && kind != webrtc.RTPCodecTypeAudio) || (publication.Kind == "video" && kind != webrtc.RTPCodecTypeVideo) || !strings.EqualFold(codec.MimeType, publication.Codec) {
		return nil, nil, ErrCodecMismatch
	}
	result := make(chan error, 1)
	var stopped <-chan struct{}
	err := s.withPolicy(ctx, Request{Operation: Publish, PublicationID: publicationID, Kind: publication.Kind, Source: publication.Source, Codec: publication.Codec}, func() error {
		negotiated := sfu.NegotiatedCodec{MimeType: codec.MimeType, ClockRate: codec.ClockRate, Channels: codec.Channels, FMTP: codec.SDPFmtpLine}
		rid := remote.RID()
		var err error
		if rid == "" {
			err = s.room.EnablePublicationRTPWithCodec(s.scope.ParticipantID, s.generation, publicationID, maxSubscribers, queueSize, negotiated)
		} else {
			maxBitrate, ok := layerselect.PresetMaxBitrate(rid)
			if !ok {
				return ErrUnsupportedRID
			}
			err = s.room.EnablePublicationLayerRTPWithCodec(s.scope.ParticipantID, s.generation, publicationID, maxSubscribers, queueSize, negotiated, rid, maxBitrate)
		}
		if err != nil {
			return err
		}
		bindingCtx, cancel := context.WithCancel(ctx)
		stopped = bindingCtx.Done()
		binding := &incomingBinding{cancel: cancel}
		if s.incoming == nil {
			s.incoming = make(map[string]*incomingBinding)
		}
		bindingKey := incomingLayerKey(publicationID, rid)
		s.incoming[bindingKey] = binding
		go func() {
			err := webrtcbridge.ForwardInboundWithCodec(bindingCtx, remote, s.room, s.scope.ParticipantID, s.generation, publicationID, codec)
			cancel()
			s.mu.Lock()
			if s.incoming[bindingKey] == binding {
				delete(s.incoming, bindingKey)
				ended, removeErr := s.room.RemovePublicationLayer(s.scope.ParticipantID, s.generation, publicationID, rid)
				if removeErr == nil && ended {
					s.forgetPublicationMappingLocked(publicationID)
				}
			}
			s.mu.Unlock()
			result <- err
			close(result)
		}()
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return result, stopped, nil
}

func incomingLayerKey(publicationID, rid string) string { return publicationID + "\x00" + rid }
