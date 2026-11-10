package sfu

import (
	"errors"
	"strings"

	"github.com/pion/rtp"
	"github.com/q191201771/lalmax/sfu/rtpforward"
)

var (
	ErrMediaAlreadyEnabled    = errors.New("sfu: publication RTP already enabled")
	ErrMediaNotEnabled        = errors.New("sfu: publication RTP not enabled")
	ErrInvalidNegotiatedCodec = errors.New("sfu: invalid negotiated codec")
)

// EnablePublicationRTP enables one bounded, single-encoding fanout for a
// publication owned by the current session. The caller must authorize first.
func (r *Room) EnablePublicationRTP(participantID, session, publicationID string, maxSubscribers, queueSize int) error {
	return r.enablePublicationLayerRTP(participantID, session, publicationID, maxSubscribers, queueSize, rtpforward.LayerInfo{}, NegotiatedCodec{}, false)
}

// EnablePublicationRTPWithCodec atomically publishes transport codec metadata
// with a single-encoding fanout. Only trusted transports may supply these
// values; this validates negotiated metadata but cannot verify the bitstream.
func (r *Room) EnablePublicationRTPWithCodec(participantID, session, publicationID string, maxSubscribers, queueSize int, codec NegotiatedCodec) error {
	if !validNegotiatedCodec(codec) {
		return ErrInvalidNegotiatedCodec
	}
	return r.enablePublicationLayerRTP(participantID, session, publicationID, maxSubscribers, queueSize, rtpforward.LayerInfo{}, codec, false)
}

// EnablePublicationLayerRTPWithCodec adds an encoding to a ready publication.
// RID is the negotiated WebRTC encoding identifier and MaxBitrate is the
// publisher's configured encoding cap used by the downstream selector.
func (r *Room) EnablePublicationLayerRTPWithCodec(participantID, session, publicationID string, maxSubscribers, queueSize int, codec NegotiatedCodec, rid string, maxBitrate uint64) error {
	if !validNegotiatedCodec(codec) {
		return ErrInvalidNegotiatedCodec
	}
	return r.enablePublicationLayerRTP(participantID, session, publicationID, maxSubscribers, queueSize, rtpforward.LayerInfo{RID: rid, MaxBitrate: maxBitrate}, codec, true)
}

func validNegotiatedCodec(codec NegotiatedCodec) bool {
	return codec.MimeType != "" && len(codec.MimeType) <= 128 && codec.ClockRate != 0 && len(codec.FMTP) <= 4096 && !strings.ContainsAny(codec.MimeType+codec.FMTP, "\r\n\x00")
}

func (r *Room) enablePublicationLayerRTP(participantID, session, publicationID string, maxSubscribers, queueSize int, layer rtpforward.LayerInfo, codec NegotiatedCodec, additional bool) error {
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
	if codec.MimeType != "" && !strings.EqualFold(codec.MimeType, p.Codec) {
		return ErrInvalidNegotiatedCodec
	}
	if f := r.rtpPublications[publicationID]; f != nil {
		if !additional {
			return ErrMediaAlreadyEnabled
		}
		if p.Negotiated != codec {
			return ErrInvalidNegotiatedCodec
		}
		if err := f.AddLayer(layer); err != nil {
			if errors.Is(err, rtpforward.ErrInvalidLayer) {
				for _, existing := range f.Layers() {
					if existing.RID == layer.RID {
						return ErrMediaAlreadyEnabled
					}
				}
			}
			return err
		}
		return nil
	}
	if additional && layer.RID == "" {
		return rtpforward.ErrInvalidLayer
	}
	f, err := rtpforward.NewLayered(maxSubscribers, queueSize)
	if err != nil {
		return err
	}
	if err = f.AddLayer(layer); err != nil {
		f.Close()
		return err
	}
	if r.rtpPublications == nil {
		r.rtpPublications = make(map[string]*rtpforward.LayeredFanout)
	}
	r.rtpPublications[publicationID] = f
	p.RTPReady = true
	p.Negotiated = codec
	r.publications[publicationID] = p
	r.notify("track_ready")
	return nil
}

