package recorder

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/q191201771/lal/pkg/base"
	maxlogic "github.com/q191201771/lalmax/logic"
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
	store  segmentStore
	db     recordingDB
	bus    *event.EventBus
	engine media.Engine
	segDur time.Duration
	groups groupFinder
	lookup func(string) (config.CameraConfig, bool)
	should func(string) bool
	accept func(config.CameraConfig) bool

	mu         sync.Mutex
	sessions   map[string]*recSession
	desired    map[string]bool
	desiredSet map[string]bool
	closeOnce  sync.Once
	cancel     context.CancelFunc
}

// NewGroupWriter records from the in-process lalmax group manager.
// engine may be nil in tests; Run then only reconciles groups on its ticker.
func NewGroupWriter(store *storage.Manager, db *storage.DB, bus *event.EventBus, engine media.Engine, segDur time.Duration) *GroupWriter {
	if segDur <= 0 {
		segDur = DefaultSegmentDur
	}
	var recDB recordingDB
	if db != nil {
		recDB = db
	}
	return &GroupWriter{
		store:      store,
		db:         recDB,
		bus:        bus,
		engine:     engine,
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

// Attach subscribes cameraID when its lalmax group already exists.
func (w *GroupWriter) Attach(cameraID string) {
	if w == nil || cameraID == "" || media.IsSubStreamID(cameraID) {
		return
	}
	cam, ok := w.camera(cameraID)
	if !ok {
		return
	}
	w.AttachCamera(cam)
}

// AttachCamera subscribes cam when its group exists and the camera is accepted.
func (w *GroupWriter) AttachCamera(cam config.CameraConfig) {
	if w == nil || cam.ID == "" || media.IsSubStreamID(cam.ID) || !w.Accepts(cam) {
		return
	}
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
		cameraID: cam.ID,
		subID:    "nvr-record-" + cam.ID,
		audio:    cam.AudioEnabled,
		segDur:   w.segDur,
		store:    w.store,
		db:       w.db,
		bus:      w.bus,
		group:    group,
		owner:    w,
		queue:    make(chan base.RtmpMsg, groupQueueSize),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
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
	if w == nil || w.groups == nil {
		return
	}
	seen := make(map[string]struct{})
	w.groups.Each(func(streamName string) {
		if media.IsSubStreamID(streamName) {
			return
		}
		seen[streamName] = struct{}{}
		w.Attach(streamName)
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

	w.Sync()
	var events <-chan media.Event
	if w.engine != nil {
		ch, err := w.engine.SubscribeEvents(ctx, media.EventFilter{Types: []media.EventType{
			media.EventPublisherStarted,
			media.EventPublisherStopped,
			media.EventRelayPullStarted,
			media.EventRelayPullStopped,
			media.EventStreamActive,
			media.EventStreamStopped,
		}})
		if err != nil {
			slog.Error("group recorder failed to subscribe to stream events", "error", err)
		} else {
			events = ch
		}
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			w.Sync()
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			w.onEvent(ev)
		}
	}
}

func (w *GroupWriter) onEvent(ev media.Event) {
	if media.IsSubStreamID(ev.StreamID) {
		return
	}
	switch ev.Type {
	case media.EventPublisherStopped, media.EventRelayPullStopped, media.EventStreamStopped:
		w.Detach(ev.StreamID)
	default:
		w.Attach(ev.StreamID)
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
	case r.s.queue <- cloned:
	default:
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
