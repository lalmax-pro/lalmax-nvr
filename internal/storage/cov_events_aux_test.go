package storage

import (
	"context"
	"database/sql"
	"math"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
	"github.com/stretchr/testify/require"
)

// covClosedDB returns an initialised database that has already been closed so
// that every helper surfaces a storage error instead of silently succeeding.
func covClosedDB(t *testing.T) *DB {
	t.Helper()
	db := newTestDB(t)
	require.NoError(t, db.Close())
	return db
}

// TestCovEventsErrorPaths covers event CRUD failures and filter building.
func TestCovEventsErrorPaths(t *testing.T) {
	ctx := context.Background()

	// Happy paths first: filters, ack/delete semantics.
	db := newTestDB(t)
	started := covTestTime(0)
	ended := covTestTime(30 * time.Minute)
	ack := covTestTime(45 * time.Minute)
	events := []model.Event{
		{CameraID: "cam-1", Source: "mqtt", Type: "motion", Message: "first", StartedAt: started, EndedAt: &ended},
		{CameraID: "cam-1", Source: "onvif", Type: "tamper", Severity: model.EventSeverityWarning, StartedAt: started.Add(time.Hour)},
		{CameraID: "cam-2", Source: "mqtt", Type: "motion", StartedAt: started.Add(2 * time.Hour), AcknowledgedAt: &ack},
	}
	for i := range events {
		id, err := db.InsertEvent(ctx, events[i])
		require.NoError(t, err)
		require.NotZero(t, id)
		events[i].ID = id
	}

	got, err := db.GetEvent(ctx, events[0].ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "mqtt", got.Source)
	require.NotNil(t, got.EndedAt)
	require.Equal(t, model.EventStatusOpen, got.Status)
	require.Equal(t, model.EventSeverityInfo, got.Severity)

	notFound, err := db.GetEvent(ctx, 99999)
	require.NoError(t, err)
	require.Nil(t, notFound)

	list, total, err := db.ListEvents(ctx, EventsFilter{CameraID: "cam-1"})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, list, 2)
	require.Equal(t, events[1].ID, list[0].ID, "ordered by started_at DESC")

	list, total, err = db.ListEvents(ctx, EventsFilter{Source: "mqtt", Type: "motion", Status: model.EventStatusOpen})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, list, 2)

	list, total, err = db.ListEvents(ctx, EventsFilter{
		Since: formatTime(started), Until: formatTime(started.Add(90 * time.Minute)), Limit: 1, Offset: 1,
	})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, list, 1, "pagination is applied")

	require.NoError(t, db.AcknowledgeEvent(ctx, events[0].ID, time.Time{}))
	acked, err := db.GetEvent(ctx, events[0].ID)
	require.NoError(t, err)
	require.Equal(t, model.EventStatusAcknowledged, acked.Status)
	require.NotNil(t, acked.AcknowledgedAt)
	require.ErrorIs(t, db.AcknowledgeEvent(ctx, 99999, time.Time{}), sql.ErrNoRows)

	require.NoError(t, db.DeleteEvent(ctx, events[2].ID))
	require.ErrorIs(t, db.DeleteEvent(ctx, events[2].ID), sql.ErrNoRows)

	require.NoError(t, db.Close())
	_, err = db.InsertEvent(ctx, model.Event{Type: "closed"})
	require.Error(t, err)
	_, _, err = db.ListEvents(ctx, EventsFilter{})
	require.Error(t, err)
	_, err = db.GetEvent(ctx, 1)
	require.Error(t, err)
	require.Error(t, db.AcknowledgeEvent(ctx, 1, time.Time{}))
	require.Error(t, db.DeleteEvent(ctx, 1))
}

