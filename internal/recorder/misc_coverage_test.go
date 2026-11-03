package recorder

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/gortsplib/v5/pkg/format"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// --- fakes ---

type covStateRepo struct {
	desired map[string]bool
	err     error
}

func (r *covStateRepo) DesiredRecordingStreams(context.Context) (map[string]bool, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.desired, nil
}

type covPlanRepo struct {
	covStateRepo
	plans   []storage.RecordingPlan
	listErr error
}

func (r *covPlanRepo) ListRecordingPlans(context.Context) ([]storage.RecordingPlan, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.plans, nil
}

// covErrRecorder is a model.Recorder whose Stop fails, and which records
// adaptive triggers.
type covErrRecorder struct {
	stopErr  error
	triggers int
}

func (r *covErrRecorder) Start(context.Context) error   { return nil }
func (r *covErrRecorder) Stop() error                   { return r.stopErr }
func (r *covErrRecorder) Status() model.RecorderStatus  { return model.StatusRecording }
func (r *covErrRecorder) TriggerAdaptive(time.Duration) { r.triggers++ }

// --- task manager ---

func TestCovTask_SettersHooksAndAdaptive(t *testing.T) {
	engine := &stubTaskEngine{byID: map[string]*media.StreamInfo{
		"s1": {StreamID: "s1", VideoCodec: "h264", Active: true},
	}}
	bus := event.NewEventBus(8)
	tm := NewTaskManager(engine, newHXStore(t), &hjDB{}, metrics.NewMetrics(), bus, 0)
	require.Equal(t, DefaultSegmentDur, tm.segmentDur)

	tm.SetOwnerFunc(func(id string) StreamOwner {
		return StreamOwner{Name: id, AudioEnabled: true} // CameraID intentionally empty
	})
	tm.SetShouldRecord(func(id string) bool { return id == "s1" })
	tm.SetPlayURL(func(context.Context, string) string { return "rtsp://127.0.0.1:1/live" })
	tm.SetPlanMode(func(id string) (string, bool) { return storage.RecordingModeAdaptive, true })
	tm.SetAdaptiveInterval(func(string) time.Duration { return time.Minute })

	var started, stopped int
	tm.SetLifecycleHooks(
		func(string, model.Recorder) { started++ },
		func(string, model.Recorder) { stopped++ },
	)

	require.NoError(t, tm.Ensure(context.Background(), " s1 "))
	require.True(t, tm.Running("s1"))
	require.Equal(t, 1, started)
	require.Equal(t, 1, tm.Count())
	require.NotNil(t, tm.Recorder("s1"))
	require.Contains(t, tm.Status(), "s1")
	require.Equal(t, []string{"s1"}, tm.RunningIDs())

	tm.TriggerAdaptive("s1", time.Minute)
	tm.TriggerAdaptive("missing", time.Minute) // no running task

	require.NoError(t, tm.Stop(context.Background(), "s1", ReasonStreamDown))
	require.Equal(t, 1, stopped)
	require.False(t, tm.Running("s1"))
	// Stopping an unknown stream is a no-op.
	require.NoError(t, tm.Stop(context.Background(), "s1", ReasonStreamDown))

	tm.OnStreamUp(context.Background(), "s1")
	require.True(t, tm.Running("s1"), "OnStreamUp restarts a planned stream")
	tm.StopAll()
	require.Zero(t, tm.Count())
}

