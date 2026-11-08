package bridge

import (
	"context"
	"errors"
	"fmt"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
)

// Publisher manages lal custom publisher sessions. Target authorization and
// namespace validation must be performed by the caller before RunH264ToLal.
type Publisher interface {
	AddCustomizePubSession(string) (logic.ICustomizePubSessionContext, error)
	DelCustomizePubSession(logic.ICustomizePubSessionContext)
}

var ErrInvalidPublisher = errors.New("bridge: invalid publisher or target")

// RunH264ToLal owns sub and a video-only lal custom publisher for the duration
// of the call. lal creates the target in its live app and rejects an occupied
// input. No asynchronous goroutine is created: a blocked FeedAvPacket cannot be
// interrupted by ctx and delays cleanup. The caller must arrange for bounded
// lal processing. Cancellation never concurrently deletes an in-flight session.
func RunH264ToLal(ctx context.Context, sub RTPSubscriber, publisher Publisher, stream string) error {
	return runToLal(ctx, sub, publisher, stream, RunH264)
}

// RunOpusToLal owns a single audio subscriber and custom publisher. It does
// not combine or synchronize audio with a video track.
func RunOpusToLal(ctx context.Context, sub RTPSubscriber, publisher Publisher, stream string) error {
	return runToLal(ctx, sub, publisher, stream, RunOpus)
}

func runToLal(ctx context.Context, sub RTPSubscriber, publisher Publisher, stream string, run func(context.Context, RTPSubscriber, VideoSink) error) error {
	if ctx == nil || sub == nil || publisher == nil || stream == "" {
		return ErrInvalidPublisher
	}
	defer sub.Close()
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
	session.WithOption(func(option *base.AvPacketStreamOption) {
		option.VideoFormat = base.AvPacketStreamVideoFormatAnnexb
	})
	return run(ctx, sub, func(ctx context.Context, frame *base.AvPacket) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return session.FeedAvPacket(*frame)
	})
}
