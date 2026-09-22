package recorder

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

var taskLogger = slog.Default().With("component", "record-task")

const (
	ReasonPlanInactive  = "plan_inactive"
	ReasonDeviceRemoved = "device_removed"
	ReasonDeviceStopped = "device_stopped"
	ReasonStreamDown    = "stream_down"
	ReasonUnbind        = "unbind"
	ReasonShutdown      = "shutdown"
)

// StreamOwner describes who a record task writes as.
type StreamOwner struct {
	CameraID     string
	Name         string
	Encoding     string
	AudioEnabled bool
	RTSPURL      string
}

// StreamOwnerFunc resolves a stream to a recording owner. CameraID may equal StreamID for plan-only streams.
type StreamOwnerFunc func(streamID string) StreamOwner

// TaskManager owns in-process record tasks keyed by stream_id.
// A task exists only while a plan (or event window) wants the stream written to disk.
type TaskManager struct {
	engine     media.Engine
	store      SegmentStore
	db         RecordingDB
	metrics    *metrics.Metrics
	eventBus   *event.EventBus
	segmentDur time.Duration
	ownerOf    StreamOwnerFunc
	shouldRec  func(streamID string) bool
	playURL    func(ctx context.Context, streamID string) string
	planMode   func(streamID string) (string, bool)
	adaptEvery func(streamID string) time.Duration
	skip       func(streamID string) bool
	onStart    func(streamID string, rec model.Recorder)
	onStop     func(streamID string, rec model.Recorder)

	mu    sync.Mutex
	tasks map[string]model.Recorder
}

func NewTaskManager(engine media.Engine, store SegmentStore, db RecordingDB, m *metrics.Metrics, bus *event.EventBus, segmentDur time.Duration) *TaskManager {
	if segmentDur <= 0 {
		segmentDur = DefaultSegmentDur
	}
	return &TaskManager{
		engine:     engine,
		store:      store,
		db:         db,
		metrics:    m,
		eventBus:   bus,
		segmentDur: segmentDur,
		tasks:      make(map[string]model.Recorder),
	}
}

func (m *TaskManager) SetOwnerFunc(fn StreamOwnerFunc) {
	m.mu.Lock()
	m.ownerOf = fn
	m.mu.Unlock()
}

func (m *TaskManager) SetShouldRecord(fn func(streamID string) bool) {
	m.mu.Lock()
	m.shouldRec = fn
	m.mu.Unlock()
}

// SetPlayURL supplies the RTSP loopback used when in-process frames are unavailable.
func (m *TaskManager) SetPlayURL(fn func(ctx context.Context, streamID string) string) {
	m.mu.Lock()
	m.playURL = fn
	m.mu.Unlock()
}

// SetPlanMode supplies the recording mode for a stream (adaptive gate, etc.).
func (m *TaskManager) SetPlanMode(fn func(streamID string) (string, bool)) {
	m.mu.Lock()
	m.planMode = fn
	m.mu.Unlock()
}

// SetAdaptiveInterval supplies the calm-state keyframe interval for adaptive plans.
func (m *TaskManager) SetAdaptiveInterval(fn func(streamID string) time.Duration) {
	m.mu.Lock()
	m.adaptEvery = fn
	m.mu.Unlock()
}

// SetSkip reports streams this manager must not record (MJPEG, Xiaomi, timelapse).
func (m *TaskManager) SetSkip(fn func(streamID string) bool) {
	m.mu.Lock()
	m.skip = fn
	m.mu.Unlock()
}

// SetLifecycleHooks observes task start and stop. onStop runs before the recorder is stopped.
func (m *TaskManager) SetLifecycleHooks(onStart, onStop func(streamID string, rec model.Recorder)) {
	m.mu.Lock()
	m.onStart = onStart
	m.onStop = onStop
	m.mu.Unlock()
}

// Ensure starts a record task for streamID if one is not already running.
func (m *TaskManager) Ensure(ctx context.Context, streamID string) error {
	if m == nil {
		return fmt.Errorf("record task manager not configured")
	}
	streamID = strings.TrimSpace(streamID)
	if streamID == "" {
		return fmt.Errorf("stream ID is required")
	}
	if media.IsSubStreamID(streamID) {
		return nil
	}

	m.mu.Lock()
	if m.skip != nil && m.skip(streamID) {
		m.mu.Unlock()
		return nil
	}
	if _, ok := m.tasks[streamID]; ok {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	rec, err := m.newRecorder(ctx, streamID)
	if err != nil {
		return err
	}
	if err := rec.Start(ctx); err != nil {
		return fmt.Errorf("start record task %q: %w", streamID, err)
	}

	m.mu.Lock()
	if _, ok := m.tasks[streamID]; ok {
		m.mu.Unlock()
		_ = rec.Stop()
		return nil
	}
	m.tasks[streamID] = rec
	onStart := m.onStart
	if m.metrics != nil {
		m.metrics.ActiveCameras.Inc()
	}
	m.mu.Unlock()
	if onStart != nil {
		onStart(streamID, rec)
	}
	taskLogger.Info("record task started", "stream_id", streamID)
	return nil
}

// Stop tears down the record task and closes the in-process frame subscription.
func (m *TaskManager) Stop(_ context.Context, streamID, reason string) error {
	if m == nil {
		return nil
	}
	streamID = strings.TrimSpace(streamID)
	if streamID == "" {
		return nil
	}
	m.mu.Lock()
	rec := m.tasks[streamID]
	delete(m.tasks, streamID)
	onStop := m.onStop
	if rec != nil && m.metrics != nil {
		m.metrics.ActiveCameras.Dec()
	}
	m.mu.Unlock()
	if rec == nil {
		return nil
	}
	if onStop != nil {
		onStop(streamID, rec)
	}
	if err := rec.Stop(); err != nil {
		taskLogger.Warn("record task stop failed", "stream_id", streamID, "reason", reason, "error", err)
		return err
	}
	taskLogger.Info("record task stopped", "stream_id", streamID, "reason", reason)
	return nil
}

func (m *TaskManager) Running(streamID string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.tasks[streamID]
	return ok
}

func (m *TaskManager) Recorder(streamID string) model.Recorder {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tasks[streamID]
}

func (m *TaskManager) Status() map[string]model.RecorderStatus {
	out := map[string]model.RecorderStatus{}
	if m == nil {
		return out
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, rec := range m.tasks {
		out[id] = rec.Status()
	}
	return out
}

func (m *TaskManager) RunningIDs() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.tasks))
	for id := range m.tasks {
		ids = append(ids, id)
	}
	return ids
}

