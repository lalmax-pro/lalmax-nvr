package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

// covTestTime returns a deterministic UTC timestamp offset by d.
func covTestTime(d time.Duration) time.Time {
	return time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC).Add(d)
}

func covInsertRecording(t *testing.T, db *DB, id, cameraID string, startedAt time.Time, opts ...func(*model.Recording)) *model.Recording {
	t.Helper()
	rec := &model.Recording{
		ID:        id,
		CameraID:  cameraID,
		FilePath:  filepath.Join("/rec", cameraID, id+".mp4"),
		Format:    model.FormatH264,
		StartedAt: startedAt,
		EndedAt:   startedAt.Add(30 * time.Second),
		Duration:  30,
		FileSize:  2048,
	}
	for _, opt := range opts {
		opt(rec)
	}
	require.NoError(t, db.InsertRecording(context.Background(), rec))
	return rec
}

// TestCovStreamHistoryLifecycle exercises the stream_history lifecycle:
// insert, media state tracking, finish, listing and startup finalization.
func TestCovStreamHistoryLifecycle(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	started := covTestTime(0)

	h := &StreamHistory{
		StreamID:   "live/cam1",
		AppName:    "live",
		Protocol:   "rtmp",
		RemoteAddr: "10.0.0.1:1234",
		SessionID:  "sess-1",
		StartedAt:  started,
	}
	require.NoError(t, db.InsertStreamHistory(ctx, h))
	require.NotZero(t, h.ID)

	// Media start is recorded once and is idempotent (COALESCE keeps the first).
	require.NoError(t, db.RecordStreamHistoryMediaState(ctx, "live/cam1", started.Add(2*time.Second), true))
	require.NoError(t, db.RecordStreamHistoryMediaState(ctx, "live/cam1", started.Add(9*time.Second), true))
	require.NoError(t, db.RecordStreamHistoryMediaState(ctx, "live/cam1", started.Add(20*time.Second), false))

	// Empty stream ID and zero time are tolerated.
	require.NoError(t, db.RecordStreamHistoryMediaState(ctx, "", started, true))
	require.NoError(t, db.RecordStreamHistoryMediaState(ctx, "live/cam1", time.Time{}, false))

	ended := started.Add(60 * time.Second)
	require.NoError(t, db.FinishStreamHistory(ctx, "live/cam1", "sess-1", ended, "unpublish", "client closed", 4096, 8192))
	// Finishing an unknown session is a no-op, not an error.
	require.NoError(t, db.FinishStreamHistory(ctx, "live/cam1", "missing", ended, "unpublish", "", 0, 0))

	items, total, err := db.ListStreamHistory(ctx, "live/cam1", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, items, 1)
	got := items[0]
	require.Equal(t, "rtmp", got.Protocol)
	require.Equal(t, "unpublish", got.OfflineReason)
	require.Equal(t, "client closed", got.OfflineDetail)
	require.Equal(t, uint64(4096), got.BytesRead)
	require.Equal(t, uint64(8192), got.BytesWritten)
	require.InDelta(t, 60.0, got.DurationSec, 0.001)
	require.NotNil(t, got.EndedAt)
	require.NotNil(t, got.MediaStartedAt)
	require.NotNil(t, got.MediaEndedAt)
	require.True(t, got.MediaStartedAt.Equal(started.Add(2*time.Second)))

	// Unfiltered listing with defaults (limit <= 0, negative offset).
	all, totalAll, err := db.ListStreamHistory(ctx, "", 0, -5)
	require.NoError(t, err)
	require.Equal(t, 1, totalAll)
	require.Len(t, all, 1)

	items, total, err = db.ListStreamHistory(ctx, "other", 10, 0)
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, items)

	// A closed session is not counted as open.
	openBefore, err := db.ListRecentStreamSnapshots(ctx, started.Add(-time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, openBefore, 1)
	require.NotNil(t, openBefore[0].EndedAt)
}

