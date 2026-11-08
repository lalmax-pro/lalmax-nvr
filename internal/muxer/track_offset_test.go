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

func TestMP4TrackEditPreservesLateOpusOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offset.mp4")
	m := NewMP4Muxer(path)
	video, err := m.AddH264Track([]byte{0x67, 0x42, 0x00, 0x0a, 0xf8, 0x41, 0xa2}, []byte{0x68, 0xce, 0x38, 0x80})
	require.NoError(t, err)
	audio, err := m.AddAudioTrack("opus", nil)
	require.NoError(t, err)
	for i := range 10 {
		require.NoError(t, m.WriteSample(video, []byte{0x65, 0x88, 0x84, 0x00, 0x10}, time.Second+time.Duration(i)*40*time.Millisecond, 40*time.Millisecond))
		require.NoError(t, m.WriteAudioSample(audio, []byte{0xf8, 0xff, 0xfe}, 1200*time.Millisecond+time.Duration(i)*20*time.Millisecond, 20*time.Millisecond))
	}
	require.NoError(t, m.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	idx := bytes.Index(data, []byte("elst"))
	require.GreaterOrEqual(t, idx, 0)
	require.GreaterOrEqual(t, len(data), idx+36)
	elst := data[idx+4:]
	require.Equal(t, uint32(2), binary.BigEndian.Uint32(elst[4:8]))
	require.Equal(t, uint32(280), binary.BigEndian.Uint32(elst[8:12]), "200 ms track offset plus 80 ms Opus priming")
	require.Equal(t, uint32(0xffffffff), binary.BigEndian.Uint32(elst[12:16]), "first edit must be empty")
	require.Equal(t, uint32(120), binary.BigEndian.Uint32(elst[20:24]))
	require.Equal(t, uint32(80), binary.BigEndian.Uint32(elst[24:28]))
	if artifact := os.Getenv("SFU_OFFSET_TEST_MP4"); artifact != "" {
		require.NoError(t, os.WriteFile(artifact, data, 0o600))
	}
}

func TestMP4TrackEditPreservesLateVideoOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "late-video.mp4")
	m := NewMP4Muxer(path)
	video, err := m.AddH264Track([]byte{0x67, 0x42, 0, 0x0a, 0xf8, 0x41, 0xa2}, []byte{0x68, 0xce, 0x38, 0x80})
	require.NoError(t, err)
	audio, err := m.AddAudioTrack("g711", []byte{0, 0, 0, 0x1f, 0x40})
	require.NoError(t, err)
	require.NoError(t, m.WriteAudioSample(audio, []byte{0xd5}, time.Second, 20*time.Millisecond))
	require.NoError(t, m.WriteSample(video, []byte{0x65, 0x88, 0x84, 0, 0x10}, 1200*time.Millisecond, 40*time.Millisecond))
	require.NoError(t, m.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	idx := bytes.Index(data, []byte("elst"))
	require.GreaterOrEqual(t, idx, 0)
	elst := data[idx+4:]
	require.Equal(t, uint32(2), binary.BigEndian.Uint32(elst[4:8]))
	require.Equal(t, uint32(200), binary.BigEndian.Uint32(elst[8:12]))
	require.Equal(t, uint32(40), binary.BigEndian.Uint32(elst[20:24]))
	require.Zero(t, binary.BigEndian.Uint32(elst[24:28]))
}