// TestCovOperationLogs covers audit log insertion, filters and errors.
func TestCovOperationLogs(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	id, err := db.InsertOperationLog(ctx, model.OperationLog{Action: "login", Resource: "session", Status: "success"})
	require.NoError(t, err)
	require.NotZero(t, id)

	userID, err := db.InsertOperationLog(ctx, model.OperationLog{
		UserID: 7, Username: "alice", Action: "update", Resource: "camera", ResourceID: "cam-1",
		IPAddress: "10.0.0.1", UserAgent: "test", CreatedAt: covTestTime(0),
	})
	require.NoError(t, err)
	require.NotZero(t, userID)

	systemLogs, total, err := db.ListOperationLogs(ctx, OperationLogsFilter{})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, systemLogs, 2)

	filtered, total, err := db.ListOperationLogs(ctx, OperationLogsFilter{Username: "alice", Action: "update", Resource: "camera", Status: "success"})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, filtered, 1)
	require.Equal(t, "alice", filtered[0].Username)
	require.Equal(t, "user", filtered[0].ActorType)
	require.Equal(t, int64(7), filtered[0].UserID)

	windowed, total, err := db.ListOperationLogs(ctx, OperationLogsFilter{
		Since: formatTime(covTestTime(0)), Until: formatTime(covTestTime(time.Hour)), Limit: 10, Offset: 0,
	})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, windowed, 1)

	require.NoError(t, db.Close())
	_, err = db.InsertOperationLog(ctx, model.OperationLog{Action: "closed"})
	require.Error(t, err)
	_, _, err = db.ListOperationLogs(ctx, OperationLogsFilter{})
	require.Error(t, err)
}