// TestCovFinalizeOpenStreamHistories covers startup cleanup of sessions left
// open by a previous process, including the negative-duration clamp.
func TestCovFinalizeOpenStreamHistories(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	started := covTestTime(0)

	open := &StreamHistory{StreamID: "live/cam1", AppName: "live", Protocol: "rtmp", SessionID: "s1", StartedAt: started}
	require.NoError(t, db.InsertStreamHistory(ctx, open))
	require.NoError(t, db.RecordStreamHistoryMediaState(ctx, "live/cam1", started.Add(time.Second), true))

	// Second stream started in the future: duration must clamp to zero.
	future := &StreamHistory{StreamID: "live/cam2", AppName: "live", Protocol: "rtsp", SessionID: "s2", StartedAt: started.Add(10 * time.Minute)}
	require.NoError(t, db.InsertStreamHistory(ctx, future))

	finished := &StreamHistory{StreamID: "live/cam3", AppName: "live", Protocol: "rtmp", SessionID: "s3", StartedAt: started}
	require.NoError(t, db.InsertStreamHistory(ctx, finished))
	require.NoError(t, db.FinishStreamHistory(ctx, "live/cam3", "s3", started.Add(time.Minute), "done", "", 0, 0))

	require.NoError(t, db.FinalizeOpenStreamHistories(ctx, started.Add(5*time.Minute), "restart"))

	// Zero endedAt falls back to now and still closes the sessions.
	require.NoError(t, db.FinalizeOpenStreamHistories(ctx, time.Time{}, "restart"))

	cam1, _, err := db.ListStreamHistory(ctx, "live/cam1", 10, 0)
	require.NoError(t, err)
	require.Len(t, cam1, 1)
	require.NotNil(t, cam1[0].EndedAt)
	require.Equal(t, "restart", cam1[0].OfflineReason)
	require.InDelta(t, 300.0, cam1[0].DurationSec, 0.001)
	require.NotNil(t, cam1[0].MediaEndedAt, "media end filled for sessions that had media start")

	cam2, _, err := db.ListStreamHistory(ctx, "live/cam2", 10, 0)
	require.NoError(t, err)
	require.Len(t, cam2, 1)
	require.Zero(t, cam2[0].DurationSec, "negative durations are clamped to zero")
	require.Nil(t, cam2[0].MediaEndedAt, "no media end without media start")

	cam3, _, err := db.ListStreamHistory(ctx, "live/cam3", 10, 0)
	require.NoError(t, err)
	require.Len(t, cam3, 1)
	require.Equal(t, "done", cam3[0].OfflineReason, "already finished sessions are untouched")
}

// TestCovRecentStreamSnapshots covers the per-stream latest snapshot query.
func TestCovRecentStreamSnapshots(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	since := covTestTime(-time.Hour)

	first := &StreamHistory{StreamID: "live/cam1", AppName: "live", Protocol: "rtmp", SessionID: "s1", StartedAt: covTestTime(0)}
	require.NoError(t, db.InsertStreamHistory(ctx, first))
	require.NoError(t, db.FinishStreamHistory(ctx, "live/cam1", "s1", covTestTime(time.Minute), "done", "", 0, 0))

	second := &StreamHistory{StreamID: "live/cam1", AppName: "live", Protocol: "rtmp", SessionID: "s2", StartedAt: covTestTime(2 * time.Minute)}
	require.NoError(t, db.InsertStreamHistory(ctx, second))

	// Stream outside the window is ignored.
	old := &StreamHistory{StreamID: "live/old", AppName: "live", Protocol: "rtmp", SessionID: "s3", StartedAt: covTestTime(-48 * time.Hour)}
	require.NoError(t, db.InsertStreamHistory(ctx, old))

	snaps, err := db.ListRecentStreamSnapshots(ctx, since, 0)
	require.NoError(t, err)
	require.Len(t, snaps, 1)
	require.Equal(t, "live/cam1", snaps[0].StreamID)
	require.Equal(t, "rtmp", snaps[0].Protocol)
	require.True(t, snaps[0].StartedAt.Equal(covTestTime(2*time.Minute)), "latest row per stream wins")
	require.Nil(t, snaps[0].EndedAt)

	require.NoError(t, db.DeleteStreamHistory(ctx, "live/cam1"))
	snaps, err = db.ListRecentStreamSnapshots(ctx, since, 10)
	require.NoError(t, err)
	require.Empty(t, snaps)
}

