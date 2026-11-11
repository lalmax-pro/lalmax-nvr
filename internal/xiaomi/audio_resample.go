// SPDX-License-Identifier: MIT

package xiaomi

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/g711"
)

// The lal G.711 playback path uses 8 kHz. Downsample 16 kHz camera audio
// before forwarding, keeping the filter and phase across MISS packet boundaries.
type missAudioNormalizer struct {
	codec  uint32
	rate   uint32
	filter audio16kDownsampler
}

func (n *missAudioNormalizer) normalize(codec, rate uint32, payload []byte) ([]byte, error) {
	if n.codec != codec || n.rate != rate {
		*n = missAudioNormalizer{codec: codec, rate: rate}
	}
	if codec == missCodecOPUS || rate == 8000 {
		return payload, nil
	}
	if rate != 16000 {
		return nil, fmt.Errorf("unsupported MISS audio sample rate %d", rate)
	}
	var pcm []byte // signed, big endian for mediacommon's G.711 codecs
	switch codec {
	case missCodecPCMA:
		var decoded g711.Alaw
		decoded.Unmarshal(payload)
		pcm = decoded
	case missCodecPCMU:
		var decoded g711.Mulaw
		decoded.Unmarshal(payload)
		pcm = decoded
	case missCodecPCM:
		if len(payload)%2 != 0 {
			return nil, fmt.Errorf("MISS PCM requires 16-bit samples")
		}
		pcm = make([]byte, len(payload))
		for i := 0; i < len(payload); i += 2 {
			pcm[i], pcm[i+1] = payload[i+1], payload[i]
		}
	default:
		return nil, fmt.Errorf("unsupported MISS audio codec %d", codec)
	}
	pcm = n.filter.process(pcm)
	switch codec {
	case missCodecPCMA:
		return g711.Alaw(pcm).Marshal()
	case missCodecPCMU:
		return g711.Mulaw(pcm).Marshal()
	default:
		for i := 0; i < len(pcm); i += 2 {
			pcm[i], pcm[i+1] = pcm[i+1], pcm[i]
		}
		return pcm, nil
	}
}

// A windowed-sinc low-pass filter suppresses frequencies above the 8 kHz
// output's Nyquist limit; dropping alternate samples alone would alias them.
var audioDownsampleTaps = func() [31]float64 {
	var taps [31]float64
	const cutoff = 3500.0 / 16000
	var sum float64
	for i := range taps {
		x := float64(i) - float64(len(taps)-1)/2
		v := 2 * cutoff
		if x != 0 {
			v = math.Sin(2*math.Pi*cutoff*x) / (math.Pi * x)
		}
		taps[i] = v * (0.54 - 0.46*math.Cos(2*math.Pi*float64(i)/float64(len(taps)-1)))
		sum += taps[i]
	}
	for i := range taps {
		taps[i] /= sum
	}
	return taps
}()

type audio16kDownsampler struct {
	history [31]float64
	next    int
	odd     bool
}

func (s *audio16kDownsampler) process(pcm []byte) []byte {
	out := make([]byte, 0, len(pcm)/2+2)
	for i := 0; i < len(pcm); i += 2 {
		s.history[s.next] = float64(int16(binary.BigEndian.Uint16(pcm[i:])))
		s.next = (s.next + 1) % len(s.history)
		s.odd = !s.odd
		if s.odd {
			continue
		}
		var value float64
		for k, tap := range audioDownsampleTaps {
			value += tap * s.history[(s.next-1-k+2*len(s.history))%len(s.history)]
		}
		sample := int16(math.Round(max(-32768, min(32767, value))))
		out = binary.BigEndian.AppendUint16(out, uint16(sample))
	}
	return out
}
