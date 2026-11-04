package sfu

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
)

var ErrInvalidTrack = errors.New("sfu: invalid track kind or source")

// Publication is a server-assigned media identity scoped to one room generation.
type Publication struct {
	ID            string
	ParticipantID string
	Kind          string // audio or video
	Source        string // camera, microphone or screen
	Codec         string
}
type Subscription struct{ ParticipantID, PublicationID string }
type subscriptionKey struct{ participantID, publicationID string }

// Publish registers a track. The caller must enforce room-scoped authorization.
func (r *Room) Publish(participantID, kind, source, codec string) (Publication, error) {
	if (kind != "audio" && kind != "video") || (source != "camera" && source != "microphone" && source != "screen") || codec == "" || (source == "camera" && kind != "video") || (source == "microphone" && kind != "audio") {
		return Publication{}, ErrInvalidTrack
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Publication{}, ErrRoomClosed
	}
	if _, ok := r.participants[participantID]; !ok {
		return Publication{}, ErrParticipantNotFound
	}
	if r.limits.MaxPublicationsPerParticipant > 0 {
		count := 0
		for _, existing := range r.publications {
			if existing.ParticipantID == participantID {
				count++
			}
		}
		if count >= r.limits.MaxPublicationsPerParticipant {
			return Publication{}, ErrPublicationLimit
		}
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Publication{}, err
	}
	p := Publication{ID: hex.EncodeToString(id[:]), ParticipantID: participantID, Kind: kind, Source: source, Codec: codec}
	r.publications[p.ID] = p
	r.notify("track_published")
	return p, nil
}
func (r *Room) Unpublish(participantID, publicationID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	p, ok := r.publications[publicationID]
	if !ok || p.ParticipantID != participantID {
		return ErrPublicationNotFound
	}
	r.removePublication(publicationID)
	r.notify("track_unpublished")
	return nil
}

// removePublication requires the room lock.
func (r *Room) removePublication(id string) {
	delete(r.publications, id)
	for key := range r.subscriptions {
		if key.publicationID == id {
			delete(r.subscriptions, key)
		}
	}
}
func (r *Room) Subscribe(participantID, publicationID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	if _, ok := r.participants[participantID]; !ok {
		return ErrParticipantNotFound
	}
	p, ok := r.publications[publicationID]
	if !ok {
		return ErrPublicationNotFound
	}
	if p.ParticipantID == participantID {
		return ErrOwnPublication
	}
	key := subscriptionKey{participantID, publicationID}
	if _, ok := r.subscriptions[key]; ok {
		return ErrSubscriptionExists
	}
	if r.limits.MaxSubscriptions > 0 && len(r.subscriptions) >= r.limits.MaxSubscriptions {
		return ErrSubscriptionLimit
	}
	r.subscriptions[key] = Subscription{participantID, publicationID}
	r.notify("subscription_changed")
	return nil
}
func (r *Room) Unsubscribe(participantID, publicationID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	key := subscriptionKey{participantID, publicationID}
	if _, ok := r.subscriptions[key]; !ok {
		return ErrSubscriptionNotFound
	}
	delete(r.subscriptions, key)
	r.notify("subscription_changed")
	return nil
}