func TestCovTask_EnsureBranches(t *testing.T) {
	t.Run("nil manager", func(t *testing.T) {
		var tm *TaskManager
		require.ErrorContains(t, tm.Ensure(context.Background(), "s1"), "not configured")
		require.NoError(t, tm.Stop(context.Background(), "s1", ReasonStreamDown))
		require.False(t, tm.Running("s1"))
		require.Nil(t, tm.Recorder("s1"))
		require.Empty(t, tm.Status())
		require.Nil(t, tm.RunningIDs())
		require.Zero(t, tm.Count())
		tm.TriggerAdaptive("s1", time.Second)
		tm.StopAll()
	})

	t.Run("empty and sub stream ids", func(t *testing.T) {
		tm := NewTaskManager(&stubTaskEngine{}, newHXStore(t), nil, nil, nil, time.Second)
		require.ErrorContains(t, tm.Ensure(context.Background(), "  "), "required")
		require.NoError(t, tm.Ensure(context.Background(), "cam_sub"))
		require.False(t, tm.Running("cam_sub"))
	})

	t.Run("skip function", func(t *testing.T) {
		tm := NewTaskManager(&stubTaskEngine{}, newHXStore(t), nil, nil, nil, time.Second)
		tm.SetSkip(func(id string) bool { return true })
		require.NoError(t, tm.Ensure(context.Background(), "skip-me"))
		require.False(t, tm.Running("skip-me"))
	})

	t.Run("unrecordable codec", func(t *testing.T) {
		engine := &stubTaskEngine{byID: map[string]*media.StreamInfo{
			"mj": {StreamID: "mj", VideoCodec: "mjpeg", Active: true},
		}}
		tm := NewTaskManager(engine, newHXStore(t), nil, nil, nil, time.Second)
		require.ErrorContains(t, tm.Ensure(context.Background(), "mj"), "not recordable")
	})

	t.Run("h265 stream creates a recorder", func(t *testing.T) {
		engine := &stubTaskEngine{byID: map[string]*media.StreamInfo{
			"h265": {StreamID: "h265", VideoCodec: "h265", Active: true},
		}}
		tm := NewTaskManager(engine, newHXStore(t), nil, nil, nil, time.Second)
		t.Cleanup(tm.StopAll)
		require.NoError(t, tm.Ensure(context.Background(), "h265"))
		require.IsType(t, (*H265Recorder)(nil), tm.Recorder("h265"))
	})

	t.Run("owner encoding falls back to engine", func(t *testing.T) {
		engine := &stubTaskEngine{byID: map[string]*media.StreamInfo{
			"enc": {StreamID: "enc", VideoCodec: "h264", Active: true},
		}}
		tm := NewTaskManager(engine, newHXStore(t), nil, nil, nil, time.Second)
		t.Cleanup(tm.StopAll)
		tm.SetOwnerFunc(func(id string) StreamOwner {
			return StreamOwner{CameraID: "cam-owner", Name: id, Encoding: "H264", AudioEnabled: false}
		})
		tm.SetPlanMode(func(string) (string, bool) { return storage.RecordingModeContinuous, true })
		require.NoError(t, tm.Ensure(context.Background(), "enc"))
		require.IsType(t, (*H264Recorder)(nil), tm.Recorder("enc"))
	})

	t.Run("on stream up without planner callback", func(t *testing.T) {
		tm := NewTaskManager(&stubTaskEngine{}, newHXStore(t), nil, nil, nil, time.Second)
		tm.OnStreamUp(context.Background(), "none")
		tm.SetShouldRecord(func(string) bool { return false })
		tm.OnStreamUp(context.Background(), "none")
		require.Zero(t, tm.Count())
	})

	t.Run("on stream up logs ensure failures", func(t *testing.T) {
		engine := &stubTaskEngine{byID: map[string]*media.StreamInfo{
			"bad": {StreamID: "bad", VideoCodec: "mjpeg", Active: true},
		}}
		tm := NewTaskManager(engine, newHXStore(t), nil, nil, nil, time.Second)
		tm.SetShouldRecord(func(string) bool { return true })
		tm.OnStreamUp(context.Background(), "bad")
		require.Zero(t, tm.Count())
	})
}

func TestCovTask_StopFailuresAndDeviceHooks(t *testing.T) {
	tm := NewTaskManager(&stubTaskEngine{}, newHXStore(t), nil, metrics.NewMetrics(), nil, time.Second)
	bad := &covErrRecorder{stopErr: errors.New("stop failed")}
	tm.tasks["dev"] = bad
	require.ErrorContains(t, tm.Stop(context.Background(), "dev", ReasonDeviceStopped), "stop failed")
	require.False(t, tm.Running("dev"))

	tm.tasks["dev2"] = &covErrRecorder{}
	tm.OnDeviceStopped(context.Background(), "ingest", "dev2")
	require.False(t, tm.Running("dev2"))

	tm.tasks["dev3"] = &covErrRecorder{}
	tm.OnDeviceRemoved(context.Background(), "ingest", "dev3")
	require.False(t, tm.Running("dev3"))

	tm.tasks["dev4"] = &covErrRecorder{}
	tm.OnStreamDown(context.Background(), "dev4")
	require.False(t, tm.Running("dev4"))

	tm.tasks["dev5"] = &covErrRecorder{}
	tm.TriggerAdaptive("dev5", time.Second)
	require.Equal(t, 1, tm.tasks["dev5"].(*covErrRecorder).triggers)
}