// TestCovTraceSpans covers span storage, trace listing, pruning and errors.
func TestCovTraceSpans(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	base := covTestTime(0).UnixNano()
	spans := []TraceSpanRecord{
		{TraceID: "trace-a", SpanID: "span-1", Name: "root", Service: "api", Kind: "server", Status: "OK", StatusCode: 0,
			StartUnixNS: base, EndUnixNS: base + int64(time.Second)},
		{TraceID: "trace-a", SpanID: "span-2", ParentSpanID: "span-1", Name: "db", Service: "api", Status: "ERROR", StatusCode: 2,
			StartUnixNS: base, EndUnixNS: base + int64(2*time.Second), Attributes: `{"k":"v"}`},
		{TraceID: "trace-b", SpanID: "span-3", Name: "root", Service: "worker",
			StartUnixNS: base + int64(3*time.Second), EndUnixNS: base + int64(4*time.Second),
			Attributes: "not-json", Events: `[{"name":"e"}]`, Links: "[broken"},
	}
	for _, span := range spans {
		require.NoError(t, db.StoreTraceSpan(ctx, span))
	}

	traces, err := db.ListTraces(ctx, 0, -1)
	require.NoError(t, err)
	require.Len(t, traces, 2)
	require.Equal(t, "trace-b", traces[0].TraceID, "most recent trace first")
	require.Equal(t, 1, traces[0].SpanCount)
	require.Equal(t, "trace-a", traces[1].TraceID)
	require.Equal(t, 2, traces[1].SpanCount)
	require.Equal(t, 1, traces[1].ErrorCount)
	require.NotZero(t, traces[1].StartTime)
	require.NotZero(t, traces[1].EndUnixNS)

	limited, err := db.ListTraces(ctx, 1, 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	require.Equal(t, "trace-a", limited[0].TraceID)

	traceSpans, err := db.GetTraceSpans(ctx, "trace-a")
	require.NoError(t, err)
	require.Len(t, traceSpans, 2)
	require.Equal(t, "span-1", traceSpans[0].SpanID)
	require.True(t, traceSpans[0].StartTime > 0)
	require.Equal(t, "{}", traceSpans[0].Attributes, "invalid JSON is replaced")
	require.Equal(t, "[]", traceSpans[0].Events)

	fallback, err := db.GetTraceSpans(ctx, "trace-b")
	require.NoError(t, err)
	require.Len(t, fallback, 1)
	require.Equal(t, "{}", fallback[0].Attributes)
	require.Equal(t, "[]", fallback[0].Links)

	empty, err := db.GetTraceSpans(ctx, "trace-unknown")
	require.NoError(t, err)
	require.Empty(t, empty)

	// Pruning by cutoff removes the older trace only.
	require.NoError(t, db.PruneTraceSpans(ctx, covTestTime(3*time.Second), 0))
	traces, err = db.ListTraces(ctx, 10, 0)
	require.NoError(t, err)
	require.Len(t, traces, 1)
	require.Equal(t, "trace-b", traces[0].TraceID)

	require.NoError(t, db.Close())
	require.Error(t, db.StoreTraceSpan(ctx, TraceSpanRecord{}))
	_, err = db.ListTraces(ctx, 10, 0)
	require.Error(t, err)
	_, err = db.GetTraceSpans(ctx, "trace-b")
	require.Error(t, err)
	require.Error(t, db.PruneTraceSpans(ctx, time.Time{}, 100))
	require.Error(t, db.migrateTraceSpans(ctx))
}

// TestCovHealthEvents covers health event listing, summaries and errors.
func TestCovHealthEvents(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	now := covTestTime(0)
	require.NoError(t, db.InsertHealthEvent(ctx, model.HealthEvent{
		CameraID: "cam-1", EventType: "check", Status: string(model.HealthStatusHealthy), CreatedAt: now,
	}))
	require.NoError(t, db.InsertHealthEvent(ctx, model.HealthEvent{
		CameraID: "cam-1", EventType: "check", Status: string(model.HealthStatusError), Message: "timeout", CreatedAt: now.Add(time.Minute),
	}))
	require.NoError(t, db.InsertHealthEvent(ctx, model.HealthEvent{
		CameraID: "cam-2", EventType: "probe", Status: string(model.HealthStatusWarning), Metadata: `{"ms":900}`, CreatedAt: now,
	}))

	events, total, err := db.ListHealthEvents(ctx, HealthEventsFilter{CameraID: "cam-1"})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, events, 2)
	require.Equal(t, string(model.HealthStatusError), events[0].Status)
	require.Equal(t, "timeout", events[0].Message)
	require.Equal(t, now.Add(time.Minute), events[0].CreatedAt)

	probes, total, err := db.ListHealthEvents(ctx, HealthEventsFilter{EventType: "probe", Limit: 5})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, probes, 1)
	require.Equal(t, "cam-2", probes[0].CameraID)
	require.Equal(t, `{"ms":900}`, probes[0].Metadata)

	windowed, total, err := db.ListHealthEvents(ctx, HealthEventsFilter{Since: formatTime(now.Add(30 * time.Second))})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, windowed, 1)

	latest, err := db.GetLatestCameraHealth(ctx, "cam-1")
	require.NoError(t, err)
	require.NotNil(t, latest)
	require.Equal(t, string(model.HealthStatusError), latest.Status)

	missing, err := db.GetLatestCameraHealth(ctx, "cam-unknown")
	require.NoError(t, err)
	require.Nil(t, missing)

	summary, err := db.GetCameraHealthSummary(ctx)
	require.NoError(t, err)
	require.Len(t, summary, 2)
	require.Equal(t, string(model.HealthStatusError), summary["cam-1"].LatestStatus)
	require.Equal(t, "check", summary["cam-1"].LatestEvent)
	require.Equal(t, string(model.HealthStatusWarning), summary["cam-2"].LatestStatus)

	deleted, err := db.DeleteHealthEventsByType(ctx, "probe", covTestTime(time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)

	deleted, err = db.DeleteHealthEventsBefore(ctx, now.Add(30*time.Second))
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted, "only the event at 'now' is older than the cutoff")

	remaining, total, err := db.ListHealthEvents(ctx, HealthEventsFilter{})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, remaining, 1)
	require.Equal(t, "cam-1", remaining[0].CameraID)

	require.Equal(t, model.EventSeverityCritical, healthEventSeverity(string(model.HealthStatusError)))
	require.Equal(t, model.EventSeverityWarning, healthEventSeverity(string(model.HealthStatusWarning)))
	require.Equal(t, model.EventSeverityInfo, healthEventSeverity(string(model.HealthStatusHealthy)))
	require.Equal(t, model.EventSeverityInfo, healthEventSeverity("unknown-status"))

	// InsertHealthEvent also mirrors the event into the product event stream.
	_, totalEvents, err := db.ListEvents(ctx, EventsFilter{Source: model.EventSourceHealth})
	require.NoError(t, err)
	require.Equal(t, 3, totalEvents, "health checks are mirrored into the product event stream")

	require.NoError(t, db.Close())
	require.Error(t, db.InsertHealthEvent(ctx, model.HealthEvent{CameraID: "cam"}))
	_, _, err = db.ListHealthEvents(ctx, HealthEventsFilter{})
	require.Error(t, err)
	_, err = db.GetLatestCameraHealth(ctx, "cam-1")
	require.Error(t, err)
	_, err = db.DeleteHealthEventsBefore(ctx, now)
	require.Error(t, err)
	_, err = db.DeleteHealthEventsByType(ctx, "check", now)
	require.Error(t, err)
	_, err = db.GetCameraHealthSummary(ctx)
	require.Error(t, err)
}

