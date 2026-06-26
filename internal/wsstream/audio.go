package wsstream

import "encoding/binary"

// aacSampleRates is the MPEG-4 sampling frequency table indexed by the 4-bit
// samplingFrequencyIndex field of an AudioSpecificConfig.
var aacSampleRates = [...]uint32{
	96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050,
	16000, 12000, 11025, 8000, 7350, 0, 0, 0,
}

// BuildAudioCodecInfo translates a recorder's audio description into an
// AudioCodecInfo ready for the wire.
//
//	codec        — "aac" or "g711" (as reported by the recorder)
//	muxerConfig  — for AAC: the AudioSpecificConfig (ASC) bytes;
//	               for G.711: a 5-byte blob {muLaw:1, sampleRate:4 BE}
//	               produced by the recorder.
//
// Returns false when there is no usable audio track.
func BuildAudioCodecInfo(codec string, muxerConfig []byte) (*AudioCodecInfo, bool) {
	switch codec {
	case "aac":
		rate, channels, ok := parseAudioSpecificConfig(muxerConfig)
		if !ok {
			return nil, false
		}
		return &AudioCodecInfo{
			Codec:      AudioCodecAAC,
			SampleRate: rate,
			Channels:   channels,
			Config:     append([]byte(nil), muxerConfig...),
		}, true

	case "g711":
		// Recorder encodes G.711 params as {muLaw:1, sampleRate:4 BE}.
		if len(muxerConfig) < 5 {
			return nil, false
		}
		codecByte := AudioCodecG711A
		if muxerConfig[0] == 1 {
			codecByte = AudioCodecG711U
		}
		rate := binary.BigEndian.Uint32(muxerConfig[1:5])
		if rate == 0 {
			rate = 8000
		}
		return &AudioCodecInfo{
			Codec:      codecByte,
			SampleRate: rate,
			Channels:   1,
		}, true
	}
	return nil, false
}

// parseAudioSpecificConfig extracts the sample rate and channel count from an
// MPEG-4 AudioSpecificConfig. It reads the first three fields:
//
//	audioObjectType(5) | samplingFrequencyIndex(4) | channelConfiguration(4)
//
// When samplingFrequencyIndex == 15, an explicit 24-bit sample rate follows.
func parseAudioSpecificConfig(asc []byte) (sampleRate uint32, channels byte, ok bool) {
	if len(asc) < 2 {
		return 0, 0, false
	}

	br := bitReader{data: asc}
	objType := br.read(5)
	if objType == 31 { // escape: audioObjectType += 32 + read(6)
		br.read(6)
	}

	freqIdx := br.read(4)
	if freqIdx == 15 {
		sampleRate = uint32(br.read(24))
	} else if int(freqIdx) < len(aacSampleRates) {
		sampleRate = aacSampleRates[freqIdx]
	}

	chanConfig := br.read(4)
	if chanConfig >= 1 && chanConfig <= 2 {
		channels = byte(chanConfig)
	} else {
		channels = 1 // 0 or program-config-element — default to mono for playback
	}

	if br.err || sampleRate == 0 {
		return 0, 0, false
	}
	return sampleRate, channels, true
}

// bitReader reads big-endian bit fields from a byte slice.
type bitReader struct {
	data []byte
	pos  int // bit position
	err  bool
}

func (b *bitReader) read(n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		byteIdx := b.pos >> 3
		if byteIdx >= len(b.data) {
			b.err = true
			return v << uint(n-i-1)
		}
		bit := (b.data[byteIdx] >> uint(7-(b.pos&7))) & 1
		v = (v << 1) | uint32(bit)
		b.pos++
	}
	return v
}
