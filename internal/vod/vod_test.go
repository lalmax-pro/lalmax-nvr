package vod

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/merge"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/muxer"
	"github.com/stretchr/testify/require"
)

func createTinyH264(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "vod_h264.mp4")
	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH264Track(sps, pps)
	require.NoError(t, err)
	require.NoError(t, m.WriteSample(trackID, []byte{0x65, 0x88, 0x80, 0x40}, 0, 33*time.Millisecond))
	require.NoError(t, m.WriteSample(trackID, []byte{0x41, 0x10, 0x00, 0x0c}, 33*time.Millisecond, 33*time.Millisecond))
	require.NoError(t, m.WriteSample(trackID, []byte{0x65, 0x88, 0x80, 0x41}, 7*time.Second, 33*time.Millisecond))
	require.NoError(t, m.Close())
	return path
}

func TestSplitFragments_SplitsOnKeyframeAfterTarget(t *testing.T) {
	path := createTinyH264(t, t.TempDir())
	info, err := merge.ParseSegment(path)
	require.NoError(t, err)
	frags := SplitFragments(info)
	require.GreaterOrEqual(t, len(frags), 1)
	require.Equal(t, 0, frags[0].First)
	if len(frags) > 1 {
		require.True(t, info.Samples[frags[1].First].IsKeyFrame)
	}
}

func TestWriteInitAndFragment(t *testing.T) {
	path := createTinyH264(t, t.TempDir())
	info, err := merge.ParseSegment(path)
	require.NoError(t, err)
	info.FilePath = path

	var initBuf bytes.Buffer
	require.NoError(t, WriteInit(&initBuf, info))
	require.Greater(t, initBuf.Len(), 32)
	require.Contains(t, string(initBuf.Bytes()[4:8]), "ftyp")

	frags := SplitFragments(info)
	require.NotEmpty(t, frags)
	var fragBuf bytes.Buffer
	require.NoError(t, WriteFragment(&fragBuf, info, 1, frags[0].First, frags[0].Last))
	require.Greater(t, fragBuf.Len(), 16)
	require.Contains(t, string(fragBuf.Bytes()[4:8]), "moof")
}

func TestBuildPlaylist_PDTAndDiscontinuity(t *testing.T) {
	t1 := time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 3, 10, 8, 10, 0, 0, time.UTC)
	items := []PlaylistItem{
		{
			Recording: &model.Recording{ID: "a/b", StartedAt: t1, Format: model.FormatH264},
			Frags:     []FragmentRange{{First: 0, Last: 10, Duration: 6 * time.Second}},
		},
		{
			Recording: &model.Recording{ID: "c", StartedAt: t2, Format: model.FormatH264},
			Frags:     []FragmentRange{{First: 0, Last: 4, Duration: 3 * time.Second}},
		},
	}
	body := BuildPlaylist("cam1", items, "/api/cameras/cam1/playback/{recId}")
	require.Contains(t, body, "#EXT-X-DISCONTINUITY")
	require.Contains(t, body, "#EXT-X-PROGRAM-DATE-TIME:")
	require.Contains(t, body, "#EXT-X-MAP:URI=")
	require.Contains(t, body, "a%2Fb")
	require.True(t, strings.Contains(body, "f0-10.m4s"))
	require.True(t, strings.HasSuffix(strings.TrimSpace(body), "#EXT-X-ENDLIST"))
}

func TestIsVODFormat(t *testing.T) {
	require.True(t, IsVODFormat(model.FormatH264))
	require.True(t, IsVODFormat(model.FormatH265))
	require.False(t, IsVODFormat(model.FormatMJPEG))
}
