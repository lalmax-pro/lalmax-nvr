package camera

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/health"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/merge"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/lalmax-pro/lalmax-nvr/internal/recorder"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi"
)

// cam2Recorder is a controllable model.Recorder used to drive manager branches.
type cam2Recorder struct {
	status   model.RecorderStatus
	startErr error
	stopErr  error
	onStart  func()
	onStop   func()
	started  int
	stopped  int
}

func (r *cam2Recorder) Start(context.Context) error {
	r.started++
	if r.onStart != nil {
		r.onStart()
	}
	return r.startErr
}

func (r *cam2Recorder) Stop() error {
	r.stopped++
	if r.onStop != nil {
		r.onStop()
	}
	return r.stopErr
}

func (r *cam2Recorder) Status() model.RecorderStatus { return r.status }

// cam2PausableRecorder additionally implements model.PausableRecorder.
type cam2PausableRecorder struct {
	cam2Recorder
	paused bool
}

func (r *cam2PausableRecorder) Pause()         { r.paused = true }
func (r *cam2PausableRecorder) Resume()        { r.paused = false }
func (r *cam2PausableRecorder) IsPaused() bool { return r.paused }

// cam2EventEngine streams media events for MonitorStreamEvents.
type cam2EventEngine struct {
	stubMediaEngine
	events chan media.Event
	subErr error
}

func (e *cam2EventEngine) SubscribeEvents(context.Context, media.EventFilter) (<-chan media.Event, error) {
	if e.subErr != nil {
		return nil, e.subErr
	}
	return e.events, nil
}

// cam2Subscriber is an in-memory onvif.EventSubscriber.
type cam2Subscriber struct {
	unsubscribes   int
	unsubscribeErr error
}

func (s *cam2Subscriber) Subscribe(context.Context, string) error { return nil }
func (s *cam2Subscriber) Unsubscribe(context.Context, string) error {
	s.unsubscribes++
	return s.unsubscribeErr
}
func (s *cam2Subscriber) GetEventMessages(context.Context) ([]onvif.ONVIFEvent, error) {
	return nil, nil
}

// cam2Repo is a persistence stub whose unhandled methods fail loudly through the
// embedded nil interface, so only the calls under test can succeed.
type cam2Repo struct {
	storage.CameraManagerRepository
	failUpsert        bool
	failExtras        bool
	failBind          bool
	failProfile       bool
	failMetadata      bool
	failActivation    bool
	failStableID      bool
	failArchiveDB     bool
	failArchiveRecs   bool
	failUnarchiveDB   bool
	failUnarchiveRecs bool
	failSetStream     bool
	binding           *storage.StreamBinding
	err               error
	calls             []string
}

func (r *cam2Repo) record(name string) error {
	r.calls = append(r.calls, name)
	return r.err
}

func (r *cam2Repo) UpsertCamera(context.Context, string, string, string, string, string, string, string, bool, string, string, string, ...string) error {
	if r.failUpsert {
		return r.record("upsert")
	}
	return nil
}

func (r *cam2Repo) SaveCameraExtras(context.Context, config.CameraConfig) error {
	if r.failExtras {
		return r.record("extras")
	}
	return nil
}

func (r *cam2Repo) BindStreamToCamera(context.Context, string, string) error {
	if r.failBind {
		return r.record("bind")
	}
	return nil
}

func (r *cam2Repo) GetBindingByCameraID(context.Context, string) (*storage.StreamBinding, error) {
	return r.binding, nil
}

func (r *cam2Repo) SetCameraStream(context.Context, string, string) error {
	if r.failSetStream {
		return r.record("set-stream")
	}
	return nil
}

func (r *cam2Repo) UpdateCameraProfileName(context.Context, string, string) error {
	if r.failProfile {
		return r.record("profile-name")
	}
	return nil
}

func (r *cam2Repo) UpdateCameraMetadata(context.Context, string, string, string, string, string, string, int) error {
	if r.failMetadata {
		return r.record("metadata")
	}
	return nil
}

func (r *cam2Repo) UpdateCameraActivation(context.Context, string, string) error {
	if r.failActivation {
		return r.record("activation")
	}
	return nil
}

func (r *cam2Repo) UpdateCameraStableID(context.Context, string, string) error {
	if r.failStableID {
		return r.record("stable-id")
	}
	return nil
}

func (r *cam2Repo) ArchiveCameraDB(context.Context, string) error {
	if r.failArchiveDB {
		return r.record("archive-db")
	}
	return nil
}

func (r *cam2Repo) UnarchiveCameraDB(context.Context, string) error {
	if r.failUnarchiveDB {
		return r.record("unarchive-db")
	}
	return nil
}

func (r *cam2Repo) ArchiveAllRecordings(context.Context, string) (int64, error) {
	if r.failArchiveRecs {
		return 0, r.record("archive-recordings")
	}
	return 3, nil
}

func (r *cam2Repo) UnarchiveAllRecordings(context.Context, string) (int64, error) {
	if r.failUnarchiveRecs {
		return 0, r.record("unarchive-recordings")
	}
	return 2, nil
}

// cam2NewManager builds a pure in-memory manager (no storage, no database).
func cam2NewManager(cfg *config.Config) *CameraManager {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if cfg.Storage.SegmentDuration == "" {
		cfg.Storage.SegmentDuration = "1m"
	}
	return NewCameraManager(cfg, nil, nil, "")
}

// cam2StopMachinery tears down recorders created by the manager under test.
func cam2StopMachinery(t *testing.T, mgr *CameraManager) {
	t.Helper()
	t.Cleanup(func() {
		mgr.StopAllStreamRecordings()
		_ = mgr.Stop()
	})
}

func TestCam2AccessorsAndHelpers(t *testing.T) {
	mgr := cam2NewManager(&config.Config{
		Cameras: []config.CameraConfig{
			{ID: "cam-a", StreamID: "ingest-a", Protocol: "rtsp", Encoding: "h264", Enabled: true},
			{ID: "cam-b", Protocol: "rtsp", Encoding: "h264", Enabled: true},
		},
	})
	ctx := context.Background()

	mgr.SetEventBus(nil)
	mgr.SetEventManager(nil)
	mgr.SetRecordTasks(nil)
	mgr.SetMediaEngine(nil)

	require.Equal(t, "cam-a", mgr.CameraByStream("ingest-a").ID)
	require.Equal(t, "cam-b", mgr.CameraByStream("cam-b").ID)
	require.Nil(t, mgr.CameraByStream(""))
	require.Nil(t, mgr.CameraByStream("missing"))

	rec := &cam2Recorder{status: model.StatusRecording}
	mgr.mu.Lock()
	mgr.recorders["cam-a"] = rec
	mgr.mu.Unlock()
	require.Same(t, rec, mgr.GetRecorder("cam-a"))
	require.Nil(t, mgr.GetRecorder("cam-b"))

	detail := &model.CameraErrorDetail{Type: "network", Message: "boom"}
	mgr.SetErrorDetail("cam-a", detail)
	require.Same(t, detail, mgr.GetErrorDetail("cam-a"))
	mgr.SetErrorDetail("cam-a", nil)
	require.Nil(t, mgr.GetErrorDetail("cam-a"))

	list := mgr.ListCameraConfigs()
	require.Len(t, list, 2)
	list[0].ID = "mutated"
	require.Equal(t, "cam-a", mgr.GetCameraConfig("cam-a").ID)
	require.Nil(t, mgr.GetCameraConfig("missing"))

	value := "v"
	number := 9
	empty := ""
	require.Equal(t, "", strPtrOrEmpty(nil))
	require.Equal(t, "v", strPtrOrEmpty(&value))
	require.Equal(t, "", strPtrOrEmpty(&empty))
	require.Equal(t, 0, intPtrOrZero(nil))
	require.Equal(t, 9, intPtrOrZero(&number))

	require.False(t, isDupInStreamError(nil))
	require.True(t, isDupInStreamError(errors.New("group: in stream already exist at group")))
	require.False(t, isDupInStreamError(errors.New("other failure")))
	require.False(t, isMediaStreamNotFound(nil))
	require.True(t, isMediaStreamNotFound(errors.New("HTTP 404")))
	require.True(t, isMediaStreamNotFound(errors.New("stream Not Found")))
	require.False(t, isMediaStreamNotFound(errors.New("other failure")))

	var nilMgr *CameraManager
	require.Nil(t, nilMgr.recordTaskManager())
	require.False(t, nilMgr.RecordsViaTask(config.CameraConfig{Protocol: "rtsp", Encoding: "h264"}))

	_, err := mgr.GetONVIFClient(ctx, "missing")
	require.Error(t, err)

	// A refused loopback probe fails fast and leaves the encoding undetected.
	require.Empty(t, mgr.probeGB28181Encoding("rtsp://127.0.0.1:1/stream", config.CameraConfig{ID: "cam-a"}))
}

func TestCam2SetProtocolEnabledStopsMatchingCameras(t *testing.T) {
	mgr := cam2NewManager(&config.Config{
		Cameras: []config.CameraConfig{
			{ID: "rtsp-1", Protocol: "rtsp", Encoding: "h264", Enabled: true},
			{ID: "onvif-1", Protocol: "onvif", Encoding: "h264", Enabled: true},
		},
	})
	rtspRec := &cam2Recorder{status: model.StatusRecording}
	onvifRec := &cam2Recorder{status: model.StatusRecording}
	mgr.recorders["rtsp-1"] = rtspRec
	mgr.recorders["onvif-1"] = onvifRec
	mgr.recorders["orphan"] = &cam2Recorder{status: model.StatusRecording}

	mgr.SetProtocolEnabled("rtsp", true) // enabling never stops anything
	require.Len(t, mgr.recorders, 3)

	mgr.SetProtocolEnabled("rtsp", false)
	require.NotContains(t, mgr.recorders, "rtsp-1")
	require.Contains(t, mgr.recorders, "onvif-1")
	require.Contains(t, mgr.recorders, "orphan")
	require.Equal(t, 0, onvifRec.stopped)
}

