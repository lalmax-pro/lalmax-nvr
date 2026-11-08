package recorder

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/observability"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/q191201771/lal/pkg/base"
	maxlogic "github.com/q191201771/lalmax/logic"
	"go.opentelemetry.io/otel/attribute"
)

const groupQueueSize = 2048

// segmentStore is the recording file layout used by storage.Manager.
type segmentStore interface {
	CreateSegment(cameraID, format string) (tempPath, finalPath string, err error)
	CloseSegment(tempPath, finalPath string) error
}

// recordingDB persists a finished segment. Nil skips the database row.
type recordingDB interface {
	InsertRecordingWithRetry(ctx context.Context, r *model.Recording, maxRetries int, backoff time.Duration) error
}

// subscribeGroup is the lalmax group surface the writer needs.
type subscribeGroup interface {
	AddSubscriber(info maxlogic.SubscriberInfo, subscriber maxlogic.Subscriber)
	RemoveSubscriber(subscriberID string)
}

// groupFinder locates in-process lalmax groups. Tests substitute a fake.
type groupFinder interface {
	Find(streamName string) (subscribeGroup, bool)
	Each(func(streamName string))
}

type lalmaxGroups struct{}

func (lalmaxGroups) Find(streamName string) (subscribeGroup, bool) {
	ok, group := maxlogic.GetGroupManagerInstance().GetGroupByStreamName(streamName)
	if !ok || group == nil {
		return nil, false
	}
	return group, true
}

func (lalmaxGroups) Each(fn func(streamName string)) {
	maxlogic.GetGroupManagerInstance().Iterate(func(key maxlogic.StreamKey, _ *maxlogic.Group) bool {
		if key.StreamName != "" {
			fn(key.StreamName)
		}
		return true
	})
}

// GroupWriter subscribes to every managed camera that reaches a lalmax group.
// The subscription stays up while the group exists. Recording plans only flip
// the per-camera write switch.
type GroupWriter struct {
	store   segmentStore
	db      recordingDB
	bus     *event.EventBus
	engine  media.Engine
	metrics *metrics.Metrics
	segDur  time.Duration
	groups  groupFinder
	lookup  func(string) (config.CameraConfig, bool)
	should  func(string) bool
	accept  func(config.CameraConfig) bool

	mu         sync.Mutex
	sessions   map[string]*recSession
	desired    map[string]bool
	desiredSet map[string]bool
	closeOnce  sync.Once
	cancel     context.CancelFunc
}

// NewGroupWriter records from the in-process lalmax group manager.
// engine may be nil in tests; Run then only reconciles groups on its ticker.
func NewGroupWriter(store *storage.Manager, db RecordingDB, bus *event.EventBus, engine media.Engine, segDur time.Duration, metricsOpt ...*metrics.Metrics) *GroupWriter {
	if segDur <= 0 {
		segDur = DefaultSegmentDur
	}
	var recDB recordingDB
	if db != nil {
		recDB = db
	}
	var m *metrics.Metrics
	if len(metricsOpt) > 0 {
		m = metricsOpt[0]
	}
	return &GroupWriter{
		store:      store,
		db:         recDB,
		bus:        bus,
		engine:     engine,
		metrics:    m,
		segDur:     segDur,
		groups:     lalmaxGroups{},
		sessions:   make(map[string]*recSession),
		desired:    make(map[string]bool),
		desiredSet: make(map[string]bool),
	}
}

// SetLookup resolves a stream name to a camera. Called from the writer goroutine.
func (w *GroupWriter) SetLookup(fn func(string) (config.CameraConfig, bool)) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.lookup = fn
	w.mu.Unlock()
}

// SetShouldWrite is the default write switch before SetWriting is called.
func (w *GroupWriter) SetShouldWrite(fn func(string) bool) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.should = fn
	w.mu.Unlock()
}

// SetAccept decides which cameras this writer owns.
func (w *GroupWriter) SetAccept(fn func(config.CameraConfig) bool) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.accept = fn
	w.mu.Unlock()
}

// Covers reports whether this writer owns cam, including cameras that are currently disabled.
func (w *GroupWriter) Covers(cam config.CameraConfig) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	fn := w.accept
	w.mu.Unlock()
	if fn != nil {
		return fn(cam)
	}
	return CameraUsesGroupRecording(cam)
}