// TestCovRecordingPlans covers plan CRUD, windows and stream selection.
func TestCovRecordingPlans(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	require.NotEmpty(t, NewRecordingPlanID())

	continuous := &RecordingPlan{ID: NewRecordingPlanID(), StreamID: "live/cam1", Name: "Always", Mode: RecordingModeContinuous, Enabled: true}
	require.NoError(t, db.UpsertRecordingPlan(ctx, continuous))

	got, err := db.GetRecordingPlan(ctx, continuous.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.False(t, got.CreatedAt.IsZero(), "the database stamps created_at")
	require.Equal(t, "live/cam1", got.StreamID)
	require.Equal(t, RecordingModeContinuous, got.Mode)
	require.True(t, got.Enabled)

	byStream, err := db.GetRecordingPlanByStream(ctx, "live/cam1")
	require.NoError(t, err)
	require.NotNil(t, byStream)
	require.Equal(t, continuous.ID, byStream.ID)

	missing, err := db.GetRecordingPlan(ctx, "plan-unknown")
	require.NoError(t, err)
	require.Nil(t, missing)
	missing, err = db.GetRecordingPlanByStream(ctx, "live/unknown")
	require.NoError(t, err)
	require.Nil(t, missing)

	// A window that is active right now keeps the scheduled plan desired.
	scheduled := &RecordingPlan{
		ID: NewRecordingPlanID(), StreamID: "live/cam2", Name: "Office hours", Mode: RecordingModeScheduled, Enabled: true,
		Windows: []ScheduleWindow{{DayOfWeek: int(time.Now().Weekday()), StartTime: "00:00", EndTime: "23:59"}},
	}
	require.NoError(t, db.UpsertRecordingPlan(ctx, scheduled))
	off := &RecordingPlan{ID: NewRecordingPlanID(), StreamID: "live/cam3", Name: "Disabled", Mode: RecordingModeOff, Enabled: false}
	require.NoError(t, db.UpsertRecordingPlan(ctx, off))

	plans, err := db.ListRecordingPlans(ctx)
	require.NoError(t, err)
	require.Len(t, plans, 3)
	require.Equal(t, "live/cam1", plans[0].StreamID)
	for _, plan := range plans {
		if plan.StreamID == "live/cam2" {
			require.Len(t, plan.Windows, 1)
			require.Equal(t, "00:00", plan.Windows[0].StartTime)
		}
	}

	// Updating the same plan replaces its windows instead of duplicating them.
	// While its window is active the scheduled plan is desired.
	desired, err := db.DesiredRecordingStreams(ctx)
	require.NoError(t, err)
	require.True(t, desired["live/cam2"])

	scheduled.Name = "Office hours v2"
	inactiveDay := (int(time.Now().Weekday()) + 3) % 7
	scheduled.Windows = []ScheduleWindow{
		{DayOfWeek: inactiveDay, StartTime: "09:00", EndTime: "12:00"},
		{DayOfWeek: inactiveDay, StartTime: "13:00", EndTime: "17:00"},
	}
	require.NoError(t, db.UpsertRecordingPlan(ctx, scheduled))
	updated, err := db.GetRecordingPlan(ctx, scheduled.ID)
	require.NoError(t, err)
	require.Equal(t, "Office hours v2", updated.Name)
	require.Len(t, updated.Windows, 2)

	_ = desired

	desired, err = db.DesiredRecordingStreams(ctx)
	require.NoError(t, err)
	require.True(t, desired["live/cam1"])
	require.False(t, desired["live/cam2"], "outside its weekly windows the scheduled plan is not desired")
	require.False(t, desired["live/cam3"], "disabled/off plans are not requested")

	require.NoError(t, db.DeleteRecordingPlan(ctx, off.ID))
	deleted, err := db.GetRecordingPlan(ctx, off.ID)
	require.NoError(t, err)
	require.Nil(t, deleted)

	require.NoError(t, db.DeleteRecordingPlanByStream(ctx, "live/cam2"))
	deleted, err = db.GetRecordingPlan(ctx, scheduled.ID)
	require.NoError(t, err)
	require.Nil(t, deleted)
	desired, err = db.DesiredRecordingStreams(ctx)
	require.NoError(t, err)
	require.False(t, desired["live/cam2"])

	// Deleting a plan removes its windows as well.
	var windows int
	require.NoError(t, db.queryRowContext(ctx, `SELECT COUNT(*) FROM recording_plan_windows`).Scan(&windows))
	require.Zero(t, windows)

	require.NoError(t, db.Close())
	require.Error(t, db.UpsertRecordingPlan(ctx, &RecordingPlan{ID: "plan-x", StreamID: "live/x"}))
	_, err = db.ListRecordingPlans(ctx)
	require.Error(t, err)
	_, err = db.GetRecordingPlan(ctx, "plan-x")
	require.Error(t, err)
	_, err = db.GetRecordingPlanByStream(ctx, "live/x")
	require.Error(t, err)
	require.Error(t, db.DeleteRecordingPlan(ctx, "plan-x"))
	require.Error(t, db.DeleteRecordingPlanByStream(ctx, "live/x"))
	_, err = db.DesiredRecordingStreams(ctx)
	require.Error(t, err)
	_, err = db.listPlanWindows(ctx, "plan-x")
	require.Error(t, err)
	require.Error(t, db.migrateRecordingPlans(ctx))
}

// TestCovScheduleWindowsActive covers the weekly window matcher.
func TestCovScheduleWindowsActive(t *testing.T) {
	// 2024-05-01 is a Wednesday (3).
	now := time.Date(2024, 5, 1, 10, 30, 0, 0, time.UTC)

	require.False(t, ScheduleWindowsActive(nil, now))
	require.True(t, ScheduleWindowsActive([]ScheduleWindow{{DayOfWeek: 3, StartTime: "08:00", EndTime: "12:00"}}, now))
	require.False(t, ScheduleWindowsActive([]ScheduleWindow{{DayOfWeek: 3, StartTime: "11:00", EndTime: "12:00"}}, now))
	require.False(t, ScheduleWindowsActive([]ScheduleWindow{{DayOfWeek: 2, StartTime: "08:00", EndTime: "12:00"}}, now))

	// Overnight windows wrap around midnight.
	require.True(t, ScheduleWindowsActive([]ScheduleWindow{{DayOfWeek: 3, StartTime: "22:00", EndTime: "02:00"}}, now.AddDate(0, 0, 0).Add(12*time.Hour)))
	require.True(t, ScheduleWindowsActive([]ScheduleWindow{{DayOfWeek: 3, StartTime: "22:00", EndTime: "06:00"}},
		time.Date(2024, 5, 2, 5, 0, 0, 0, time.UTC)), "an overnight window continues into the next day")
	require.False(t, ScheduleWindowsActive([]ScheduleWindow{{DayOfWeek: 4, StartTime: "22:00", EndTime: "06:00"}},
		time.Date(2024, 5, 2, 5, 0, 0, 0, time.UTC)), "the wrong weekday does not match")
	require.False(t, ScheduleWindowsActive([]ScheduleWindow{{DayOfWeek: 3, StartTime: "10:00", EndTime: "10:00"}}, now),
		"zero-length windows never match")
	// Malformed entries are ignored rather than panicking.
	require.False(t, ScheduleWindowsActive([]ScheduleWindow{{DayOfWeek: 3, StartTime: "not-a-time", EndTime: "12:00"}}, now))
	require.False(t, ScheduleWindowsActive([]ScheduleWindow{{DayOfWeek: 9, StartTime: "08:00", EndTime: "12:00"}}, now))
}

// TestCovPlatformEventsErrorPaths covers platform event queries on failure.
func TestCovPlatformEventsErrorPaths(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	require.NoError(t, db.AddPlatformEvent(ctx, PlatformEventRow{
		PlatformID: 1, PlatformName: "acme", EventType: PlatformEventRegister, Details: "done",
	}))
	rows, total, err := db.ListPlatformEvents(ctx, 1, string(PlatformEventRegister), 10, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, rows, 1)
	require.Equal(t, "acme", rows[0].PlatformName)
	require.Equal(t, "done", rows[0].Details)
	require.NotZero(t, rows[0].CreatedAt)

	require.NoError(t, db.Close())
	require.Error(t, db.AddPlatformEvent(ctx, PlatformEventRow{PlatformID: 1}))
	_, _, err = db.ListPlatformEvents(ctx, 1, "", 10, 0)
	require.Error(t, err)
}

// TestCovTranscodingPersistence covers the versioned intent document.
func TestCovTranscodingPersistence(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	profiles, err := db.ListTranscodeProfiles(ctx)
	require.NoError(t, err)
	require.Empty(t, profiles)
	jobs, err := db.ListTranscodeJobs(ctx)
	require.NoError(t, err)
	require.Empty(t, jobs)

	// Invalid specs never reach the intent document.
	_, err = db.SaveTranscodeProfile(ctx, transcode.Profile{}, 0)
	require.Error(t, err)
	_, err = db.SaveTranscodeJob(ctx, transcode.Job{}, 0)
	require.Error(t, err)
	_, err = db.SaveTranscodeProfile(ctx, transcode.Profile{ID: "prof-1", Name: "Web"}, math.MaxInt64)
	require.Error(t, err)

	profile := transcode.Profile{
		ID: "prof-1", Name: "Web", Backend: "ffmpeg",
		Video: transcode.VideoSpec{Codec: "h264", Encoder: "libx264", MaxWidth: 1280, MaxHeight: 720, FPS: 25, BitrateKbps: 1000, GOPFrames: 50},
		Audio: transcode.AudioSpec{Codec: "none"},
	}
	saved, err := db.SaveTranscodeProfile(ctx, profile, 0)
	require.NoError(t, err)
	require.Equal(t, uint64(1), saved.Revision)

	profiles, err = db.ListTranscodeProfiles(ctx)
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	require.Equal(t, "prof-1", profiles[0].ID)
	require.Equal(t, uint64(1), profiles[0].Revision)

	// Versioned update requires the current revision.
	profile.Revision = 1
	profile.Name = "Web HD"
	saved, err = db.SaveTranscodeProfile(ctx, profile, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(2), saved.Revision)
	_, err = db.SaveTranscodeProfile(ctx, profile, 1)
	require.Error(t, err, "stale revisions are refused")

	// Jobs must reference an existing profile revision.
	_, err = db.SaveTranscodeJob(ctx, transcode.Job{
		ID: "job-bad", ProfileID: "prof-1", ProfileRevision: 99,
		Source: transcode.SourceRef{EngineID: "engine", StreamID: "cam1", AppName: "live"}, RunPolicy: transcode.Always,
	}, 0)
	require.Error(t, err)

	job := transcode.Job{
		ID: "job-1", ProfileID: "prof-1", ProfileRevision: 2,
		Source: transcode.SourceRef{EngineID: "engine", StreamID: "cam1", AppName: "live"}, RunPolicy: transcode.Always,
	}
	savedJob, err := db.SaveTranscodeJob(ctx, job, 0)
	require.NoError(t, err)
	require.Equal(t, uint64(1), savedJob.Generation)

	jobs, err = db.ListTranscodeJobs(ctx)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, "job-1", jobs[0].ID)
	require.Equal(t, "cam1", jobs[0].Source.StreamID)

	// A referenced profile is frozen.
	_, err = db.SaveTranscodeProfile(ctx, profile, 2)
	require.Error(t, err)
	require.Error(t, db.DeleteTranscodeProfile(ctx, "prof-1", 2))

	require.NoError(t, db.SetTranscodeJobStopped(ctx, "job-1", 1, true))
	require.Error(t, db.SetTranscodeJobStopped(ctx, "job-1", 99, false), "stale generation is refused")

	require.Error(t, db.DeleteTranscodeJob(ctx, "job-1", 5), "stale generation is refused")
	require.NoError(t, db.DeleteTranscodeJob(ctx, "job-1", 1))

	require.NoError(t, db.DeleteTranscodeProfile(ctx, "prof-1", 2))
	require.Error(t, db.DeleteTranscodeProfile(ctx, "prof-1", 2), "deleted profiles are gone")

	profiles, err = db.ListTranscodeProfiles(ctx)
	require.NoError(t, err)
	require.Empty(t, profiles)
	jobs, err = db.ListTranscodeJobs(ctx)
	require.NoError(t, err)
	require.Empty(t, jobs)

	require.EqualError(t, conflict(), "revision_conflict: configuration changed; reload and retry")
	affected, err := emptySQLResult{}.RowsAffected()
	require.NoError(t, err)
	require.Zero(t, affected)
	lastID, err := emptySQLResult{}.LastInsertId()
	require.NoError(t, err)
	require.Zero(t, lastID)

	closed := covClosedDB(t)
	_, _, err = closed.loadTranscodeIntent(ctx)
	require.Error(t, err)
	_, err = closed.ListTranscodeProfiles(ctx)
	require.Error(t, err)
	_, err = closed.ListTranscodeJobs(ctx)
	require.Error(t, err)
	_, err = closed.SaveTranscodeProfile(ctx, profile, 0)
	require.Error(t, err)
	_, err = closed.SaveTranscodeJob(ctx, job, 0)
	require.Error(t, err)
	require.Error(t, closed.DeleteTranscodeProfile(ctx, "prof-1", 0))
	require.Error(t, closed.DeleteTranscodeJob(ctx, "job-1", 0))
	require.Error(t, closed.SetTranscodeJobStopped(ctx, "job-1", 1, true))
	err = closed.changeTranscodeIntent(ctx, func(*transcodeIntent) error { return nil })
	require.Error(t, err, "the load error short-circuits before the change callback")
	require.Error(t, closed.createTranscodeTable(ctx))
}