func TestCam2GroupWriterBranches(t *testing.T) {
	ctx := context.Background()
	mgr := cam2NewManager(&config.Config{
		Cameras: []config.CameraConfig{
			{ID: "group-cam", StreamID: "group-ingest", Protocol: "rtmp-pull", Encoding: "h264", URL: "rtmp://127.0.0.1/live/group-cam", Enabled: true},
			{ID: "http-cam", Protocol: "http", Encoding: "jpeg", URL: "http://127.0.0.1/jpg", Enabled: true},
		},
	})
	t.Cleanup(mgr.StopAllStreamRecordings)

	mgr.SetGroupWriter(nil)
	require.False(t, mgr.groupCovers(mgr.cfg.Cameras[0]))

	writer := recorder.NewGroupWriter(nil, nil, nil, nil, time.Minute)
	mgr.SetGroupWriter(writer)
	defer mgr.SetGroupWriter(nil)

	// The accept callback installed by SetGroupWriter drives Covers.
	require.True(t, writer.Covers(mgr.cfg.Cameras[0]))
	require.False(t, writer.Covers(mgr.cfg.Cameras[1]))
	require.True(t, mgr.groupCovers(mgr.cfg.Cameras[0]))

	// Adaptive plans stay on the record task path instead of the group writer.
	mgr.SetRecordingModeSource(func(string) (string, bool) { return storage.RecordingModeAdaptive, true })
	require.False(t, mgr.groupCovers(mgr.cfg.Cameras[0]))
	mgr.SetRecordingModeSource(nil)

	// startRecorderHeld delegates to startGroupRecording when the writer covers it.
	mgr.SetMediaEngine(&stubMediaEngine{})
	require.NoError(t, mgr.startRecorder(ctx, mgr.cfg.Cameras[0], time.Minute))
	require.Equal(t, 0, mgr.RecorderCount(), "group recording keeps no local recorder")

	// Status keeps cameras owned by the writer but not attached out of the result.
	require.NotContains(t, mgr.Status(), "group-cam")

	// Pause/resume through the group writer.
	require.NoError(t, mgr.PauseRecording(ctx, "group-cam"))
	require.True(t, mgr.RecordingPaused("group-cam"))
	require.NoError(t, mgr.PauseRecording(ctx, "group-cam"))
	require.NoError(t, mgr.ResumeRecording(ctx, "group-cam"))
	require.False(t, mgr.RecordingPaused("group-cam"))
	require.EqualError(t, mgr.ResumeRecording(ctx, "group-cam"), `camera "group-cam" recording is not paused`)

	// StopCamera detaches the writer side for a camera it covers.
	mgr.recorders["group-cam"] = &cam2Recorder{status: model.StatusRecording}
	require.NoError(t, mgr.StopCamera(ctx, "group-cam"))
	require.Equal(t, 0, mgr.RecorderCount())
}

func TestCam2RecordTaskDelegation(t *testing.T) {
	ctx := context.Background()
	mgr, _, db, _ := newTestManager(t)
	cam2StopMachinery(t, mgr)

	engine := &stubMediaEngine{}
	mgr.SetMediaEngine(engine)
	tasks := recorder.NewTaskManager(nil, nil, db, nil, nil, time.Minute)
	tasks.SetSkip(func(string) bool { return true })
	mgr.SetRecordTasks(tasks)
	require.NotNil(t, mgr.recordTaskManager())

	require.True(t, mgr.RecordsViaTask(config.CameraConfig{Protocol: "rtsp", Encoding: "h264"}))
	require.True(t, mgr.RecordsViaTask(config.CameraConfig{Protocol: "rtmp-pull", Encoding: "h265"}))
	require.False(t, mgr.RecordsViaTask(config.CameraConfig{Protocol: "rtmp-pull", Encoding: "mjpeg"}))
	require.False(t, mgr.RecordsViaTask(config.CameraConfig{Protocol: "http", Encoding: "jpeg"}))
	require.False(t, mgr.RecordsViaTask(config.CameraConfig{Protocol: "timelapse", Encoding: "h264"}))
	require.False(t, mgr.RecordsViaTask(config.CameraConfig{Protocol: "unknown", Encoding: "h264"}))

	mgr.SetMediaEngine(nil)
	require.False(t, mgr.RecordsViaTask(config.CameraConfig{Protocol: "rtsp", Encoding: "h264"}))
	mgr.SetMediaEngine(engine)

	// Task-backed cameras are reported as stopped while no task runs.
	statuses := mgr.Status()
	require.Equal(t, model.StatusStopped, statuses["cam-h264"])
	require.NotContains(t, statuses, "cam-mjpeg")
	require.NotContains(t, statuses, "cam-jpeg")
	require.Equal(t, model.StatusStopped, mgr.CameraStatus("cam-h264"))
	require.Equal(t, model.StatusError, mgr.CameraStatus("missing"))

	// snapshotRecorders merges manager recorders with live record tasks.
	require.Empty(t, mgr.snapshotRecorders())

	// startRecorderHeld hands the ingest to the record task instead of a recorder.
	mgr.SetRecordingDecision(func(string) (bool, bool) { return true, true })
	require.NoError(t, mgr.startRecorder(ctx, *mgr.GetCameraConfig("cam-h264"), time.Minute))
	mgr.SetRecordingDecision(nil)

	// Pause stops the record task, resume re-ensures it.
	require.NoError(t, mgr.PauseRecording(ctx, "cam-h264"))
	require.True(t, mgr.RecordingPaused("cam-h264"))
	require.NoError(t, mgr.PauseRecording(ctx, "cam-h264"))
	require.NoError(t, mgr.ResumeRecording(ctx, "cam-h264"))
	require.False(t, mgr.RecordingPaused("cam-h264"))

	// stopRecordTask is reached from StopCamera and RemoveCamera.
	require.NoError(t, mgr.StopCamera(ctx, "cam-h264"))
	require.NoError(t, mgr.RemoveCamera(ctx, "cam-mjpeg"))

	// Stop also drains the task manager.
	require.NoError(t, mgr.Stop())
}

func TestCam2StatusWithTasksAndWriter(t *testing.T) {
	mgr := cam2NewManager(&config.Config{
		Cameras: []config.CameraConfig{
			{ID: "task-cam", StreamID: "task-ingest", Protocol: "rtsp", Encoding: "h264", Enabled: true},
			{ID: "group-cam", Protocol: "rtmp-pull", Encoding: "h264", URL: "rtmp://127.0.0.1/live/x", Enabled: true},
			{ID: "off-cam", Protocol: "http", Encoding: "jpeg", Enabled: false},
		},
	})
	mgr.SetMediaEngine(&stubMediaEngine{})
	tasks := recorder.NewTaskManager(nil, nil, nil, nil, nil, time.Minute)
	tasks.SetSkip(func(string) bool { return true })
	mgr.SetRecordTasks(tasks)
	mgr.SetGroupWriter(recorder.NewGroupWriter(nil, nil, nil, nil, time.Minute))
	defer mgr.SetGroupWriter(nil)

	mgr.pausedRecorders["task-cam"] = true
	statuses := mgr.Status()
	require.Equal(t, model.StatusPaused, statuses["task-cam"])
	require.Equal(t, model.StatusStopped, statuses["group-cam"], "task-backed group camera")
	require.NotContains(t, statuses, "off-cam")
	require.Equal(t, model.StatusStopped, mgr.CameraStatus("task-cam"), "CameraStatus does not report the pause flag")
	require.Empty(t, mgr.snapshotRecorders())
}

func TestCam2MonitorStreamEvents(t *testing.T) {
	ctx := context.Background()
	mgr := cam2NewManager(&config.Config{Cameras: []config.CameraConfig{
		{ID: "cam-m", StreamID: "ingest-m", Protocol: "rtsp", Encoding: "h264", Enabled: true},
	}})
	require.NotPanics(t, func() { mgr.MonitorStreamEvents(ctx) }, "a manager without an engine must be a no-op")

	engine := &cam2EventEngine{events: make(chan media.Event)}
	engine.subErr = errors.New("subscribe failed")
	mgr.SetMediaEngine(engine)
	mgr.MonitorStreamEvents(ctx) // logs and returns on subscribe failure

	// Cancellation ends the loop.
	engine.subErr = nil
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.MonitorStreamEvents(runCtx)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("MonitorStreamEvents did not stop on cancellation")
	}

	// A closed channel ends the loop too.
	events := make(chan media.Event)
	engine.events = events
	done = make(chan struct{})
	go func() {
		defer close(done)
		mgr.MonitorStreamEvents(ctx)
	}()
	events <- media.Event{Type: media.EventStreamActive, StreamID: "ingest-m"}
	close(events)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("MonitorStreamEvents did not stop on channel close")
	}
}

func TestCam2HandleStreamEventBranches(t *testing.T) {
	ctx := context.Background()
	mgr := cam2NewManager(&config.Config{
		Cameras: []config.CameraConfig{
			{ID: "rtsp-cam", StreamID: "rtsp-ingest", Protocol: "rtsp", Encoding: "", Enabled: true},
			{ID: "xiaomi-cam", StreamID: "xiaomi-ingest", Protocol: "xiaomi", Encoding: "h264", Enabled: true},
			{ID: "off-cam", StreamID: "off-ingest", Protocol: "rtsp", Encoding: "", Enabled: false},
			{ID: "busy-cam", StreamID: "busy-ingest", Protocol: "rtsp", Encoding: "", Enabled: true},
		},
	})
	t.Cleanup(mgr.StopAllStreamRecordings)

	// Unknown stream: nothing to do.
	mgr.handleStreamEvent(ctx, media.Event{Type: media.EventStreamStopped, StreamID: "unknown"})

	// Offline events stop the recorder of a non-xiaomi camera.
	stopped := 0
	mgr.recorders["rtsp-cam"] = &cam2Recorder{status: model.StatusRecording, onStop: func() { stopped++ }}
	mgr.handleStreamEvent(ctx, media.Event{Type: media.EventRelayPullStopped, StreamID: "rtsp-ingest"})
	require.Equal(t, 1, stopped)
	require.NotContains(t, mgr.recorders, "rtsp-cam")

	// Xiaomi cameras are torn down by the device session, not by stream events.
	mgr.recorders["xiaomi-cam"] = &cam2Recorder{status: model.StatusRecording}
	mgr.handleStreamEvent(ctx, media.Event{Type: media.EventStreamStopped, StreamID: "xiaomi-ingest"})
	require.Contains(t, mgr.recorders, "xiaomi-cam")

	// Online events are ignored for disabled cameras.
	mgr.handleStreamEvent(ctx, media.Event{Type: media.EventPublisherStarted, StreamID: "off-ingest"})
	require.NotContains(t, mgr.recorders, "off-cam")

	// Online events are ignored when the recorder is already running.
	mgr.recorders["busy-cam"] = &cam2Recorder{status: model.StatusRecording}
	mgr.handleStreamEvent(ctx, media.Event{Type: media.EventStreamActive, StreamID: "busy-ingest"})
	require.Contains(t, mgr.recorders, "busy-cam")
	mgr.mu.Lock()
	delete(mgr.recorders, "busy-cam")
	mgr.mu.Unlock()

	// Online event without a recorder tries to start one; the unsupported
	// encoding makes createRecorder return nil, so no goroutine is left behind.
	mgr.handleStreamEvent(ctx, media.Event{Type: media.EventPublisherStarted, StreamID: "busy-ingest"})
	require.NotContains(t, mgr.recorders, "busy-cam")
}

