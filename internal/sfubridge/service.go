// Package sfubridge provides trusted, owner-scoped bridge management.
package sfubridge

import (
	"context"
	"errors"
)

var (
	ErrClosed  = errors.New("sfu bridge service closed")
	ErrLimit   = errors.New("sfu bridge limit reached")
	ErrInvalid = errors.New("invalid sfu bridge configuration")
)

// OutputStore persists output identities, never media credentials.
type OutputStore interface {
	RegisterSFUBridgeOutput(context.Context, string) error
	ListSFUBridgeOutputIDs(context.Context) ([]string, error)
}

// Handle is a trusted server-side capability; PlaybackToken must stay secret.
type Handle struct {
	StreamID      string
	PlaybackToken string
	done          chan error
	cancel        context.CancelFunc
}

func (h *Handle) Done() <-chan error { return h.done }
func (h *Handle) Stop()              { h.cancel() }

// NewHandle wraps a service-owned bridge. The service reports completion only
// after revoking the output and releasing the publisher. The channel must be
// buffered so completion cannot block when creation races registry shutdown.
func NewHandle(streamID, playbackToken string, done chan error, cancel context.CancelFunc) (*Handle, error) {
	if streamID == "" || playbackToken == "" || cap(done) < 1 || cancel == nil {
		return nil, ErrInvalid
	}
	return &Handle{StreamID: streamID, PlaybackToken: playbackToken, done: done, cancel: cancel}, nil
}
