// SPDX-License-Identifier: MIT

package xiaomi

import (
	"fmt"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/g711"
	"github.com/q191201771/lal/pkg/base"
)

// missAudioPayload preserves lal's G.711 variant. StreamHub's generic G.711
// consumers expect A-law, so mu-law and raw PCM must actually be converted.
func missAudioPayload(codecID uint32, payload []byte) (base.AvPacketPt, []byte, []byte, error) {
	switch codecID {
	case missCodecPCMA:
		return base.AvPacketPtG711A, payload, payload, nil
	case missCodecPCMU:
		var pcm g711.Mulaw
		pcm.Unmarshal(payload)
		alaw, err := g711.Alaw(pcm).Marshal()
		return base.AvPacketPtG711U, payload, alaw, err
	case missCodecPCM:
		if len(payload)%2 != 0 {
			return 0, nil, nil, fmt.Errorf("MISS PCM requires 16-bit samples")
		}
		// MISS PCM is signed 16-bit little endian; mediacommon uses big endian.
		pcm := make(g711.Alaw, len(payload))
		for i := 0; i < len(payload); i += 2 {
			pcm[i], pcm[i+1] = payload[i+1], payload[i]
		}
		alaw, err := pcm.Marshal()
		return base.AvPacketPtG711A, alaw, alaw, err
	case missCodecOPUS:
		return base.AvPacketPtOpus, payload, payload, nil
	default:
		return 0, nil, nil, fmt.Errorf("unsupported MISS audio codec %d", codecID)
	}
}
