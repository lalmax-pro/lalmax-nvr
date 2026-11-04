// Package sfu contains the media-independent state of SFU rooms.
package sfu

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
)

var (
	ErrInvalidWatchBuffer   = errors.New("sfu: invalid watch buffer")
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

// Create atomically creates a room. A positive limit bounds its membership;
// zero means unlimited.
func (m *Manager) Create(key Key, limit int) (*Room, error) {
	if key.Namespace == "" || key.ID == "" || limit < 0 {
		return nil, ErrInvalidID
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
	r := &Room{key: key, generation: hex.EncodeToString(id[:]), limit: limit, participants: make(map[string]Participant), publications: make(map[string]Publication), subscriptions: make(map[subscriptionKey]Subscription)}
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
	mu            sync.Mutex
	key           Key
	generation    string
	limit         int
	closed        bool
	participants  map[string]Participant
	publications  map[string]Publication
	subscriptions map[subscriptionKey]Subscription
	sequence      uint64
	watchers      map[chan Event]struct{}
}

func (r *Room) Join(p Participant) error {
	if p.ID == "" {
		return ErrInvalidID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	if _, ok := r.participants[p.ID]; ok {
		return ErrParticipantExists
	}
	if r.limit > 0 && len(r.participants) >= r.limit {
		return ErrRoomFull
	}
	r.participants[p.ID] = p
	r.notify("participant_joined")
	return nil
}
func (r *Room) Leave(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	if _, ok := r.participants[id]; !ok {
		return ErrParticipantNotFound
	}
	delete(r.participants, id)
	for key := range r.subscriptions {
		if key.participantID == id {
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
	clear(r.participants)
	clear(r.publications)
	clear(r.subscriptions)
	r.notify("room_closed")
	for ch := range r.watchers {
		close(ch)
		delete(r.watchers, ch)
	}
}
