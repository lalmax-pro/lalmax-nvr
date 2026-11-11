// Package layerselect selects a simulcast layer for one downstream peer.
package layerselect

import (
	"sort"
	"time"
)

// Layer describes a negotiated source encoding. MaxBitrate is the sender's
// configured cap, not a measured wire rate.
type Layer struct {
	RID        string
	MaxBitrate uint64
}

const (
	LowRID  = "l"
	MidRID  = "m"
	HighRID = "h"

	LowMaxBitrate  uint64 = 150_000
	MidMaxBitrate  uint64 = 500_000
	HighMaxBitrate uint64 = 1_500_000

	upgradeHeadroomPercent = 125
	selectionBudgetPercent = 80
	upgradeStability       = 2 * time.Second
)

// PresetMaxBitrate returns the configured cap for the browser sender's
// standard camera encodings. Unknown RIDs are not assigned an estimated cap.
func PresetMaxBitrate(rid string) (uint64, bool) {
	switch rid {
	case LowRID:
		return LowMaxBitrate, true
	case MidRID:
		return MidMaxBitrate, true
	case HighRID:
		return HighMaxBitrate, true
	default:
		return 0, false
	}
}

// Selector keeps the upgrade candidate stable before raising quality. A
// downgrade is immediate when the current layer no longer fits the estimate.
type Selector struct {
	candidate string
	since     time.Time
}

// Choose returns the selected RID. The lowest layer is used when the estimate
// is unknown or no layer fits. Upgrades require two seconds of spare capacity;
// a layer removal is applied immediately.
func (s *Selector) Choose(current string, targetBitrate uint64, layers []Layer, now time.Time) string {
	ordered := append([]Layer(nil), layers...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].MaxBitrate == ordered[j].MaxBitrate {
			return ordered[i].RID < ordered[j].RID
		}
		return ordered[i].MaxBitrate < ordered[j].MaxBitrate
	})
	if len(ordered) == 0 {
		s.reset()
		return ""
	}
	currentIndex := -1
	for i, layer := range ordered {
		if layer.RID == current {
			currentIndex = i
			break
		}
	}
	if currentIndex < 0 {
		s.reset()
		return affordableLayer(ordered, targetBitrate).RID
	}
	if targetBitrate == 0 {
		s.reset()
		return ordered[0].RID
	}

	candidate := affordableLayer(ordered, targetBitrate)
	candidateIndex := 0
	for i := range ordered {
		if ordered[i].RID == candidate.RID {
			candidateIndex = i
			break
		}
	}
	if candidateIndex < currentIndex {
		s.reset()
		return candidate.RID
	}
	if candidate.RID == current {
		s.reset()
		return current
	}
	if targetBitrate < candidate.MaxBitrate*upgradeHeadroomPercent/100 {
		s.reset()
		return current
	}
	if s.candidate != candidate.RID {
		s.candidate = candidate.RID
		s.since = now
		return current
	}
	if now.Sub(s.since) < upgradeStability {
		return current
	}
	s.reset()
	return candidate.RID
}

func affordableLayer(layers []Layer, targetBitrate uint64) Layer {
	if targetBitrate == 0 {
		return layers[0]
	}
	budget := targetBitrate/100*selectionBudgetPercent + targetBitrate%100*selectionBudgetPercent/100
	selected := layers[0]
	for _, layer := range layers {
		if layer.MaxBitrate > budget {
			break
		}
		selected = layer
	}
	return selected
}

func (s *Selector) reset() { s.candidate, s.since = "", time.Time{} }
