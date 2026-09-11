package event

import (
	"sync"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

// LiveHub fans persisted product events out to SSE subscribers.
type LiveHub struct {
	mu   sync.RWMutex
	subs map[chan model.Event]struct{}
}

func NewLiveHub() *LiveHub {
	return &LiveHub{subs: make(map[chan model.Event]struct{})}
}

func (h *LiveHub) Subscribe() chan model.Event {
	ch := make(chan model.Event, 32)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *LiveHub) Unsubscribe(ch chan model.Event) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *LiveHub) Publish(ev model.Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