// Accepts reports whether cam should be subscribed now.
func (w *GroupWriter) Accepts(cam config.CameraConfig) bool {
	return w.Covers(cam) && cam.Enabled
}

// SetWriting turns disk writing on or off. The subscriber stays attached.
func (w *GroupWriter) SetWriting(cameraID string, on bool) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.desiredSet[cameraID] = true
	w.desired[cameraID] = on
	s := w.sessions[cameraID]
	w.mu.Unlock()
	if s != nil {
		s.writing.Store(on)
	}
}

// Rotate closes the current segment and opens the next one on the following keyframe.
func (w *GroupWriter) Rotate(cameraID string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	s := w.sessions[cameraID]
	w.mu.Unlock()
	if s != nil {
		s.rotate.Store(true)
	}
}

// State reports whether a subscriber is attached and whether it is writing.
func (w *GroupWriter) State(cameraID string) (attached, writing bool) {
	if w == nil {
		return false, false
	}
	w.mu.Lock()
	s := w.sessions[cameraID]
	w.mu.Unlock()
	if s == nil {
		return false, false
	}
	return true, s.writing.Load()
}

// StreamInfo exposes the live codec parameters and frame hub for a subscribed group.
// It is used by the WebSocket endpoint when this camera is not owned by CameraManager's recorder map.
func (w *GroupWriter) StreamInfo(cameraID string) (model.Format, []byte, []byte, []byte, *model.StreamHub, bool) {
	if w == nil {
		return "", nil, nil, nil, nil, false
	}
	w.mu.Lock()
	s := w.sessions[cameraID]
	w.mu.Unlock()
	if s == nil || s.hub == nil {
		return "", nil, nil, nil, nil, false
	}
	s.streamMu.RLock()
	defer s.streamMu.RUnlock()
	if s.liveCodec == "" || len(s.liveSPS) == 0 || len(s.livePPS) == 0 {
		return "", nil, nil, nil, s.hub, false
	}
	return model.Format(s.liveCodec), append([]byte(nil), s.liveSPS...), append([]byte(nil), s.livePPS...), append([]byte(nil), s.liveVPS...), s.hub, true
}

// Attach subscribes cameraID when its lalmax group already exists.
func (w *GroupWriter) Attach(cameraID string) {
	w.AttachContext(context.Background(), cameraID)
}

// AttachContext subscribes cameraID and carries the caller trace into the
// asynchronous group writer session.
func (w *GroupWriter) AttachContext(ctx context.Context, cameraID string) {
	if w == nil || cameraID == "" || media.IsSubStreamID(cameraID) {
		return
	}
	cam, ok := w.camera(cameraID)
	if !ok {
		return
	}
	w.AttachCameraContext(ctx, cam)
}

// AttachCamera subscribes cam when its group exists and the camera is accepted.
func (w *GroupWriter) AttachCamera(cam config.CameraConfig) {
	w.AttachCameraContext(context.Background(), cam)
}

