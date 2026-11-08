// Package sfubridge assembles an authorized SFU subscription and a protected
// NVR internal output. It is intended for use by trusted application code.
package sfubridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
	"github.com/q191201771/lalmax/sfu/bridge"
)

var ErrClosed = errors.New("sfu bridge service closed")
var ErrLimit = errors.New("sfu bridge limit reached")
var ErrInvalid = errors.New("invalid sfu bridge configuration")

// SourceValidator must check the actual negotiated H264 packetization mode,
// profile and no-B-frame constraint. Publication metadata alone is insufficient.
type SourceValidator func(context.Context, sfu.Publication) error

// OutputStore persists output identities, never media credentials.
type OutputStore interface {
	RegisterSFUBridgeOutput(context.Context, string) error
	ListSFUBridgeOutputIDs(context.Context) ([]string, error)
}

type Service struct {
	mu        sync.Mutex
	closed    bool
	active    int
	limit     int
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeWait sync.Once
	closeDone chan struct{}
	room      *sfu.Room
	policy    access.Authorizer
	publisher bridge.Publisher
	grants    *auth.MediaAuthorizer
	validate  SourceValidator
	outputs   OutputStore
}

// New requires a trusted policy and source validator; no permissive defaults are supplied.
func New(room *sfu.Room, policy access.Authorizer, publisher bridge.Publisher, grants *auth.MediaAuthorizer, validate SourceValidator, limit int) (*Service, error) {
	if room == nil || policy == nil || publisher == nil || grants == nil || validate == nil || limit < 1 || limit > 1024 {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{room: room, policy: policy, publisher: publisher, grants: grants, validate: validate, limit: limit, ctx: ctx, cancel: cancel, closeDone: make(chan struct{})}, nil
}

// NewPersistent restores retired output names before constructing a service.
// All application playback and publish paths must use the same authorizer, and
// startup must fail closed if the identity store cannot be read.
func NewPersistent(ctx context.Context, room *sfu.Room, policy access.Authorizer, publisher bridge.Publisher, grants *auth.MediaAuthorizer, validate SourceValidator, limit int, outputs OutputStore) (*Service, error) {
	if ctx == nil || outputs == nil || grants == nil {
		return nil, ErrInvalid
	}
	ids, err := outputs.ListSFUBridgeOutputIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("load SFU output identities: %w", err)
	}
	for _, id := range ids {
		grants.RestoreInternalStream(id)
	}
	service, err := New(room, policy, publisher, grants, validate, limit)
	if err != nil {
		return nil, err
	}
	service.outputs = outputs
	return service, nil
}

// Handle is a trusted service capability, not a client-facing authorization
// token. PlaybackToken is secret. Done reports the terminal result once.
type Handle struct {
	StreamID      string
	PlaybackToken string
	done          chan error
	cancel        context.CancelFunc
}

func (h *Handle) Done() <-chan error { return h.done }
func (h *Handle) Stop()              { h.cancel() }

// NewHandle wraps a trusted service-owned bridge. The service must send its
// terminal result after revoking the output and releasing the publisher. The
// buffered channel permits completion even if registry creation races close.
func NewHandle(streamID, playbackToken string, done chan error, cancel context.CancelFunc) (*Handle, error) {
	if streamID == "" || playbackToken == "" || cap(done) < 1 || cancel == nil {
		return nil, ErrInvalid
	}
	return &Handle{StreamID: streamID, PlaybackToken: playbackToken, done: done, cancel: cancel}, nil
}

func randomID() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Start uses an authenticated server-supplied principal. The policy checks
// join, subscribe and manage_bridge for the generated target. A successful
// return means queued, not that the lal publisher has registered yet. The
// handle must remain in a server-side ownership registry; do not expose Stop
// without authorizing the requesting principal. requestCtx controls setup only.
func (s *Service) Start(requestCtx context.Context, principal, publicationID string) (*Handle, error) {
	return s.start(requestCtx, principal, publicationID, "")
}

// StartAV creates one output from same-participant video and audio tracks.
func (s *Service) StartAV(requestCtx context.Context, principal, videoID, audioID string) (*Handle, error) {
	if audioID == "" || audioID == videoID {
		return nil, ErrInvalid
	}
	return s.start(requestCtx, principal, videoID, audioID)
}

