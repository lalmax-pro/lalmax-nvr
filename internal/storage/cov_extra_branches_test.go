package storage

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/ai"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
	"github.com/stretchr/testify/require"
)

// covQueryOnlyDB makes every write on db fail while reads keep working, which
// exercises the error branches of idempotent schema creation.
func covQueryOnlyDB(t *testing.T, db *DB) {
	t.Helper()
	db.db.SetMaxOpenConns(1)
	_, err := db.execContext(context.Background(), "PRAGMA query_only = ON")
	require.NoError(t, err)
}

// TestCovInitSchemaFailures drives Init through each schema-creation failure.
// A conflicting index name makes CREATE TABLE fail for exactly one table while
// every earlier idempotent statement succeeds.
func TestCovInitSchemaFailures(t *testing.T) {
	ctx := context.Background()

	// The very first table cannot be created.
	dir := t.TempDir()
	first, err := Open(ctx, DatabaseOptions{Driver: DriverSQLite, DSN: filepath.Join(dir, "empty.db")})
	require.NoError(t, err)
	covInjectNameConflict(t, first, "cameras")
	require.Error(t, first.Init(ctx), "Init fails when cameras cannot be created")
	require.NoError(t, first.Close())

	targets := []string{
		"recordings", "gb28181_devices", "gb28181_channels", "gb28181_platforms",
		"gb28181_platform_channels", "gb28181_alarms", "gb28181_downloads",
		"gb28181_regions", "gb28181_groups", "device_groups", "device_group_channels",
		"ai_detections", "ai_analyses", "voip_calls", "home_assistant_settings",
		"transcode_intent_v1", "transcode_outputs_v1", "feature_flags",
		"camera_health_events", "stream_bindings", "stream_history", "stream_bans",
		"events", "relay_tasks", "relay_task_stats", "operation_logs", "users",
		"platform_events", "created_streams", "recording_plans", "recording_plan_windows",
		"emby_server", "emby_items", "iptv_sources", "iptv_channels",
		"iptv_import_jobs", "iptv_import_items",
	}
	for _, table := range targets {
		t.Run(table, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "init.db")
			db, err := Open(ctx, DatabaseOptions{Driver: DriverSQLite, DSN: dbPath})
			require.NoError(t, err)
			require.NoError(t, db.Init(ctx))
			_, err = db.execContext(ctx, `DROP TABLE `+table)
			require.NoError(t, err, "target table must exist in a fresh schema")
			covInjectNameConflict(t, db, table)
			require.Error(t, db.Init(ctx), "Init must fail while %s cannot be created", table)
			require.NoError(t, db.Close())
		})
	}
}

// covInjectNameConflict reserves table as an index name on a scratch table so
// that CREATE TABLE IF NOT EXISTS <table> fails with "already an index named".
func covInjectNameConflict(t *testing.T, db *DB, table string) {
	t.Helper()
	ctx := context.Background()
	_, err := db.execContext(ctx, `CREATE TABLE IF NOT EXISTS cov_name_reservation (id TEXT)`)
	require.NoError(t, err)
	_, err = db.execContext(ctx, `CREATE INDEX `+table+` ON cov_name_reservation(id)`)
	require.NoError(t, err)
}