// --- scheduler ---

func TestCovScheduler_ReconcileLoop(t *testing.T) {
	db := newSchedulerTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		StreamID: "live", Mode: storage.RecordingModeContinuous, Enabled: true,
	}))
	planner := NewRecordingPlanner(db)
	require.NoError(t, planner.Refresh(ctx))

	engine := &stubTaskEngine{byID: map[string]*media.StreamInfo{
		"live": {StreamID: "live", VideoCodec: "h264", Active: true},
	}}
	tasks := NewTaskManager(engine, newHXStore(t), nil, metrics.NewMetrics(), nil, time.Hour)
	t.Cleanup(tasks.StopAll)

	s := NewRecordingScheduler(db)
	s.SetPlanner(planner)
	s.SetTasks(tasks)
	checks := make(chan struct{}, 16)
	s.SetAliveStreams(func(context.Context) ([]string, error) {
		checks <- struct{}{}
		return []string{"live"}, nil
	})
	started := make(chan struct{}, 4)
	s.SetSourceReconciler(func(context.Context, map[string]bool, func(string) bool) bool {
		select {
		case started <- struct{}{}:
		default:
		}
		return true // ask the scheduler to reconcile again shortly
	})

	// nil receiver ReconcileNow must be a no-op.
	var nilSched *RecordingScheduler
	nilSched.ReconcileNow()

	s.Start(ctx)
	<-checks // initial reconcile
	<-started
	// The reconciler kick schedules a delayed reconcile; drive it directly too.
	s.ReconcileNow()
	for i := 0; i < 2; i++ {
		select {
		case <-checks:
		case <-time.After(5 * time.Second):
			t.Fatal("scheduler did not reconcile again")
		}
	}
	s.Stop()
}

func TestCovScheduler_DesiredStateAndErrorPaths(t *testing.T) {
	db := newSchedulerTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		StreamID: "p1", Mode: storage.RecordingModeContinuous, Enabled: true,
	}))
	planner := NewRecordingPlanner(db)
	require.NoError(t, planner.Refresh(ctx))

	t.Run("desired from planner and db", func(t *testing.T) {
		s := NewRecordingScheduler(db)
		desired := s.desiredState(ctx, planner)
		require.Equal(t, map[string]bool{"p1": true}, desired)

		desired = s.desiredState(ctx, nil)
		require.Equal(t, map[string]bool{"p1": true}, desired)

		// No planner and no db yields nil.
		require.Nil(t, NewRecordingScheduler(nil).desiredState(ctx, nil))

		errSched := NewRecordingScheduler(&covStateRepo{err: errors.New("query failed")})
		require.Nil(t, errSched.desiredState(ctx, nil))
	})

	t.Run("check without tasks", func(t *testing.T) {
		s := NewRecordingScheduler(db)
		s.check(ctx) // must not panic
	})

	t.Run("planner refresh failure", func(t *testing.T) {
		badPlanner := NewRecordingPlanner(&covPlanRepo{
			covStateRepo: covStateRepo{err: errors.New("desired failed")},
		})
		s := NewRecordingScheduler(db)
		s.SetPlanner(badPlanner)
		s.SetTasks(NewTaskManager(&stubTaskEngine{}, newHXStore(t), nil, nil, nil, time.Hour))
		s.check(ctx)
	})

	t.Run("alive list failure", func(t *testing.T) {
		s := NewRecordingScheduler(db)
		tasks := NewTaskManager(&stubTaskEngine{}, newHXStore(t), nil, nil, nil, time.Hour)
		t.Cleanup(tasks.StopAll)
		s.SetTasks(tasks)
		s.SetAliveStreams(func(context.Context) ([]string, error) {
			return nil, errors.New("lalmax down")
		})
		s.check(ctx)
	})

	t.Run("write switch suppresses tasks", func(t *testing.T) {
		s := NewRecordingScheduler(db)
		tasks := NewTaskManager(&stubTaskEngine{}, newHXStore(t), nil, nil, nil, time.Hour)
		t.Cleanup(tasks.StopAll)
		s.SetPlanner(planner)
		s.SetTasks(tasks)
		s.SetAliveStreams(func(context.Context) ([]string, error) { return []string{"p1"}, nil })
		var switchArgs []string
		s.SetWriteSwitch(func(streamID string, want bool) bool {
			switchArgs = append(switchArgs, streamID)
			return true
		})
		s.check(ctx)
		require.Equal(t, []string{"p1"}, switchArgs)
		require.False(t, tasks.Running("p1"))
	})

	t.Run("ensure failure is tolerated", func(t *testing.T) {
		engine := &stubTaskEngine{byID: map[string]*media.StreamInfo{
			"bad": {StreamID: "bad", VideoCodec: "mjpeg", Active: true},
		}}
		s := NewRecordingScheduler(&covStateRepo{desired: map[string]bool{"bad": true}})
		tasks := NewTaskManager(engine, newHXStore(t), nil, nil, nil, time.Hour)
		t.Cleanup(tasks.StopAll)
		s.SetTasks(tasks)
		s.SetAliveStreams(func(context.Context) ([]string, error) { return []string{"bad"}, nil })
		s.check(ctx)
		require.False(t, tasks.Running("bad"))
	})

	t.Run("stop failures are tolerated", func(t *testing.T) {
		s := NewRecordingScheduler(&covStateRepo{desired: map[string]bool{}})
		tasks := NewTaskManager(&stubTaskEngine{}, newHXStore(t), nil, nil, nil, time.Hour)
		tasks.tasks["down"] = &covErrRecorder{stopErr: errors.New("stop failed")}
		tasks.tasks["inactive"] = &covErrRecorder{stopErr: errors.New("stop failed")}
		s.SetTasks(tasks)
		// "down" is not in the alive set, "inactive" is alive but not wanted.
		s.SetAliveStreams(func(context.Context) ([]string, error) { return []string{"inactive"}, nil })
		s.check(ctx)
		require.Zero(t, tasks.Count())
	})
}

