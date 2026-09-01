package vod

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/merge"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

// PlaylistItem is one recording included in a VOD playlist.
type PlaylistItem struct {
	Recording *model.Recording
	Info      *merge.SegmentInfo
	Frags     []FragmentRange
}

// IsVODFormat reports whether the recording can be served as fMP4 VOD.
func IsVODFormat(format model.Format) bool {
	switch format {
	case model.FormatH264, model.FormatH265, "timelapse":
		return true
	default:
		return false
	}
}

// BuildPlaylist renders an HLS VOD media playlist covering the given items in time order.
// fragmentBase is a sprintf pattern with %s=recordingID.
func BuildPlaylist(cameraID string, items []PlaylistItem, fragmentBase string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:7\n")
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")

	target := int(TargetFragmentDur.Seconds())
	if target < 1 {
		target = 6
	}
	for _, it := range items {
		for _, f := range it.Frags {
			sec := int(f.Duration.Seconds() + 0.999)
			if sec > target {
				target = sec
			}
		}
	}
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", target)

	for i, it := range items {
		if it.Recording == nil || len(it.Frags) == 0 {
			continue
		}
		if i > 0 {
			// Separate init MAP (and any wall-clock gap) requires a discontinuity.
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		if !it.Recording.StartedAt.IsZero() {
			fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", it.Recording.StartedAt.UTC().Format(time.RFC3339Nano))
		}
		recEsc := url.PathEscape(it.Recording.ID)
		initURI := strings.ReplaceAll(fragmentBase, "{recId}", recEsc) + "/init.mp4"
		fmt.Fprintf(&b, "#EXT-X-MAP:URI=\"%s\"\n", initURI)
		for _, f := range it.Frags {
			fmt.Fprintf(&b, "#EXTINF:%.3f,\n", f.Duration.Seconds())
			fmt.Fprintf(&b, "%s/f%d-%d.m4s\n", strings.ReplaceAll(fragmentBase, "{recId}", recEsc), f.First, f.Last)
		}
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	_ = cameraID
	return b.String()
}
