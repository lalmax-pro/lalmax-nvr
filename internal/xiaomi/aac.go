// SPDX-License-Identifier: MIT

package xiaomi

import (
	"bytes"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/q191201771/lal/pkg/base"
)

func (r *XiaomiRecorder) AudioCodec() string {
	r.audioInfoMu.RLock()
	defer r.audioInfoMu.RUnlock()
	return r.audioInfoCodec
}

func (r *XiaomiRecorder) AudioMuxerConfig() []byte {
	r.audioInfoMu.RLock()
	defer r.audioInfoMu.RUnlock()
	return bytes.Clone(r.audioInfoConfig)
}

func (r *XiaomiRecorder) forwardLegacyAAC(payload []byte) {
	var packets mpeg4audio.ADTSPackets
	if err := packets.Unmarshal(payload); err != nil {
		xiaomiLogger.Warn("invalid legacy AAC packet", "camera_id", r.cfg.CameraID, "error", err)
		return
	}
	pts := time.Since(r.streamStart).Nanoseconds() * 90000 / int64(time.Second)
	for _, packet := range packets {
		if packet.ChannelConfig == 0 {
			xiaomiLogger.Warn("legacy AAC channel layout is not supported", "camera_id", r.cfg.CameraID)
			return
		}
		asc, err := (mpeg4audio.AudioSpecificConfig{Type: packet.Type, SampleRate: packet.SampleRate, ChannelConfig: packet.ChannelConfig}).Marshal()
		if err != nil {
			xiaomiLogger.Warn("invalid legacy AAC configuration", "camera_id", r.cfg.CameraID, "error", err)
			return
		}
		r.mu.Lock()
		pub := r.pubSession
		r.mu.Unlock()
		r.audioInfoMu.RLock()
		changed := r.audioInfoCodec != "aac" || !bytes.Equal(r.audioInfoConfig, asc)
		r.audioInfoMu.RUnlock()
		if changed {
			if pub != nil {
				if err := pub.FeedAudioSpecificConfig(asc); err != nil {
					xiaomiLogger.Warn("failed to configure legacy AAC", "camera_id", r.cfg.CameraID, "error", err)
					return
				}
			}
			r.audioInfoMu.Lock()
			r.audioInfoCodec, r.audioInfoConfig = "aac", bytes.Clone(asc)
			r.audioInfoMu.Unlock()
		}
		if r.Hub != nil {
			r.Hub.BroadcastAudio(pts, model.AudioAAC, packet.AU)
		}
		if pub != nil {
			pkt := base.AvPacket{PayloadType: base.AvPacketPtAac, Timestamp: pts / 90, Pts: pts / 90, Payload: packet.AU}
			if err := pub.FeedAvPacket(pkt); err != nil {
				xiaomiLogger.Error("failed to feed legacy AAC", "camera_id", r.cfg.CameraID, "error", err)
				return
			}
		}
		pts += int64(1024 * 90000 / packet.SampleRate)
	}
}