// WritePublicationRTP writes an RTP packet to a single-encoding publication.
func (r *Room) WritePublicationRTP(participantID, session, publicationID string, packet *rtp.Packet) error {
	return r.WritePublicationLayerRTP(participantID, session, publicationID, "", packet)
}

// WritePublicationLayerRTP validates the publisher session and writes one
// encoding's packet without holding the Room lock while it fans out.
func (r *Room) WritePublicationLayerRTP(participantID, session, publicationID, rid string, packet *rtp.Packet) error {
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
	return f.WriteRTP(rid, packet)
}

// RemovePublicationLayer ends one encoding without ending the publication
// while sibling RIDs remain. The bool reports that the publication ended.
func (r *Room) RemovePublicationLayer(participantID, session, publicationID, rid string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false, ErrRoomClosed
	}
	if err := r.checkSessionLocked(participantID, session); err != nil {
		return false, err
	}
	p, ok := r.publications[publicationID]
	if !ok || p.ParticipantID != participantID {
		return false, ErrPublicationNotFound
	}
	f := r.rtpPublications[publicationID]
	if f == nil {
		return false, ErrMediaNotEnabled
	}
	if err := f.RemoveLayer(rid); err != nil {
		return false, err
	}
	if len(f.Layers()) != 0 {
		r.notify("track_layer_removed")
		return false, nil
	}
	r.removePublication(publicationID)
	r.notify("track_unpublished")
	return true, nil
}

// OpenSubscriptionRTP requires an existing Room subscription and current
// session; caller is responsible for authorization and transport mapping.
func (r *Room) OpenSubscriptionRTP(participantID, session, publicationID string) (*rtpforward.LayeredSubscriber, error) {
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
		r.rtpSubscriptions = make(map[subscriptionKey]*rtpforward.LayeredSubscriber)
	}
	r.rtpSubscriptions[key] = sub
	return sub, nil
}

// UpdateSubscriberBandwidth applies one subscriber's GCC estimate to every
// open publication subscription. A zero estimate safely selects the lowest
// available RID.
func (r *Room) UpdateSubscriberBandwidth(participantID string, bitrate int) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrRoomClosed
	}
	if _, ok := r.participants[participantID]; !ok {
		r.mu.Unlock()
		return ErrParticipantNotFound
	}
	subs := make([]*rtpforward.LayeredSubscriber, 0)
	for key, sub := range r.rtpSubscriptions {
		if key.participantID == participantID {
			subs = append(subs, sub)
		}
	}
	r.mu.Unlock()
	if bitrate < 0 {
		bitrate = 0
	}
	for _, sub := range subs {
		if err := sub.SetTargetBitrate(uint64(bitrate)); err != nil && !errors.Is(err, rtpforward.ErrLayerSubscriberClosed) {
			return err
		}
	}
	return nil
}

// SubscriptionLayer reports the source RID currently selected for this
// subscriber and publication.
func (r *Room) SubscriptionLayer(participantID, session, publicationID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", ErrRoomClosed
	}
	if err := r.checkSessionLocked(participantID, session); err != nil {
		return "", err
	}
	if _, ok := r.subscriptions[subscriptionKey{participantID, publicationID}]; !ok {
		return "", ErrSubscriptionNotFound
	}
	sub := r.rtpSubscriptions[subscriptionKey{participantID, publicationID}]
	if sub == nil {
		return "", ErrMediaNotEnabled
	}
	return sub.SelectedLayer(), nil
}

func (r *Room) closeRTPSubscriptionLocked(key subscriptionKey) {
	if sub := r.rtpSubscriptions[key]; sub != nil {
		sub.Close()
		delete(r.rtpSubscriptions, key)
	}
}
