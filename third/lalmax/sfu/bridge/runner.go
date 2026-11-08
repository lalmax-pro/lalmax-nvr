package bridge

import (
	"context"
	"errors"
	"fmt"

	"github.com/pion/rtp"
	"github.com/q191201771/lal/pkg/base"
)

// RTPSubscriber provides one bounded, already-authorized RTP subscription.
// The bridge owns the subscription and closes it on exit.
type RTPSubscriber interface {
	Packets() <-chan *rtp.Packet
	Err() error
	Close()
}

// VideoSink receives video frames sequentially. It must return when ctx is
// canceled when possible; RunH264 waits for the current call before releasing its subscriber.
// A sink retaining packet data after returning must take its own copy.
type VideoSink func(ctx context.Context, packet *base.AvPacket) error

var ErrInvalidRunner = errors.New("bridge: invalid runner arguments")

// RunH264 consumes an already authorized subscriber until cancellation, a
// terminal source error, an invalid packet, or a sink failure. It owns and
// closes sub. The bounded fanout disconnects a slow consumer independently of
// the source and other subscribers. This function does not create a Group.
func RunH264(ctx context.Context, sub RTPSubscriber, sink VideoSink) error {
	return runTrack(ctx, sub, sink, NewH264().Push)
}

// RunOpus forwards one authorized Opus subscriber to the sink. It owns sub.
// Its timestamps are relative to this track and are not synchronized to video.
func RunOpus(ctx context.Context, sub RTPSubscriber, sink VideoSink) error {
	return runTrack(ctx, sub, sink, (&Opus{}).Push)
}

func runTrack(ctx context.Context, sub RTPSubscriber, sink VideoSink, decode func(*rtp.Packet) (*base.AvPacket, error)) error {
	if ctx == nil || sub == nil || sink == nil {
		return ErrInvalidRunner
	}
	defer sub.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case packet, ok := <-sub.Packets():
			if !ok {
				return sub.Err()
			}
			// Do not publish buffered packets after the subscription has ended.
			if err := sub.Err(); err != nil {
				return err
			}
			frame, err := decode(packet)
			if err != nil {
				return fmt.Errorf("bridge: decode: %w", err)
			}
			if frame != nil {
				if err := sink(ctx, frame); err != nil {
					return fmt.Errorf("bridge: sink: %w", err)
				}
			}
		}
	}
}