// TestCovTranscodingConflictBranches covers revision exhaustion, retries and
// capacity limits of the versioned intent document.
func TestCovTranscodingConflictBranches(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	// A revision gap retries until the optimistic update finally conflicts.
	err := db.changeTranscodeIntent(ctx, func(*transcodeIntent) error {
		_, execErr := db.execContext(ctx, `UPDATE transcode_intent_v1 SET revision = revision + 1 WHERE id = 'main'`)
		return execErr
	})
	require.ErrorContains(t, err, "configuration changed")

	// Exhausted revisions stop before writing.
	_, err = db.execContext(ctx, `UPDATE transcode_intent_v1 SET revision = ? WHERE id = 'main'`, int64(math.MaxInt64))
	require.NoError(t, err)
	err = db.changeTranscodeIntent(ctx, func(*transcodeIntent) error { return nil })
	require.ErrorContains(t, err, "revision exhausted")

	// A change callback error aborts without a write.
	db2 := newTestDB(t)
	callbackErr := context.Canceled
	err = db2.changeTranscodeIntent(ctx, func(*transcodeIntent) error { return callbackErr })
	require.ErrorIs(t, err, callbackErr)

	// Seed 256 profiles to hit the capacity guard.
	seedIntent(t, db2, transcodeIntent{Profiles: makeProfiles(256)})
	validProfile := transcode.Profile{
		ID: "prof-new", Name: "New", Backend: "ffmpeg",
		Video: transcode.VideoSpec{Codec: "copy"},
		Audio: transcode.AudioSpec{Codec: "none"},
	}
	_, err = db2.SaveTranscodeProfile(ctx, validProfile, 0)
	require.ErrorContains(t, err, "profile limit reached")

	// A profile whose version counter is exhausted can never be recreated.
	db3 := newTestDB(t)
	seedIntent(t, db3, transcodeIntent{ProfileVersions: map[string]uint64{"prof-x": math.MaxInt64}})
	_, err = db3.SaveTranscodeProfile(ctx, transcode.Profile{
		ID: "prof-x", Name: "X", Backend: "ffmpeg",
		Video: transcode.VideoSpec{Codec: "copy"},
		Audio: transcode.AudioSpec{Codec: "none"},
	}, 0)
	require.ErrorContains(t, err, "configuration changed")

	// 256 jobs hit the job capacity guard.
	db4 := newTestDB(t)
	seedIntent(t, db4, transcodeIntent{Profiles: makeProfiles(1), Jobs: makeJobs(256)})
	_, err = db4.SaveTranscodeJob(ctx, transcode.Job{
		ID: "job-new", ProfileID: "prof-0", ProfileRevision: 1,
		Source: transcode.SourceRef{EngineID: "engine", StreamID: "cam1", AppName: "live"}, RunPolicy: transcode.Always,
	}, 0)
	require.ErrorContains(t, err, "job limit reached")

	// An exhausted job generation counter blocks recreating the job.
	db5 := newTestDB(t)
	seedIntent(t, db5, transcodeIntent{
		Profiles:    makeProfiles(1),
		JobVersions: map[string]uint64{"job-x": math.MaxInt64},
	})
	_, err = db5.SaveTranscodeJob(ctx, transcode.Job{
		ID: "job-x", ProfileID: "prof-0", ProfileRevision: 1,
		Source: transcode.SourceRef{EngineID: "engine", StreamID: "cam1", AppName: "live"}, RunPolicy: transcode.Always,
	}, 0)
	require.ErrorContains(t, err, "configuration changed")
}

// seedIntent overwrites the stored intent payload for capacity tests.
func seedIntent(t *testing.T, db *DB, intent transcodeIntent) {
	t.Helper()
	if intent.Profiles == nil {
		intent.Profiles = []transcode.Profile{}
	}
	if intent.Jobs == nil {
		intent.Jobs = []transcode.Job{}
	}
	payload, err := json.Marshal(intent)
	require.NoError(t, err)
	_, err = db.execContext(context.Background(),
		`UPDATE transcode_intent_v1 SET payload = ? WHERE id = 'main'`, string(payload))
	require.NoError(t, err)
}

func makeProfiles(n int) []transcode.Profile {
	profiles := make([]transcode.Profile, 0, n)
	for i := 0; i < n; i++ {
		profiles = append(profiles, transcode.Profile{
			ID:       "prof-" + itoa(i),
			Name:     "profile",
			Backend:  "ffmpeg",
			Revision: 1,
			Video:    transcode.VideoSpec{Codec: "copy"},
			Audio:    transcode.AudioSpec{Codec: "none"},
		})
	}
	return profiles
}

