package media

import (
	"context"
	"errors"
	"time"
)

// ErrFramesNotSupported is returned when the engine cannot deliver in-process frames
// (for example HTTP-mode lalmax). Recorders should fall back to RTSP loopback.
var ErrFramesNotSupported = errors.New("in-process frame subscription is not supported")

type FrameKind int

const (
	FrameVideo FrameKind = iota
	FrameAudio
)

// MediaFrame is one annex-b NALU (video) or one raw audio access unit from the
// media engine. Video Data always includes a 4-byte start code.
type MediaFrame struct {
	Kind        FrameKind
	Codec       string // h264, h265, aac, g711
	IsKey       bool
	IsSeqHeader bool
	PTS         time.Duration
	Data        []byte
}

type FrameSubscription interface {
	Frames() <-chan MediaFrame
	Err() <-chan error
	Close() error
}

type SubscribeFramesRequest struct {
	StreamID string
	AppName  string
}

// FrameWaitTimeout is how long SubscribeFrames waits for the lalmax group to appear.
// GB28181 RTP often arrives after the recorder has already started.
const FrameWaitTimeout = 15 * time.Second

func streamAppName(req SubscribeFramesRequest) string {
	if req.AppName != "" {
		return req.AppName
	}
	return "live"
}

var (
	_ Engine              = (*LalmaxHTTP)(nil)
	_ Engine              = (*EmbeddedLalmax)(nil)
	_ PlayHandlerProvider = (*EmbeddedLalmax)(nil)
)

func waitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, FrameWaitTimeout)
}