// TestCovStreamBans covers insert/get/expiry/delete/list semantics.
func TestCovStreamBans(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	noExpiry := &StreamBan{StreamID: "live/perm", Reason: "abuse", CreatedAt: now}
	require.NoError(t, db.InsertStreamBan(ctx, noExpiry))
	require.NotZero(t, noExpiry.ID)

	future := now.Add(time.Hour)
	expiring := &StreamBan{StreamID: "live/temp", Reason: "spam", CreatedAt: now, ExpiresAt: &future}
	require.NoError(t, db.InsertStreamBan(ctx, expiring))

	past := now.Add(-time.Hour)
	expired := &StreamBan{StreamID: "live/old", Reason: "old", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: &past}
	require.NoError(t, db.InsertStreamBan(ctx, expired))

	ban, err := db.GetStreamBan(ctx, "live/perm")
	require.NoError(t, err)
	require.NotNil(t, ban)
	require.Equal(t, "abuse", ban.Reason)
	require.Nil(t, ban.ExpiresAt)

	banned, err := db.IsStreamBanned(ctx, "live/temp")
	require.NoError(t, err)
	require.True(t, banned)

	// An expired ban is reported as absent and auto-deleted.
	ban, err = db.GetStreamBan(ctx, "live/old")
	require.NoError(t, err)
	require.Nil(t, ban)
	var remaining int
	require.NoError(t, db.queryRowContext(ctx, `SELECT COUNT(*) FROM stream_bans WHERE stream_id='live/old'`).Scan(&remaining))
	require.Zero(t, remaining)

	ban, err = db.GetStreamBan(ctx, "live/unknown")
	require.NoError(t, err)
	require.Nil(t, ban)
	banned, err = db.IsStreamBanned(ctx, "live/unknown")
	require.NoError(t, err)
	require.False(t, banned)

	// ListStreamBans skips the expired row that still exists in storage.
	_, err = db.execContext(ctx, `INSERT INTO stream_bans (stream_id, reason, created_at, expires_at) VALUES (?,?,?,?)`,
		"live/list-expired", "x", timeToDB(now), timeToDBPtr(&past))
	require.NoError(t, err)

	bans, err := db.ListStreamBans(ctx)
	require.NoError(t, err)
	require.Len(t, bans, 2)
	ids := []string{bans[0].StreamID, bans[1].StreamID}
	require.ElementsMatch(t, []string{"live/perm", "live/temp"}, ids)

	// Deleting an unknown ban reports sql.ErrNoRows; deleting a known one works.
	require.ErrorIs(t, db.DeleteStreamBan(ctx, "live/missing"), sql.ErrNoRows)
	require.NoError(t, db.DeleteStreamBan(ctx, "live/temp"))
	bans, err = db.ListStreamBans(ctx)
	require.NoError(t, err)
	require.Len(t, bans, 1)
}

// TestCovStreamTimeHelpers covers the nil/zero branches of the pointer helpers.
func TestCovStreamTimeHelpers(t *testing.T) {
	require.Nil(t, timeToDBPtr(nil))
	zero := time.Time{}
	require.Nil(t, timeToDBPtr(&zero))
	when := covTestTime(time.Minute)
	require.Equal(t, when.Format(sqliteTimeFormat), timeToDBPtr(&when))

	var dest time.Time
	require.NoError(t, parseTimeScan(&dest).Scan(nil))
	require.True(t, dest.IsZero())
	require.NoError(t, parseTimeScan(&dest).Scan(when))
	require.True(t, dest.Equal(when))
	require.NoError(t, parseTimeScan(&dest).Scan("2024-05-01 12:00:00.5"))
	require.Equal(t, 2024, dest.Year())
}

