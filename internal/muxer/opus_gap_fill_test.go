package muxer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpusGapFillUsesBoundedSilence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gap-fill.mp4")
	m := NewMP4Muxer(path)
	video, err := m.AddH264Track([]byte{0x67, 0x42, 0, 0x0a, 0xf8, 0x41, 0xa2}, []byte{0x68, 0xce, 0x38, 0x80})
	require.NoError(t, err)
	audio, err := m.AddAudioTrack("opus", nil)
	require.NoError(t, err)
	for i := range 25 {
		require.NoError(t, m.WriteSample(video, []byte{0x65, 0x88, 0x84, 0, 0x10}, time.Duration(i)*20*time.Millisecond, 20*time.Millisecond))
	}
	for i := 1; i <= 20; i++ {
		pts := time.Duration(i) * 20 * time.Millisecond
		if i > 10 {
			pts += 80 * time.Millisecond
		}
		unfilled, err := m.WriteOpusSampleWithGapFill(audio, []byte{0xf8, 0xff, 0xfe}, pts, 20*time.Millisecond)
		require.NoError(t, err)
		require.Zero(t, unfilled)
	}
	require.Len(t, m.findTrackLocked(audio).samples, 24, "four missing packets become silence")
	require.NoError(t, m.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	if artifact := os.Getenv("SFU_GAP_FILL_TEST_MP4"); artifact != "" {
		require.NoError(t, os.WriteFile(artifact, data, 0o600))
	}
}

func TestOpusGapFillReportsUnsupportedAndRejectsRegression(t *testing.T) {
	m := NewMP4Muxer(filepath.Join(t.TempDir(), "unsupported.mp4"))
	audio, err := m.AddAudioTrack("opus", nil)
	require.NoError(t, err)
	_, err = m.WriteOpusSampleWithGapFill(audio, []byte{0xf8, 0xff, 0xfe}, 0, 20*time.Millisecond)
	require.NoError(t, err)
	unfilled, err := m.WriteOpusSampleWithGapFill(audio, []byte{0xf8, 0xff, 0xfe}, 25*time.Millisecond, 20*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, 5*time.Millisecond, unfilled)
	unfilled, err = m.WriteOpusSampleWithGapFill(audio, []byte{0xf8, 0xff, 0xfe}, 4*time.Second, 20*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, 3955*time.Millisecond, unfilled)
	_, err = m.WriteOpusSampleWithGapFill(audio, []byte{0xf8, 0xff, 0xfe}, 3*time.Second, 20*time.Millisecond)
	require.ErrorContains(t, err, "regressed")
	require.Len(t, m.findTrackLocked(audio).samples, 3)
	require.NoError(t, m.Close())
}

func TestOpusGapFillToleratesMillisecondRounding(t *testing.T) {
	m := NewMP4Muxer(filepath.Join(t.TempDir(), "rounding.mp4"))
	audio, err := m.AddAudioTrack("opus", nil)
	require.NoError(t, err)
	for _, pts := range []time.Duration{0, 21 * time.Millisecond, 41 * time.Millisecond} {
		unfilled, err := m.WriteOpusSampleWithGapFill(audio, []byte{0xf8, 0xff, 0xfe}, pts, 20*time.Millisecond)
		require.NoError(t, err)
		require.Zero(t, unfilled)
	}
	require.Len(t, m.findTrackLocked(audio).samples, 3, "rounding must not invent silence")
	require.NoError(t, m.Close())
}
