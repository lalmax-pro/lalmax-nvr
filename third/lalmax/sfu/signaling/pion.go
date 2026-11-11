package signaling

import (
	"context"
	"errors"
	"sync"

	"github.com/pion/webrtc/v4"
)

// ErrNegotiationUsed means an earlier offer failed and this peer must reconnect.
var ErrNegotiationUsed = errors.New("signaling: negotiation unavailable after failure")

// PionNegotiator answers serialized non-trickle SDP offers. The caller creates and
// configures the PeerConnection, including codecs, transport policy, OnTrack,
// authorization and RTCP handling; this type does not bind Room media.
type PionNegotiator struct {
	mu     sync.Mutex
	pc     *webrtc.PeerConnection
	failed bool
}

func NewPionNegotiator(pc *webrtc.PeerConnection) (*PionNegotiator, error) {
	if pc == nil {
		return nil, errors.New("signaling: nil PeerConnection")
	}
	return &PionNegotiator{pc: pc}, nil
}
func (n *PionNegotiator) Answer(ctx context.Context, offer string) (string, error) {
	if ctx == nil || offer == "" {
		return "", errors.New("signaling: invalid offer")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.failed {
		return "", ErrNegotiationUsed
	}
	success := false
	defer func() {
		if !success {
			n.failed = true
			_ = n.pc.Close()
		}
	}()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := n.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		return "", err
	}
	answer, err := n.pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	gathered := webrtc.GatheringCompletePromise(n.pc)
	if err = n.pc.SetLocalDescription(answer); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-gathered:
	}
	local := n.pc.LocalDescription()
	if local == nil {
		return "", errors.New("signaling: missing local description")
	}
	success = true
	return local.SDP, nil
}
func (n *PionNegotiator) Close() error { n.mu.Lock(); defer n.mu.Unlock(); return n.pc.Close() }
