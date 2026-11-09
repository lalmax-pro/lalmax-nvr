package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/livekit/livekit-server/pkg/rtc/types"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

const (
	maxLiveKitSessions       = 4096
	liveKitSessionRetention  = 15 * time.Minute
	liveKitSessionCheckEvery = 5 * time.Second
)

var errLiveKitJoinDenied = errors.New("LiveKit join denied")

type nvrLiveKitSession struct {
	principal, room, generation string
	retainUntil                 time.Time
}

// nvrLiveKitSessions binds the random NVR-issued identity to its authorized
// account and exact room generation. Entries are bounded and retained after
// disconnect for longer than LiveKit's ten-minute refreshed-token lifetime.
type nvrLiveKitSessions struct {
	runtime *nvrLiveKitRuntime
	mu      sync.Mutex
	items   map[string]nvrLiveKitSession
}

func newNVRLiveKitSessions(runtime *nvrLiveKitRuntime) *nvrLiveKitSessions {
	return &nvrLiveKitSessions{runtime: runtime, items: make(map[string]nvrLiveKitSession)}
}

func (s *nvrLiveKitSessions) register(identity, principal, room, generation string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) >= maxLiveKitSessions {
		return errors.New("LiveKit session capacity reached")
	}
	if _, exists := s.items[identity]; exists {
		return errors.New("LiveKit session identity collision")
	}
	// Before first use only the original short-lived credential exists. The
	// retention window is extended when LiveKit accepts a connection and can
	// subsequently issue a refreshed token.
	s.items[identity] = nvrLiveKitSession{principal: principal, room: room, generation: generation, retainUntil: time.Now().Add(liveKitTokenValidity)}
	return nil
}

// authorize runs after LiveKit verifies the JWT. Only the reserved nvr_
// namespace is managed here; independently signed operator tokens and the
// hidden bridge participant retain their native LiveKit behavior.
func (s *nvrLiveKitSessions) authorize(ctx context.Context, grants *auth.ClaimGrants, actualRoomID livekit.RoomID) error {
	if grants == nil || grants.Video == nil {
		return errLiveKitJoinDenied
	}
	identity := grants.Identity
	if !strings.HasPrefix(identity, "nvr_") {
		return nil
	}
	s.mu.Lock()
	session, ok := s.items[identity]
	s.mu.Unlock()
	if !ok || grants.Video.Room != session.room || (actualRoomID != "" && string(actualRoomID) != session.generation) {
		return errLiveKitJoinDenied
	}
	s.runtime.mu.Lock()
	server, closed := s.runtime.server, s.runtime.closed
	s.runtime.mu.Unlock()
	if closed || server == nil || !server.IsRunning() {
		return errLiveKitJoinDenied
	}
	room := server.RoomManager().GetRoom(ctx, livekit.RoomName(session.room))
	if room == nil || room.IsClosed() || string(room.ID()) != session.generation {
		return errLiveKitJoinDenied
	}
	if err := s.runtime.policy.check(ctx, session.principal); err != nil {
		return errLiveKitJoinDenied
	}
	s.mu.Lock()
	if current, exists := s.items[identity]; exists && current.principal == session.principal && current.generation == session.generation {
		current.retainUntil = time.Now().Add(liveKitSessionRetention)
		s.items[identity] = current
	} else {
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		return errLiveKitJoinDenied
	}
	return nil
}

func (s *nvrLiveKitSessions) monitor(ctx context.Context) {
	ticker := time.NewTicker(liveKitSessionCheckEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.revalidate(ctx)
		}
	}
}

func (s *nvrLiveKitSessions) revalidate(ctx context.Context) {
	s.runtime.mu.Lock()
	server, closed := s.runtime.server, s.runtime.closed
	s.runtime.mu.Unlock()
	if closed || server == nil || !server.IsRunning() {
		return
	}
	s.mu.Lock()
	snapshot := make(map[string]nvrLiveKitSession, len(s.items))
	for identity, session := range s.items {
		snapshot[identity] = session
	}
	s.mu.Unlock()
	checked := make(map[string]error)
	for identity, session := range snapshot {
		if ctx.Err() != nil {
			return
		}
		room := server.RoomManager().GetRoom(ctx, livekit.RoomName(session.room))
		if room == nil || room.IsClosed() || string(room.ID()) != session.generation {
			s.pruneExpired(identity, session, time.Now())
			continue
		}
		participant := room.GetParticipant(livekit.ParticipantIdentity(identity))
		if participant == nil {
			s.pruneExpired(identity, session, time.Now())
			continue
		}
		s.mu.Lock()
		if current, exists := s.items[identity]; exists && current.generation == session.generation {
			current.retainUntil = time.Now().Add(liveKitSessionRetention)
			s.items[identity] = current
		}
		s.mu.Unlock()
		checkErr, checkedBefore := checked[session.principal]
		if !checkedBefore {
			checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			checkErr = s.runtime.policy.check(checkCtx, session.principal)
			cancel()
			checked[session.principal] = checkErr
		}
		if checkErr != nil && ctx.Err() == nil {
			room.RemoveParticipant(participant.Identity(), participant.ID(), types.ParticipantCloseReasonServiceRequestRemoveParticipant)
		}
	}
}

func (s *nvrLiveKitSessions) pruneExpired(identity string, observed nvrLiveKitSession, now time.Time) {
	if now.Before(observed.retainUntil) {
		return
	}
	s.mu.Lock()
	if current, exists := s.items[identity]; exists && current == observed && !now.Before(current.retainUntil) {
		delete(s.items, identity)
	}
	s.mu.Unlock()
}
