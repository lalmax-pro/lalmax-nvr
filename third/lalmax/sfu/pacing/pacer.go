// Package pacing provides a bounded, blocking RTP pacer for a single peer.
package pacing

import (
	"errors"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

var (
	ErrClosed        = errors.New("pacing: closed")
	ErrUnknownStream = errors.New("pacing: unknown stream")
	ErrOverloaded    = errors.New("pacing: delay or pending limit exceeded")
	ErrInvalidPacket = errors.New("pacing: invalid RTP packet")
)

type binding struct {
	writer     interceptor.RTPWriter
	generation uint64
	done       chan struct{}
}

// Pacer schedules outgoing RTP across a peer's streams. It does not copy or
// queue packets: callers wait for their reservation and the number and duration of
// waiting callers are bounded. Call RemoveStream when a sender is unbound.
type Pacer struct {
	mu         sync.Mutex
	streams    map[uint32]binding
	generation uint64
	rate       int
	next       time.Time
	maxDelay   time.Duration
	maxPending int
	pending    int
	closed     bool
	done       chan struct{}
}

func New(initialBitrate int, maxDelay time.Duration, maxPending int) (*Pacer, error) {
	if initialBitrate <= 0 || maxDelay <= 0 || maxPending <= 0 {
		return nil, errors.New("pacing: invalid configuration")
	}
	return &Pacer{streams: make(map[uint32]binding), rate: initialBitrate, maxDelay: maxDelay, maxPending: maxPending, done: make(chan struct{})}, nil
}
func (p *Pacer) AddStream(ssrc uint32, writer interceptor.RTPWriter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if old, ok := p.streams[ssrc]; ok {
		close(old.done)
	}
	p.generation++
	p.streams[ssrc] = binding{writer: writer, generation: p.generation, done: make(chan struct{})}
}
func (p *Pacer) RemoveStream(ssrc uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.streams[ssrc]; ok {
		close(old.done)
		delete(p.streams, ssrc)
	}
}
func (p *Pacer) SetTargetBitrate(rate int) {
	if rate < 1 {
		rate = 1
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rate = rate
}
func (p *Pacer) Write(header *rtp.Header, payload []byte, attrs interceptor.Attributes) (int, error) {
	if header == nil {
		return 0, ErrInvalidPacket
	}
	size := header.MarshalSize() + len(payload) + int(header.PaddingSize)
	if size > 65535 {
		return 0, ErrInvalidPacket
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0, ErrClosed
	}
	stream, ok := p.streams[header.SSRC]
	if !ok || stream.writer == nil {
		p.mu.Unlock()
		return 0, ErrUnknownStream
	}
	now := time.Now()
	at := p.next
	if at.Before(now) {
		at = now
	}
	if at.Sub(now) > p.maxDelay || p.pending >= p.maxPending {
		p.mu.Unlock()
		return 0, ErrOverloaded
	}
	// RTP packets are bounded above, so this calculation cannot overflow int64.
	bits := int64(size) * 8
	slot := time.Duration(bits * int64(time.Second) / int64(p.rate))
	if slot > p.maxDelay {
		p.mu.Unlock()
		return 0, ErrOverloaded
	}
	p.next = at.Add(slot)
	p.pending++
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.pending--; p.mu.Unlock() }()
	if delay := time.Until(at); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-p.done:
			return 0, ErrClosed
		case <-stream.done:
			p.mu.Lock()
			closed := p.closed
			p.mu.Unlock()
			if closed {
				return 0, ErrClosed
			}
			return 0, ErrUnknownStream
		}
	}
	p.mu.Lock()
	current, ok := p.streams[header.SSRC]
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return 0, ErrClosed
	}
	if !ok || current.generation != stream.generation {
		return 0, ErrUnknownStream
	}
	return stream.writer.Write(header, payload, attrs)
}
func (p *Pacer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		for _, stream := range p.streams {
			close(stream.done)
		}
		clear(p.streams)
		close(p.done)
	}
	return nil
}