// --- event manager ---

func TestCovEventManager_NilAndControls(t *testing.T) {
	var nilMgr *EventManager
	require.False(t, nilMgr.IsActive("cam"))
	nilMgr.SetControls(nil, nil)
	nilMgr.Trigger("cam", "motion")
	nilMgr.End("cam", "manual")
	nilMgr.Stop()

	var resumed, paused int
	mgr := NewEventManager(0, 0, func(context.Context, string) error {
		resumed++
		return errors.New("resume failed")
	}, func(context.Context, string) error {
		paused++
		return errors.New("pause failed")
	}, nil)
	require.Equal(t, 30*time.Second, mgr.postRoll)
	require.Equal(t, 5*time.Minute, mgr.maxDur)

	mgr.Trigger("cam", "motion")
	require.True(t, mgr.IsActive("cam"))
	require.Equal(t, 1, resumed)
	mgr.Trigger("cam", "motion2") // extends, stops the previous timer
	require.Equal(t, 2, resumed)
	mgr.End("cam", "manual")
	require.Equal(t, 1, paused)
	require.False(t, mgr.IsActive("cam"))
	// Ending an unknown session does nothing.
	mgr.End("cam", "manual")
	require.Equal(t, 1, paused)

	// Empty camera IDs are ignored.
	mgr.Trigger("", "motion")
	mgr.End("", "manual")
	require.Equal(t, 2, resumed)
}

func TestCovEventManager_EventModeAndStop(t *testing.T) {
	// A camera outside event mode is never triggered.
	mgr := NewEventManager(time.Minute, time.Minute, nil, nil, func(string) bool { return false })
	mgr.Trigger("cam", "motion")
	require.False(t, mgr.IsActive("cam"))

	// SetControls replaces the callbacks.
	var paused int
	mgr.SetControls(nil, func(context.Context, string) error { paused++; return nil })
	mgr.isEventMode = nil
	mgr.Trigger("cam", "motion")
	require.True(t, mgr.IsActive("cam"))

	// Stop cancels all sessions without pausing.
	mgr.Stop()
	require.False(t, mgr.IsActive("cam"))
	require.Zero(t, paused)

	// A capped window uses maxDur instead of postRoll.
	short := NewEventManager(time.Hour, time.Millisecond, nil, nil, nil)
	short.Trigger("cam", "motion")
	require.True(t, short.IsActive("cam"))
	short.Stop()
}