func TestCam2HandleStreamEventWithRecordTask(t *testing.T) {
	ctx := context.Background()
	mgr := cam2NewManager(&config.Config{
		Cameras: []config.CameraConfig{
			{ID: "task-cam", StreamID: "task-ingest", Protocol: "rtsp", Encoding: "h264", Enabled: true},
		},
	})
	mgr.SetMediaEngine(&stubMediaEngine{})
	tasks := recorder.NewTaskManager(nil, nil, nil, nil, nil, time.Minute)
	tasks.SetSkip(func(string) bool { return true })
	mgr.SetRecordTasks(tasks)

	mgr.handleStreamEvent(ctx, media.Event{Type: media.EventStreamStopped, StreamID: "task-ingest"})
	mgr.handleStreamEvent(ctx, media.Event{Type: media.EventStreamActive, StreamID: "task-ingest"})
	require.Equal(t, 0, mgr.RecorderCount())
}

func TestCam2MediaPullBranches(t *testing.T) {
	ctx := context.Background()
	mgr, _, db, _ := newTestManager(t)
	engine := &stubMediaEngine{}
	mgr.SetMediaEngine(engine)

	cam := config.CameraConfig{ID: "pull-cam", StreamID: "pull-ingest", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/stream"}

	// Source resolution failure bubbles up.
	mgr.onvifStreamResolver = func(context.Context, config.CameraConfig) (string, error) {
		return "", errors.New("no stream uri")
	}
	require.EqualError(t, mgr.startMediaPullLocked(ctx, config.CameraConfig{ID: "onvif-x", Protocol: "onvif"}), "no stream uri")

	// Empty source URL.
	mgr.onvifStreamResolver = func(context.Context, config.CameraConfig) (string, error) { return "", nil }
	require.EqualError(t, mgr.startMediaPullLocked(ctx, config.CameraConfig{ID: "onvif-y", Protocol: "onvif"}), "empty media source URL")
	mgr.onvifStreamResolver = nil

	// Duplicate in-stream is not an error.
	engine.startErr = errors.New("in stream already exist at group")
	require.NoError(t, mgr.startMediaPullLocked(ctx, cam))

	// Any other pull failure is returned.
	sentinel := errors.New("pull rejected")
	engine.startErr = sentinel
	require.ErrorIs(t, mgr.startMediaPullLocked(ctx, cam), sentinel)
	engine.startErr = nil

	// Relay-pull protocols pass their URL through untouched.
	source, err := mgr.resolveMediaSourceURL(ctx, config.CameraConfig{Protocol: "rtmp-pull", URL: "rtmp://127.0.0.1/live/a"})
	require.NoError(t, err)
	require.Equal(t, "rtmp://127.0.0.1/live/a", source)

	// Unsupported protocols have no source.
	source, err = mgr.resolveMediaSourceURL(ctx, config.CameraConfig{Protocol: "http"})
	require.NoError(t, err)
	require.Empty(t, source)

	// stopMediaPullLocked resolves the bound stream first and stops both.
	require.NoError(t, db.BindStreamToCamera(ctx, "bound-ingest", "bound-cam"))
	engine.stopPulls = nil
	require.NoError(t, mgr.stopMediaPullLocked(ctx, "bound-cam"))
	require.Equal(t, []string{"bound-ingest", "bound-cam"}, engine.stopPulls)

	// A real stop error surfaces, a "not found" one is tolerated.
	engine.stopErr = errors.New("stop failed")
	require.EqualError(t, mgr.stopMediaPullLocked(ctx, "bound-cam"), "stop failed")
	engine.stopErr = errors.New("stream 404 not found")
	require.NoError(t, mgr.stopMediaPullLocked(ctx, "bound-cam"))
	engine.stopErr = nil

	// Missing engine or camera id is a no-op.
	mgr.SetMediaEngine(nil)
	require.NoError(t, mgr.stopMediaPullLocked(ctx, "bound-cam"))
	mgr.SetMediaEngine(engine)
	require.NoError(t, mgr.stopMediaPullLocked(ctx, ""))
}

func TestCam2PrepareONVIFEncoderConfigFallback(t *testing.T) {
	ctx := context.Background()
	mgr := cam2NewManager(nil)
	mgr.SetMediaEngine(&stubMediaEngine{})
	mgr.onvifProfileResolver = func(context.Context, config.CameraConfig) ([]onvif.DeviceProfile, error) {
		return []onvif.DeviceProfile{{Token: "profile-1"}}, nil
	}

	// A profile without encoding info falls back to VideoEncoderConfigurations,
	// which needs a device connection: an invalid URL fails without any I/O.
	cam := config.CameraConfig{ID: "onvif-cam", Protocol: "onvif", URL: "://bad"}
	prepared, err := mgr.prepareCameraForStart(ctx, cam)
	require.NoError(t, err)
	require.Equal(t, "profile-1", prepared.ProfileToken)
	require.Empty(t, prepared.Encoding)

	require.Empty(t, mgr.probeEncodingFromVideoEncoderConfigs(ctx, cam, "profile-1"))

	// Non-ONVIF cameras are returned untouched even with a media engine.
	rtspCam := config.CameraConfig{ID: "rtsp-cam", Protocol: "rtsp", Encoding: "h264"}
	prepared, err = mgr.prepareCameraForStart(ctx, rtspCam)
	require.NoError(t, err)
	require.Equal(t, rtspCam, prepared)
}

func TestCam2ProbeONVIFEncodingAndBuildURL(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{Storage: config.StorageConfig{SegmentDuration: "1m"}}
	cfg.Cameras = []config.CameraConfig{{ID: "onvif-probe", Protocol: "onvif", Encoding: "", Enabled: true}}
	mgr := cam2NewManager(cfg)

	cam := config.CameraConfig{ID: "onvif-probe", Protocol: "onvif", URL: "rtsp://127.0.0.1:1/stream", FrameWatchdogTimeout: "9s"}

	// Profile lookup failure.
	sentinel := errors.New("no profiles")
	mgr.onvifProfileResolver = func(context.Context, config.CameraConfig) ([]onvif.DeviceProfile, error) {
		return nil, sentinel
	}
	rec, url := mgr.probeONVIFEncodingAndBuildURL(ctx, cam, time.Minute)
	require.Nil(t, rec)
	require.Empty(t, url)

	// No H264/H265 profile.
	mgr.onvifProfileResolver = func(context.Context, config.CameraConfig) ([]onvif.DeviceProfile, error) {
		return []onvif.DeviceProfile{{Token: "p", Encoding: "MPEG4"}}, nil
	}
	rec, url = mgr.probeONVIFEncodingAndBuildURL(ctx, cam, time.Minute)
	require.Nil(t, rec)
	require.Empty(t, url)

	// H264 detection creates an H264 recorder backed by the relay URL.
	engine := &stubMediaEngine{}
	mgr.SetMediaEngine(engine)
	mgr.onvifStreamResolver = func(context.Context, config.CameraConfig) (string, error) {
		return "rtsp://127.0.0.1:1/stream", nil
	}
	mgr.onvifProfileResolver = func(context.Context, config.CameraConfig) ([]onvif.DeviceProfile, error) {
		return []onvif.DeviceProfile{{Token: "p-h264", Encoding: "H264"}}, nil
	}
	rec, url = mgr.probeONVIFEncodingAndBuildURL(ctx, cam, time.Minute)
	require.IsType(t, &recorder.H264Recorder{}, rec)
	require.Equal(t, "rtsp://127.0.0.1/live/onvif-probe", url)

	// H265 detection.
	mgr.onvifProfileResolver = func(context.Context, config.CameraConfig) ([]onvif.DeviceProfile, error) {
		return []onvif.DeviceProfile{{Token: "p-h265", Encoding: "H265"}}, nil
	}
	rec, url = mgr.probeONVIFEncodingAndBuildURL(ctx, cam, time.Minute)
	require.IsType(t, &recorder.H265Recorder{}, rec)
	require.Equal(t, "rtsp://127.0.0.1/live/onvif-probe", url)

	// Play URL failure after the pull restart.
	mgr.SetMediaEngine(&issue74PlayEngine{playErr: errors.New("no play url")})
	mgr.onvifProfileResolver = func(context.Context, config.CameraConfig) ([]onvif.DeviceProfile, error) {
		return []onvif.DeviceProfile{{Token: "p-h264", Encoding: "H264"}}, nil
	}
	rec, url = mgr.probeONVIFEncodingAndBuildURL(ctx, cam, time.Minute)
	require.Nil(t, rec)
	require.Empty(t, url)

	// Pull restart failure.
	failing := &stubMediaEngine{startErr: errors.New("pull failed")}
	mgr.SetMediaEngine(failing)
	rec, url = mgr.probeONVIFEncodingAndBuildURL(ctx, cam, time.Minute)
	require.Nil(t, rec)
	require.Empty(t, url)
}

func TestCam2StartRecorderHeldErrorBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("persist prepared state", func(t *testing.T) {
		cfg := &config.Config{Storage: config.StorageConfig{SegmentDuration: "1m"}}
		cfg.Cameras = []config.CameraConfig{{ID: "onvif-persist", Protocol: "onvif", Encoding: "h264", Enabled: true}}
		mgr := NewCameraManager(cfg, nil, &cam2Repo{failUpsert: true, err: errors.New("upsert failed")}, "")
		mgr.SetMediaEngine(&stubMediaEngine{})
		mgr.onvifProfileResolver = func(context.Context, config.CameraConfig) ([]onvif.DeviceProfile, error) {
			return []onvif.DeviceProfile{{Token: "p", Encoding: "H265"}}, nil
		}
		err := mgr.startRecorder(ctx, cfg.Cameras[0], time.Minute)
		require.ErrorContains(t, err, "failed to persist prepared camera state")
	})

	t.Run("media pull", func(t *testing.T) {
		mgr := cam2NewManager(nil)
		mgr.SetMediaEngine(&stubMediaEngine{startErr: errors.New("pull failed")})
		cam := config.CameraConfig{ID: "rtmp-cam", Protocol: "rtmp-pull", Encoding: "h264", URL: "rtmp://127.0.0.1/live/x"}
		err := mgr.startRecorder(ctx, cam, time.Minute)
		require.ErrorContains(t, err, "failed to start media pull")
	})

	t.Run("unsupported protocol", func(t *testing.T) {
		mgr := cam2NewManager(nil)
		mgr.SetMediaEngine(&stubMediaEngine{})
		cam := config.CameraConfig{ID: "http-cam", Protocol: "http", Encoding: "h264", URL: "http://127.0.0.1/jpg"}
		err := mgr.startRecorder(ctx, cam, time.Minute)
		require.ErrorContains(t, err, "does not support recording")
	})

	t.Run("health overrides", func(t *testing.T) {
		cfg := &config.Config{
			Storage: config.StorageConfig{SegmentDuration: "1m"},
			Health:  config.HealthConfig{Enabled: true},
		}
		cfg.Cameras = []config.CameraConfig{{ID: "rtsp-health", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x", Enabled: true}}
		mgr := NewCameraManager(cfg, nil, nil, "", metrics.NewMetrics())
		cam2StopMachinery(t, mgr)
		mgr.SetMediaEngine(&stubMediaEngine{})
		require.NoError(t, mgr.startRecorder(ctx, cfg.Cameras[0], time.Minute))
		require.Equal(t, 1, mgr.RecorderCount())
	})
}

func TestCam2AddCameraPersistenceBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("upsert and extras failures", func(t *testing.T) {
		repo := &cam2Repo{failUpsert: true, err: errors.New("upsert failed")}
		cfg := &config.Config{Storage: config.StorageConfig{SegmentDuration: "bogus"}}
		mgr := NewCameraManager(cfg, nil, repo, filepath.Join(t.TempDir(), "missing", "config.yaml"))
		id, err := mgr.AddCamera(ctx, config.CameraConfig{
			ID: "cam-upsert", Name: "Upsert", Protocol: "rtsp", Encoding: "h264",
			URL: "rtsp://127.0.0.1:1/x", Enabled: false,
		})
		require.NoError(t, err)
		require.Equal(t, "cam-upsert", id)
		require.Contains(t, repo.calls, "upsert")
	})

	t.Run("profile name and extras and bind failures", func(t *testing.T) {
		repo := &cam2Repo{failProfile: true, failExtras: true, failBind: true, err: errors.New("boom")}
		cfg := &config.Config{Storage: config.StorageConfig{SegmentDuration: "1m"}}
		mgr := NewCameraManager(cfg, nil, repo, "")
		id, err := mgr.AddCamera(ctx, config.CameraConfig{
			ID: "cam-extras", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x", Enabled: false,
		})
		require.NoError(t, err)
		require.Equal(t, "cam-extras", id)
		require.Contains(t, repo.calls, "extras")
	})

	t.Run("activation and stable id", func(t *testing.T) {
		cfg := &config.Config{Storage: config.StorageConfig{SegmentDuration: "1m"}}
		mgr := NewCameraManager(cfg, nil, &cam2Repo{}, "")
		id, err := mgr.AddCamera(ctx, config.CameraConfig{
			ID: "cam-state", Protocol: "rtsp", Encoding: "", URL: "rtsp://127.0.0.1:1/x",
			Enabled: true, ActivationState: config.ActivationActive, StableID: "SN-42",
		})
		require.NoError(t, err)
		require.Equal(t, "cam-state", id)
		// The unsupported encoding makes the recorder start fail, which is logged only.
		require.Equal(t, 0, mgr.RecorderCount())
	})

	t.Run("recorder starts", func(t *testing.T) {
		cfg := &config.Config{Storage: config.StorageConfig{SegmentDuration: "1m"}}
		mgr := NewCameraManager(cfg, nil, &cam2Repo{}, "")
		cam2StopMachinery(t, mgr)
		mgr.SetMediaEngine(&stubMediaEngine{})
		_, err := mgr.AddCamera(ctx, config.CameraConfig{
			ID: "cam-start", Protocol: "rtsp", Encoding: "h264",
			URL: "rtsp://127.0.0.1:1/x", Enabled: true,
		})
		require.NoError(t, err)
		require.Equal(t, 1, mgr.RecorderCount())
	})
}

func TestCam2UpdateCameraAllFields(t *testing.T) {
	ctx := context.Background()
	mgr, _, db, _ := newTestManager(t)
	cam2StopMachinery(t, mgr)
	mgr.metrics = metrics.NewMetrics()

	_, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID: "cam-upd", Name: "old", Protocol: "onvif", Encoding: "h264",
		URL: "rtsp://127.0.0.1:1/old", Enabled: false,
	})
	require.NoError(t, err)

	name, rawURL := "new name", "rtsp://127.0.0.1:1/new"
	protocol, encoding := "onvif", "h265"
	transport, username, password := " UDP ", "user", "pass"
	endpoint, token, streamEncoding := "", "token-1", "H265"
	audio := true
	subURL, subToken := "rtsp://127.0.0.1:1/sub", "sub-token"
	hints := []string{"10.0.0.0/24"}
	stableID, activation := "SN-1", config.ActivationActive
	adaptive := &config.CameraAdaptiveConfig{TimelapseInterval: "10s"}
	longitude, latitude := 1.25, 2.5
	description, location, brand, modelName, serialNumber := "d", "l", "b", "m", "s"
	retentionDays := 14

	updated, err := mgr.UpdateCamera(ctx, "cam-upd", CameraUpdate{
		Name: &name, URL: &rawURL, Protocol: &protocol, Encoding: &encoding,
		RTSPTransport: &transport, Username: &username, Password: &password,
		ONVIFEndpoint: &endpoint, ProfileToken: &token, StreamEncoding: &streamEncoding,
		AudioEnabled: &audio, SubStreamURL: &subURL, SubProfileToken: &subToken,
		SubnetHints: &hints, StableID: &stableID, ActivationState: &activation,
		Adaptive: adaptive, Longitude: &longitude, Latitude: &latitude,
		Description: &description, Location: &location, Brand: &brand,
		Model: &modelName, SerialNumber: &serialNumber, RetentionDays: &retentionDays,
	})
	require.NoError(t, err)
	require.Equal(t, "new name", updated.Name)
	require.Equal(t, "h265", updated.Encoding)
	require.Equal(t, "udp", updated.RTSPTransport, "transport is normalized")
	require.Equal(t, "token-1", updated.ProfileToken)
	require.Equal(t, "H265", updated.StreamEncoding)
	require.True(t, updated.AudioEnabled)
	require.Equal(t, hints, updated.SubnetHints)
	require.Equal(t, "SN-1", updated.StableID)
	require.Equal(t, config.ActivationActive, updated.ActivationState)
	require.Equal(t, adaptive, updated.Adaptive)
	require.Equal(t, longitude, updated.Longitude)
	require.Equal(t, latitude, updated.Latitude)

	row, err := db.GetCamera(ctx, "cam-upd")
	require.NoError(t, err)
	require.Equal(t, "d", row.Description)
	require.Equal(t, 14, row.RetentionDays)

	// Enabling a pending camera does not start a recorder.
	pending := config.ActivationPending
	enabled := true
	updated, err = mgr.UpdateCamera(ctx, "cam-upd", CameraUpdate{Enabled: &enabled, ActivationState: &pending})
	require.NoError(t, err)
	require.True(t, updated.Enabled)
	require.Equal(t, 0, mgr.RecorderCount())

	// Completing activation with a restartable protocol starts the recorder.
	active := config.ActivationActive
	rtsp, h264 := "rtsp", "h264"
	streamURL := "rtsp://127.0.0.1:1/live"
	updated, err = mgr.UpdateCamera(ctx, "cam-upd", CameraUpdate{
		ActivationState: &active, Protocol: &rtsp, Encoding: &h264, URL: &streamURL,
	})
	require.NoError(t, err)
	require.Equal(t, "rtsp", updated.Protocol)
	require.Equal(t, 1, mgr.RecorderCount())

	// An invalid segment duration falls back to the recorder default.
	mgr.cfg.Storage.SegmentDuration = "bogus"
	h265 := "h265"
	_, err = mgr.UpdateCamera(ctx, "cam-upd", CameraUpdate{Encoding: &h265})
	require.NoError(t, err)
	require.Equal(t, 1, mgr.RecorderCount())

	// Disabling detaches and stops the recorder.
	disabled := false
	updated, err = mgr.UpdateCamera(ctx, "cam-upd", CameraUpdate{Enabled: &disabled})
	require.NoError(t, err)
	require.False(t, updated.Enabled)
	require.Equal(t, 0, mgr.RecorderCount())
	require.Equal(t, model.StatusError, mgr.CameraStatus("cam-upd"))

	// Unknown camera.
	_, err = mgr.UpdateCamera(ctx, "missing", CameraUpdate{})
	var notFound *model.CameraNotFoundError
	require.ErrorAs(t, err, &notFound)
}