func (s *Service) start(requestCtx context.Context, principal, publicationID, audioID string) (*Handle, error) {
	if requestCtx == nil || principal == "" || publicationID == "" {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	if s.active >= s.limit {
		s.mu.Unlock()
		return nil, ErrLimit
	}
	s.active++
	s.wg.Add(1)
	s.mu.Unlock()
	handedOff := false
	defer func() {
		if !handedOff {
			s.finish()
		}
	}()
	if err := requestCtx.Err(); err != nil {
		return nil, err
	}
	if err := s.ctx.Err(); err != nil {
		return nil, ErrClosed
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	publish, err := randomID()
	if err != nil {
		return nil, err
	}
	playback, err := randomID()
	if err != nil {
		return nil, err
	}
	target := "sfu_" + id
	session, err := access.JoinRoom(requestCtx, s.room, s.policy, principal, sfu.Participant{ID: "bridge_" + id})
	if err != nil {
		return nil, err
	}
	defer func() {
		if !handedOff {
			_ = session.Close()
		}
	}()
	if err = session.Subscribe(requestCtx, publicationID); err != nil {
		return nil, err
	}
	if audioID != "" {
		if err = session.Subscribe(requestCtx, audioID); err != nil {
			return nil, err
		}
	}
	var pub sfu.Publication
	var audio sfu.Publication
	for _, p := range s.room.Snapshot().Publications {
		if p.ID == publicationID {
			pub = p
		}
		if p.ID == audioID {
			audio = p
		}
	}
	if pub.ID == "" {
		return nil, sfu.ErrPublicationNotFound
	}
	if err = s.validate(requestCtx, pub); err != nil {
		return nil, err
	}
	if audioID != "" {
		if audio.ID == "" {
			return nil, sfu.ErrPublicationNotFound
		}
		if pub.Kind != "video" || audio.Kind != "audio" || pub.ParticipantID != audio.ParticipantID {
			return nil, ErrInvalid
		}
		if err = s.validate(requestCtx, audio); err != nil {
			return nil, err
		}
	}
	if err = s.grants.ReserveInternalStream(target, publish, playback); err != nil {
		return nil, fmt.Errorf("reserve output: %w", err)
	}
	defer func() {
		if !handedOff {
			s.grants.RevokeInternalStream(target)
		}
	}()
	// Persist the name before exposing it to any publisher. On error the
	// deferred revocation keeps this identity denied until this process exits.
	if s.outputs != nil {
		if err = s.outputs.RegisterSFUBridgeOutput(requestCtx, target); err != nil {
			return nil, fmt.Errorf("record output identity: %w", err)
		}
	}
	lifetime, cancel := context.WithCancel(s.ctx)
	var done <-chan error
	if audioID != "" {
		done, err = session.StartAVBridgeWithContexts(requestCtx, lifetime, audioID, publicationID, target, s.publisher)
	} else if pub.Kind == "audio" {
		done, err = session.StartOpusBridgeWithContexts(requestCtx, lifetime, publicationID, target, s.publisher)
	} else {
		done, err = session.StartH264BridgeWithContexts(requestCtx, lifetime, publicationID, target, s.publisher)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	h := &Handle{StreamID: target, PlaybackToken: playback, done: make(chan error, 1), cancel: cancel}
	handedOff = true
	go func() {
		err := <-done
		cancel()
		_ = session.Close()
		s.grants.RevokeInternalStream(target)
		s.finish()
		h.done <- err
		close(h.done)
	}()
	return h, nil
}

func (s *Service) finish() { s.mu.Lock(); s.active--; s.mu.Unlock(); s.wg.Done() }

// Close rejects new starts and cancels existing bridges. It waits for cleanup
// until ctx expires; a blocked synchronous lal write can delay cleanup.
func (s *Service) Close(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalid
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
	}
	s.mu.Unlock()
	s.closeWait.Do(func() { go func() { s.wg.Wait(); close(s.closeDone) }() })
	select {
	case <-s.closeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