// AttachCameraContext subscribes cam and carries the caller trace into the
// asynchronous group writer session.
func (w *GroupWriter) AttachCameraContext(ctx context.Context, cam config.CameraConfig) {
	if w == nil || cam.ID == "" || media.IsSubStreamID(cam.ID) || (cam.Protocol != "voip" && !w.Accepts(cam)) {
		return
	}
	ctx, span := observability.StartBusinessSpan(ctx, "recording.group.attach",
		attribute.String("nvr.camera.id", cam.ID),
		attribute.String("nvr.stream.id", cam.StreamID),
		attribute.String("nvr.media.protocol", string(cam.Protocol)),
		attribute.String("nvr.media.encoding", cam.Encoding),
	)
	defer span.End()
	w.mu.Lock()
	if _, exists := w.sessions[cam.ID]; exists {
		w.mu.Unlock()
		return
	}
	finder := w.groups
	w.mu.Unlock()
	if finder == nil {
		return
	}
	group, ok := finder.Find(cam.ID)
	if !ok || group == nil {
		return
	}
	s := &recSession{
		cameraID:  cam.ID,
		subID:     "nvr-record-" + cam.ID,
		audio:     cam.AudioEnabled,
		audioOnly: cam.Encoding == "audio",
		traceCtx:  observability.DetachedContext(ctx),
		segDur:    w.segDur,
		store:     w.store,
		db:        w.db,
		bus:       w.bus,
		metrics:   w.metrics,
		group:     group,
		owner:     w,
		hub:       model.NewStreamHub(),
		queue:     make(chan base.RtmpMsg, groupQueueSize),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	s.hub.SetCameraID(cam.ID)
	w.mu.Lock()
	if _, exists := w.sessions[cam.ID]; exists {
		w.mu.Unlock()
		return
	}
	on := true
	if w.desiredSet[cam.ID] {
		on = w.desired[cam.ID]
	} else if w.should != nil {
		fn := w.should
		w.mu.Unlock()
		on = fn(cam.ID)
		w.mu.Lock()
		if _, exists := w.sessions[cam.ID]; exists {
			w.mu.Unlock()
			return
		}
		if w.desiredSet[cam.ID] {
			on = w.desired[cam.ID]
		}
	}
	s.writing.Store(on)
	w.sessions[cam.ID] = s
	w.mu.Unlock()
	go s.loop()
	group.AddSubscriber(maxlogic.SubscriberInfo{
		SubscriberID: s.subID,
		Protocol:     "nvr-record",
	}, &recSubscriber{s: s})
	slog.Info("recording subscriber attached", "camera_id", cam.ID, "writing", s.writing.Load())
}

// Detach removes the subscriber. Stream stop and camera deletion use this.
func (w *GroupWriter) Detach(cameraID string) {
	if w == nil {
		return
	}
	w.forget(cameraID, true)
}

func (w *GroupWriter) forget(cameraID string, removeSub bool) {
	w.mu.Lock()
	s := w.sessions[cameraID]
	if s == nil {
		w.mu.Unlock()
		return
	}
	delete(w.sessions, cameraID)
	w.mu.Unlock()
	if removeSub && s.group != nil {
		s.group.RemoveSubscriber(s.subID)
	}
	s.halt()
}

// Sync attaches groups that already exist and drops subscribers whose group is gone.
func (w *GroupWriter) Sync() {
	w.SyncContext(context.Background())
}

// SyncContext reconciles subscriptions and uses ctx as the parent for any
// sessions attached during this pass.
func (w *GroupWriter) SyncContext(ctx context.Context) {
	if w == nil || w.groups == nil {
		return
	}
	seen := make(map[string]struct{})
	w.groups.Each(func(streamName string) {
		if media.IsSubStreamID(streamName) {
			return
		}
		seen[streamName] = struct{}{}
		w.AttachContext(ctx, streamName)
	})
	w.mu.Lock()
	var stale []string
	for id := range w.sessions {
		if _, ok := seen[id]; !ok {
			stale = append(stale, id)
		}
	}
	w.mu.Unlock()
	for _, id := range stale {
		w.Detach(id)
	}
}

// Run subscribes to engine stream events and reconciles groups until ctx is cancelled.
func (w *GroupWriter) Run(ctx context.Context) {
	if w == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	w.cancel = cancel
	w.mu.Unlock()
	defer w.Close()

	w.SyncContext(ctx)
	var events <-chan media.Event
	var retryTimer *time.Timer
	var retry <-chan time.Time
	retryDelay := time.Second
	if w.engine != nil {
		retryTimer = time.NewTimer(0)
		retry = retryTimer.C
		defer retryTimer.Stop()
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			w.SyncContext(ctx)
		case <-retry:
			retry = nil
			ch, err := w.engine.SubscribeEvents(ctx, media.EventFilter{Types: []media.EventType{
				media.EventPublisherStarted, media.EventPublisherStopped,
				media.EventRelayPullStarted, media.EventRelayPullStopped,
				media.EventStreamActive, media.EventStreamStopped,
			}})
			if err != nil || ch == nil {
				slog.Warn("group recorder failed to subscribe to stream events; retrying", "error", err, "delay", retryDelay)
				retryTimer.Reset(retryDelay)
				retry = retryTimer.C
				retryDelay *= 2
				if retryDelay > 30*time.Second {
					retryDelay = 30 * time.Second
				}
				continue
			}
			events = ch
			retryDelay = time.Second
			// Events during a disconnection are not replayed.
			w.SyncContext(ctx)
		case ev, ok := <-events:
			if !ok {
				events = nil
				retryTimer.Reset(retryDelay)
				retry = retryTimer.C
				continue
			}
			w.onEvent(ctx, ev)
		}
	}
}

