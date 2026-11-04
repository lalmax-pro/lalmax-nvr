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
	return r.publish(participantID, "", false, kind, source, codec)
}

// PublishSession rejects operations from a replaced participant session. It does not perform authorization.
func (r *Room) PublishSession(participantID, session, kind, source, codec string) (Publication, error) {
	return r.publish(participantID, session, true, kind, source, codec)
}
func (r *Room) publish(participantID, session string, check bool, kind, source, codec string) (Publication, error) {
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
	if check {
		if err := r.checkSessionLocked(participantID, session); err != nil {
			return Publication{}, err
		}
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
	return r.unpublish(participantID, "", publicationID, false)
}

// UnpublishSession rejects operations from a replaced session; caller authorization remains required.
func (r *Room) UnpublishSession(participantID, session, publicationID string) error {
	return r.unpublish(participantID, session, publicationID, true)
}
func (r *Room) unpublish(participantID, session, publicationID string, check bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	if check {
		if err := r.checkSessionLocked(participantID, session); err != nil {
			return err
		}
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
	if f := r.rtpPublications[id]; f != nil {
		f.Close()
		delete(r.rtpPublications, id)
	}
	delete(r.publications, id)
	for key := range r.subscriptions {
		if key.publicationID == id {
			r.closeRTPSubscriptionLocked(key)
			delete(r.subscriptions, key)
		}
	}
}
func (r *Room) Subscribe(participantID, publicationID string) error {
	return r.subscribe(participantID, "", publicationID, false)
}

// SubscribeSession rejects operations from a replaced session; caller authorization remains required.
func (r *Room) SubscribeSession(participantID, session, publicationID string) error {
	return r.subscribe(participantID, session, publicationID, true)
}
func (r *Room) subscribe(participantID, session, publicationID string, check bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	if check {
		if err := r.checkSessionLocked(participantID, session); err != nil {
			return err
		}
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
	return r.unsubscribe(participantID, "", publicationID, false)
}

// UnsubscribeSession rejects operations from a replaced session; caller authorization remains required.
func (r *Room) UnsubscribeSession(participantID, session, publicationID string) error {
	return r.unsubscribe(participantID, session, publicationID, true)
}
func (r *Room) unsubscribe(participantID, session, publicationID string, check bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	if check {
		if err := r.checkSessionLocked(participantID, session); err != nil {
			return err
		}
	}
	key := subscriptionKey{participantID, publicationID}
	if _, ok := r.subscriptions[key]; !ok {
		return ErrSubscriptionNotFound
	}
	r.closeRTPSubscriptionLocked(key)
	delete(r.subscriptions, key)
	r.notify("subscription_changed")
	return nil
}

// checkSessionLocked validates freshness, not identity or authorization.
func (r *Room) checkSessionLocked(participantID, session string) error {
	if session == "" {
		return ErrInvalidID
	}
	current, ok := r.sessions[participantID]
	if !ok {
		return ErrParticipantNotFound
	}
	if current != session {
		return ErrStaleSession
	}
	return nil
}
