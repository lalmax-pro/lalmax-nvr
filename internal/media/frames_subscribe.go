package media

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/q191201771/lal/pkg/base"
	maxlogic "github.com/q191201771/lalmax/logic"
)

type embeddedFrameSub struct {
	id     string
	group  *maxlogic.Group
	raw    chan base.RtmpMsg
	frames chan MediaFrame
	errc   chan error
	mu     sync.Mutex
	closed atomic.Bool
	once   sync.Once
}

func (s *embeddedFrameSub) Frames() <-chan MediaFrame { return s.frames }
func (s *embeddedFrameSub) Err() <-chan error         { return s.errc }

func (s *embeddedFrameSub) Close() error {
	s.finish(nil)
	if s.group != nil {
		s.group.RemoveSubscriber(s.id)
	}
	return nil
}

func (s *embeddedFrameSub) OnMsg(msg base.RtmpMsg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() {
		return
	}
	cloned := msg.Clone()
	select {
	case s.raw <- cloned:
	default:
		// Never block the lalmax OnMsg path.
	}
}

func (s *embeddedFrameSub) OnStop() {
	s.finish(fmt.Errorf("media stream stopped"))
}

func (s *embeddedFrameSub) finish(err error) {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed.Store(true)
		close(s.raw)
		s.mu.Unlock()
		if err != nil {
			select {
			case s.errc <- err:
			default:
			}
		}
	})
}

func (s *embeddedFrameSub) convertLoop() {
	defer close(s.frames)
	for msg := range s.raw {
		for _, frame := range rtmpMsgToFrames(msg) {
			select {
			case s.frames <- frame:
			default:
			}
		}
	}
}

func (e *EmbeddedLalmax) SubscribeFrames(ctx context.Context, req SubscribeFramesRequest) (FrameSubscription, error) {
	if req.StreamID == "" {
		return nil, fmt.Errorf("stream ID is required")
	}
	waitCtx, cancel := waitContext(ctx)
	defer cancel()

	group, err := waitLalmaxGroup(waitCtx, req.StreamID, streamAppName(req))
	if err != nil {
		return nil, err
	}

	id := fmt.Sprintf("nvr-record-%s-%d", req.StreamID, time.Now().UnixNano())
	sub := &embeddedFrameSub{
		id:     id,
		group:  group,
		raw:    make(chan base.RtmpMsg, 64),
		frames: make(chan MediaFrame, 256),
		errc:   make(chan error, 1),
	}
	go sub.convertLoop()
	group.AddSubscriber(maxlogic.SubscriberInfo{
		SubscriberID: id,
		Protocol:     "NVR-RECORD",
		RemoteAddr:   "in-process",
	}, sub)
	return sub, nil
}

func (e *LalmaxHTTP) SubscribeFrames(_ context.Context, _ SubscribeFramesRequest) (FrameSubscription, error) {
	return nil, ErrFramesNotSupported
}

func waitLalmaxGroup(ctx context.Context, streamID, appName string) (*maxlogic.Group, error) {
	mgr := maxlogic.GetGroupManagerInstance()
	keys := []maxlogic.StreamKey{
		maxlogic.NewStreamKey(appName, streamID),
		maxlogic.StreamKeyFromStreamName(streamID),
	}
	if appName != "live" {
		keys = append(keys, maxlogic.NewStreamKey("live", streamID))
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		for _, key := range keys {
			if ok, g := mgr.GetGroup(key); ok && g != nil {
				return g, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for stream %s: %w", streamID, ctx.Err())
		case <-ticker.C:
		}
	}
}
