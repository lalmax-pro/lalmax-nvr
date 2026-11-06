// SPDX-License-Identifier: MIT

package xiaomi

import (
	"bytes"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi/tutk"
	"github.com/q191201771/lal/pkg/base"
	"github.com/stretchr/testify/require"
)

func TestLegacyAACFeedsConfigAndEachAccessUnit(t *testing.T) {
	frames := mpeg4audio.ADTSPackets{
		&mpeg4audio.ADTSPacket{Type: mpeg4audio.ObjectTypeAACLC, SampleRate: 16000, ChannelConfig: 1, AU: []byte{1, 2, 3}},
		&mpeg4audio.ADTSPacket{Type: mpeg4audio.ObjectTypeAACLC, SampleRate: 16000, ChannelConfig: 1, AU: []byte{4, 5}},
	}
	payload, err := frames.Marshal()
	require.NoError(t, err)
	r := NewXiaomiRecorder(XiaomiRecorderConfig{AudioEnabled: true}, nil)
	pub := &covPubSession{}
	r.pubSession = pub
	r.streamStart = time.Now()
	r.forwardAudio(missCodecAAC, payload)
	require.Equal(t, 1, pub.asc)
	require.Equal(t, "aac", r.AudioCodec())
	cfg := r.AudioMuxerConfig()
	var asc mpeg4audio.AudioSpecificConfig
	require.NoError(t, asc.Unmarshal(cfg))
	require.Equal(t, 16000, asc.SampleRate)
	require.Equal(t, uint8(1), asc.ChannelConfig)
	require.Len(t, pub.packets, 2)
	require.Equal(t, base.AvPacketPtAac, pub.packets[0].PayloadType)
	require.Equal(t, []byte{1, 2, 3}, pub.packets[0].Payload)
	require.Equal(t, []byte{4, 5}, pub.packets[1].Payload)
	require.Equal(t, int64(64), pub.packets[1].Timestamp-pub.packets[0].Timestamp)
	cfg[0] ^= 0xff
	require.NotEqual(t, cfg, r.AudioMuxerConfig())
	r.forwardAudio(missCodecAAC, payload)
	require.Equal(t, 1, pub.asc)
}

func TestLegacyAACRejectsMalformedPackets(t *testing.T) {
	r := NewXiaomiRecorder(XiaomiRecorderConfig{AudioEnabled: true}, nil)
	pub := &covPubSession{}
	r.pubSession = pub
	r.streamStart = time.Now()
	r.forwardAudio(missCodecAAC, []byte{0xff, 0xf1})
	require.Empty(t, r.AudioCodec())
	require.Empty(t, pub.packets)
	client := &MISSClient{legacy: true, model: legacyXiaobai}
	hdr := make([]byte, 20)
	hdr[0] = tutk.CodecAACLATM
	packet, err := client.readLegacyPacket(hdr, bytes.Repeat([]byte{1}, 10))
	require.NoError(t, err)
	require.Equal(t, uint32(missCodecAAC), packet.CodecID)
}

func TestLegacyAACConfigurationChangeAndConcurrentReaders(t *testing.T) {
	r := NewXiaomiRecorder(XiaomiRecorderConfig{AudioEnabled: true}, nil)
	pub := &covPubSession{}
	r.pubSession = pub
	r.streamStart = time.Now()
	mk := func(rate int) []byte {
		data, err := (mpeg4audio.ADTSPackets{&mpeg4audio.ADTSPacket{Type: mpeg4audio.ObjectTypeAACLC, SampleRate: rate, ChannelConfig: 1, AU: []byte{1, 2}}}).Marshal()
		require.NoError(t, err)
		return data
	}
	first, next := mk(16000), mk(8000)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			_ = r.AudioCodec()
			_ = r.AudioMuxerConfig()
		}
	}()
	r.forwardAudio(missCodecAAC, first)
	r.forwardAudio(missCodecAAC, next)
	<-done
	require.Equal(t, 2, pub.asc)
	var asc mpeg4audio.AudioSpecificConfig
	require.NoError(t, asc.Unmarshal(r.AudioMuxerConfig()))
	require.Equal(t, 8000, asc.SampleRate)
}
