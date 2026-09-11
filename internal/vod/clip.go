package vod

import (
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/merge"
)

// ClipWindow maps a wall-clock [start,end] onto sample indices inside one recording.
func ClipWindow(info *merge.SegmentInfo, recStart, windowStart, windowEnd time.Time) (first, last int, ok bool) {
	if info == nil || len(info.Samples) == 0 || windowEnd.Before(windowStart) {
		return 0, 0, false
	}
	timescale := info.Timescale
	if timescale == 0 {
		timescale = 1000
	}
	startOffset := windowStart.Sub(recStart)
	endOffset := windowEnd.Sub(recStart)
	if startOffset < 0 {
		startOffset = 0
	}
	if endOffset <= 0 {
		return 0, 0, false
	}
	startTick := uint64(startOffset.Seconds() * float64(timescale))
	endTick := uint64(endOffset.Seconds() * float64(timescale))

	var decode uint64
	first = -1
	last = -1
	for i, s := range info.Samples {
		next := decode + uint64(s.Duration)
		if next > startTick && first < 0 {
			first = i
		}
		if decode < endTick {
			last = i
		}
		decode = next
		if decode >= endTick && first >= 0 {
			break
		}
	}
	if first < 0 || last < first {
		return 0, 0, false
	}
	return first, last, true
}

func FilterFragments(frags []FragmentRange, first, last int) []FragmentRange {
	if len(frags) == 0 {
		return nil
	}
	var out []FragmentRange
	for _, f := range frags {
		if f.Last < first || f.First > last {
			continue
		}
		clipped := f
		if clipped.First < first {
			clipped.First = first
		}
		if clipped.Last > last {
			clipped.Last = last
		}
		out = append(out, clipped)
	}
	return out
}