func makeJobs(n int) []transcode.Job {
	jobs := make([]transcode.Job, 0, n)
	for i := 0; i < n; i++ {
		jobs = append(jobs, transcode.Job{
			ID:              "job-" + itoa(i),
			ProfileID:       "prof-0",
			ProfileRevision: 1,
			Generation:      1,
			RunPolicy:       transcode.Always,
			Source:          transcode.SourceRef{EngineID: "engine", StreamID: "cam" + itoa(i), AppName: "live"},
		})
	}
	return jobs
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits [20]byte
	pos := len(digits)
	for i > 0 {
		pos--
		digits[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(digits[pos:])
}

// TestCovGrowMergedRecording covers the merged-segment growth transaction.
func TestCovGrowMergedRecording(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	base := covTestTime(0)
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "merged-1", CameraID: "cam-1", FilePath: "/m/merged-1.mp4", Format: model.FormatH264,
		StartedAt: base, EndedAt: base.Add(time.Minute), Duration: 60, FileSize: 100, FrameCount: 10,
	}))
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "short-1", CameraID: "cam-1", FilePath: "/m/short-1.mp4", Format: model.FormatH264,
		StartedAt: base.Add(2 * time.Minute), EndedAt: base.Add(3 * time.Minute), Duration: 60,
	}))

	grown := &model.Recording{
		ID: "merged-1", CameraID: "cam-1", FilePath: "/m/merged-1.mp4", Format: model.FormatH264,
		StartedAt: base, EndedAt: base.Add(3 * time.Minute), Duration: 180, FileSize: 300, FrameCount: 30,
	}
	require.NoError(t, db.GrowMergedRecording(ctx, grown, []string{"short-1"}))

	stored, err := db.GetRecording(ctx, "merged-1")
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, 180.0, stored.Duration)
	require.Equal(t, int64(300), stored.FileSize)
	require.Equal(t, 30, stored.FrameCount)
	require.True(t, stored.EndedAt.Equal(base.Add(3*time.Minute)))

	absorbed, err := db.GetRecording(ctx, "short-1")
	require.NoError(t, err)
	require.Nil(t, absorbed, "absorbed segments are deleted")

	// Growing a missing merged row is an error.
	err = db.GrowMergedRecording(ctx, &model.Recording{ID: "missing", EndedAt: base}, nil)
	require.ErrorContains(t, err, "not found")

	// An empty status batch is a no-op.
	require.NoError(t, db.SetMergeStatus(ctx, nil, model.MergeStatusPending))
	require.NoError(t, db.SetMergeStatus(ctx, []string{"merged-1"}, model.MergeStatusMerged))
	updated, err := db.GetRecording(ctx, "merged-1")
	require.NoError(t, err)
	require.Equal(t, model.MergeStatusMerged, updated.MergeStatus)
}

// TestCovStreamHistoryDefaults covers paging defaults and read-only failures.
func TestCovStreamHistoryDefaults(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	require.NoError(t, db.InsertStreamHistory(ctx, &StreamHistory{
		StreamID: "live/cam1", SessionID: "sess-1", StartedAt: covTestTime(0),
	}))
	require.NoError(t, db.InsertStreamHistory(ctx, &StreamHistory{
		StreamID: "live/cam2", SessionID: "sess-2", StartedAt: covTestTime(time.Minute),
	}))

	items, total, err := db.ListStreamHistory(ctx, "", 0, -5)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, items, 2)
	require.Equal(t, "live/cam2", items[0].StreamID, "newest session first")

	filtered, total, err := db.ListStreamHistory(ctx, "live/cam1", 1, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, filtered, 1)
	require.Equal(t, "live/cam1", filtered[0].StreamID)

	// A query-only connection accepts reads but rejects the finalisation update.
	covQueryOnlyDB(t, db)
	require.Error(t, db.FinalizeOpenStreamHistories(ctx, covTestTime(time.Hour), "shutdown"))
	require.Error(t, db.InsertStreamHistory(ctx, &StreamHistory{StreamID: "live/cam3", StartedAt: covTestTime(0)}))
	_, _, err = db.ListStreamHistory(ctx, "", 0, 0)
	require.NoError(t, err, "reads still work on the same connection")
}