// TestCovRecordingFilterMatrix seeds a small corpus and walks every filter
// branch of ListRecordings / CountRecordingsWithFilter.
func TestCovRecordingFilterMatrix(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := covTestTime(0)

	covInsertRecording(t, db, "rec-a", "camA", base)
	covInsertRecording(t, db, "rec-b", "camB", base.Add(time.Hour), func(r *model.Recording) {
		r.Format = model.FormatMJPEG
		r.Merged = true
	})
	covInsertRecording(t, db, "rec-c", "camC", base.Add(2*time.Hour), func(r *model.Recording) {
		r.StreamID = "custom/stream"
		r.FilePath = "/rec/odd_100%.mp4"
	})
	// Archived rows are only visible when explicitly requested.
	_, err := db.execContext(ctx, `UPDATE recordings SET archived=1 WHERE id='rec-c'`)
	require.NoError(t, err)

	ids := func(recs []model.Recording) []string {
		out := make([]string, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.ID)
		}
		return out
	}
	count := func(filter model.RecordingFilter) int {
		n, err := db.CountRecordingsWithFilter(ctx, filter)
		require.NoError(t, err)
		return n
	}

	all, err := db.ListRecordings(ctx, model.RecordingFilter{})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"rec-a", "rec-b"}, ids(all))
	require.Equal(t, 2, count(model.RecordingFilter{}))

	byCamera, err := db.ListRecordings(ctx, model.RecordingFilter{CameraID: "camA"})
	require.NoError(t, err)
	require.Equal(t, []string{"rec-a"}, ids(byCamera))

	byStream, err := db.ListRecordings(ctx, model.RecordingFilter{StreamID: "custom/stream", Archived: boolPtrCov(true)})
	require.NoError(t, err)
	require.Equal(t, []string{"rec-c"}, ids(byStream))

	// Lazy rows inserted before the stream_id column existed fall back to camera_id.
	_, err = db.execContext(ctx, `INSERT INTO recordings (id, camera_id, stream_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived)
		VALUES ('rec-legacy','camLegacy','', '/rec/legacy.mp4','h264',?,?,10,1,1,0,'pending',0)`,
		timeToDB(base.Add(3*time.Hour)), timeToDB(base.Add(3*time.Hour+time.Minute)))
	require.NoError(t, err)
	legacy, err := db.ListRecordings(ctx, model.RecordingFilter{StreamID: "camLegacy"})
	require.NoError(t, err)
	require.Equal(t, []string{"rec-legacy"}, ids(legacy))

	merged := true
	unmerged := false
	byMerged, err := db.ListRecordings(ctx, model.RecordingFilter{Merged: &merged})
	require.NoError(t, err)
	require.Equal(t, []string{"rec-b"}, ids(byMerged))
	require.Equal(t, 1, count(model.RecordingFilter{Merged: &merged}))

	byUnmerged, err := db.ListRecordings(ctx, model.RecordingFilter{Merged: &unmerged})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"rec-a", "rec-legacy"}, ids(byUnmerged))

	byTime, err := db.ListRecordings(ctx, model.RecordingFilter{
		StartTime: base.Add(30 * time.Minute),
		EndTime:   base.Add(90 * time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, []string{"rec-b"}, ids(byTime))
	require.Equal(t, 1, count(model.RecordingFilter{StartTime: base.Add(30 * time.Minute), EndTime: base.Add(90 * time.Minute)}))

	byFormat, err := db.ListRecordings(ctx, model.RecordingFilter{Format: model.FormatMJPEG})
	require.NoError(t, err)
	require.Equal(t, []string{"rec-b"}, ids(byFormat))
	require.Equal(t, 1, count(model.RecordingFilter{Format: model.FormatMJPEG}))

	// Search matches camera_id, format and file_path with LIKE escaping.
	for _, tc := range []struct {
		name    string
		search  string
		archive bool
		want    []string
	}{
		{name: "camera", search: "camA", want: []string{"rec-a"}},
		{name: "format", search: "mjpeg", want: []string{"rec-b"}},
		{name: "path literal percent", search: "_100%", archive: true, want: []string{"rec-c"}},
		{name: "no match", search: "nothing-matches", want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter := model.RecordingFilter{Search: tc.search}
			if tc.archive {
				filter.Archived = boolPtrCov(true)
			}
			recs, err := db.ListRecordings(ctx, filter)
			require.NoError(t, err)
			require.ElementsMatch(t, tc.want, ids(recs))
			require.Equal(t, len(tc.want), count(filter))
		})
	}

	archived := true
	notArchived := false
	byArchived, err := db.ListRecordings(ctx, model.RecordingFilter{Archived: &archived})
	require.NoError(t, err)
	require.Equal(t, []string{"rec-c"}, ids(byArchived))
	byNotArchived, err := db.ListRecordings(ctx, model.RecordingFilter{Archived: &notArchived})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"rec-a", "rec-b", "rec-legacy"}, ids(byNotArchived))

	// Sorting and paging.
	asc, err := db.ListRecordings(ctx, model.RecordingFilter{SortBy: "camera_id", SortOrder: "asc", Limit: 2})
	require.NoError(t, err)
	require.Equal(t, []string{"rec-a", "rec-b"}, ids(asc))

	page, err := db.ListRecordings(ctx, model.RecordingFilter{SortBy: "started_at", SortOrder: "desc", Limit: 1, Offset: 1})
	require.NoError(t, err)
	require.Equal(t, []string{"rec-b"}, ids(page))

	// Unknown sort column falls back to started_at and unknown order to DESC.
	fallback, err := db.ListRecordings(ctx, model.RecordingFilter{SortBy: "evil; DROP TABLE recordings", SortOrder: "sideways"})
	require.NoError(t, err)
	require.Equal(t, []string{"rec-legacy", "rec-b", "rec-a"}, ids(fallback))

	// Cache hits return the same rows.
	cached, err := db.ListRecordings(ctx, model.RecordingFilter{})
	require.NoError(t, err)
	require.Equal(t, len(all), len(cached))
}

