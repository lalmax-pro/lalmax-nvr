// SPDX-License-Identifier: MIT

package xiaomi

import (
	"encoding/binary"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/g711"
	"github.com/q191201771/lal/pkg/base"
	"github.com/stretchr/testify/require"
)

func audioTone16k(samples int, frequency float64, littleEndian bool) []byte {
	pcm := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		sample := uint16(int16(math.Round(12000 * math.Sin(2*math.Pi*frequency*float64(i)/16000))))
		if littleEndian {
			binary.LittleEndian.PutUint16(pcm[i*2:], sample)
		} else {
			binary.BigEndian.PutUint16(pcm[i*2:], sample)
		}
	}
	return pcm
}

func TestMISSAudioDownsamplingMaintainsPacketBoundaries(t *testing.T) {
	t.Helper()
	for _, codec := range []uint32{missCodecPCM, missCodecPCMA, missCodecPCMU} {
		t.Run(fmt.Sprint(codec), func(t *testing.T) {
			t.Helper()
			var payload []byte
			bytesPerSample := 1
			var err error
			switch codec {
			case missCodecPCM:
				payload = audioTone16k(16001, 1000, true)
				bytesPerSample = 2
			case missCodecPCMA:
				payload, err = g711.Alaw(audioTone16k(16001, 1000, false)).Marshal()
			case missCodecPCMU:
				payload, err = g711.Mulaw(audioTone16k(16001, 1000, false)).Marshal()
			}
			require.NoError(t, err)
			var whole, split missAudioNormalizer
			expected, err := whole.normalize(codec, 16000, payload)
			require.NoError(t, err)
			var actual []byte
			// Odd sample counts exercise phase continuity across MISS packets.
			for offset := 0; offset < len(payload); {
				end := min(len(payload), offset+257*bytesPerSample)
				part, err := split.normalize(codec, 16000, payload[offset:end])
				require.NoError(t, err)
				actual = append(actual, part...)
				offset = end
			}
			require.Len(t, actual, 8000*bytesPerSample)
			require.Equal(t, expected, actual)
		})
	}
}

func TestMISSAudioDownsamplingPreservesToneAndSuppressesAliasing(t *testing.T) {
	t.Helper()
	var lowFilter, highFilter missAudioNormalizer
	low, err := lowFilter.normalize(missCodecPCM, 16000, audioTone16k(16000, 1000, true))
	require.NoError(t, err)
	high, err := highFilter.normalize(missCodecPCM, 16000, audioTone16k(16000, 6000, true))
	require.NoError(t, err)
	require.Len(t, low, 16000) // One second at 8 kHz, 16 bits/sample.
	rms := func(pcm []byte) float64 {
		var sum float64
		for i := 128; i < len(pcm); i += 2 {
			v := float64(int16(binary.LittleEndian.Uint16(pcm[i:])))
			sum += v * v
		}
		return math.Sqrt(sum / float64((len(pcm)-128)/2))
	}
	require.Less(t, rms(high), rms(low)*0.03, "high-frequency input must not alias into audible speech")
	crossings := 0
	previous := int16(binary.LittleEndian.Uint16(low[126:]))
	for i := 128; i < len(low); i += 2 {
		sample := int16(binary.LittleEndian.Uint16(low[i:]))
		if previous <= 0 && sample > 0 {
			crossings++
		}
		previous = sample
	}
	frequency := float64(crossings) * 8000 / float64(len(low)/2-64)
	require.InDelta(t, 1000, frequency, 3)
}

func TestMISSAudioSampleRateHandling(t *testing.T) {
	t.Helper()
	var n missAudioNormalizer
	payload := []byte{0x78, 1, 2}
	got, err := n.normalize(missCodecOPUS, 16000, payload)
	require.NoError(t, err)
	require.Equal(t, payload, got, "Opus retains its own sample clock")
	got, err = n.normalize(missCodecPCMA, 8000, payload)
	require.NoError(t, err)
	require.Equal(t, payload, got)
	_, err = n.normalize(missCodecPCMA, 44100, payload)
	require.Error(t, err)
	_, err = n.normalize(missCodecPCM, 16000, []byte{0})
	require.Error(t, err)
}

func TestRecorderForwards16kAudioAs8k(t *testing.T) {
	t.Helper()
	pub := &covPubSession{}
	r := NewXiaomiRecorder(XiaomiRecorderConfig{AudioEnabled: true}, nil)
	r.pubSession = pub
	r.streamStart = time.Now()
	r.forwardAudio(missCodecPCM, audioTone16k(16000, 1000, true), 16000)
	require.Len(t, pub.packets, 1)
	require.Equal(t, base.AvPacketPtG711A, pub.last().PayloadType)
	require.Len(t, pub.last().Payload, 8000)
}