// --- planner ---

func TestCovPlanner_ModeAndErrors(t *testing.T) {
	var nilPlanner *RecordingPlanner
	mode, ok := nilPlanner.Mode("s")
	require.Empty(t, mode)
	require.False(t, ok)
	require.Nil(t, nilPlanner.Desired())
	require.NoError(t, nilPlanner.Refresh(context.Background()))

	// A planner without a repository is a no-op.
	empty := NewRecordingPlanner(nil)
	require.NoError(t, empty.Refresh(context.Background()))

	repo := &covPlanRepo{
		covStateRepo: covStateRepo{desired: map[string]bool{"a": true}},
		plans: []storage.RecordingPlan{
			{StreamID: "a", Mode: storage.RecordingModeAdaptive, Enabled: true},
		},
	}
	p := NewRecordingPlanner(repo)
	require.NoError(t, p.Refresh(context.Background()))
	mode, ok = p.Mode("a")
	require.True(t, ok)
	require.Equal(t, storage.RecordingModeAdaptive, mode)
	mode, ok = p.Mode("missing")
	require.False(t, ok)
	require.Empty(t, mode)
	require.Equal(t, map[string]bool{"a": true}, p.Desired())

	// DesiredRecordingStreams failure.
	badState := NewRecordingPlanner(&covPlanRepo{covStateRepo: covStateRepo{err: errors.New("state failed")}})
	require.ErrorContains(t, badState.Refresh(context.Background()), "state failed")

	// ListRecordingPlans failure.
	badList := NewRecordingPlanner(&covPlanRepo{
		covStateRepo: covStateRepo{desired: map[string]bool{}},
		listErr:      errors.New("list failed"),
	})
	require.ErrorContains(t, badList.Refresh(context.Background()), "list failed")
}

// --- backoff ---

func TestCovBackoff_TiersAndClassification(t *testing.T) {
	tiers := []struct {
		attempt int
		want    time.Duration
	}{
		{0, time.Second},
		{4, time.Second},
		{5, 5 * time.Second},
		{9, 5 * time.Second},
		{10, 10 * time.Second},
		{19, 10 * time.Second},
		{20, time.Minute},
		{100, time.Minute},
	}
	for _, tc := range tiers {
		require.Equal(t, tc.want, TieredBackoff(tc.attempt), "attempt %d", tc.attempt)
		require.GreaterOrEqual(t, TieredBackoffWithJitter(tc.attempt), tc.want)
	}

	cases := []struct {
		err  error
		want string
	}{
		{nil, "unknown"},
		{errors.New("frame watchdog timeout"), "frame_watchdog"},
		{errors.New("DESCRIBE failed"), "rtsp_negotiation"},
		{errors.New("SETUP rejected"), "rtsp_negotiation"},
		{errors.New("PLAY rejected"), "rtsp_negotiation"},
		{errors.New("dial tcp: connection refused"), "connection_refused"},
		{errors.New("no route to host"), "connection_refused"},
		{errors.New("i/o timeout"), "connection_timeout"},
		{errors.New("context deadline exceeded"), "connection_timeout"},
		{errors.New("read: EOF"), "connection_lost"},
		{errors.New("connection closed"), "connection_lost"},
		{errors.New("something else"), "connection_error"},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, classifyDisconnectReason(tc.err))
	}
}

// --- rtsp probe ---

