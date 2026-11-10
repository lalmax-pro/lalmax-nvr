package signaling

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
	"github.com/q191201771/lalmax/sfu/webrtcbridge"
)

// RoomFactory assembles Pion media for the session's authorized subscriptions.
// The trusted callbacks must enforce authentication, network and codec policy.
// It owns inbound RTCP reads and keeps the latest Sender Report in the Room
// while the publication is alive, including sources with multiple RID layers.
type RoomFactory struct {
	NewPeer        func(context.Context, Identity) (*webrtc.PeerConnection, error)
	Codec          func(sfu.Publication) (webrtc.RTPCodecCapability, error)
	MaxSubscribers int
	QueueSize      int
	OnInbound      func(Identity, *webrtc.TrackRemote, error)
	OnInboundPeer  func(context.Context, Identity, *webrtc.PeerConnection, *webrtc.TrackRemote, <-chan error, error)
	feedbackMu     sync.Mutex
	feedback       map[feedbackKey]*feedbackBinding
	reportMu       sync.Mutex
	reportReaders  map[senderReportReaderKey]*senderReportCollector
}

type feedbackKey struct {
	room                    sfu.Key
	generation, publication string
	rid                     string
}

type feedbackBinding struct {
	requester *webrtcbridge.KeyframeRequester
	stopped   <-chan struct{}
}

func (f *RoomFactory) forwardFeedback(key feedbackKey, outgoingSSRC uint32, packets []rtcp.Packet) {
	f.feedbackMu.Lock()
	binding := f.feedback[key]
	f.feedbackMu.Unlock()
	if binding == nil {
		return
	}
	select {
	case <-binding.stopped:
		return
	default:
	}
	_ = binding.requester.Handle(outgoingSSRC, packets)
}

func (f *RoomFactory) requestKeyframe(key feedbackKey) {
	f.feedbackMu.Lock()
	binding := f.feedback[key]
	f.feedbackMu.Unlock()
	if binding == nil {
		return
	}
	select {
	case <-binding.stopped:
		return
	default:
	}
	_ = binding.requester.Request()
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
	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		publication, done, stopped, err := session.StartMappedIncomingWithPublication(bindCtx, track, f.MaxSubscribers, f.QueueSize)
		if err == nil {
			f.trackSenderReports(bindCtx, receiver, track.RID(), session, publication, uint32(track.SSRC()), stopped)
		}
		if err == nil && track.Kind() == webrtc.RTPCodecTypeVideo {
			key := feedbackKey{room: session.RoomKey(), generation: session.RoomGeneration(), publication: publication, rid: track.RID()}
			requester, _ := webrtcbridge.NewKeyframeRequester(pc, 0, uint32(track.SSRC()), time.Second)
			binding := &feedbackBinding{requester: requester, stopped: stopped}
			f.feedbackMu.Lock()
			if f.feedback == nil {
				f.feedback = make(map[feedbackKey]*feedbackBinding)
			}
			f.feedback[key] = binding
			f.feedbackMu.Unlock()
			// Preserve the callback's single termination result while also cleaning
			// up this exact binding if a source ends or is replaced.
			callbackDone := make(chan error, 1)
			go func(sourceDone <-chan error) {
				<-stopped
				f.feedbackMu.Lock()
				if f.feedback[key] == binding {
					delete(f.feedback, key)
				}
				f.feedbackMu.Unlock()
				result := <-sourceDone
				callbackDone <- result
				close(callbackDone)
			}(done)
			done = callbackDone
		}
		if f.OnInbound != nil {
			f.OnInbound(id, track, err)
		}
		if f.OnInboundPeer != nil {
			f.OnInboundPeer(bindCtx, id, pc, track, done, err)
		}
	})
	negotiator, err := NewPionNegotiator(pc)
	if err != nil {
		cancel()
		_ = pc.Close()
		return nil, err
	}
	return &roomNegotiator{pion: negotiator, pc: pc, session: session, factory: f, room: session.RoomKey(), generation: session.RoomGeneration(), ctx: bindCtx, cancel: cancel, senders: make(map[string]outgoingSender)}, nil
}

type outgoingSender struct {
	sender  *webrtc.RTPSender
	stopped <-chan struct{}
}

type roomNegotiator struct {
	mu         sync.Mutex
	pion       *PionNegotiator
	pc         *webrtc.PeerConnection
	session    *access.Session
	factory    *RoomFactory
	room       sfu.Key
	generation string
	ctx        context.Context
	cancel     context.CancelFunc
	senders    map[string]outgoingSender
	closed     bool
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
	for id, binding := range n.senders {
		_, wanted := desired[id]
		select {
		case <-binding.stopped:
			wanted = false
		default:
		}
		if !wanted {
			if err = n.pc.RemoveTrack(binding.sender); err != nil {
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
		layerChanged := func(rid string) {
			n.factory.requestKeyframe(feedbackKey{room: n.room, generation: n.generation, publication: p.ID, rid: rid})
		}
		_, stopped, err := n.session.StartOutgoingWithLayerChange(n.ctx, p.ID, local, layerChanged)
		if err != nil {
			return fmt.Errorf("bind outgoing track: %w", err)
		}
		go n.readFeedback(sender, p.ID, stopped)
		// Stop unblocks ReadRTCP even if the client never renegotiates or sends
		// another packet. RemoveTrack remains safe when the next offer arrives.
		go func() { <-stopped; _ = sender.Stop() }()
		n.senders[p.ID] = outgoingSender{sender: sender, stopped: stopped}
	}
	return nil
}

func (n *roomNegotiator) readFeedback(sender *webrtc.RTPSender, publication string, stopped <-chan struct{}) {
	for {
		packets, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		select {
		case <-stopped:
			return
		default:
		}
		if !n.session.OutgoingActive(publication) {
			continue
		}
		rid, err := n.session.SubscriptionLayer(publication)
		if err != nil {
			continue
		}
		params := sender.GetParameters()
		if len(params.Encodings) != 1 || params.Encodings[0].SSRC == 0 {
			continue
		}
		key := feedbackKey{room: n.room, generation: n.generation, publication: publication, rid: rid}
		n.factory.forwardFeedback(key, uint32(params.Encodings[0].SSRC), packets)
	}
}
func (n *roomNegotiator) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.cancel()
	n.closed = true
	return n.pion.Close()
}