func TestCam2UpdateCameraPersistenceFailures(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Storage: config.StorageConfig{SegmentDuration: "1m"},
		Cameras: []config.CameraConfig{{
			ID: "cam-repo", Protocol: "rtsp", Encoding: "h264",
			URL: "rtsp://127.0.0.1:1/x", Enabled: false,
		}},
	}
	repo := &cam2Repo{err: errors.New("db down")}
	mgr := NewCameraManager(cfg, nil, repo, "")
	name := "renamed"
	description := "desc"
	retention := 3

	repo.failUpsert = true
	_, err := mgr.UpdateCamera(ctx, "cam-repo", CameraUpdate{Name: &name})
	require.NoError(t, err)
	require.Contains(t, repo.calls, "upsert")

	repo.failUpsert = false
	repo.failExtras = true
	_, err = mgr.UpdateCamera(ctx, "cam-repo", CameraUpdate{Name: &name})
	require.NoError(t, err)
	require.Contains(t, repo.calls, "extras")

	repo.failExtras = false
	repo.failMetadata = true
	_, err = mgr.UpdateCamera(ctx, "cam-repo", CameraUpdate{Description: &description, RetentionDays: &retention})
	require.NoError(t, err)
	require.Contains(t, repo.calls, "metadata")

	repo.failMetadata = false
	repo.failActivation = true
	repo.failStableID = true
	activation := config.ActivationActive
	stable := "SN-9"
	_, err = mgr.UpdateCamera(ctx, "cam-repo", CameraUpdate{ActivationState: &activation, StableID: &stable})
	require.NoError(t, err)
}

