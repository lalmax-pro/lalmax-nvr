// Package rtpforward provides a bounded synthetic RTP source for tests.
// It is not used by the LiveKit runtime or its production media path.
package rtpforward

import (
	"errors"
	"sync"

	"github.com/pion/rtp"
)

var (
	ErrClosed              = errors.New("rtp fanout closed")
	ErrSlowSubscriber      = errors.New("rtp subscriber queue full")
	ErrSubscriberClosed    = errors.New("rtp subscriber closed")
	ErrDuplicateSubscriber = errors.New("rtp subscriber already exists")
	ErrSubscriberLimit     = errors.New("rtp subscriber limit reached")
	ErrInvalidConfig       = errors.New("invalid rtp fanout configuration")
	ErrInvalidPacket       = errors.New("invalid or oversized rtp packet")
)

const maxPacketSize = 65535

// Fanout is owned by one publication. Authorization and WebRTC negotiation
// belong to the caller. It never sends RTP itself or rewrites RTP headers.
type Fanout struct {
	mu             sync.Mutex
	closed         bool
	maxSubscribers int
	queueSize      int
	subscribers    map[string]*Subscriber
}

// New requires explicit limits: at most 1024 subscribers and 256 packets per queue.
func New(maxSubscribers, queueSize int) (*Fanout, error) {
	if maxSubscribers < 1 || maxSubscribers > 1024 || queueSize < 1 || queueSize > 256 {
		return nil, ErrInvalidConfig
	}
	return &Fanout{maxSubscribers: maxSubscribers, queueSize: queueSize, subscribers: make(map[string]*Subscriber)}, nil
}

type Subscriber struct {
	owner   *Fanout
	id      string
	packets chan *rtp.Packet
	reason  error // protected by owner.mu
}

// Subscribe returns a channel owned by the fanout. Packets are independently
// cloned for each subscriber. Once closed, drain the queue and inspect Err.
func (f *Fanout) Subscribe(id string) (*Subscriber, error) {
	if id == "" {
		return nil, ErrInvalidConfig
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, ErrClosed
	}
	if _, ok := f.subscribers[id]; ok {
		return nil, ErrDuplicateSubscriber
	}
	if len(f.subscribers) >= f.maxSubscribers {
		return nil, ErrSubscriberLimit
	}
	sub := &Subscriber{owner: f, id: id, packets: make(chan *rtp.Packet, f.queueSize)}
	f.subscribers[id] = sub
	return sub, nil
}
func (s *Subscriber) Packets() <-chan *rtp.Packet { return s.packets }

// Err reports the terminal reason; nil means the subscriber is still active.
func (s *Subscriber) Err() error { s.owner.mu.Lock(); defer s.owner.mu.Unlock(); return s.reason }

// Close is idempotent and never removes a later subscriber with the same ID.
func (s *Subscriber) Close() {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	s.owner.removeLocked(s, ErrSubscriberClosed)
}
func (f *Fanout) removeLocked(s *Subscriber, reason error) {
	if f.subscribers[s.id] != s {
		return
	}
	delete(f.subscribers, s.id)
	s.reason = reason
	close(s.packets)
}

// WriteRTP copies packet data before returning. A full subscriber queue is
// disconnected without blocking the publisher or other subscribers. The caller
// must not concurrently mutate the input packet during this call.
func (f *Fanout) WriteRTP(packet *rtp.Packet) error {
	if packet == nil || packet.MarshalSize() > maxPacketSize {
		return ErrInvalidPacket
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	for _, sub := range f.subscribers {
		select {
		case sub.packets <- packet.Clone():
		default:
			f.removeLocked(sub, ErrSlowSubscriber)
		}
	}
	return nil
}

// Close closes all subscriber queues. It is safe to call repeatedly.
func (f *Fanout) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = true
	for _, sub := range f.subscribers {
		f.removeLocked(sub, ErrClosed)
	}
}
