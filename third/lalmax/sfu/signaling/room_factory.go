package signaling

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
)

// RoomFactory assembles a Pion negotiator from the session's current authorized
// subscriptions. It only supports tracks selected before the first offer.
// NewPeer and Codec are mandatory trusted callbacks; they must enforce network,
// codec profile/fmtp and media policy. This factory does not authenticate users.
type RoomFactory struct {
	NewPeer        func(context.Context, Identity) (*webrtc.PeerConnection, error)
	Codec          func(sfu.Publication) (webrtc.RTPCodecCapability, error)
	MaxSubscribers int
	QueueSize      int
	// OnInbound reports whether an incoming track was bound; implementations must
	// not block. No client response or permission decision is made by this hook.
	OnInbound func(Identity, *webrtc.TrackRemote, error)
}

func (f *RoomFactory) NewNegotiator(ctx context.Context, id Identity, session *access.Session) (Negotiator, error) {
	if ctx == nil || session == nil || f == nil || f.NewPeer == nil || f.Codec == nil || f.MaxSubscribers < 1 || f.MaxSubscribers > 1024 || f.QueueSize < 1 || f.QueueSize > 256 {
		return nil, errors.New("signaling: invalid room factory configuration")
	}
	pubs, err := session.SubscribedPublications(ctx)
	if err != nil {
		return nil, err
	}
	pc, err := f.NewPeer(ctx, id)
	if err != nil {
		return nil, err
	}
	if pc == nil {
		return nil, errors.New("signaling: peer factory returned nil")
	}
	bindCtx, cancel := context.WithCancel(ctx)
	success := false
	defer func() {
		if !success {
			cancel()
			_ = pc.Close()
		}
	}()
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		_, err := session.StartMappedIncoming(bindCtx, track, f.MaxSubscribers, f.QueueSize)
		if f.OnInbound != nil {
			f.OnInbound(id, track, err)
		}
	})
	for _, p := range pubs {
		capability, err := f.Codec(p)
		if err != nil {
			return nil, err
		}
		if capability.MimeType == "" || !strings.EqualFold(capability.MimeType, p.Codec) {
			return nil, errors.New("signaling: publication codec mismatch")
		}
		local, err := webrtc.NewTrackLocalStaticRTP(capability, p.ID, "sfu")
		if err != nil {
			return nil, err
		}
		sender, err := pc.AddTrack(local)
		if err != nil {
			return nil, err
		}
		// Drain RTCP so Pion interceptors can process feedback. Mapping keyframe
		// feedback upstream and sender lifecycle management are future integration.
		go func() {
			for {
				if _, _, err := sender.ReadRTCP(); err != nil {
					return
				}
			}
		}()
		if _, err = session.StartOutgoing(bindCtx, p.ID, local); err != nil {
			return nil, fmt.Errorf("bind outgoing track: %w", err)
		}
	}
	negotiator, err := NewPionNegotiator(pc)
	if err != nil {
		return nil, err
	}
	success = true
	return &roomNegotiator{Negotiator: negotiator, cancel: cancel}, nil
}

type roomNegotiator struct {
	Negotiator
	cancel context.CancelFunc
}

func (n *roomNegotiator) Close() error { n.cancel(); return n.Negotiator.Close() }