func TestCam2ArchiveCameraBranches(t *testing.T) {
	ctx := context.Background()
	mgr, _, db, _ := newTestManager(t)
	mgr.metrics = metrics.NewMetrics()
	cam2StopMachinery(t, mgr)

	require.ErrorContains(t, mgr.ArchiveCamera(ctx, "missing"), "not found")

	require.NoError(t, db.UpsertCamera(ctx, "cam-h264", "H264 Camera", "rtsp", "h264",
		"rtsp://127.0.0.1:1/stream", "", "", true, "", "", ""))

	stopped := 0
	mgr.recorders["cam-h264"] = &cam2Recorder{status: model.StatusRecording, onStop: func() { stopped++ }}
	mgr.pausedRecorders["cam-h264"] = true
	require.NoError(t, mgr.ArchiveCamera(ctx, "cam-h264"))
	require.Equal(t, 1, stopped)
	require.Nil(t, mgr.GetCameraConfig("cam-h264"))
	require.NotContains(t, mgr.pausedRecorders, "cam-h264")

	row, err := db.GetCamera(ctx, "cam-h264")
	require.NoError(t, err)
	require.True(t, row.Archived)

	t.Run("database failures", func(t *testing.T) {
		repo := &cam2Repo{failArchiveDB: true, err: errors.New("archive failed")}
		cfg := &config.Config{
			Storage: config.StorageConfig{SegmentDuration: "1m"},
			Cameras: []config.CameraConfig{{ID: "cam-arch", Protocol: "rtsp", Encoding: "h264", Enabled: false}},
		}
		mgr := NewCameraManager(cfg, nil, repo, "")
		err := mgr.ArchiveCamera(ctx, "cam-arch")
		require.ErrorContains(t, err, "failed to archive camera in DB")
	})

	t.Run("recording archive failure", func(t *testing.T) {
		repo := &cam2Repo{failArchiveRecs: true, err: errors.New("archive recordings failed")}
		cfg := &config.Config{
			Storage: config.StorageConfig{SegmentDuration: "1m"},
			Cameras: []config.CameraConfig{{ID: "cam-arch", Protocol: "rtsp", Encoding: "h264", Enabled: false}},
		}
		mgr := NewCameraManager(cfg, nil, repo, filepath.Join(t.TempDir(), "missing", "config.yaml"))
		require.NoError(t, mgr.ArchiveCamera(ctx, "cam-arch"))
		require.Nil(t, mgr.GetCameraConfig("cam-arch"))
	})
}

func TestCam2RestoreArchivedCameraBranches(t *testing.T) {
	ctx := context.Background()
	mgr, _, db, _ := newTestManager(t)
	cam2StopMachinery(t, mgr)

	require.EqualError(t, mgr.RestoreArchivedCamera(ctx, nil), "archived camera not found")

	require.NoError(t, db.UpsertCamera(ctx, "restored", "Restored", "rtsp", "", "rtsp://127.0.0.1:1/r", "", "", true, "", "", ""))
	require.NoError(t, db.ArchiveCameraDB(ctx, "restored"))
	row := &storage.CameraRow{
		ID: "restored", Name: "Restored", Protocol: "rtsp", Encoding: "",
		URL: "rtsp://127.0.0.1:1/r", Enabled: true,
	}
	require.NoError(t, mgr.RestoreArchivedCamera(ctx, row))
	require.NotNil(t, mgr.GetCameraConfig("restored"))
	// An unsupported encoding fails to start, which is logged rather than returned.
	require.Equal(t, 0, mgr.RecorderCount())

	var exists *model.CameraAlreadyExistsError
	require.ErrorAs(t, mgr.RestoreArchivedCamera(ctx, row), &exists)

	archivedRow, err := db.GetCamera(ctx, "restored")
	require.NoError(t, err)
	require.False(t, archivedRow.Archived)

	t.Run("database failure", func(t *testing.T) {
		repo := &cam2Repo{failUnarchiveDB: true, err: errors.New("unarchive failed")}
		mgr := NewCameraManager(&config.Config{Storage: config.StorageConfig{SegmentDuration: "1m"}}, nil, repo, "")
		err := mgr.RestoreArchivedCamera(ctx, &storage.CameraRow{ID: "cam-restore", URL: "rtsp://127.0.0.1:1/r"})
		require.ErrorContains(t, err, "failed to restore camera in DB")
	})

	t.Run("recording restore failure and persist failure", func(t *testing.T) {
		repo := &cam2Repo{failUnarchiveRecs: true, err: errors.New("unarchive recordings failed")}
		mgr := NewCameraManager(&config.Config{Storage: config.StorageConfig{SegmentDuration: "1m"}}, nil, repo,
			filepath.Join(t.TempDir(), "missing", "config.yaml"))
		require.NoError(t, mgr.RestoreArchivedCamera(ctx, &storage.CameraRow{
			ID: "cam-restore-2", Protocol: "rtsp", Encoding: "", URL: "rtsp://127.0.0.1:1/r",
		}))
		require.NotNil(t, mgr.GetCameraConfig("cam-restore-2"))
	})
}

func TestCam2StartCameraStopCameraBranches(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Storage: config.StorageConfig{SegmentDuration: "1m"},
		Cameras: []config.CameraConfig{
			{ID: "cam-ok", StreamID: "ok-ingest", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x", Enabled: true},
			{ID: "cam-off", Protocol: "rtsp", Encoding: "h264", Enabled: false},
			{ID: "cam-pending", Protocol: "rtsp", Encoding: "h264", Enabled: true, ActivationState: config.ActivationPending},
			{ID: "cam-bad-enc", Protocol: "rtsp", Encoding: "", URL: "rtsp://127.0.0.1:1/x", Enabled: true},
		},
	}
	mgr := NewCameraManager(cfg, nil, nil, "", metrics.NewMetrics())
	cam2StopMachinery(t, mgr)
	mgr.SetMediaEngine(&stubMediaEngine{})

	var notFound *model.CameraNotFoundError
	startErr := mgr.StartCamera(ctx, "missing")
	require.ErrorAs(t, startErr, &notFound)

	var disabledErr *model.CameraDisabledError
	startErr = mgr.StartCamera(ctx, "cam-off")
	require.ErrorAs(t, startErr, &disabledErr)

	startErr = mgr.StartCamera(ctx, "cam-pending")
	require.ErrorContains(t, startErr, "pending activation")

	// A recorder that is already recording blocks a manual start.
	mgr.recorders["cam-ok"] = &cam2Recorder{status: model.StatusRecording}
	var runningErr *model.CameraAlreadyRunningError
	startErr = mgr.StartCamera(ctx, "cam-ok")
	require.ErrorAs(t, startErr, &runningErr)

	// Reconnecting counts as running too.
	mgr.recorders["cam-ok"] = &cam2Recorder{status: model.StatusReconnecting}
	startErr = mgr.StartCamera(ctx, "cam-ok")
	require.ErrorAs(t, startErr, &runningErr)

	// A stale recorder is replaced.
	stale := &cam2Recorder{status: model.StatusStopped}
	mgr.recorders["cam-ok"] = stale
	startErr = mgr.StartCamera(ctx, "cam-ok")
	require.NoError(t, startErr)
	require.Equal(t, 1, stale.stopped)
	require.Equal(t, 1, mgr.RecorderCount())

	// Unsupported encodings report the start failure.
	startErr = mgr.StartCamera(ctx, "cam-bad-enc")
	require.ErrorContains(t, startErr, "does not support recording")

	// StopCamera for an unknown camera reports not found.
	require.EqualError(t, mgr.StopCamera(ctx, "missing"), `camera "missing" not found`)
	require.NoError(t, mgr.StopCamera(ctx, "cam-ok"))
	require.Equal(t, 0, mgr.RecorderCount())
	// Stopping an already stopped camera is an error as well.
	require.Error(t, mgr.StopCamera(ctx, "cam-ok"))
}

func TestCam2StartSkipsIneligibleCameras(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Storage: config.StorageConfig{SegmentDuration: "1m"},
		Cameras: []config.CameraConfig{
			{ID: "cam-gb", Protocol: "gb28181", Encoding: "h264", URL: "rtsp://127.0.0.1:1/gb", Enabled: true},
			{ID: "cam-pending", Protocol: "rtsp", Encoding: "h264", Enabled: true, ActivationState: config.ActivationPending},
			{ID: "cam-off", Protocol: "rtsp", Encoding: "h264", Enabled: false},
		},
	}
	mgr := NewCameraManager(cfg, nil, &cam2Repo{}, "")
	require.NoError(t, mgr.Start(ctx))
	require.Equal(t, 0, mgr.RecorderCount())

	t.Run("upsert failure", func(t *testing.T) {
		repo := &cam2Repo{failUpsert: true, err: errors.New("upsert failed")}
		mgr := NewCameraManager(cfg, nil, repo, "")
		require.NoError(t, mgr.Start(ctx))
		require.Contains(t, repo.calls, "upsert")
	})

	t.Run("extras failure", func(t *testing.T) {
		repo := &cam2Repo{failExtras: true, err: errors.New("extras failed")}
		mgr := NewCameraManager(cfg, nil, repo, "")
		require.NoError(t, mgr.Start(ctx))
		require.Contains(t, repo.calls, "extras")
	})
}