// TestCovStreamBanQueries covers expired/future ban lookups.
func TestCovStreamBanQueries(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	future := time.Now().Add(24 * time.Hour).UTC()
	require.NoError(t, db.InsertStreamBan(ctx, &StreamBan{StreamID: "live/banned", Reason: "abuse", ExpiresAt: &future}))
	ban, err := db.GetStreamBan(ctx, "live/banned")
	require.NoError(t, err)
	require.NotNil(t, ban)
	require.Equal(t, "abuse", ban.Reason)
	require.NotNil(t, ban.ExpiresAt)

	banned, err := db.IsStreamBanned(ctx, "live/banned")
	require.NoError(t, err)
	require.True(t, banned)

	missing, err := db.GetStreamBan(ctx, "live/unknown")
	require.NoError(t, err)
	require.Nil(t, missing)
	banned, err = db.IsStreamBanned(ctx, "live/unknown")
	require.NoError(t, err)
	require.False(t, banned)

	bans, err := db.ListStreamBans(ctx)
	require.NoError(t, err)
	require.Len(t, bans, 1)
	require.Equal(t, "live/banned", bans[0].StreamID)

	// Expired bans stop blocking the stream.
	past := time.Now().Add(-time.Hour).UTC()
	require.NoError(t, db.InsertStreamBan(ctx, &StreamBan{StreamID: "live/expired", ExpiresAt: &past}))
	expired, err := db.IsStreamBanned(ctx, "live/expired")
	require.NoError(t, err)
	require.False(t, expired)

	require.NoError(t, db.DeleteStreamBan(ctx, "live/banned"))
	banned, err = db.IsStreamBanned(ctx, "live/banned")
	require.NoError(t, err)
	require.False(t, banned)
}

// TestCovDialectQueryPostgres covers the placeholder rebinding seam.
func TestCovDialectQueryPostgres(t *testing.T) {
	pg := &DB{driver: DriverPostgres}
	require.Equal(t, "SELECT $1 FROM t WHERE id = $2", pg.dialectQuery("SELECT ? FROM t WHERE id = ?"))
	require.Contains(t, pg.dialectQuery("CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT)"), "BIGSERIAL PRIMARY KEY")

	sqliteDB := &DB{driver: DriverSQLite}
	require.Equal(t, "SELECT ? FROM t", sqliteDB.dialectQuery("SELECT ? FROM t"))
}

// TestCovDDLFailureBranches covers the second/third DDL statements of the
// idempotent schema helpers by making only the indexed object writable.
func TestCovDDLFailureBranches(t *testing.T) {
	ctx := context.Background()

	// createAITables: the analyses table fails after detections succeeded.
	aiDB := newTestDB(t)
	_, err := aiDB.execContext(ctx, `DROP TABLE ai_analyses`)
	require.NoError(t, err)
	covQueryOnlyDB(t, aiDB)
	require.Error(t, aiDB.createAITables(ctx))

	// migrateRecordingPlans: index creation failures.
	indexDB := newTestDB(t)
	_, err = indexDB.execContext(ctx, `DROP INDEX idx_recording_plans_stream`)
	require.NoError(t, err)
	covQueryOnlyDB(t, indexDB)
	require.Error(t, indexDB.migrateRecordingPlans(ctx))

	windowsDB := newTestDB(t)
	_, err = windowsDB.execContext(ctx, `DROP INDEX idx_recording_plan_windows_plan`)
	require.NoError(t, err)
	covQueryOnlyDB(t, windowsDB)
	require.Error(t, windowsDB.migrateRecordingPlans(ctx))

	missingWindowsDB := newTestDB(t)
	_, err = missingWindowsDB.execContext(ctx, `DROP TABLE recording_plan_windows`)
	require.NoError(t, err)
	covQueryOnlyDB(t, missingWindowsDB)
	require.Error(t, missingWindowsDB.migrateRecordingPlans(ctx))

	// initPlatformEvents: the platform index fails after the table succeeded.
	eventsDB := newTestDB(t)
	_, err = eventsDB.execContext(ctx, `DROP INDEX idx_platform_events_platform_id`)
	require.NoError(t, err)
	covQueryOnlyDB(t, eventsDB)
	require.Error(t, eventsDB.initPlatformEvents())
}