func TestCovRTSPProbe_Encodings(t *testing.T) {
	t.Run("h264", func(t *testing.T) {
		srv := newTestRTSPServer(t)
		defer srv.close()
		res, err := ProbeRTSPEncoding(context.Background(), RTSPProbeConfig{RTSPURL: srv.rtspURL})
		require.NoError(t, err)
		require.True(t, res.HasH264)
		require.False(t, res.HasH265)
		require.False(t, res.HasMJPEG)
		require.False(t, res.HasAudio)
		require.Equal(t, "h264", res.DetectedEncoding)
	})

	t.Run("h265", func(t *testing.T) {
		srv := newTestRTSPServerH265(t)
		defer srv.close()
		res, err := ProbeRTSPEncoding(context.Background(), RTSPProbeConfig{
			RTSPURL:  srv.rtspURL,
			Username: "admin",
			Password: "pass",
		})
		require.NoError(t, err)
		require.True(t, res.HasH265)
		require.Equal(t, "h265", res.DetectedEncoding)
	})

	t.Run("mjpeg", func(t *testing.T) {
		srv := newMjpegTestServer(t)
		defer srv.close()
		res, err := ProbeRTSPEncoding(context.Background(), RTSPProbeConfig{RTSPURL: srv.rtspURL, Timeout: 5 * time.Second})
		require.NoError(t, err)
		require.True(t, res.HasMJPEG)
		require.Equal(t, "mjpeg", res.DetectedEncoding)
	})

	t.Run("aac audio", func(t *testing.T) {
		srv := newTestRTSPServerWithAudio(t)
		defer srv.close()
		res, err := ProbeRTSPEncoding(context.Background(), RTSPProbeConfig{RTSPURL: srv.rtspURL})
		require.NoError(t, err)
		require.True(t, res.HasH264)
		require.True(t, res.HasAudio)
		require.Equal(t, "h264", res.DetectedEncoding)
	})

	t.Run("g711 audio", func(t *testing.T) {
		srv := newHXG711Server(t, &format.H264{
			PayloadTyp:        96,
			PacketizationMode: 1,
			SPS:               testSPS,
			PPS:               testPPS,
		}, true)
		res, err := ProbeRTSPEncoding(context.Background(), RTSPProbeConfig{RTSPURL: srv.rtspURL})
		require.NoError(t, err)
		require.True(t, res.HasAudio)
		require.True(t, res.HasH264)
	})

	t.Run("invalid url", func(t *testing.T) {
		_, err := ProbeRTSPEncoding(context.Background(), RTSPProbeConfig{RTSPURL: "://bad"})
		require.ErrorContains(t, err, "invalid RTSP URL")
	})

	t.Run("unreachable", func(t *testing.T) {
		_, err := ProbeRTSPEncoding(context.Background(), RTSPProbeConfig{
			RTSPURL: "rtsp://127.0.0.1:1/live",
			Timeout: time.Second,
		})
		require.Error(t, err)
	})
}

// --- frames / transport / group cover ---

func TestCovFrameWatchdog_Timeout(t *testing.T) {
	done := make(chan struct{})
	fired := make(chan struct{}, 1)
	runFrameWatchdog(context.Background(), time.Millisecond,
		make(chan struct{}), make(chan struct{}), done,
		func() { fired <- struct{}{} },
	)
	<-done
	require.Len(t, fired, 1)
	require.Equal(t, "cam", streamIDOrCameraID("  ", "cam"))
	require.Equal(t, "sid", streamIDOrCameraID(" sid ", "cam"))
}

func TestCovRTSPTransport(t *testing.T) {
	require.NotNil(t, rtspTransportProtocol("udp"))
	require.NotNil(t, rtspTransportProtocol("tcp"))
	require.NotNil(t, rtspTransportProtocol(""))
}

func TestCovGroupCover_Protocols(t *testing.T) {
	cases := []struct {
		cam  config.CameraConfig
		want bool
	}{
		{config.CameraConfig{Protocol: string(model.ProtoHTTP)}, false},
		{config.CameraConfig{Protocol: string(model.ProtoXiaomi)}, true},
		{config.CameraConfig{Protocol: string(model.ProtoGB28181)}, true},
		{config.CameraConfig{Protocol: "rtmp-pull"}, true},
		{config.CameraConfig{Protocol: "http-flv-pull"}, true},
		{config.CameraConfig{Protocol: "udp-ts-pull"}, true},
		{config.CameraConfig{Protocol: string(model.ProtoRTSP), Encoding: "H264"}, true},
		{config.CameraConfig{Protocol: string(model.ProtoRTSP), Encoding: "h265"}, true},
		{config.CameraConfig{Protocol: string(model.ProtoRTSP), Encoding: "mjpeg"}, false},
		{config.CameraConfig{Protocol: string(model.ProtoRTSP), StreamEncoding: "H265"}, true},
		{config.CameraConfig{Protocol: string(model.ProtoONVIF), StreamEncoding: "h264"}, true},
		{config.CameraConfig{Protocol: string(model.ProtoONVIF)}, false},
		{config.CameraConfig{Protocol: "unknown"}, false},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, CameraUsesGroupRecording(tc.cam), "protocol %q", tc.cam.Protocol)
	}
}