func TestCam2RemoveCameraBranches(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Storage: config.StorageConfig{SegmentDuration: "1m"},
		Cameras: []config.CameraConfig{
			{ID: "cam-rm", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x", Enabled: true},
			{ID: "cam-rm-err", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x", Enabled: true},
		},
	}
	mgr := NewCameraManager(cfg, nil, nil, "", metrics.NewMetrics())
	cam2StopMachinery(t, mgr)
	mgr.SetMediaEngine(&stubMediaEngine{})
	healthMgr := health.NewManager(config.HealthConfig{Enabled: true}, nil)
	require.NotNil(t, healthMgr)
	mgr.SetHealthManager(healthMgr)
	mgr.SetHealthManager(nil)

	stopped := 0
	mgr.recorders["cam-rm"] = &cam2Recorder{status: model.StatusRecording, onStop: func() { stopped++ }}
	require.NoError(t, mgr.RemoveCamera(ctx, "cam-rm"))
	require.Equal(t, 1, stopped)
	require.Nil(t, mgr.GetCameraConfig("cam-rm"))

	// A recorder that disappears the camera while stopping exercises the
	// second lookup returning nothing.
	mgr.recorders["cam-rm-err"] = &cam2Recorder{status: model.StatusRecording, onStop: func() {
		mgr.mu.Lock()
		cams := mgr.cfg.Cameras
		kept := cams[:0]
		for _, cam := range cams {
			if cam.ID != "cam-rm-err" {
				kept = append(kept, cam)
			}
		}
		mgr.cfg.Cameras = kept
		mgr.mu.Unlock()
	}}
	require.NoError(t, mgr.RemoveCamera(ctx, "cam-rm-err"))

	var notFound *model.CameraNotFoundError
	require.ErrorAs(t, mgr.RemoveCamera(ctx, "missing"), &notFound)

	// Persist failures are logged, not returned.
	cfgBad := &config.Config{
		Storage: config.StorageConfig{SegmentDuration: "1m"},
		Cameras: []config.CameraConfig{{ID: "cam-persist", Protocol: "rtsp", Encoding: "h264", Enabled: false}},
	}
	mgrBad := NewCameraManager(cfgBad, nil, nil, filepath.Join(t.TempDir(), "missing", "config.yaml"))
	require.NoError(t, mgrBad.RemoveCamera(ctx, "cam-persist"))
	require.Nil(t, mgrBad.GetCameraConfig("cam-persist"))
}

func TestCam2RestartAndSetCameraStreamBranches(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Storage: config.StorageConfig{SegmentDuration: "bogus"},
		Cameras: []config.CameraConfig{
			{ID: "cam-rs", StreamID: "rs-ingest", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x", Enabled: true},
			{ID: "cam-off", Protocol: "rtsp", Encoding: "h264", Enabled: false},
		},
	}
	repo := &cam2Repo{}
	mgr := NewCameraManager(cfg, nil, repo, "", metrics.NewMetrics())
	cam2StopMachinery(t, mgr)
	mgr.SetMediaEngine(&stubMediaEngine{})

	var notFound *model.CameraNotFoundError
	err := mgr.RestartRecorder(ctx, "missing")
	require.ErrorAs(t, err, &notFound)

	var disabledErr *model.CameraDisabledError
	err = mgr.RestartRecorder(ctx, "cam-off")
	require.ErrorAs(t, err, &disabledErr)

	// Restart with a stale recorder and an invalid segment duration.
	stale := &cam2Recorder{status: model.StatusStopped}
	mgr.recorders["cam-rs"] = stale
	require.NoError(t, mgr.RestartRecorder(ctx, "cam-rs"))
	require.Equal(t, 1, stale.stopped)
	require.Equal(t, 1, mgr.RecorderCount())

	// SetCameraStream guards.
	require.EqualError(t, mgr.SetCameraStream(ctx, "cam-rs", ""), "stream ID is required")
	require.ErrorAs(t, mgr.SetCameraStream(ctx, "missing", "new-ingest"), &notFound)

	// Rebinding an idle camera updates the ingest stream and the binding index.
	require.NoError(t, mgr.StopCamera(ctx, "cam-rs"))
	require.NoError(t, mgr.SetCameraStream(ctx, "cam-rs", "rebound-ingest"))
	require.Equal(t, "rebound-ingest", mgr.GetCameraConfig("cam-rs").StreamID)
	require.Equal(t, 0, mgr.RecorderCount())

	// Persistence failures are returned to the caller.
	repo.failSetStream = true
	repo.err = errors.New("set stream failed")
	require.EqualError(t, mgr.SetCameraStream(ctx, "cam-rs", "another-ingest"), "set stream failed")
}

func TestCam2ONVIFAccessorBranches(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Storage: config.StorageConfig{SegmentDuration: "1m"},
		Cameras: []config.CameraConfig{
			{ID: "onvif-1", Name: "ONVIF", Protocol: "onvif", Encoding: "h264", ONVIFEndpoint: "http://127.0.0.1:1/onvif/device_service", Enabled: true},
			{ID: "onvif-bad-ep", Protocol: "onvif", Encoding: "h264", ONVIFEndpoint: "://bad", Enabled: true},
			{ID: "rtsp-1", Protocol: "rtsp", Encoding: "h264", Enabled: true},
		},
	}
	mgr := cam2NewManager(cfg)

	// Missing, non-ONVIF, and uncached cameras.
	var notFound *model.CameraNotFoundError
	_, err := mgr.GetONVIFPTZController(ctx, "missing")
	require.ErrorAs(t, err, &notFound)

	var notONVIF *model.ONVIFNotCameraError
	_, err = mgr.GetONVIFPTZController(ctx, "rtsp-1")
	require.ErrorAs(t, err, &notONVIF)

	// An unusable endpoint fails the connection without touching the network.
	var connErr *model.ONVIFConnectionError
	_, err = mgr.GetONVIFPTZController(ctx, "onvif-bad-ep")
	require.ErrorAs(t, err, &connErr)

	// A cached but unconnected client is returned as-is, so every accessor
	// surfaces the "not connected" error from the client itself.
	client := onvif.NewClient("http://127.0.0.1:1/onvif/device_service", "admin", "pass")
	mgr.onvifMu.Lock()
	mgr.onvifClients["onvif-1"] = client
	mgr.onvifMu.Unlock()

	_, err = mgr.GetONVIFPTZController(ctx, "onvif-1")
	require.ErrorContains(t, err, "get profiles for camera")

	_, err = mgr.GetImagingController(ctx, "onvif-1")
	require.ErrorContains(t, err, "get profiles for camera")

	_, err = mgr.GetSnapshotProvider(ctx, "onvif-1")
	require.ErrorContains(t, err, "get profiles for camera")

	_, err = mgr.GetDeviceManager(ctx, "onvif-1")
	require.ErrorContains(t, err, "failed to create device manager")

	// ImagingController and SnapshotProvider for unknown cameras.
	_, err = mgr.GetImagingController(ctx, "missing")
	require.ErrorAs(t, err, &notFound)
	_, err = mgr.GetSnapshotProvider(ctx, "missing")
	require.ErrorAs(t, err, &notFound)
	_, err = mgr.GetDeviceManager(ctx, "missing")
	require.ErrorAs(t, err, &notFound)

	// The ONVIF stream resolver wins over any device access.
	mgr.onvifStreamResolver = func(context.Context, config.CameraConfig) (string, error) {
		return "rtsp://127.0.0.1:1/resolved", nil
	}
	streamURL, err := mgr.resolveONVIFStreamURL(ctx, cfg.Cameras[0])
	require.NoError(t, err)
	require.Equal(t, "rtsp://127.0.0.1:1/resolved", streamURL)
}

func TestCam2ONVIFEventSubscriptions(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Storage: config.StorageConfig{SegmentDuration: "1m"},
		Cameras: []config.CameraConfig{
			{ID: "onvif-1", Protocol: "onvif", Encoding: "h264", ONVIFEndpoint: "http://127.0.0.1:1/onvif/device_service", Enabled: true},
			{ID: "rtsp-1", Protocol: "rtsp", Encoding: "h264", Enabled: true},
		},
	}
	mgr := cam2NewManager(cfg)

	// Unknown and non-ONVIF cameras are rejected before any device access.
	require.Error(t, mgr.SubscribeONVIFEvents(ctx, "missing", nil))
	require.Error(t, mgr.SubscribeONVIFEvents(ctx, "rtsp-1", nil))

	// A cached, unconnected client cannot create a subscriber.
	client := onvif.NewClient("http://127.0.0.1:1/onvif/device_service", "admin", "pass")
	mgr.onvifMu.Lock()
	mgr.onvifClients["onvif-1"] = client
	mgr.onvifMu.Unlock()
	require.ErrorContains(t, mgr.SubscribeONVIFEvents(ctx, "onvif-1", func(onvif.ONVIFEvent) {}), "failed to create event subscriber")

	// Already subscribed cameras short-circuit.
	existing := &cam2Subscriber{}
	mgr.eventSubscribers["onvif-1"] = existing
	require.NoError(t, mgr.SubscribeONVIFEvents(ctx, "onvif-1", nil))

	// Unsubscribe removes the subscriber and forwards the call.
	require.NoError(t, mgr.UnsubscribeONVIFEvents(ctx, "onvif-1"))
	require.Equal(t, 1, existing.unsubscribes)
	require.NotContains(t, mgr.eventSubscribers, "onvif-1")
	require.NoError(t, mgr.UnsubscribeONVIFEvents(ctx, "onvif-1"))

	// Unsubscribe errors are logged, not returned.
	failing := &cam2Subscriber{unsubscribeErr: errors.New("unsubscribe failed")}
	mgr.eventSubscribers["onvif-1"] = failing
	require.NoError(t, mgr.UnsubscribeONVIFEvents(ctx, "onvif-1"))
	require.Equal(t, 1, failing.unsubscribes)

	// StopAllONVIFEvents drains every subscription.
	first, second := &cam2Subscriber{}, &cam2Subscriber{}
	mgr.eventSubscribers["a"] = first
	mgr.eventSubscribers["b"] = second
	mgr.StopAllONVIFEvents(ctx)
	require.Empty(t, mgr.eventSubscribers)
	require.Equal(t, 1, first.unsubscribes)
	require.Equal(t, 1, second.unsubscribes)
}