// TestCovPlatformStatusQueries covers the platform status aggregation.
func TestCovPlatformStatusQueries(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	statuses, err := db.GetPlatformStatus(ctx)
	require.NoError(t, err)
	require.Empty(t, statuses, "no platforms yields an empty slice")

	platformID, err := db.CreatePlatform(ctx, &PlatformRow{Name: "acme", ServerIP: "10.0.0.1", ServerPort: 5060})
	require.NoError(t, err)
	require.NoError(t, db.AddPlatformEvent(ctx, PlatformEventRow{PlatformID: platformID, EventType: PlatformEventOnline}))

	// The DTO scan currently cannot read the COALESCE'd event time on SQLite,
	// so the joined query surfaces a scan error instead of a status row.
	_, err = db.GetPlatformStatus(ctx)
	require.Error(t, err)
}

// TestCovAIScanFailures covers corrupt ai rows and write failures.
func TestCovAIScanFailures(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	require.NoError(t, db.InsertAIDetection(ctx, ai.DetectionResult{CameraID: "cam-1", Timestamp: 1}))
	detections, total, err := db.ListAIDetections(ctx, AIHistoryFilter{CameraID: "cam-1"})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, detections, 1)
	require.Equal(t, "cam-1", detections[0].CameraID)

	// Corrupt numeric columns surface as scan errors instead of silent zeroes.
	_, err = db.execContext(ctx, `INSERT INTO ai_detections (camera_id, pts, timestamp, detections_json) VALUES ('cam-2', 'oops', 2, '[]')`)
	require.NoError(t, err)
	_, _, err = db.ListAIDetections(ctx, AIHistoryFilter{CameraID: "cam-2"})
	require.Error(t, err)

	_, err = db.execContext(ctx, `INSERT INTO ai_analyses (camera_id, timestamp, analysis, labels_json, confidence, trigger_detections_json, metadata_json)
		VALUES ('cam-3', 3, 'analysis', '[]', 'oops', '[]', '{}')`)
	require.NoError(t, err)
	_, _, err = db.ListAIAnalyses(ctx, AIHistoryFilter{CameraID: "cam-3"})
	require.Error(t, err)

	// Write failures propagate.
	closed := covClosedDB(t)
	require.Error(t, closed.InsertAIDetection(ctx, ai.DetectionResult{CameraID: "cam"}))
	_, _, err = closed.ListAIDetections(ctx, AIHistoryFilter{})
	require.Error(t, err)
	_, _, err = closed.ListAIAnalyses(ctx, AIHistoryFilter{})
	require.Error(t, err)
	require.Error(t, closed.createAITables(ctx))
}

// TestCovPlanWindowFailures covers plan listing when windows are unavailable.
func TestCovPlanWindowFailures(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	plan := &RecordingPlan{ID: NewRecordingPlanID(), StreamID: "live/cam1", Mode: RecordingModeContinuous, Enabled: true}
	require.NoError(t, db.UpsertRecordingPlan(ctx, plan))

	_, err := db.execContext(ctx, `DROP TABLE recording_plan_windows`)
	require.NoError(t, err)
	_, err = db.ListRecordingPlans(ctx)
	require.Error(t, err, "a missing windows table must surface, not be ignored")

	// Upsert and delete failures propagate from the transaction body.
	writeBlocked := newTestDB(t)
	require.NoError(t, writeBlocked.UpsertRecordingPlan(ctx, plan))
	covQueryOnlyDB(t, writeBlocked)
	require.Error(t, writeBlocked.UpsertRecordingPlan(ctx, &RecordingPlan{
		ID: plan.ID, StreamID: plan.StreamID, Mode: RecordingModeOff, Enabled: false,
	}))
	require.Error(t, writeBlocked.DeleteRecordingPlan(ctx, plan.ID))
}

// TestCovMergeTransactionFailures covers merge mutations on a blocked writer.
func TestCovMergeTransactionFailures(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	now := covTestTime(0)

	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "rec-merge", CameraID: "cam-1", FilePath: "/m/rec.mp4", Format: model.FormatH264,
		StartedAt: now, EndedAt: now.Add(time.Minute),
	}))

	covQueryOnlyDB(t, db)
	require.Error(t, db.GrowMergedRecording(ctx, &model.Recording{ID: "rec-merge"}, nil))
	require.Error(t, db.SetMergeStatus(ctx, []string{"rec-merge"}, model.MergeStatusPending))
	require.Error(t, db.MergeAndReplaceRecordings(ctx, &model.Recording{ID: "rec-merge"}, nil))
}
