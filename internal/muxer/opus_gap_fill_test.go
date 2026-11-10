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
	require.Len(t, m.findTrackLocked(audio).samples, 25, "four missing packets become silence")
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
	unfilled, err := m.WriteOpusSampleWithGapFill(audio, []byte{0xf8, 0xff, 0xfe}, 21250*time.Microsecond, 20*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, 1250*time.Microsecond, unfilled)
	unfilled, err = m.WriteOpusSampleWithGapFill(audio, []byte{0xf8, 0xff, 0xfe}, 4*time.Second, 20*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, 3958750*time.Microsecond, unfilled)
	_, err = m.WriteOpusSampleWithGapFill(audio, []byte{0xf8, 0xff, 0xfe}, 3*time.Second, 20*time.Millisecond)
	require.ErrorContains(t, err, "regressed")
	require.Len(t, m.findTrackLocked(audio).samples, 4)
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
	require.Len(t, m.findTrackLocked(audio).samples, 4, "rounding must not invent silence")
	require.NoError(t, m.Close())
}

func TestOpusGapFillVariableFrameDurations(t *testing.T) {
	cases := []struct {
		name string
		gap  time.Duration
		want []time.Duration
	}{
		{"fractional", 2500 * time.Microsecond, []time.Duration{2500 * time.Microsecond}},
		{"rounded", 2 * time.Millisecond, []time.Duration{2500 * time.Microsecond}},
		{"five", 5 * time.Millisecond, []time.Duration{5 * time.Millisecond}},
		{"ten", 10 * time.Millisecond, []time.Duration{10 * time.Millisecond}},
		{"mixed", 37500 * time.Microsecond, []time.Duration{20 * time.Millisecond, 10 * time.Millisecond, 5 * time.Millisecond, 2500 * time.Microsecond}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMP4Muxer(filepath.Join(t.TempDir(), "gap.mp4"))
			audio, err := m.AddAudioTrack("opus", nil)
			require.NoError(t, err)
			unfilled, err := m.WriteOpusSampleWithGapFill(audio, []byte{0xf8, 0xff, 0xfe}, 0, 20*time.Millisecond)
			require.NoError(t, err)
			require.Zero(t, unfilled)
			unfilled, err = m.WriteOpusSampleWithGapFill(audio, []byte{0xf8, 0xff, 0xfe}, 20*time.Millisecond+tc.gap, 20*time.Millisecond)
			require.NoError(t, err)
			require.Zero(t, unfilled)
			samples := m.findTrackLocked(audio).samples
			require.Len(t, samples, 3+len(tc.want))
			for i, want := range tc.want {
				require.Equal(t, want, samples[i+2].duration)
			}
			require.NoError(t, m.Close())
		})
	}
}

func TestOpusGapFillDecoderFixture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "variable-gaps.mp4")
	m := NewMP4Muxer(path)
	audio, err := m.AddAudioTrack("opus", nil)
	require.NoError(t, err)
	for i := range 8 {
		pts := time.Duration(i) * 57500 * time.Microsecond
		unfilled, err := m.WriteOpusSampleWithGapFill(audio, []byte{0xf8, 0xff, 0xfe}, pts, 20*time.Millisecond)
		require.NoError(t, err)
		require.Zero(t, unfilled)
	}
	require.NoError(t, m.Close())
	if artifact := os.Getenv("SFU_VARIABLE_GAP_TEST_MP4"); artifact != "" {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(artifact, data, 0o600))
	}
}

func TestOpusGapFillCapsPacketCount(t *testing.T) {
	m := NewMP4Muxer(filepath.Join(t.TempDir(), "bounded.mp4"))
	audio, err := m.AddAudioTrack("opus", nil)
	require.NoError(t, err)
	_, err = m.WriteOpusSampleWithGapFill(audio, []byte{0xf8, 0xff, 0xfe}, 0, 20*time.Millisecond)
	require.NoError(t, err)
	unfilled, err := m.WriteOpusSampleWithGapFill(audio, []byte{0xf8, 0xff, 0xfe}, 2020*time.Millisecond, 20*time.Millisecond)
	require.NoError(t, err)
	require.Zero(t, unfilled)
	require.Len(t, m.findTrackLocked(audio).samples, 23, "two seconds need sixteen 120 ms packets and four 20 ms packets")
	require.NoError(t, m.Close())
}

func TestOpusGapFillLongAndBounded(t *testing.T) {
	m := NewMP4Muxer(filepath.Join(t.TempDir(), "long-gap.mp4"))
	audio, err := m.AddAudioTrack("opus", nil)
	require.NoError(t, err)
	packet := []byte{0xf8, 0xff, 0xfe}
	_, err = m.WriteOpusSampleWithGapFill(audio, packet, 0, 20*time.Millisecond)
	require.NoError(t, err)
	unfilled, err := m.WriteOpusSampleWithGapFill(audio, packet, 60*time.Second+20*time.Millisecond, 20*time.Millisecond)
	require.NoError(t, err)
	require.Zero(t, unfilled)
	require.Len(t, m.findTrackLocked(audio).samples, 503, "60 seconds is bounded to 500 filler packets")
	unfilled, err = m.WriteOpusSampleWithGapFill(audio, packet, 121*time.Second+40*time.Millisecond, 20*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, 61*time.Second, unfilled)
	require.Len(t, m.findTrackLocked(audio).samples, 504, "unsupported gaps must not expand into unbounded packets")
	require.NoError(t, m.Close())
}