func TestCam2InitStreamHubMetricCallbacks(t *testing.T) {
	m := metrics.NewMetrics()
	cfg := &config.Config{Storage: config.StorageConfig{SegmentDuration: "1m"}}
	cfg.Cameras = []config.CameraConfig{{ID: "hub-cam", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x", Enabled: true}}
	mgr := NewCameraManager(cfg, nil, nil, "", m)

	rec, ok := mgr.createRecorder(cfg.Cameras[0], time.Minute).(*recorder.H264Recorder)
	require.True(t, ok)
	require.NotNil(t, rec.Hub)

	// The hub callbacks are wired because metrics are enabled.
	for i := 0; i < 100; i++ {
		rec.Hub.OnBroadcast("hub-cam", i%2 == 0)
	}
	rec.Hub.OnDrop("consumer-1")
	rec.Hub.OnBufferDepth("hub-cam", "consumer-1", 4)
	rec.Hub.OnJitterBufferDepth("hub-cam", 6)
	rec.Hub.OnJitterReorder("hub-cam")

	// initStreamHub tolerates recorders it does not own.
	initStreamHub(&cam2Recorder{}, "hub-cam", "rtsp", &mgr.frameSampleCounter, m)
	initStreamHub(nil, "hub-cam", "rtsp", nil, nil)
}

// cam2StopPullEngine fails the second StopPull call so both teardown paths run.
type cam2StopPullEngine struct {
	stubMediaEngine
	stopCalls int
	failOn    int
	stopErr   error
}

func (e *cam2StopPullEngine) StopPull(_ context.Context, streamID string) error {
	e.stopCalls++
	e.stopPulls = append(e.stopPulls, streamID)
	if e.stopCalls == e.failOn {
		return e.stopErr
	}
	return nil
}

func TestCam2CreateRecorderProtocolBranches(t *testing.T) {
	engine := &stubMediaEngine{}
	mgr := cam2NewManager(&config.Config{Storage: config.StorageConfig{SegmentDuration: "1m"}})
	mgr.SetMediaEngine(engine)

	// Xiaomi recorders are wired with the error reporter on both code paths.
	rec := mgr.createRecorder(config.CameraConfig{ID: "xiaomi", Protocol: "xiaomi", Encoding: "h264"}, time.Minute)
	require.IsType(t, &xiaomi.XiaomiRecorder{}, rec)

	mgr.SetMediaEngine(nil)
	require.NotNil(t, mgr.createRecorder(config.CameraConfig{ID: "xiaomi", Protocol: "xiaomi", Encoding: "h264"}, time.Minute))

	// GB28181 without a media engine probes the relay/direct URL first.
	gb := mgr.createRecorder(config.CameraConfig{
		ID: "gb", Protocol: "gb28181", Encoding: "h264",
		URL: "rtsp://127.0.0.1:1/stream", FrameWatchdogTimeout: "5s",
	}, time.Minute)
	require.NotNil(t, gb)
	require.NotNil(t, mgr.createRecorder(config.CameraConfig{
		ID: "gb2", Protocol: "gb28181", Encoding: "h265",
		URL: "rtsp://127.0.0.1:1/stream", FrameWatchdogTimeout: "5s",
	}, time.Minute))

	// Legacy ONVIF recording keeps the watchdog setting.
	require.NotNil(t, mgr.createRecorder(config.CameraConfig{
		ID: "onvif-legacy", Protocol: "onvif", Encoding: "h264",
		ONVIFEndpoint: "http://127.0.0.1:1/onvif/device_service", FrameWatchdogTimeout: "5s",
	}, time.Minute))

	mgr.SetMediaEngine(engine)
	// RTSP H.265 through the lalmax relay keeps the watchdog setting.
	require.NotNil(t, mgr.createRecorder(config.CameraConfig{
		ID: "rtsp-h265", Protocol: "rtsp", Encoding: "h265",
		URL: "rtsp://127.0.0.1:1/stream", FrameWatchdogTimeout: "5s",
	}, time.Minute))
	require.NotNil(t, mgr.createRecorder(config.CameraConfig{
		ID: "rtsp-h264", Protocol: "rtsp", Encoding: "h264",
		URL: "rtsp://127.0.0.1:1/stream", FrameWatchdogTimeout: "5s",
	}, time.Minute))

	// ONVIF relays with a known encoding keep the watchdog setting too.
	require.NotNil(t, mgr.createRecorder(config.CameraConfig{
		ID: "onvif-h264", Protocol: "onvif", Encoding: "h264",
		ONVIFEndpoint: "http://127.0.0.1:1/onvif/device_service", FrameWatchdogTimeout: "5s",
	}, time.Minute))
	require.NotNil(t, mgr.createRecorder(config.CameraConfig{
		ID: "onvif-h265", Protocol: "onvif", Encoding: "h265",
		ONVIFEndpoint: "http://127.0.0.1:1/onvif/device_service", FrameWatchdogTimeout: "5s",
	}, time.Minute))
}

func TestCam2SnapshotAndStreamRecorderBookkeeping(t *testing.T) {
	cfg := &config.Config{Storage: config.StorageConfig{SegmentDuration: "1m"}}
	mgr := NewCameraManager(cfg, nil, nil, "", metrics.NewMetrics())
	mgr.SetHealthManager(health.NewManager(config.HealthConfig{Enabled: true}, nil))
	t.Cleanup(func() { mgr.StopAllStreamRecordings() })

	// snapshotRecorders without record tasks copies the recorder map.
	mgr.recorders["cam-snap"] = &cam2Recorder{status: model.StatusRecording}
	snapshot := mgr.snapshotRecorders()
	require.Len(t, snapshot, 1)
	require.Contains(t, snapshot, "cam-snap")

	// Plan-only stream recorders are tracked, stopped, and unregistered.
	mgr.streamRecorders["plan-a"] = &cam2Recorder{status: model.StatusRecording}
	mgr.streamRecorders["plan-b"] = &cam2Recorder{status: model.StatusRecording}
	mgr.streamConfigs["plan-a"] = config.CameraConfig{ID: "plan-a"}
	require.True(t, mgr.StreamRecordingActive("plan-a"))
	mgr.StopAllStreamRecordings()
	require.False(t, mgr.StreamRecordingActive("plan-a"))
	require.Empty(t, mgr.streamRecorders)
}

func TestCam2StartStreamRecordingLifecycle(t *testing.T) {
	ctx := context.Background()
	engine := &stubMediaEngine{
		activeStreams: map[string]bool{"plan-stream": true},
		streamCodec:   "h264",
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{SegmentDuration: "bogus"},
		Health:  config.HealthConfig{Enabled: true},
	}
	mgr := NewCameraManager(cfg, nil, nil, "", metrics.NewMetrics())
	mgr.SetMediaEngine(engine)
	mgr.SetHealthManager(health.NewManager(config.HealthConfig{Enabled: true}, nil))
	t.Cleanup(func() { mgr.StopAllStreamRecordings() })

	require.NoError(t, mgr.StartStreamRecording(ctx, "plan-stream"))
	require.True(t, mgr.StreamRecordingActive("plan-stream"))
	require.Len(t, mgr.streamRecorders, 1)

	// Starting the same plan twice is a no-op.
	require.NoError(t, mgr.StartStreamRecording(ctx, "plan-stream"))
	require.Equal(t, 1, len(mgr.streamRecorders))

	mgr.StopAllStreamRecordings()
	require.False(t, mgr.StreamRecordingActive("plan-stream"))
}

func TestCam2PauseResumeRecorderBranches(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Storage: config.StorageConfig{SegmentDuration: "1m"},
		Cameras: []config.CameraConfig{
			{ID: "cam-pause", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x", Enabled: true},
			{ID: "cam-fail-stop", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x", Enabled: true},
			{ID: "cam-restart", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x", Enabled: true},
		},
	}
	mgr := NewCameraManager(cfg, nil, nil, "", metrics.NewMetrics())
	cam2StopMachinery(t, mgr)
	mgr.SetMediaEngine(&stubMediaEngine{})

	// Already paused cameras are a no-op.
	pausable := &cam2PausableRecorder{cam2Recorder: cam2Recorder{status: model.StatusRecording}}
	mgr.recorders["cam-pause"] = pausable
	mgr.pausedRecorders["cam-pause"] = true
	require.NoError(t, mgr.PauseRecording(ctx, "cam-pause"))

	// Pausable recorders are paused in place.
	delete(mgr.pausedRecorders, "cam-pause")
	require.NoError(t, mgr.PauseRecording(ctx, "cam-pause"))
	require.True(t, pausable.paused)
	require.Equal(t, 0, pausable.stopped)
	require.True(t, mgr.RecordingPaused("cam-pause"))

	// A failing stop is logged, not returned.
	mgr.recorders["cam-fail-stop"] = &cam2Recorder{status: model.StatusRecording, stopErr: errors.New("stop failed")}
	require.NoError(t, mgr.PauseRecording(ctx, "cam-fail-stop"))
	require.True(t, mgr.RecordingPaused("cam-fail-stop"))

	// Unknown camera without a recorder reports not found.
	require.ErrorAs(t, mgr.PauseRecording(ctx, "missing"), new(*model.CameraNotFoundError))

	// Resuming a pausable recorder leaves the metrics balanced.
	require.NoError(t, mgr.ResumeRecording(ctx, "cam-pause"))
	require.False(t, mgr.RecordingPaused("cam-pause"))
	require.False(t, pausable.paused)
	require.EqualError(t, mgr.ResumeRecording(ctx, "cam-pause"), `camera "cam-pause" recording is not paused`)

	// Paused but no recorder left: not found.
	mgr.pausedRecorders["cam-gone"] = true
	require.ErrorAs(t, mgr.ResumeRecording(ctx, "cam-gone"), new(*model.CameraNotFoundError))

	// A paused recorder whose camera config disappeared cannot be resumed.
	mgr.recorders["cam-orphan"] = &cam2Recorder{status: model.StatusRecording}
	mgr.pausedRecorders["cam-orphan"] = true
	require.ErrorAs(t, mgr.ResumeRecording(ctx, "cam-orphan"), new(*model.CameraNotFoundError))

	// A failed restart is reported, and the default segment duration is used.
	mgr.cfg.Storage.SegmentDuration = "bogus"
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, config.CameraConfig{
		ID: "cam-restore-fail", Protocol: "rtsp", Encoding: "",
		URL: "rtsp://127.0.0.1:1/x", Enabled: true,
	})
	mgr.recorders["cam-restore-fail"] = &cam2Recorder{status: model.StatusRecording}
	mgr.pausedRecorders["cam-restore-fail"] = true
	require.ErrorContains(t, mgr.ResumeRecording(ctx, "cam-restore-fail"), "failed to resume recording")

	// A non-pausable recorder forces a restart.
	mgr.recorders["cam-restart"] = &cam2Recorder{status: model.StatusRecording}
	mgr.pausedRecorders["cam-restart"] = true
	require.NoError(t, mgr.ResumeRecording(ctx, "cam-restart"))
	require.False(t, mgr.RecordingPaused("cam-restart"))
	// cam-pause, cam-fail-stop, the orphaned recorder, and cam-restart.
	require.Equal(t, 4, mgr.RecorderCount())
}

func TestCam2ResumeRecordingTaskBranches(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Storage: config.StorageConfig{SegmentDuration: "1m"},
		Cameras: []config.CameraConfig{{ID: "cam-task", StreamID: "task-ingest", Protocol: "rtsp", Encoding: "h264", Enabled: true}},
	}
	mgr := cam2NewManager(cfg)
	mgr.SetMediaEngine(&stubMediaEngine{})
	tasks := recorder.NewTaskManager(nil, nil, nil, nil, nil, time.Minute)
	mgr.SetRecordTasks(tasks)

	// Task-backed camera that is not paused cannot be resumed.
	require.EqualError(t, mgr.ResumeRecording(ctx, "cam-task"), `camera "cam-task" recording is not paused`)

	// A plan-less task manager fails Ensure, which is only logged.
	mgr.pausedRecorders["cam-task"] = true
	require.NoError(t, mgr.ResumeRecording(ctx, "cam-task"))
	require.False(t, mgr.RecordingPaused("cam-task"))
}

func TestCam2ResolveAndHelperBranches(t *testing.T) {
	ctx := context.Background()

	// ONVIF stream URI without a resolver falls back to the device client, and
	// an unusable endpoint fails without any I/O.
	mgr := cam2NewManager(nil)
	_, err := mgr.resolveONVIFStreamURL(ctx, config.CameraConfig{ID: "cam", URL: "://bad"})
	require.ErrorContains(t, err, "onvif connect")

	// Credentials helper: invalid URLs and URLs that already carry a user.
	_, err = applySourceCredentials("://bad", "user", "pass")
	require.Error(t, err)
	withUser, err := applySourceCredentials("rtsp://admin:secret@127.0.0.1/live", "user", "pass")
	require.NoError(t, err)
	require.Equal(t, "rtsp://admin:secret@127.0.0.1/live", withUser)
	credentialed, err := applySourceCredentials("rtsp://127.0.0.1/live", "user", "pass")
	require.NoError(t, err)
	require.Equal(t, "rtsp://user:pass@127.0.0.1/live", credentialed)
	empty, err := applySourceCredentials("", "user", "pass")
	require.NoError(t, err)
	require.Empty(t, empty)
	anonymous, err := applySourceCredentials("rtsp://127.0.0.1/live", "", "")
	require.NoError(t, err)
	require.Equal(t, "rtsp://127.0.0.1/live", anonymous)

	// Stream discovery helpers.
	require.False(t, mgr.hasExistingLalmaxStream(ctx, config.CameraConfig{ID: "cam"}))
	require.False(t, sameMediaURLPath("://bad", "http://127.0.0.1/live"))
	require.True(t, sameMediaURLPath("rtsp://127.0.0.1/live", "rtsp://127.0.0.1/live"))
	require.Empty(t, OrderedSnapshotTokens("", nil))
	require.Empty(t, mgr.probeRTSPEncoding(config.CameraConfig{ID: "cam", Protocol: "onvif", Encoding: "h264"}))
	require.Empty(t, mgr.getONVIFProfileName(ctx, config.CameraConfig{ID: "cam"}))

	// ONVIF clients fall back to the stream URL as endpoint.
	cfg := &config.Config{Storage: config.StorageConfig{SegmentDuration: "1m"}}
	cfg.Cameras = []config.CameraConfig{{ID: "onvif-url", Protocol: "onvif", URL: "://bad"}}
	mgr = cam2NewManager(cfg)
	_, err = mgr.getOrCreateONVIFClient(ctx, "onvif-url")
	require.Error(t, err)
}

func TestCam2TeardownErrorBranches(t *testing.T) {
	ctx := context.Background()
	mgr, _, db, _ := newTestManager(t)
	engine := &cam2StopPullEngine{failOn: 2, stopErr: errors.New("second stop failed")}
	mgr.SetMediaEngine(engine)
	require.NoError(t, db.BindStreamToCamera(ctx, "bound-ingest", "stop-cam"))
	require.EqualError(t, mgr.stopMediaPullLocked(ctx, "stop-cam"), "second stop failed")
}

func TestCam2SegmentDurationFallbacks(t *testing.T) {
	ctx := context.Background()

	// StartCamera falls back to the recorder default segment duration.
	cfg := &config.Config{
		Storage: config.StorageConfig{SegmentDuration: "bogus"},
		Cameras: []config.CameraConfig{{ID: "cam-seg", Protocol: "rtsp", Encoding: "", URL: "rtsp://127.0.0.1:1/x", Enabled: true}},
	}
	mgr := cam2NewManager(cfg)
	require.ErrorContains(t, mgr.StartCamera(ctx, "cam-seg"), "does not support recording")

	// RestoreArchivedCamera falls back to the default as well.
	repo := &cam2Repo{}
	restoreMgr := NewCameraManager(&config.Config{Storage: config.StorageConfig{SegmentDuration: "bogus"}}, nil, repo, "")
	require.NoError(t, restoreMgr.RestoreArchivedCamera(ctx, &storage.CameraRow{
		ID: "cam-restore-seg", Protocol: "rtsp", Encoding: "", URL: "rtsp://127.0.0.1:1/x", Enabled: true,
	}))
	require.NotNil(t, restoreMgr.GetCameraConfig("cam-restore-seg"))
}

func TestCam2HealthManagerNotifications(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Storage: config.StorageConfig{SegmentDuration: "1m"},
		Cameras: []config.CameraConfig{
			{ID: "cam-arch-health", Protocol: "rtsp", Encoding: "h264", Enabled: false},
			{ID: "cam-rm-health", Protocol: "rtsp", Encoding: "h264", Enabled: false},
		},
	}
	mgr := NewCameraManager(cfg, nil, &cam2Repo{}, "")
	mgr.SetHealthManager(health.NewManager(config.HealthConfig{Enabled: true}, nil))
	mgr.recorders["cam-arch-health"] = &cam2Recorder{status: model.StatusRecording}
	mgr.recorders["cam-rm-health"] = &cam2Recorder{status: model.StatusRecording}

	require.NoError(t, mgr.ArchiveCamera(ctx, "cam-arch-health"))
	require.NoError(t, mgr.RemoveCamera(ctx, "cam-rm-health"))
	require.Empty(t, mgr.recorders)
}

func TestCam2UpdateCameraStartFailureAndStatusMerge(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Storage: config.StorageConfig{SegmentDuration: "1m"},
		Cameras: []config.CameraConfig{{ID: "cam-enable", Protocol: "rtsp", Encoding: "", URL: "rtsp://127.0.0.1:1/x", Enabled: false}},
	}
	mgr := cam2NewManager(cfg)

	// Enabling a camera without a usable encoding logs the start failure.
	enabled := true
	updated, err := mgr.UpdateCamera(ctx, "cam-enable", CameraUpdate{Enabled: &enabled})
	require.NoError(t, err)
	require.True(t, updated.Enabled)
	require.Equal(t, 0, mgr.RecorderCount())

	// Status keeps already known recorders when tasks are also configured.
	cfg.Cameras = []config.CameraConfig{{ID: "cam-both", StreamID: "both-ingest", Protocol: "rtsp", Encoding: "h264", Enabled: true}}
	mgr.cfg.Cameras = cfg.Cameras
	mgr.SetMediaEngine(&stubMediaEngine{})
	tasks := recorder.NewTaskManager(nil, nil, nil, nil, nil, time.Minute)
	tasks.SetSkip(func(string) bool { return true })
	mgr.SetRecordTasks(tasks)
	mgr.recorders["cam-both"] = &cam2Recorder{status: model.StatusRecording}
	require.Equal(t, model.StatusRecording, mgr.Status()["cam-both"])
	// snapshotRecorders keeps the manager recorder and skips task lookups for it.
	require.Len(t, mgr.snapshotRecorders(), 1)
}

func TestCam2ArchiveCameraWithMergeManager(t *testing.T) {
	ctx := context.Background()
	mergeMgr := merge.NewMergeManager(nil, nil,
		func() config.MergeConfig { return config.MergeConfig{} },
		func(string) *config.MergeConfig { return nil },
		func() []config.CameraConfig { return nil },
	)
	cfg := &config.Config{
		Storage: config.StorageConfig{SegmentDuration: "1m"},
		Cameras: []config.CameraConfig{{ID: "cam-merge", Protocol: "rtsp", Encoding: "h264", Enabled: false}},
	}
	mgr := NewCameraManager(cfg, nil, &cam2Repo{}, "", mergeMgr)
	require.NoError(t, mgr.ArchiveCamera(ctx, "cam-merge"))
	require.Nil(t, mgr.GetCameraConfig("cam-merge"))
}

func TestCam2NewCameraManagerMergeOption(t *testing.T) {
	mergeMgr := &merge.MergeManager{}
	mgr := NewCameraManager(&config.Config{}, nil, nil, "", mergeMgr)
	require.Same(t, mergeMgr, mgr.mergeMgr)
}