func boolPtrCov(b bool) *bool { return &b }

// TestCovRecordingReadModels covers the read-only list helpers used by the
// retention and repair jobs.
func TestCovRecordingReadModels(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	oldStart := now.AddDate(0, 0, -60)
	oldEnd := oldStart.Add(10 * time.Minute)

	covInsertRecording(t, db, "old-h264", "camA", oldStart, func(r *model.Recording) {
		r.EndedAt = oldEnd
		r.Duration = 600
	})
	covInsertRecording(t, db, "old-mjpeg", "camA", oldStart.Add(time.Hour), func(r *model.Recording) {
		r.Format = model.FormatMJPEG
		r.EndedAt = oldEnd.Add(time.Hour)
		r.FileSize = 4096
	})
	covInsertRecording(t, db, "fresh", "camA", now.Add(-time.Hour))
	covInsertRecording(t, db, "other-cam", "camB", oldStart.Add(2*time.Hour), func(r *model.Recording) {
		r.EndedAt = oldEnd.Add(2 * time.Hour)
	})
	covInsertRecording(t, db, "locked", "camA", oldStart.Add(3*time.Hour), func(r *model.Recording) {
		r.EndedAt = oldEnd.Add(3 * time.Hour)
	})
	require.NoError(t, db.SetRecordingLocked(ctx, "locked", true))
	covInsertRecording(t, db, "zero-duration", "camC", oldStart.Add(4*time.Hour), func(r *model.Recording) {
		r.EndedAt = oldEnd.Add(4 * time.Hour)
		r.Duration = 0
		r.FileSize = 2 << 20
	})
	// Archived expired row only shows up in the archived query.
	covInsertRecording(t, db, "archived-old", "camA", oldStart.Add(5*time.Hour), func(r *model.Recording) {
		r.EndedAt = oldEnd.Add(5 * time.Hour)
	})
	_, err := db.execContext(ctx, `UPDATE recordings SET archived=1 WHERE id='archived-old'`)
	require.NoError(t, err)

	expired, err := db.ListExpiredRecordings(ctx, 30)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"old-h264", "old-mjpeg", "other-cam", "zero-duration"}, covRecordingIDs(expired))

	expiredCam, err := db.ListExpiredRecordingsByCamera(ctx, "camA", 30)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"old-h264", "old-mjpeg"}, covRecordingIDs(expiredCam))

	expiredArchived, err := db.ListExpiredArchivedRecordingsByCamera(ctx, "camA", 30)
	require.NoError(t, err)
	require.Equal(t, []string{"archived-old"}, covRecordingIDs(expiredArchived))

	oldest, err := db.ListOldestRecordings(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"old-h264", "old-mjpeg"}, covRecordingIDs(oldest))
	require.NotContains(t, covRecordingIDs(oldest), "locked", "locked rows are never candidates for deletion")

	paths, err := db.ListRecordingPathsByCamera(ctx, "camA")
	require.NoError(t, err)
	require.True(t, paths["old-h264.mp4"])
	require.True(t, paths["locked.mp4"])
	require.False(t, paths["other-cam.mp4"])

	pending, err := db.ListPendingMJPEGRecordings(ctx, "camA")
	require.NoError(t, err)
	require.Equal(t, []string{"old-mjpeg"}, covRecordingIDs(pending))

	repairable, err := db.RepairZeroDurationRecordings(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"zero-duration"}, covRecordingIDs(repairable))

	owners, err := db.ListDistinctRecordingOwners(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"camA", "camB", "camC"}, owners)

	require.NoError(t, db.UpdateRecordingDuration(ctx, "zero-duration", 42.5, oldEnd.Add(5*time.Hour)))
	updated, err := db.GetRecording(ctx, "zero-duration")
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.InDelta(t, 42.5, updated.Duration, 0.001)
	require.True(t, updated.EndedAt.Equal(oldEnd.Add(5*time.Hour)))
}