// TriggerAdaptive promotes a running adaptive task to full-speed for hold.
func (m *TaskManager) TriggerAdaptive(streamID string, hold time.Duration) {
	rec := m.Recorder(streamID)
	if rec == nil {
		return
	}
	if t, ok := rec.(interface{ TriggerAdaptive(time.Duration) }); ok {
		t.TriggerAdaptive(hold)
	}
}

func (m *TaskManager) Count() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tasks)
}

func (m *TaskManager) StopAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	ids := make([]string, 0, len(m.tasks))
	for id := range m.tasks {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Stop(context.Background(), id, ReasonShutdown)
	}
}

// OnDeviceRemoved stops the task for the device's ingest stream.
func (m *TaskManager) OnDeviceRemoved(ctx context.Context, _, streamID string) {
	_ = m.Stop(ctx, streamID, ReasonDeviceRemoved)
}

// OnDeviceStopped stops recording when ingest is torn down.
func (m *TaskManager) OnDeviceStopped(ctx context.Context, _, streamID string) {
	_ = m.Stop(ctx, streamID, ReasonDeviceStopped)
}

// OnStreamDown stops the task; the scheduler will Ensure again if the plan is still active when the stream returns.
func (m *TaskManager) OnStreamDown(ctx context.Context, streamID string) {
	_ = m.Stop(ctx, streamID, ReasonStreamDown)
}

// OnStreamUp starts the task immediately when a plan wants this stream.
func (m *TaskManager) OnStreamUp(ctx context.Context, streamID string) {
	m.mu.Lock()
	fn := m.shouldRec
	m.mu.Unlock()
	if fn == nil || !fn(streamID) {
		return
	}
	if err := m.Ensure(ctx, streamID); err != nil {
		taskLogger.Debug("record task ensure after stream up failed", "stream_id", streamID, "error", err)
	}
}

func (m *TaskManager) newRecorder(ctx context.Context, streamID string) (model.Recorder, error) {
	owner := StreamOwner{CameraID: streamID, Name: streamID}
	m.mu.Lock()
	ownerFn := m.ownerOf
	m.mu.Unlock()
	if ownerFn != nil {
		owner = ownerFn(streamID)
	}
	if owner.CameraID == "" {
		owner.CameraID = streamID
	}
	m.mu.Lock()
	playURL := m.playURL
	modeFn := m.planMode
	adaptFn := m.adaptEvery
	m.mu.Unlock()
	if owner.RTSPURL == "" && playURL != nil {
		owner.RTSPURL = playURL(ctx, streamID)
	}

	var gate *AdaptiveGate
	if modeFn != nil {
		if mode, ok := modeFn(streamID); ok && mode == storage.RecordingModeAdaptive {
			interval := time.Duration(0)
			if adaptFn != nil {
				interval = adaptFn(streamID)
			}
			gate = NewAdaptiveGate(interval)
		}
	}

	encoding := strings.ToLower(strings.TrimSpace(owner.Encoding))
	if m.engine != nil {
		if info, err := m.engine.GetStream(ctx, streamID); err == nil && info != nil {
			if c := strings.ToLower(strings.TrimSpace(info.VideoCodec)); c != "" {
				encoding = c
			}
		}
	}
	if encoding != string(model.FormatH264) && encoding != string(model.FormatH265) {
		return nil, fmt.Errorf("stream %q codec %q is not recordable", streamID, encoding)
	}

	var frameSource func(context.Context) (media.FrameSubscription, error)
	if m.engine != nil {
		sid := streamID
		frameSource = func(ctx context.Context) (media.FrameSubscription, error) {
			return m.engine.SubscribeFrames(ctx, media.SubscribeFramesRequest{
				StreamID: sid,
				AppName:  "live",
			})
		}
	}

	switch encoding {
	case string(model.FormatH264):
		return NewH264Recorder(H264Config{
			CameraID:     owner.CameraID,
			StreamID:     streamID,
			RTSPURL:      owner.RTSPURL,
			SegmentDur:   m.segmentDur,
			DB:           m.db,
			AudioEnabled: owner.AudioEnabled,
			EventBus:     m.eventBus,
			FrameSource:  frameSource,
			Adaptive:     gate,
		}, m.store, m.metrics), nil
	case string(model.FormatH265):
		return NewH265Recorder(H265Config{
			CameraID:     owner.CameraID,
			StreamID:     streamID,
			RTSPURL:      owner.RTSPURL,
			SegmentDur:   m.segmentDur,
			DB:           m.db,
			AudioEnabled: owner.AudioEnabled,
			EventBus:     m.eventBus,
			FrameSource:  frameSource,
			Adaptive:     gate,
		}, m.store, m.metrics), nil
	default:
		return nil, fmt.Errorf("unsupported encoding %q", encoding)
	}
}
