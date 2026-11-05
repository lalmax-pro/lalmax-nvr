// Package access wraps Room control operations with a mandatory policy check.
// It does not authenticate credentials. The caller must supply a trusted
// principal and must not expose raw Room methods to untrusted clients.
package access

import (
	"context"
	"errors"
	"sync"

	"github.com/q191201771/lalmax/sfu"
)

var (
	ErrDenied  = errors.New("sfu access denied")
	ErrInvalid = errors.New("sfu access: invalid input")
	ErrClosed  = errors.New("sfu access: session closed")
)

type Operation string

const (
	Join        Operation = "join"
	Publish     Operation = "publish"
	Unpublish   Operation = "unpublish"
	Subscribe   Operation = "subscribe"
	Unsubscribe Operation = "unsubscribe"
)

// Request carries server-side identity and scope to a trusted policy provider.
// Principal must come from authenticated server state, not a client field.
type Request struct {
	Principal      string
	Room           sfu.Key
	RoomGeneration string
	ParticipantID  string
	Operation      Operation
	PublicationID  string
	Kind           string
	Source         string
	Codec          string
}

// Authorizer must fail closed and is called on every control operation.
type Authorizer interface {
	Authorize(context.Context, Request) error
}
type Session struct {
	mu         sync.Mutex
	room       *sfu.Room
	policy     Authorizer
	scope      Request
	generation string
	closed     bool
}

// JoinRoom checks policy before allocating a participant. The policy must be
// trusted and the principal authenticated before calling this function.
func JoinRoom(ctx context.Context, room *sfu.Room, policy Authorizer, principal string, participant sfu.Participant) (*Session, error) {
	if ctx == nil || room == nil || policy == nil || principal == "" || participant.ID == "" {
		return nil, ErrInvalid
	}
	snap := room.Snapshot()
	req := Request{Principal: principal, Room: snap.Key, RoomGeneration: snap.Generation, ParticipantID: participant.ID, Operation: Join}
	if err := authorize(ctx, policy, req); err != nil {
		return nil, err
	}
	generation, err := room.JoinSession(participant)
	if err != nil {
		return nil, err
	}
	return &Session{room: room, policy: policy, scope: req, generation: generation}, nil
}
func authorize(ctx context.Context, policy Authorizer, req Request) error {
	if ctx == nil {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := policy.Authorize(ctx, req); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrDenied
	}
	return ctx.Err()
}

// withPolicy serializes operations and cleanup so a session cannot be used
// after Close. A slow policy does not hold the Room lock.
func (s *Session) withPolicy(ctx context.Context, req Request, run func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	req.Principal = s.scope.Principal
	req.Room = s.scope.Room
	req.RoomGeneration = s.scope.RoomGeneration
	req.ParticipantID = s.scope.ParticipantID
	if err := authorize(ctx, s.policy, req); err != nil {
		return err
	}
	return run()
}
func (s *Session) Publish(ctx context.Context, kind, source, codec string) (sfu.Publication, error) {
	var p sfu.Publication
	err := s.withPolicy(ctx, Request{Operation: Publish, Kind: kind, Source: source, Codec: codec}, func() error {
		var err error
		p, err = s.room.PublishSession(s.scope.ParticipantID, s.generation, kind, source, codec)
		return err
	})
	return p, err
}
func (s *Session) Unpublish(ctx context.Context, id string) error {
	return s.withPolicy(ctx, Request{Operation: Unpublish, PublicationID: id}, func() error { return s.room.UnpublishSession(s.scope.ParticipantID, s.generation, id) })
}
func (s *Session) Subscribe(ctx context.Context, id string) error {
	return s.withPolicy(ctx, Request{Operation: Subscribe, PublicationID: id}, func() error { return s.room.SubscribeSession(s.scope.ParticipantID, s.generation, id) })
}
func (s *Session) Unsubscribe(ctx context.Context, id string) error {
	return s.withPolicy(ctx, Request{Operation: Unsubscribe, PublicationID: id}, func() error { return s.room.UnsubscribeSession(s.scope.ParticipantID, s.generation, id) })
}

// Close always permits cleanup even if authorization has been revoked.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	err := s.room.LeaveSession(s.scope.ParticipantID, s.generation)
	if errors.Is(err, sfu.ErrRoomClosed) || errors.Is(err, sfu.ErrParticipantNotFound) || errors.Is(err, sfu.ErrStaleSession) {
		return nil
	}
	return err
}