// TestCovRecordingHelpers covers the small recording helpers directly.
func TestCovRecordingHelpers(t *testing.T) {
	require.Equal(t, model.MergeStatusMerged, mergeStatusFromBool(true))
	require.Equal(t, model.MergeStatusPending, mergeStatusFromBool(false))

	var rec model.Recording
	require.NoError(t, scanRecording(&rec, sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{}))
	require.True(t, rec.StartedAt.IsZero())
	require.True(t, rec.EndedAt.IsZero())
	require.True(t, rec.ReconnectedAt.IsZero())
	require.Equal(t, model.MergeStatusPending, rec.MergeStatus, "legacy rows default to pending")

	started := covTestTime(0)
	ended := covTestTime(time.Minute)
	reconnected := covTestTime(30 * time.Second)
	require.NoError(t, scanRecording(&rec,
		sql.NullString{String: formatTime(started), Valid: true},
		sql.NullString{String: formatTime(ended), Valid: true},
		sql.NullString{String: model.MergeStatusMerged, Valid: true},
		sql.NullString{String: formatTime(reconnected), Valid: true}))
	require.Equal(t, started, rec.StartedAt)
	require.Equal(t, ended, rec.EndedAt)
	require.Equal(t, reconnected, rec.ReconnectedAt)
	require.Equal(t, model.MergeStatusMerged, rec.MergeStatus)

	// A legacy "merged" bool is honoured when merge_status is absent.
	legacy := model.Recording{Merged: true}
	require.NoError(t, scanRecording(&legacy, sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{}))
	require.Equal(t, model.MergeStatusMerged, legacy.MergeStatus)

	// Unparsable timestamps degrade to the zero time instead of failing.
	bad := model.Recording{}
	require.NoError(t, scanRecording(&bad,
		sql.NullString{String: "not-a-timestamp", Valid: true},
		sql.NullString{}, sql.NullString{}, sql.NullString{}))
	require.True(t, bad.StartedAt.IsZero())

	streamRec := model.Recording{StreamID: "live/cam1", CameraID: "cam-1"}
	require.Equal(t, "live/cam1", recordingStreamID(&streamRec))
	require.Equal(t, "cam-1", recordingStreamID(&model.Recording{CameraID: "cam-1"}))
	require.Equal(t, "", recordingStreamID(nil))
}
