package sfu

import (
	"errors"

	"github.com/pion/rtp"
	"github.com/q191201771/lalmax/sfu/rtpforward"
)

var (
	ErrMediaAlreadyEnabled = errors.New("sfu: publication RTP already enabled")
	ErrMediaNotEnabled     = errors.New("sfu: publication RTP not enabled")
)

// EnablePublicationRTP enables one bounded fanout for a publication owned by
// the current session. The caller must perform authorization separately.
func (r *Room) EnablePublicationRTP(participantID, session, publicationID string, maxSubscribers, queueSize int) error {
	f, err := rtpforward.New(maxSubscribers, queueSize)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	if err := r.checkSessionLocked(participantID, session); err != nil {
		return err
	}
	p, ok := r.publications[publicationID]
	if !ok || p.ParticipantID != participantID {
		return ErrPublicationNotFound
	}
	if r.rtpPublications[publicationID] != nil {
		return ErrMediaAlreadyEnabled
	}
	if r.rtpPublications == nil {
		r.rtpPublications = make(map[string]*rtpforward.Fanout)
	}
	r.rtpPublications[publicationID] = f
	p.RTPReady = true
	r.publications[publicationID] = p
	r.notify("track_ready")
	return nil
}

// WritePublicationRTP validates publisher session and writes one packet without
// keeping the room lock during fanout. Removal closes the fanout concurrently.
func (r *Room) WritePublicationRTP(participantID, session, publicationID string, packet *rtp.Packet) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrRoomClosed
	}
	if err := r.checkSessionLocked(participantID, session); err != nil {
		r.mu.Unlock()
		return err
	}
	p, ok := r.publications[publicationID]
	if !ok || p.ParticipantID != participantID {
		r.mu.Unlock()
		return ErrPublicationNotFound
	}
	f := r.rtpPublications[publicationID]
	r.mu.Unlock()
	if f == nil {
		return ErrMediaNotEnabled
	}
	return f.WriteRTP(packet)
}

// OpenSubscriptionRTP requires an existing Room subscription and current
// session; caller is responsible for authorization and RTP transport mapping.
func (r *Room) OpenSubscriptionRTP(participantID, session, publicationID string) (*rtpforward.Subscriber, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrRoomClosed
	}
	if err := r.checkSessionLocked(participantID, session); err != nil {
		return nil, err
	}
	key := subscriptionKey{participantID, publicationID}
	if _, ok := r.subscriptions[key]; !ok {
		return nil, ErrSubscriptionNotFound
	}
	f := r.rtpPublications[publicationID]
	if f == nil {
		return nil, ErrMediaNotEnabled
	}
	// Slow/closed consumers may reopen without disturbing the Room subscription.
	if previous := r.rtpSubscriptions[key]; previous != nil {
		if previous.Err() == nil {
			return nil, rtpforward.ErrDuplicateSubscriber
		}
		delete(r.rtpSubscriptions, key)
	}
	sub, err := f.Subscribe(participantID)
	if err != nil {
		return nil, err
	}
	if r.rtpSubscriptions == nil {
		r.rtpSubscriptions = make(map[subscriptionKey]*rtpforward.Subscriber)
	}
	r.rtpSubscriptions[key] = sub
	return sub, nil
}
func (r *Room) closeRTPSubscriptionLocked(key subscriptionKey) {
	if sub := r.rtpSubscriptions[key]; sub != nil {
		sub.Close()
		delete(r.rtpSubscriptions, key)
	}
}
