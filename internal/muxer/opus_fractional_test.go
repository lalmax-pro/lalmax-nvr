package muxer

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpusFractionalFrameDurationsUse48kClock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fractional-opus.mp4")
	m := NewMP4Muxer(path)
	audio, err := m.AddAudioTrack("opus", nil)
	require.NoError(t, err)
	for i := range 400 {
		require.NoError(t, m.WriteAudioSample(audio, []byte{0xe0, 0xff, 0xfe}, time.Duration(i)*2500*time.Microsecond, 2500*time.Microsecond))
	}
	require.NoError(t, m.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	mdhd := bytes.Index(data, []byte("mdhd"))
	require.GreaterOrEqual(t, mdhd, 0)
	require.GreaterOrEqual(t, len(data), mdhd+24)
	require.Equal(t, uint32(48000), binary.BigEndian.Uint32(data[mdhd+16:mdhd+20]))
	require.Equal(t, uint32(51840), binary.BigEndian.Uint32(data[mdhd+20:mdhd+24]), "one second of source plus 80 ms of priming")
	stts := bytes.Index(data, []byte("stts"))
	require.GreaterOrEqual(t, stts, 0)
	require.GreaterOrEqual(t, len(data), stts+28)
	require.Equal(t, uint32(120), binary.BigEndian.Uint32(data[stts+24:stts+28]), "2.5 ms equals 120 ticks at 48 kHz")
	edit := bytes.Index(data, []byte("elst"))
	require.GreaterOrEqual(t, edit, 0)
	require.GreaterOrEqual(t, len(data), edit+32)
	require.Equal(t, uint32(3840), binary.BigEndian.Uint32(data[edit+28:edit+32]), "80 ms pre-skip uses Opus media ticks")
	if artifact := os.Getenv("SFU_FRACTIONAL_TEST_MP4"); artifact != "" {
		require.NoError(t, os.WriteFile(artifact, data, 0o600))
	}
}

func TestOpusLongTrackUsesVersionOneMediaDuration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long-opus.mp4")
	m := NewMP4Muxer(path)
	audio, err := m.AddAudioTrack("opus", nil)
	require.NoError(t, err)
	// Synthetic oversized samples exercise 64-bit metadata without a 25-hour test.
	for i := range 2 {
		require.NoError(t, m.WriteAudioSample(audio, []byte{0xf8, 0xff, 0xfe}, time.Duration(i)*45000*time.Second, 45000*time.Second))
	}
	require.NoError(t, m.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	mdhd := bytes.Index(data, []byte("mdhd"))
	require.GreaterOrEqual(t, mdhd, 0)
	require.GreaterOrEqual(t, len(data), mdhd+36)
	require.Equal(t, byte(1), data[mdhd+4], "media header must use 64-bit duration")
	require.Equal(t, uint32(48000), binary.BigEndian.Uint32(data[mdhd+24:mdhd+28]))
	require.Equal(t, uint64(4320003840), binary.BigEndian.Uint64(data[mdhd+28:mdhd+36]))
}