func covRecordingIDs(recs []model.Recording) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.ID)
	}
	return out
}

// TestCovRecordingMutations covers update, lock and stream-id fallbacks.
func TestCovRecordingMutations(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := covTestTime(0)

	rec := covInsertRecording(t, db, "rec-1", "camA", base)
	require.Equal(t, "camA", recordingStreamID(rec))
	require.Equal(t, "custom", recordingStreamID(&model.Recording{StreamID: " custom ", CameraID: "cam"}))
	require.Equal(t, "cam", recordingStreamID(&model.Recording{StreamID: "   ", CameraID: "cam"}))
	require.Equal(t, "", recordingStreamID(nil))

	got, err := db.GetRecording(ctx, "rec-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, model.MergeStatusPending, got.MergeStatus)

	rec.StreamID = "custom/stream"
	rec.EndedAt = base.Add(2 * time.Minute)
	rec.Duration = 120
	rec.FileSize = 9999
	rec.ReconnectedAt = base.Add(time.Minute)
	rec.GapReason = "stream interrupted"
	rec.Merged = true
	rec.MergeStatus = model.MergeStatusMerged
	require.NoError(t, db.UpdateRecording(ctx, rec))

	got, err = db.GetRecording(ctx, "rec-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "custom/stream", got.StreamID)
	require.InDelta(t, 120.0, got.Duration, 0.001)
	require.Equal(t, int64(9999), got.FileSize)
	require.Equal(t, "stream interrupted", got.GapReason)
	require.True(t, got.ReconnectedAt.Equal(base.Add(time.Minute)))
	require.Equal(t, model.MergeStatusMerged, got.MergeStatus)

	// Update without reconnected-at stores NULL again.
	rec.ReconnectedAt = time.Time{}
	require.NoError(t, db.UpdateRecording(ctx, rec))
	got, err = db.GetRecording(ctx, "rec-1")
	require.NoError(t, err)
	require.True(t, got.ReconnectedAt.IsZero())

	require.NoError(t, db.SetRecordingLocked(ctx, "rec-1", true))
	got, err = db.GetRecording(ctx, "rec-1")
	require.NoError(t, err)
	require.True(t, got.Locked)
	require.NoError(t, db.SetRecordingLocked(ctx, "rec-1", false))
	got, err = db.GetRecording(ctx, "rec-1")
	require.NoError(t, err)
	require.False(t, got.Locked)
	require.ErrorIs(t, db.SetRecordingLocked(ctx, "missing", true), sql.ErrNoRows)

	require.NoError(t, db.SetMerged(ctx, "rec-1", false))
	got, err = db.GetRecording(ctx, "rec-1")
	require.NoError(t, err)
	require.False(t, got.Merged)
	require.Equal(t, model.MergeStatusPending, got.MergeStatus)

	// A NULL merge_status (legacy rows) still derives from the merged flag.
	var legacy model.Recording
	legacy.Merged = true
	require.NoError(t, scanRecording(&legacy, sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{}))
	require.Equal(t, model.MergeStatusMerged, legacy.MergeStatus)
	require.True(t, legacy.StartedAt.IsZero())
	legacy.Merged = false
	require.NoError(t, scanRecording(&legacy, sql.NullString{}, sql.NullString{}, sql.NullString{String: "custom", Valid: true}, sql.NullString{}))
	require.Equal(t, "custom", legacy.MergeStatus)
}

// TestCovRecordingBatchOps covers orphan insertion, path lookups and deletes.
func TestCovRecordingBatchOps(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := covTestTime(0)

	orphans := []*model.Recording{
		{ID: "orphan-1", CameraID: "camA", FilePath: "/rec/orphan-1.mp4", Format: model.FormatH264, StartedAt: base},
		{ID: "orphan-2", CameraID: "camB", StreamID: "custom/b", FilePath: "/rec/orphan-2.mp4", Format: model.FormatMJPEG, StartedAt: base.Add(time.Minute), Merged: true},
	}
	inserted, err := db.InsertOrphanRecordings(ctx, orphans)
	require.NoError(t, err)
	require.Equal(t, 2, inserted)

	// Re-inserting the same IDs is ignored.
	again, err := db.InsertOrphanRecordings(ctx, orphans)
	require.NoError(t, err)
	require.Zero(t, again)

	paths, err := db.GetRecordingsByPathSet(ctx, []string{"/rec/orphan-1.mp4", "/rec/missing.mp4"})
	require.NoError(t, err)
	require.True(t, paths["/rec/orphan-1.mp4"])
	require.False(t, paths["/rec/missing.mp4"])

	empty, err := db.GetRecordingsByPathSet(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)

	covInsertRecording(t, db, "camA-recent", "camA", base.Add(2*time.Hour))

	prior, err := db.GetRecording(ctx, "orphan-2")
	require.NoError(t, err)
	require.NotNil(t, prior)
	require.Equal(t, "custom/b", prior.StreamID)
	require.True(t, prior.Merged)

	deleted, err := db.DeleteRecordingsBatch(ctx, []string{"orphan-1", "does-not-exist"})
	require.NoError(t, err)
	require.Equal(t, []string{"orphan-1"}, deleted)
	gone, err := db.GetRecording(ctx, "orphan-1")
	require.NoError(t, err)
	require.Nil(t, gone)

	none, err := db.DeleteRecordingsBatch(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, none)

	removed, err := db.DeleteRecordingsByCamera(ctx, "camA")
	require.NoError(t, err)
	require.Equal(t, int64(1), removed)
	remaining, err := db.ListRecordings(ctx, model.RecordingFilter{CameraID: "camA"})
	require.NoError(t, err)
	require.Empty(t, remaining)

	require.NoError(t, db.DeleteRecording(ctx, "orphan-2"))
	gone, err = db.GetRecording(ctx, "orphan-2")
	require.NoError(t, err)
	require.Nil(t, gone)
}

// TestCovInsertRecordingWithRetry covers the non-busy fast-fail path and the
// single-attempt success path.
func TestCovInsertRecordingWithRetry(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := covTestTime(0)

	rec := &model.Recording{ID: "retry-1", CameraID: "camA", FilePath: "/rec/retry-1.mp4", Format: model.FormatH264, StartedAt: base}
	require.NoError(t, db.InsertRecordingWithRetry(ctx, rec, 3, time.Millisecond))
	got, err := db.GetRecording(ctx, "retry-1")
	require.NoError(t, err)
	require.NotNil(t, got)

	// Duplicate primary key is not a busy error: fail immediately without retrying.
	dup := &model.Recording{ID: "retry-1", CameraID: "camA", FilePath: "/rec/retry-1.mp4", Format: model.FormatH264, StartedAt: base}
	err = db.InsertRecordingWithRetry(ctx, dup, 5, time.Microsecond)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "failed after 5 attempts")
}
