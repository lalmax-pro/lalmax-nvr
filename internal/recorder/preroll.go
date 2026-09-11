package recorder

import "sync"

type prerollFrame struct {
	data []byte
	key  bool
}

// prerollRing keeps the last N keyframe-aligned access units while recording is paused.
type prerollRing struct {
	mu      sync.Mutex
	maxGOPs int
	frames  []prerollFrame
	gops    int
}

func newPrerollRing(maxGOPs int) *prerollRing {
	if maxGOPs <= 0 {
		maxGOPs = 2
	}
	return &prerollRing{maxGOPs: maxGOPs}
}

func (r *prerollRing) Push(data []byte, key bool) {
	if r == nil || len(data) == 0 {
		return
	}
	cp := append([]byte(nil), data...)
	r.mu.Lock()
	defer r.mu.Unlock()
	if key {
		r.gops++
		for r.gops > r.maxGOPs && len(r.frames) > 0 {
			r.frames = r.frames[1:]
			if len(r.frames) == 0 || r.frames[0].key {
				r.gops--
			}
		}
		if r.gops > r.maxGOPs {
			r.gops = r.maxGOPs
		}
	}
	r.frames = append(r.frames, prerollFrame{data: cp, key: key})
}

func (r *prerollRing) Drain() [][]byte {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]byte, 0, len(r.frames))
	for _, f := range r.frames {
		out = append(out, f.data)
	}
	r.frames = r.frames[:0]
	r.gops = 0
	return out
}
