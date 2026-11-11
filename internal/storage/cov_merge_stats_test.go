package storage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

func covInsertCameraRow(t *testing.T, db *DB, id, name string) {
	t.Helper()
	_, err := db.execContext(context.Background(),
		`INSERT INTO cameras (id, name, protocol, url, enabled) VALUES (?,?,?,?,1)`, id, name, "rtsp", "rtsp://"+id)
	require.NoError(t, err)
}

func covInsertRawRecording(t *testing.T, db *DB, id, cameraID string, startedAt time.Time, opt func(map[string]any)) {
	t.Helper()
	values := map[string]any{
		"id":           id,
		"camera_id":    cameraID,
		"stream_id":    cameraID,
		"file_path":    "/rec/" + id + ".mp4",
		"format":       string(model.FormatH264),
		"started_at":   timeToDB(startedAt),
		"ended_at":     timeToDB(startedAt.Add(time.Minute)),
		"duration":     60.0,
		"file_size":    int64(1024),
		"frame_count":  60,
		"merged":       0,
		"merge_status": model.MergeStatusPending,
		"archived":     0,
		"locked":       0,
		"gap_reason":   "",
	}
	if opt != nil {
		opt(values)
	}
	_, err := db.execContext(context.Background(), `INSERT INTO recordings
		(id, camera_id, stream_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived, locked, gap_reason)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		values["id"], values["camera_id"], values["stream_id"], values["file_path"], values["format"],
		values["started_at"], values["ended_at"], values["duration"], values["file_size"], values["frame_count"],
		values["merged"], values["merge_status"], values["archived"], values["locked"], values["gap_reason"])
	require.NoError(t, err)
}

// TestCovMergeWindows covers merge-window discovery, segment listing and the
// singleton (window-boundary orphan) query.
func TestCovMergeWindows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	covInsertCameraRow(t, db, "cam1", "Camera One")

	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)
	// Two segments in the same hour bucket.
	covInsertRawRecording(t, db, "seg-a", "cam1", base.Add(1*time.Minute), nil)
	covInsertRawRecording(t, db, "seg-b", "cam1", base.Add(11*time.Minute), nil)
	// One lone segment in another bucket.
	covInsertRawRecording(t, db, "seg-c", "cam1", base.Add(2*time.Hour+5*time.Minute), nil)
	// Already merged segments are not merge candidates.
	covInsertRawRecording(t, db, "seg-merged", "cam1", base.Add(2*time.Minute), func(v map[string]any) {
		v["merged"] = 1
		v["merge_status"] = model.MergeStatusMerged
		v["file_size"] = int64(2048)
	})
	// Archived and incomplete segments are excluded.
	covInsertRawRecording(t, db, "seg-archived", "cam1", base.Add(3*time.Minute), func(v map[string]any) {
		v["archived"] = 1
	})
	covInsertRawRecording(t, db, "seg-open", "cam1", base.Add(4*time.Minute), func(v map[string]any) {
		v["ended_at"] = nil
	})

	// Archived segments are not filtered by the query, so expect all three
	// pending closed segments (merged/open ones are excluded).
	segments, err := db.ListMergeableSegments(ctx, "cam1", base.Add(-time.Minute), base.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, segments, 3)
	require.Equal(t, "seg-a", segments[0].ID)
	require.Equal(t, "seg-archived", segments[1].ID)
	require.Equal(t, "seg-b", segments[2].ID)

	other, err := db.ListMergeableSegments(ctx, "cam-other", base.Add(-time.Minute), base.Add(time.Hour))
	require.NoError(t, err)
	require.Empty(t, other)

	windows, err := db.ListCameraMergeWindows(ctx, "cam1", 0, time.Hour)
	require.NoError(t, err)
	require.Len(t, windows, 1)
	require.Equal(t, 3, windows[0].SegmentCount, "the archived pending segment is counted too")
	require.Equal(t, string(model.FormatH264), windows[0].Format)
	require.False(t, windows[0].StartTime.IsZero())
	require.False(t, windows[0].EndTime.IsZero())

	// A large minimum age excludes every window.
	stale, err := db.ListCameraMergeWindows(ctx, "cam1", 48*time.Hour, time.Hour)
	require.NoError(t, err)
	require.Empty(t, stale)

	pending, err := db.CountPendingMerges(ctx, "cam1")
	require.NoError(t, err)
	require.Equal(t, 3, pending, "merged, archived and open segments are excluded")

	owners, err := db.ListPendingMergeCameraIDs(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"cam1"}, owners)

	singletons, err := db.ListSingletonPendingRecordings(ctx, "cam1", 0, time.Hour)
	require.NoError(t, err)
	require.Len(t, singletons, 1)
	require.Equal(t, "seg-c", singletons[0].ID)
	require.Equal(t, model.FormatH264, singletons[0].Format)

	// No camera means no rows.
	none, err := db.ListSingletonPendingRecordings(ctx, "cam-other", 0, time.Hour)
	require.NoError(t, err)
	require.Empty(t, none)
}

// TestCovMergeWindowSeconds covers the window fallback rules.
func TestCovMergeWindowSeconds(t *testing.T) {
	require.Equal(t, int64(3600), mergeWindowSeconds(0))
	require.Equal(t, int64(3600), mergeWindowSeconds(30*time.Second))
	require.Equal(t, int64(3600), mergeWindowSeconds(59*time.Second))
	require.Equal(t, int64(60), mergeWindowSeconds(time.Minute))
	require.Equal(t, int64(7200), mergeWindowSeconds(2*time.Hour))
	require.Equal(t, int64(90), mergeWindowSeconds(90*time.Second))
}

// TestCovMergeMutations covers merge/replace, growth, status updates and config.
func TestCovMergeMutations(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	covInsertCameraRow(t, db, "cam1", "Camera One")
	base := covTestTime(0)

	// Replace a set of segments.
	oldIDs := []string{"old-1", "old-2"}
	for _, id := range oldIDs {
		covInsertRawRecording(t, db, id, "cam1", base, nil)
	}
	merged := &model.Recording{
		ID: "merged-1", CameraID: "cam1", FilePath: "/rec/merged-1.mp4", Format: model.FormatH264,
		StartedAt: base, EndedAt: base.Add(2 * time.Minute), Duration: 120, FileSize: 4096, FrameCount: 120,
	}
	require.NoError(t, db.MergeAndReplaceRecordings(ctx, merged, oldIDs))
	got, err := db.GetRecording(ctx, "merged-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, model.MergeStatusMerged, got.MergeStatus)
	for _, id := range oldIDs {
		gone, err := db.GetRecording(ctx, id)
		require.NoError(t, err)
		require.Nil(t, gone)
	}

	// Without old IDs it degrades to a plain insert.
	plain := &model.Recording{ID: "plain-1", CameraID: "cam1", FilePath: "/rec/plain-1.mp4", Format: model.FormatH264, StartedAt: base.Add(time.Hour)}
	require.NoError(t, db.MergeAndReplaceRecordings(ctx, plain, nil))
	got, err = db.GetRecording(ctx, "plain-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, model.MergeStatusPending, got.MergeStatus)

	// Growing a merged file updates it and removes the absorbed segments.
	absorbed := []string{"abs-1", "abs-2"}
	for _, id := range absorbed {
		covInsertRawRecording(t, db, id, "cam1", base.Add(2*time.Minute), nil)
	}
	grown := &model.Recording{ID: "merged-1", EndedAt: base.Add(3 * time.Minute), Duration: 180, FileSize: 8192, FrameCount: 180}
	require.NoError(t, db.GrowMergedRecording(ctx, grown, absorbed))
	got, err = db.GetRecording(ctx, "merged-1")
	require.NoError(t, err)
	require.InDelta(t, 180.0, got.Duration, 0.001)
	require.Equal(t, int64(8192), got.FileSize)
	require.True(t, got.EndedAt.Equal(base.Add(3*time.Minute)))
	for _, id := range absorbed {
		gone, err := db.GetRecording(ctx, id)
		require.NoError(t, err)
		require.Nil(t, gone)
	}
	require.ErrorContains(t, db.GrowMergedRecording(ctx, &model.Recording{ID: "missing"}, nil), "not found")

	// Batch merge status updates.
	require.NoError(t, db.SetMergeStatus(ctx, nil, model.MergeStatusMerged), "empty slice is a no-op")
	require.NoError(t, db.SetMergeStatus(ctx, []string{"plain-1", "seg-none"}, model.MergeStatusFailed))
	got, err = db.GetRecording(ctx, "plain-1")
	require.NoError(t, err)
	require.Equal(t, model.MergeStatusFailed, got.MergeStatus)

	// Merge config: nil pointers leave values untouched.
	enabled := true
	interval := "10m"
	windowSize := "1h"
	minAge := "5m"
	batch := 20
	minSegments := 3
	debounce := "30s"
	require.NoError(t, db.UpsertCameraMerge(ctx, "cam1", &enabled, &interval, &windowSize, &minAge, &batch, &minSegments, &enabled, &debounce))
	var (
		gotEnabled  sql.NullBool
		gotInterval sql.NullString
		gotWindow   sql.NullString
		gotMinAge   sql.NullString
		gotBatch    sql.NullInt64
		gotMinSegs  sql.NullInt64
		gotRolling  sql.NullBool
		gotDebounce sql.NullString
	)
	require.NoError(t, db.queryRowContext(ctx, `SELECT merge_enabled, merge_check_interval, merge_window_size, merge_min_segment_age,
		merge_batch_limit, merge_min_segments_to_merge, merge_rolling_enabled, merge_rolling_debounce FROM cameras WHERE id='cam1'`).
		Scan(&gotEnabled, &gotInterval, &gotWindow, &gotMinAge, &gotBatch, &gotMinSegs, &gotRolling, &gotDebounce))
	require.True(t, gotEnabled.Valid && gotEnabled.Bool)
	require.Equal(t, "10m", gotInterval.String)
	require.Equal(t, "1h", gotWindow.String)
	require.Equal(t, "5m", gotMinAge.String)
	require.Equal(t, int64(20), gotBatch.Int64)
	require.Equal(t, int64(3), gotMinSegs.Int64)
	require.True(t, gotRolling.Valid && gotRolling.Bool)
	require.Equal(t, "30s", gotDebounce.String)

	// Only the batch limit changes; everything else is preserved.
	newBatch := 50
	require.NoError(t, db.UpsertCameraMerge(ctx, "cam1", nil, nil, nil, nil, &newBatch, nil, nil, nil))
	require.NoError(t, db.queryRowContext(ctx, `SELECT merge_enabled, merge_check_interval, merge_batch_limit FROM cameras WHERE id='cam1'`).
		Scan(&gotEnabled, &gotInterval, &gotBatch))
	require.True(t, gotEnabled.Valid && gotEnabled.Bool)
	require.Equal(t, "10m", gotInterval.String)
	require.Equal(t, int64(50), gotBatch.Int64)

	// Merged-window lookups.
	hourStart := base.Add(-time.Minute)
	hourEnd := base.Add(10 * time.Minute)
	mergedInHour, err := db.ListHourMergedRecordings(ctx, "cam1", hourStart, hourEnd)
	require.NoError(t, err)
	require.Len(t, mergedInHour, 1)
	require.Equal(t, "merged-1", mergedInHour[0].ID)
	windowMerged, err := db.ListWindowMergedRecordings(ctx, "cam1", hourStart, hourEnd)
	require.NoError(t, err)
	require.Len(t, windowMerged, 1)
	outside, err := db.ListWindowMergedRecordings(ctx, "cam1", base.Add(-2*time.Hour), base.Add(-time.Hour))
	require.NoError(t, err)
	require.Empty(t, outside)
}

// TestCovRecordingTimeline covers the compact DVR timeline projection.
func TestCovRecordingTimeline(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	covInsertCameraRow(t, db, "cam1", "Camera One")
	base := covTestTime(0)

	covInsertRawRecording(t, db, "tl-1", "cam1", base, func(v map[string]any) {
		v["gap_reason"] = "stream gap"
	})
	covInsertRawRecording(t, db, "tl-2", "cam1", base.Add(5*time.Minute), func(v map[string]any) {
		v["locked"] = 1
		v["format"] = string(model.FormatMJPEG)
		v["merged"] = 1
		v["merge_status"] = model.MergeStatusMerged
	})
	covInsertRawRecording(t, db, "tl-archived", "cam1", base.Add(10*time.Minute), func(v map[string]any) {
		v["archived"] = 1
	})
	covInsertRawRecording(t, db, "tl-outside", "cam1", base.Add(48*time.Hour), nil)

	entries, err := db.ListRecordingTimeline(ctx, "cam1", base.Add(-time.Hour), base.Add(time.Hour), 0)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, "tl-1", entries[0].ID)
	require.Equal(t, "stream gap", entries[0].GapReason)
	require.False(t, entries[0].Locked)
	require.Equal(t, "tl-2", entries[1].ID)
	require.True(t, entries[1].Locked)
	require.True(t, entries[1].Merged)
	require.Equal(t, model.FormatMJPEG, entries[1].Format)
	require.InDelta(t, 60.0, entries[1].Duration, 0.001)

	limited, err := db.ListRecordingTimeline(ctx, "cam1", base.Add(-time.Hour), base.Add(time.Hour), 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)

	// limit above the cap is clamped and still returns rows.
	capped, err := db.ListRecordingTimeline(ctx, "cam1", base.Add(-time.Hour), base.Add(time.Hour), 50000)
	require.NoError(t, err)
	require.Len(t, capped, 2)
}

// TestCovRecordingStats covers the aggregate statistics helpers.
func TestCovRecordingStats(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	covInsertCameraRow(t, db, "cam1", "Camera One")
	covInsertCameraRow(t, db, "cam2", "Camera Two")

	covInsertRawRecording(t, db, "st-1", "cam1", now.Add(-time.Hour), func(v map[string]any) {
		v["file_size"] = int64(100)
	})
	covInsertRawRecording(t, db, "st-2", "cam1", now.Add(-2*time.Hour), func(v map[string]any) {
		v["file_size"] = int64(200)
	})
	covInsertRawRecording(t, db, "st-3", "cam2", now.Add(-3*time.Hour), func(v map[string]any) {
		v["file_size"] = int64(300)
	})

	count, err := db.CountRecordings(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, count)

	count, err = db.CountRecordingsByCamera(ctx, "cam1")
	require.NoError(t, err)
	require.Equal(t, 2, count)
	count, err = db.CountRecordingsByCamera(ctx, "cam-missing")
	require.NoError(t, err)
	require.Zero(t, count)

	trends, err := db.GetRecordingTrends(ctx, 0)
	require.NoError(t, err)
	require.NotEmpty(t, trends)
	var trendTotal int
	var trendSize int64
	cameraCounts := map[string]int{}
	for _, day := range trends {
		require.NotEmpty(t, day.Date)
		trendTotal += day.Recordings
		trendSize += day.TotalSize
		for name, n := range day.CameraCounts {
			cameraCounts[name] += n
		}
	}
	require.Equal(t, 3, trendTotal)
	require.Equal(t, int64(600), trendSize)
	require.Equal(t, 2, cameraCounts["Camera One"])
	require.Equal(t, 1, cameraCounts["Camera Two"])

	// Clamping: 100 days becomes 30 and still includes recent rows.
	clamped, err := db.GetRecordingTrends(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, len(trends), len(clamped))

	hourly, err := db.GetHourlyRecordingStats(ctx, 0)
	require.NoError(t, err)
	require.Len(t, hourly, 3)
	var hourlyTotal int64
	var hourlyCount int
	for _, h := range hourly {
		require.Contains(t, h.Hour, "T")
		hourlyTotal += h.TotalSize
		hourlyCount += h.Recordings
	}
	require.Equal(t, int64(600), hourlyTotal)
	require.Equal(t, 3, hourlyCount)

	clampedHours, err := db.GetHourlyRecordingStats(ctx, 100000)
	require.NoError(t, err)
	require.Len(t, clampedHours, 3)

	last, err := db.GetLastRecordingTime(ctx, "cam1")
	require.NoError(t, err)
	require.NotNil(t, last)

	noLast, err := db.GetLastRecordingTime(ctx, "cam-missing")
	require.NoError(t, err)
	require.Nil(t, noLast)

	allLast, err := db.GetAllLastRecordingTimes(ctx)
	require.NoError(t, err)
	require.Len(t, allLast, 2)
	require.Contains(t, allLast, "cam1")
	require.Contains(t, allLast, "cam2")
	require.True(t, allLast["cam1"].After(*allLast["cam2"]))

	// Uptime stats come from camera_health_events.
	require.NoError(t, db.InsertHealthEvent(ctx, model.HealthEvent{
		CameraID: "cam1", EventType: "connection_lost", Status: "error", Message: "lost", CreatedAt: now.Add(-time.Hour),
	}))
	require.NoError(t, db.InsertHealthEvent(ctx, model.HealthEvent{
		CameraID: "cam1", EventType: "connection_restored", Status: "ok", Message: "back", CreatedAt: now.Add(-30 * time.Minute),
	}))
	require.NoError(t, db.InsertHealthEvent(ctx, model.HealthEvent{
		CameraID: "cam2", EventType: "connection_lost", Status: "error", Message: "lost", CreatedAt: now.Add(-15 * time.Minute),
	}))
	require.NoError(t, db.InsertHealthEvent(ctx, model.HealthEvent{
		CameraID: "cam2", EventType: "connection_lost", Status: "error", Message: "lost again", CreatedAt: now.Add(-5 * time.Minute),
	}))

	uptime, err := db.GetCameraUptimeStats(ctx, 7)
	require.NoError(t, err)
	require.Len(t, uptime, 2)
	uptimeByID := map[string]model.CameraUptimeStat{}
	for _, u := range uptime {
		uptimeByID[u.CameraID] = u
	}
	require.Equal(t, 1, uptimeByID["cam1"].ConnectionLosses)
	require.Equal(t, 1, uptimeByID["cam1"].ConnectionRestores)
	require.Equal(t, 2, uptimeByID["cam1"].TotalEvents)
	require.Equal(t, "Camera One", uptimeByID["cam1"].CameraName)
	require.Equal(t, 2, uptimeByID["cam2"].ConnectionLosses)
	require.Zero(t, uptimeByID["cam2"].ConnectionRestores)
	require.Equal(t, 2, uptimeByID["cam2"].TotalEvents)
	require.Equal(t, "cam1", uptime[1].CameraID, "ordered by connection losses, descending")

	// Defaults and clamps.
	defaulted, err := db.GetCameraUptimeStats(ctx, 0)
	require.NoError(t, err)
	require.Len(t, defaulted, 2)
	clampedDays, err := db.GetCameraUptimeStats(ctx, 500)
	require.NoError(t, err)
	require.Len(t, clampedDays, 2)

	old, err := db.GetCameraUptimeStats(ctx, 1)
	require.NoError(t, err)
	require.Len(t, old, 2)
}

// TestCovMergeStatsEmptyDB covers the empty-result branches.
func TestCovMergeStatsEmptyDB(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	trends, err := db.GetRecordingTrends(ctx, 7)
	require.NoError(t, err)
	require.Empty(t, trends)

	hourly, err := db.GetHourlyRecordingStats(ctx, 24)
	require.NoError(t, err)
	require.Empty(t, hourly)

	uptime, err := db.GetCameraUptimeStats(ctx, 7)
	require.NoError(t, err)
	require.Empty(t, uptime)

	days, err := db.GetRecordingDays(ctx, "cam1", "2024-05")
	require.NoError(t, err)
	require.Empty(t, days)

	allLast, err := db.GetAllLastRecordingTimes(ctx)
	require.NoError(t, err)
	require.Empty(t, allLast)

	owners, err := db.ListPendingMergeCameraIDs(ctx)
	require.NoError(t, err)
	require.Empty(t, owners)

	windows, err := db.ListCameraMergeWindows(ctx, "cam1", 0, 0)
	require.NoError(t, err)
	require.Empty(t, windows)

	segments, err := db.ListMergeableSegments(ctx, "cam1", time.Time{}, time.Time{})
	require.NoError(t, err)
	require.Empty(t, segments)

	timeline, err := db.ListRecordingTimeline(ctx, "cam1", time.Time{}, time.Time{}, 10)
	require.NoError(t, err)
	require.Empty(t, timeline)
}

// TestCovMergeStatsClosedDB asserts errors propagate for every helper.
func TestCovMergeStatsClosedDB(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.Close())

	err := db.MergeAndReplaceRecordings(ctx, &model.Recording{ID: "m", CameraID: "c", FilePath: "f", Format: model.FormatH264}, []string{"old"})
	require.Error(t, err)
	_, err = db.ListMergeableSegments(ctx, "cam1", time.Time{}, time.Time{})
	require.Error(t, err)
	_, err = db.ListCameraMergeWindows(ctx, "cam1", 0, 0)
	require.Error(t, err)
	_, err = db.ListWindowMergedRecordings(ctx, "cam1", time.Time{}, time.Time{})
	require.Error(t, err)
	_, err = db.CountPendingMerges(ctx, "cam1")
	require.Error(t, err)
	_, err = db.ListRecordingTimeline(ctx, "cam1", time.Time{}, time.Time{}, 1)
	require.Error(t, err)
	require.Error(t, db.UpsertCameraMerge(ctx, "cam1", nil, nil, nil, nil, nil, nil, nil, nil))
	_, err = db.ListPendingMergeCameraIDs(ctx)
	require.Error(t, err)
	require.Error(t, db.GrowMergedRecording(ctx, &model.Recording{ID: "m"}, nil))
	require.Error(t, db.SetMergeStatus(ctx, []string{"id"}, model.MergeStatusMerged))
	_, err = db.ListSingletonPendingRecordings(ctx, "cam1", 0, 0)
	require.Error(t, err)
	_, err = db.ListHourMergedRecordings(ctx, "cam1", time.Time{}, time.Time{})
	require.Error(t, err)

	_, err = db.CountRecordings(ctx)
	require.Error(t, err)
	_, err = db.CountRecordingsByCamera(ctx, "cam1")
	require.Error(t, err)
	_, err = db.GetRecordingTrends(ctx, 7)
	require.Error(t, err)
	_, err = db.GetRecordingDays(ctx, "cam1", "2024-05")
	require.Error(t, err)
	_, err = db.GetLastRecordingTime(ctx, "cam1")
	require.Error(t, err)
	_, err = db.GetHourlyRecordingStats(ctx, 24)
	require.Error(t, err)
	_, err = db.GetCameraUptimeStats(ctx, 7)
	require.Error(t, err)
	_, err = db.GetAllLastRecordingTimes(ctx)
	require.Error(t, err)
}
