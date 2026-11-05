package signaling

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
)

// RoomFactory assembles Pion media for the session's authorized subscriptions.
// The trusted callbacks must enforce authentication, network and codec policy.
type RoomFactory struct {
	NewPeer        func(context.Context, Identity) (*webrtc.PeerConnection, error)
	Codec          func(sfu.Publication) (webrtc.RTPCodecCapability, error)
	MaxSubscribers int
	QueueSize      int
	OnInbound      func(Identity, *webrtc.TrackRemote, error)
}

func (f *RoomFactory) NewNegotiator(ctx context.Context, id Identity, session *access.Session) (Negotiator, error) {
	if ctx == nil || session == nil || f == nil || f.NewPeer == nil || f.Codec == nil || f.MaxSubscribers < 1 || f.MaxSubscribers > 1024 || f.QueueSize < 1 || f.QueueSize > 256 {
		return nil, errors.New("signaling: invalid room factory configuration")
	}
	// Fail closed before allocating a PeerConnection if subscriptions are denied.
	if _, err := session.SubscribedPublications(ctx); err != nil {
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
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		_, err := session.StartMappedIncoming(bindCtx, track, f.MaxSubscribers, f.QueueSize)
		if f.OnInbound != nil {
			f.OnInbound(id, track, err)
		}
	})
	negotiator, err := NewPionNegotiator(pc)
	if err != nil {
		cancel()
		_ = pc.Close()
		return nil, err
	}
	return &roomNegotiator{pion: negotiator, pc: pc, session: session, factory: f, ctx: bindCtx, cancel: cancel, senders: make(map[string]*webrtc.RTPSender)}, nil
}

type roomNegotiator struct {
	mu      sync.Mutex
	pion    *PionNegotiator
	pc      *webrtc.PeerConnection
	session *access.Session
	factory *RoomFactory
	ctx     context.Context
	cancel  context.CancelFunc
	senders map[string]*webrtc.RTPSender
	closed  bool
}

// Answer reconciles the authorized subscription set before each client offer.
// On failure the PC is closed: the client must reconnect and rebuild its state.
func (n *roomNegotiator) Answer(ctx context.Context, offer string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return "", ErrNegotiationUsed
	}
	if err := n.syncOutgoing(ctx); err != nil {
		n.closed = true
		n.cancel()
		_ = n.pion.Close()
		return "", err
	}
	answer, err := n.pion.Answer(ctx, offer)
	if err != nil {
		n.closed = true
		n.cancel()
		_ = n.pion.Close()
		return "", err
	}
	return answer, nil
}
func (n *roomNegotiator) syncOutgoing(ctx context.Context) error {
	pubs, err := n.session.SubscribedPublications(ctx)
	if err != nil {
		return err
	}
	desired := make(map[string]sfu.Publication, len(pubs))
	for _, p := range pubs {
		desired[p.ID] = p
	}
	for id, sender := range n.senders {
		if _, ok := desired[id]; !ok {
			if err = n.pc.RemoveTrack(sender); err != nil {
				return err
			}
			delete(n.senders, id)
		}
	}
	for _, p := range pubs {
		if _, ok := n.senders[p.ID]; ok {
			continue
		}
		capability, err := n.factory.Codec(p)
		if err != nil {
			return err
		}
		if capability.MimeType == "" || !strings.EqualFold(capability.MimeType, p.Codec) {
			return errors.New("signaling: publication codec mismatch")
		}
		local, err := webrtc.NewTrackLocalStaticRTP(capability, p.ID, "sfu")
		if err != nil {
			return err
		}
		sender, err := n.pc.AddTrack(local)
		if err != nil {
			return err
		}
		go func() {
			for {
				if _, _, err := sender.ReadRTCP(); err != nil {
					return
				}
			}
		}()
		if _, err = n.session.StartOutgoing(n.ctx, p.ID, local); err != nil {
			return fmt.Errorf("bind outgoing track: %w", err)
		}
		n.senders[p.ID] = sender
	}
	return nil
}
func (n *roomNegotiator) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.cancel()
	n.closed = true
	return n.pion.Close()
}
