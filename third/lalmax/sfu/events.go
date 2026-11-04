package sfu

// Event is a complete post-change snapshot. Sequence is scoped to Generation.
// This is an internal state feed, not an authorization-filtered signaling API.
type Event struct {
	Type     string
	Snapshot Snapshot
}

// Watch atomically captures a snapshot and registers for subsequent changes.
// The returned channel is bounded; its closure means the caller must discard
// its local view and reconnect with a new snapshot. Cancel releases the watch.
// buffer must be between 1 and 256 to limit memory per watcher.
func (r *Room) Watch(buffer int) (Snapshot, <-chan Event, func(), error) {
	if buffer < 1 || buffer > 256 {
		return Snapshot{}, nil, nil, ErrInvalidWatchBuffer
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Snapshot{}, nil, nil, ErrRoomClosed
	}
	if r.watchers == nil {
		r.watchers = make(map[chan Event]struct{})
	}
	ch := make(chan Event, buffer)
	r.watchers[ch] = struct{}{}
	cancel := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if _, ok := r.watchers[ch]; ok {
			delete(r.watchers, ch)
			close(ch)
		}
	}
	return r.snapshotLocked(), ch, cancel, nil
}

// notify is called under the room lock after a successful mutation.
func (r *Room) notify(kind string) {
	r.sequence++
	if len(r.watchers) == 0 {
		return
	}
	for ch := range r.watchers {
		event := Event{Type: kind, Snapshot: r.snapshotLocked()}
		select {
		case ch <- event:
		default:
			close(ch)
			delete(r.watchers, ch)
		}
	}
}
