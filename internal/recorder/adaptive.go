package recorder

import (
	"sync"
	"time"
)

const adaptiveWindow = 32

// AdaptiveGate decides whether a video frame should be written to disk.
// Calm: only periodic IDR frames. Active: all frames until hold expires.
type AdaptiveGate struct {
	interval    time.Duration
	spikeFactor float64

	mu            sync.Mutex
	sizes         []int
	lastSparseIDR time.Time
	activeUntil   time.Time
}

func NewAdaptiveGate(interval time.Duration) *AdaptiveGate {
	if interval < 5*time.Second {
		interval = 30 * time.Second
	}
	return &AdaptiveGate{
		interval:    interval,
		spikeFactor: 5,
	}
}

// Trigger marks the stream as active until now+hold.
func (g *AdaptiveGate) Trigger(hold time.Duration) {
	if g == nil {
		return
	}
	if hold <= 0 {
		hold = 30 * time.Second
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	until := time.Now().Add(hold)
	if until.After(g.activeUntil) {
		g.activeUntil = until
	}
}

// ObservePFrame records a P-frame size and may promote the gate to active.
func (g *AdaptiveGate) ObservePFrame(size int) {
	if g == nil || size <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sizes = append(g.sizes, size)
	if len(g.sizes) > adaptiveWindow {
		g.sizes = g.sizes[len(g.sizes)-adaptiveWindow:]
	}
	if len(g.sizes) < 8 {
		return
	}
	median := medianInt(g.sizes)
	if median > 0 && float64(size) >= float64(median)*g.spikeFactor {
		until := time.Now().Add(30 * time.Second)
		if until.After(g.activeUntil) {
			g.activeUntil = until
		}
	}
}

// ShouldWrite reports whether this frame should be muxed.
// Parameter sets (SPS/PPS) should always be written by the caller before this check.
func (g *AdaptiveGate) ShouldWrite(isIDR bool, now time.Time) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Before(g.activeUntil) {
		return true
	}
	if !isIDR {
		return false
	}
	if g.lastSparseIDR.IsZero() || now.Sub(g.lastSparseIDR) >= g.interval {
		g.lastSparseIDR = now
		return true
	}
	return false
}

func medianInt(vals []int) int {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]int(nil), vals...)
	// insertion sort is enough for tiny windows
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0 && cp[j] < cp[j-1]; j-- {
			cp[j], cp[j-1] = cp[j-1], cp[j]
		}
	}
	return cp[len(cp)/2]
}
