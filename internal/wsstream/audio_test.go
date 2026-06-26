package wsstream

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAudioCodecInfoRoundTrip(t *testing.T) {
	in := &AudioCodecInfo{
		Codec:      AudioCodecAAC,
		SampleRate: 44100,
		Channels:   2,
		Config:     []byte{0x12, 0x10}, // AAC-LC 44100 stereo ASC
	}
	data, err := EncodeAudioCodecInfo(in)
	require.NoError(t, err)
	require.Equal(t, MsgTypeAudioCodecInfo, data[0])

	out, err := DecodeAudioCodecInfo(data)
	require.NoError(t, err)
	assert.Equal(t, in.Codec, out.Codec)
	assert.Equal(t, in.SampleRate, out.SampleRate)
	assert.Equal(t, in.Channels, out.Channels)
	assert.Equal(t, in.Config, out.Config)
}

func TestAudioFrameRoundTrip(t *testing.T) {
	in := &AudioFrame{PTS: 1234567, Data: []byte{0xDE, 0xAD, 0xBE, 0xEF}}
	data, err := EncodeAudioFrame(in)
	require.NoError(t, err)
	require.Equal(t, MsgTypeAudioFrame, data[0])

	out, err := DecodeAudioFrame(data)
	require.NoError(t, err)
	assert.Equal(t, in.PTS, out.PTS)
	assert.Equal(t, in.Data, out.Data)
}

func TestBuildAudioCodecInfo_AAC(t *testing.T) {
	// ASC 0x12 0x10 = AAC-LC (objType 2), freqIdx 4 (44100), channelConfig 2.
	aci, ok := BuildAudioCodecInfo("aac", []byte{0x12, 0x10})
	require.True(t, ok)
	assert.Equal(t, AudioCodecAAC, aci.Codec)
	assert.Equal(t, uint32(44100), aci.SampleRate)
	assert.Equal(t, byte(2), aci.Channels)
	assert.Equal(t, []byte{0x12, 0x10}, aci.Config)
}

func TestBuildAudioCodecInfo_AAC_Mono8k(t *testing.T) {
	// ASC for AAC-LC (objType 2), freqIdx 11 (8000), channelConfig 1.
	// bits: 00010 1011 0001 0... => bytes 0x15 0x88
	aci, ok := BuildAudioCodecInfo("aac", []byte{0x15, 0x88})
	require.True(t, ok)
	assert.Equal(t, uint32(8000), aci.SampleRate)
	assert.Equal(t, byte(1), aci.Channels)
}

func TestBuildAudioCodecInfo_G711(t *testing.T) {
	// A-law: muLaw byte 0, rate 8000.
	alaw, ok := BuildAudioCodecInfo("g711", []byte{0, 0, 0, 0x1F, 0x40})
	require.True(t, ok)
	assert.Equal(t, AudioCodecG711A, alaw.Codec)
	assert.Equal(t, uint32(8000), alaw.SampleRate)
	assert.Equal(t, byte(1), alaw.Channels)
	assert.Nil(t, alaw.Config)

	// µ-law: muLaw byte 1.
	ulaw, ok := BuildAudioCodecInfo("g711", []byte{1, 0, 0, 0x1F, 0x40})
	require.True(t, ok)
	assert.Equal(t, AudioCodecG711U, ulaw.Codec)
}

func TestBuildAudioCodecInfo_None(t *testing.T) {
	_, ok := BuildAudioCodecInfo("", nil)
	assert.False(t, ok)
	_, ok = BuildAudioCodecInfo("opus", nil)
	assert.False(t, ok)
}
