package bridge

import (
	"container/heap"
	"errors"
	"time"

	"github.com/q191201771/lal/pkg/base"
)

var errAVQueueFull = errors.New("bridge: synchronized media queue full")

type queuedAV struct {
	packet   base.AvPacket
	arrival  time.Time
	sequence uint64
}
type avHeap []queuedAV

func (h avHeap) Len() int { return len(h) }
func (h avHeap) Less(i, j int) bool {
	if h[i].packet.Timestamp == h[j].packet.Timestamp {
		return h[i].sequence < h[j].sequence
	}
	return h[i].packet.Timestamp < h[j].packet.Timestamp
}
func (h avHeap) Swap(i, j int)   { h[i], h[j] = h[j], h[i] }
func (h *avHeap) Push(value any) { *h = append(*h, value.(queuedAV)) }
func (h *avHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = queuedAV{}
	*h = old[:n-1]
	return item
}

// avQueue bounds reordering and avoids sending regressing timestamps to lal.
// The caller periodically drains it even when only one track sends packets.
type avQueue struct {
	frames    avHeap
	lastMS    int64
	emitted   bool
	sequence  uint64
	audioMS   int64
	videoMS   int64
	audioSeen bool
	videoSeen bool
}

const avQueueLimit = 256
const avReorderWindow = 500 * time.Millisecond

func (q *avQueue) push(packet base.AvPacket, now time.Time, isVideo bool) error {
	if q.emitted && packet.Timestamp < q.lastMS {
		return nil
	}
	if len(q.frames) >= avQueueLimit {
		return errAVQueueFull
	}
	q.sequence++
	heap.Push(&q.frames, queuedAV{packet: packet, arrival: now, sequence: q.sequence})
	if isVideo {
		if !q.videoSeen || packet.Timestamp > q.videoMS {
			q.videoMS = packet.Timestamp
		}
		q.videoSeen = true
	} else {
		if !q.audioSeen || packet.Timestamp > q.audioMS {
			q.audioMS = packet.Timestamp
		}
		q.audioSeen = true
	}
	return nil
}

func (q *avQueue) drain(now time.Time, flush bool, sink func(base.AvPacket) error) error {
	for len(q.frames) > 0 {
		first := q.frames[0]
		// Both per-track watermarks must pass the frame before it is safe to
		// emit. A single leading track must not force a lagging track's valid
		// frames to be discarded. Bound the wait when a track is missing.
		ready := q.audioSeen && q.videoSeen && q.audioMS >= first.packet.Timestamp && q.videoMS >= first.packet.Timestamp
		if !flush && !ready && now.Sub(first.arrival) < avReorderWindow {
			return nil
		}
		heap.Pop(&q.frames)
		if q.emitted && first.packet.Timestamp < q.lastMS {
			continue
		}
		if err := sink(first.packet); err != nil {
			return err
		}
		q.lastMS = first.packet.Timestamp
		q.emitted = true
	}
	return nil
}
