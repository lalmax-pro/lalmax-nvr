// Package sfu contains the media-independent state of SFU rooms.
package sfu

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/q191201771/lalmax/sfu/rtpforward"
)

var (
	ErrStaleSession         = errors.New("sfu: stale participant session")
	ErrInvalidWatchBuffer   = errors.New("sfu: invalid watch buffer")
	ErrInvalidLimits        = errors.New("sfu: invalid room limits")
	ErrPublicationLimit     = errors.New("sfu: publication limit reached")
	ErrSubscriptionLimit    = errors.New("sfu: subscription limit reached")
	ErrWatcherLimit         = errors.New("sfu: watcher limit reached")
	ErrInvalidID            = errors.New("sfu: empty identifier")
	ErrRoomExists           = errors.New("sfu: room already exists")
	ErrRoomNotFound         = errors.New("sfu: room not found")
	ErrRoomClosed           = errors.New("sfu: room closed")
	ErrParticipantExists    = errors.New("sfu: participant already exists")
	ErrParticipantNotFound  = errors.New("sfu: participant not found")
	ErrRoomFull             = errors.New("sfu: room full")
	ErrPublicationNotFound  = errors.New("sfu: publication not found")
	ErrSubscriptionExists   = errors.New("sfu: subscription already exists")
	ErrSubscriptionNotFound = errors.New("sfu: subscription not found")
	ErrOwnPublication       = errors.New("sfu: cannot subscribe to own publication")
)

// Key identifies a room within a namespace.
type Key struct{ Namespace, ID string }

// Participant contains identity supplied by the authorized caller.
type Participant struct{ ID, Name string }

// Snapshot is an independent copy of room membership at one instant.
type Snapshot struct {
	Key           Key
	Generation    string
	Sequence      uint64
	Participants  []Participant
	Closed        bool
	Publications  []Publication
	Subscriptions []Subscription
}

// Manager owns room instances. Authentication is the caller's responsibility.
type Manager struct {
	mu    sync.Mutex
	rooms map[Key]*Room
}

func NewManager() *Manager { return &Manager{rooms: make(map[Key]*Room)} }

// RoomLimits specifies per-room resource limits. Zero means unlimited.
type RoomLimits struct {
	MaxParticipants               int
	MaxPublicationsPerParticipant int
	MaxSubscriptions              int
	MaxWatchers                   int
}

// Create atomically creates a room with a participant limit.
func (m *Manager) Create(key Key, limit int) (*Room, error) {
	if limit < 0 {
		return nil, ErrInvalidID
	}
	return m.CreateWithLimits(key, RoomLimits{MaxParticipants: limit})
}

// CreateWithLimits creates a room with atomic limits; zero means unlimited.
func (m *Manager) CreateWithLimits(key Key, limits RoomLimits) (*Room, error) {
	if key.Namespace == "" || key.ID == "" {
		return nil, ErrInvalidID
	}
	if limits.MaxParticipants < 0 || limits.MaxPublicationsPerParticipant < 0 || limits.MaxSubscriptions < 0 || limits.MaxWatchers < 0 {
		return nil, ErrInvalidLimits
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rooms == nil {
		m.rooms = make(map[Key]*Room)
	}
	if _, ok := m.rooms[key]; ok {
		return nil, ErrRoomExists
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	r := &Room{key: key, generation: hex.EncodeToString(id[:]), limit: limits.MaxParticipants, limits: limits, participants: make(map[string]Participant), sessions: make(map[string]string), publications: make(map[string]Publication), subscriptions: make(map[subscriptionKey]Subscription)}
	m.rooms[key] = r
	return r, nil
}
func (m *Manager) Get(key Key) (*Room, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[key]
	return r, ok
}

// Close removes and closes only the specified instance, protecting a newly
// created room from delayed cleanup of an older generation.
func (m *Manager) Close(key Key, generation string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[key]
	if !ok || r.generation != generation {
		return ErrRoomNotFound
	}
	r.close()
	delete(m.rooms, key)
	return nil
}

type Room struct {
	mu               sync.Mutex
	key              Key
	generation       string
	limit            int
	limits           RoomLimits
	closed           bool
	participants     map[string]Participant
	sessions         map[string]string
	publications     map[string]Publication
	subscriptions    map[subscriptionKey]Subscription
	sequence         uint64
	watchers         map[chan Event]struct{}
	rtpPublications  map[string]*rtpforward.Fanout
	rtpSubscriptions map[subscriptionKey]*rtpforward.Subscriber
}

// Join adds a participant. Connection handlers should use JoinSession and
// LeaveSession to protect a replacement from stale disconnect callbacks.
func (r *Room) Join(p Participant) error {
	_, err := r.JoinSession(p)
	return err
}

// JoinSession returns a unique local generation for conditional cleanup. It is
// not an authentication credential and must not be used to authorize actions.
func (r *Room) JoinSession(p Participant) (string, error) {
	if p.ID == "" {
		return "", ErrInvalidID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", ErrRoomClosed
	}
	if _, ok := r.participants[p.ID]; ok {
		return "", ErrParticipantExists
	}
	if r.limit > 0 && len(r.participants) >= r.limit {
		return "", ErrRoomFull
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	session := hex.EncodeToString(id[:])
	r.sessions[p.ID] = session
	r.participants[p.ID] = p
	r.notify("participant_joined")
	return session, nil
}

// Leave unconditionally removes the current participant. Connection callbacks should use LeaveSession.
func (r *Room) Leave(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.leaveLocked(id, "", false)
}

// LeaveSession removes only the participant generation returned by JoinSession.
func (r *Room) LeaveSession(id, session string) error {
	if session == "" {
		return ErrInvalidID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.leaveLocked(id, session, true)
}

func (r *Room) leaveLocked(id, session string, checkSession bool) error {
	if r.closed {
		return ErrRoomClosed
	}
	if _, ok := r.participants[id]; !ok {
		return ErrParticipantNotFound
	}
	if checkSession && r.sessions[id] != session {
		return ErrStaleSession
	}
	delete(r.sessions, id)
	delete(r.participants, id)
	for key := range r.subscriptions {
		if key.participantID == id {
			r.closeRTPSubscriptionLocked(key)
			delete(r.subscriptions, key)
		}
	}
	for pubID, p := range r.publications {
		if p.ParticipantID == id {
			r.removePublication(pubID)
		}
	}
	r.notify("participant_left")
	return nil
}
func (r *Room) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

func (r *Room) snapshotLocked() Snapshot {
	s := Snapshot{Key: r.key, Generation: r.generation, Sequence: r.sequence, Closed: r.closed, Participants: make([]Participant, 0, len(r.participants))}
	for _, p := range r.participants {
		s.Participants = append(s.Participants, p)
	}
	for _, p := range r.publications {
		s.Publications = append(s.Publications, p)
	}
	for _, sub := range r.subscriptions {
		s.Subscriptions = append(s.Subscriptions, sub)
	}
	return s
}
func (r *Room) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	for _, fanout := range r.rtpPublications {
		fanout.Close()
	}
	clear(r.rtpPublications)
	clear(r.rtpSubscriptions)
	clear(r.participants)
	clear(r.sessions)
	clear(r.publications)
	clear(r.subscriptions)
	r.notify("room_closed")
	for ch := range r.watchers {
		close(ch)
		delete(r.watchers, ch)
	}
}