func (w *GroupWriter) onEvent(ctx context.Context, ev media.Event) {
	if media.IsSubStreamID(ev.StreamID) {
		return
	}
	switch ev.Type {
	case media.EventPublisherStopped, media.EventRelayPullStopped, media.EventStreamStopped:
		w.Detach(ev.StreamID)
	default:
		w.AttachContext(ctx, ev.StreamID)
	}
}

// Close detaches every subscriber and waits briefly for segments to finish.
func (w *GroupWriter) Close() {
	if w == nil {
		return
	}
	w.closeOnce.Do(func() {
		w.mu.Lock()
		cancel := w.cancel
		sessions := w.sessions
		w.sessions = make(map[string]*recSession)
		w.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		var dones []<-chan struct{}
		for _, s := range sessions {
			if s.group != nil {
				s.group.RemoveSubscriber(s.subID)
			}
			s.halt()
			dones = append(dones, s.done)
		}
		deadline := time.After(5 * time.Second)
		for _, done := range dones {
			select {
			case <-done:
			case <-deadline:
				return
			}
		}
	})
}

func (w *GroupWriter) camera(id string) (config.CameraConfig, bool) {
	w.mu.Lock()
	fn := w.lookup
	w.mu.Unlock()
	if fn == nil {
		return config.CameraConfig{}, false
	}
	return fn(id)
}

func (w *GroupWriter) wantWrite(id string) bool {
	w.mu.Lock()
	set := w.desiredSet[id]
	on := w.desired[id]
	fn := w.should
	w.mu.Unlock()
	if set {
		return on
	}
	if fn != nil {
		return fn(id)
	}
	return true
}

type recSubscriber struct {
	s *recSession
}

func (r *recSubscriber) OnMsg(msg base.RtmpMsg) {
	if r == nil || r.s == nil {
		return
	}
	cloned := msg.Clone()
	select {
	case <-r.s.stop:
		return
	case r.s.queue <- cloned:
	default:
		count := r.s.queueDrops.Add(1)
		r.s.mediaGap.Store(true)
		if r.s.metrics != nil && r.s.metrics.RecorderGroupQueueDropsTotal != nil {
			r.s.metrics.RecorderGroupQueueDropsTotal.WithLabelValues(r.s.cameraID).Inc()
		}
		// Log the first overflow and periodic milestones, not every frame on the hot path.
		if count == 1 || count%1024 == 0 {
			slog.Warn("recording subscriber queue overflow: media messages dropped",
				"camera_id", r.s.cameraID, "dropped_total", count,
				"queue_capacity", cap(r.s.queue))
		}
	}
}

func (r *recSubscriber) OnStop() {
	if r == nil || r.s == nil {
		return
	}
	// The group is already dropping this subscriber. Do not call RemoveSubscriber here.
	if r.s.owner != nil {
		r.s.owner.forget(r.s.cameraID, false)
		return
	}
	r.s.halt()
}

// AttachVoIP records one call without creating a persistent per-call plan.
func (w *GroupWriter) AttachVoIP(id string, audioOnly bool) {
	if w == nil {
		return
	}
	w.DetachVoIP(id)
	w.SetWriting(id, true)
	encoding := "h264"
	if audioOnly {
		encoding = "audio"
	}
	w.AttachCamera(config.CameraConfig{ID: id, Protocol: "voip", Encoding: encoding, Enabled: true, AudioEnabled: true})
}
func (w *GroupWriter) DetachVoIP(id string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	s := w.sessions[id]
	w.mu.Unlock()
	w.Detach(id)
	if s != nil {
		<-s.done
	}
	w.mu.Lock()
	delete(w.desired, id)
	delete(w.desiredSet, id)
	w.mu.Unlock()
}
