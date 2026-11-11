package bridge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pion/rtp"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lalmax/sfu/rtpforward"
)

var ErrAVClockNotReady = errors.New("bridge: sender report clock not ready")

// AVReferences are validated RTP/NTP mappings for two tracks on one sender
// clock. Callers must check report freshness, SSRC, sender identity and codec.
type AVReferences struct {
	Audio, Video         ClockReference
	AudioSSRC, VideoSSRC uint32
}
type AVReferenceProvider func(context.Context, uint32) (AVReferences, error)

// RunAVToLal publishes authorized H264 and Opus tracks to one custom session.
// It owns both subscriptions. The provider must reject stale or unrelated
// reports and ensure they come from a common clock. While reports are missing,
// incoming packets are drained, not buffered. It starts at a video keyframe.
func RunAVToLal(ctx context.Context, audio, video rtpforward.PacketSubscriber, publisher Publisher, stream string, reports AVReferenceProvider) error {
	if ctx == nil || audio == nil || video == nil || audio == video || publisher == nil || stream == "" || reports == nil {
		return ErrInvalidPublisher
	}
	defer audio.Close()
	defer video.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	session, err := publisher.AddCustomizePubSession(stream)
	if err != nil {
		return fmt.Errorf("bridge: create publisher: %w", err)
	}
	if session == nil {
		return errors.New("bridge: publisher returned nil session")
	}
	defer publisher.DelCustomizePubSession(session)
	session.WithOption(func(o *base.AvPacketStreamOption) { o.VideoFormat = base.AvPacketStreamVideoFormatAnnexb })
	aDecoder, vDecoder := &Opus{}, NewH264()
	var clock *avClock
	var queue avQueue
	var videoSSRC uint32
	var lastVideoMS int64
	var videoTimestampSet bool
	feed := func(p base.AvPacket) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return session.FeedAvPacket(p)
	}
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	startup := time.NewTimer(15 * time.Second)
	defer startup.Stop()
	consume := func(packet *rtp.Packet, isVideo bool) error {
		if isVideo && videoSSRC != packet.SSRC {
			// A simulcast layer switch changes the source SSRC. Start a fresh
			// decoder for the new encoding and resume only at its next IDR.
			videoSSRC = packet.SSRC
			vDecoder = NewH264()
		}
		refs, err := reports(ctx, videoSSRC)
		if errors.Is(err, ErrAVClockNotReady) {
			// Preserve H264 parameter sets while waiting for RTCP, but do not
			// start output on a predicted frame after dropping its keyframe.
			if isVideo {
				frame, decodeErr := vDecoder.Push(packet)
				if decodeErr != nil {
					return decodeErr
				}
				if frame != nil {
					vDecoder.ready = false
				}
			}
			return nil
		}
		if err != nil {
			return err
		}
		if refs.Audio.Rate != 48000 || refs.Video.Rate != 90000 {
			return ErrInvalidClock
		}
		if isVideo && packet.SSRC != refs.VideoSSRC || !isVideo && packet.SSRC != refs.AudioSSRC {
			return ErrSSRCChanged
		}
		var frame *base.AvPacket
		if isVideo {
			frame, err = vDecoder.Push(packet)
		} else {
			frame, err = aDecoder.Push(packet)
		}
		if err != nil {
			return err
		}
		if frame == nil {
			return nil
		}
		if clock == nil {
			if !isVideo {
				return nil
			}
			clock = newAVClock(refs.Video, packet.Timestamp)
			startup.Stop()
		}
		var mapped time.Duration
		if isVideo {
			mapped, err = clock.video(refs.Video, packet.Timestamp)
		} else {
			mapped, err = clock.audio(refs.Audio, packet.Timestamp)
		}
		if err != nil {
			return err
		}
		if mapped < 0 {
			return nil
		}
		mappedMS := int64(mapped / time.Millisecond)
		if isVideo {
			// RTP clocks can advance by less than one millisecond, and two
			// simulcast encodings may emit the same capture timestamp around a
			// layer switch. MP4 requires strictly increasing video DTS values.
			if videoTimestampSet && mappedMS <= lastVideoMS {
				return nil
			}
			lastVideoMS, videoTimestampSet = mappedMS, true
		}
		frame.Timestamp = mappedMS
		frame.Pts = frame.Timestamp
		return queue.push(*frame, time.Now(), isVideo)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-startup.C:
			if clock == nil {
				return ErrAVClockNotReady
			}
		case <-tick.C:
			if err := queue.drain(time.Now(), false, feed); err != nil {
				return err
			}
		case packet, ok := <-audio.Packets():
			if !ok {
				return audio.Err()
			}
			if err := audio.Err(); err != nil {
				return err
			}
			if err := consume(packet, false); err != nil {
				return fmt.Errorf("bridge: audio: %w", err)
			}
		case packet, ok := <-video.Packets():
			if !ok {
				return video.Err()
			}
			if err := video.Err(); err != nil {
				return err
			}
			if err := consume(packet, true); err != nil {
				return fmt.Errorf("bridge: video: %w", err)
			}
		}
	}
}
