package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

var logger = slog.Default().With("component", "storage")

// escapeLike escapes LIKE special characters (% and _) with backslash.
// This prevents SQL injection via LIKE wildcards while allowing literal searches.
// Must be used with ESCAPE '\\' clause in the SQL query.
func escapeLike(input string) string {
	escaped := strings.ReplaceAll(input, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "%", "\\%")
	escaped = strings.ReplaceAll(escaped, "_", "\\_")
	return escaped
}

type DB struct {
	path string
	db   *sql.DB
}

// DB returns the underlying *sql.DB for advanced queries.
func (d *DB) DB() *sql.DB {
	return d.db
}

func New(dbPath string) (*DB, error) {
	dsn := dbPath
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Set pragmas on open
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA synchronous=NORMAL;"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000;"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA cache_size=-2000;"); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{path: dbPath, db: db}, nil
}

func (d *DB) Init(ctx context.Context) error {
	// create tables if not exist
	camSQL := `CREATE TABLE IF NOT EXISTS cameras (
        id TEXT PRIMARY KEY,
        name TEXT NOT NULL,
        protocol TEXT NOT NULL,
        encoding TEXT NOT NULL DEFAULT '',
        rtsp_transport TEXT NOT NULL DEFAULT 'tcp',
        url TEXT NOT NULL,
        username TEXT DEFAULT '',
        password TEXT DEFAULT '',
        enabled INTEGER DEFAULT 1,
        description TEXT DEFAULT '',
        location TEXT DEFAULT '',
        brand TEXT DEFAULT '',
        model TEXT DEFAULT '',
        serial_number TEXT DEFAULT '',
        created_at DATETIME DEFAULT CURRENT_TIMESTAMP
    );`
	recSQL := `CREATE TABLE IF NOT EXISTS recordings (
        id TEXT PRIMARY KEY,
        camera_id TEXT NOT NULL,
        file_path TEXT NOT NULL,
        format TEXT NOT NULL,
        started_at DATETIME NOT NULL,
        ended_at DATETIME,
        duration REAL,
        file_size INTEGER DEFAULT 0,
        frame_count INTEGER DEFAULT 0,
        merged INTEGER DEFAULT 0,
        created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
        FOREIGN KEY (camera_id) REFERENCES cameras(id)
    );`
	if _, err := d.db.ExecContext(ctx, camSQL); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, recSQL); err != nil {
		return err
	}
	// GB28181 tables
	if err := d.createGB28181Tables(ctx); err != nil {
		return err
	}
	// GB28181 platform tables
	if err := d.createPlatformTables(ctx); err != nil {
		return err
	}
	// GB28181 alarm table
	if err := d.createAlarmTable(ctx); err != nil {
		return err
	}
	// GB28181 download table
	if err := d.createDownloadTable(ctx); err != nil {
		return err
	}
	// Device group tables
	if err := d.createGroupTables(ctx); err != nil {
		return err
	}
	// GB28181 行政区划 / 业务分组 目录表
	if err := d.createGBRegionTables(ctx); err != nil {
		return err
	}
	if err := d.createGBGroupTables(ctx); err != nil {
		return err
	}
	if err := d.createAITables(ctx); err != nil {
		return err
	}
	// indices
	idx1 := `CREATE INDEX IF NOT EXISTS idx_recordings_camera ON recordings(camera_id);`
	idx2 := `CREATE INDEX IF NOT EXISTS idx_recordings_time ON recordings(started_at);`
	// idx3 created after migration (merged column may not exist in older DBs)
	if _, err := d.db.ExecContext(ctx, idx1); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, idx2); err != nil {
		return err
	}
	// schema metadata
	metaSQL := `CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);`
	if _, err := d.db.ExecContext(ctx, metaSQL); err != nil {
		return err
	}
	_, _ = d.db.ExecContext(ctx, "INSERT OR IGNORE INTO schema_meta (key, value) VALUES ('schema_version', '2');")
	// Migration v1 → v2: add camera metadata columns
	var version string
	if err := d.db.QueryRowContext(ctx, "SELECT value FROM schema_meta WHERE key='schema_version'").Scan(&version); err == nil && version == "1" {
		columns := []string{
			"ALTER TABLE cameras ADD COLUMN description TEXT DEFAULT ''",
			"ALTER TABLE cameras ADD COLUMN location TEXT DEFAULT ''",
			"ALTER TABLE cameras ADD COLUMN brand TEXT DEFAULT ''",
			"ALTER TABLE cameras ADD COLUMN model TEXT DEFAULT ''",
			"ALTER TABLE cameras ADD COLUMN serial_number TEXT DEFAULT ''",
		}
		for _, col := range columns {
			_, _ = d.db.ExecContext(ctx, col) // ignore error if column already exists
		}
		_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='2' WHERE key='schema_version'")
	}
	// Migration v2 → v3: add per-camera retention_days
	if version == "2" {
		_, _ = d.db.ExecContext(ctx, "ALTER TABLE cameras ADD COLUMN retention_days INTEGER DEFAULT 0")
		_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='3' WHERE key='schema_version'")
	}
	// Migration v3 → v4: pinned → merged
	if version == "3" || version == "2" {
		// Check if pinned column exists
		var pinnedExists int
		_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='pinned'`).Scan(&pinnedExists)
		if pinnedExists > 0 {
			_, _ = d.db.ExecContext(ctx, "ALTER TABLE recordings ADD COLUMN merged INTEGER DEFAULT 0")
			_, _ = d.db.ExecContext(ctx, "UPDATE recordings SET merged = pinned")
			_, _ = d.db.ExecContext(ctx, "ALTER TABLE recordings DROP COLUMN pinned")
			_, _ = d.db.ExecContext(ctx, "DROP INDEX IF EXISTS idx_recordings_pinned")
			_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_recordings_merged ON recordings(merged)")
		} else {
			// Fresh install or already migrated — just ensure merged column exists
			_, _ = d.db.ExecContext(ctx, "ALTER TABLE recordings ADD COLUMN merged INTEGER DEFAULT 0")
			_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_recordings_merged ON recordings(merged)")
		}
		_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='4' WHERE key='schema_version'")
	}
	// Migration v4 → v5: add per-camera merge config columns
	var mergeColExists int
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='merge_enabled'`).Scan(&mergeColExists)
	if mergeColExists == 0 {
		mergeColumns := []string{
			`ALTER TABLE cameras ADD COLUMN merge_enabled INTEGER`,
			`ALTER TABLE cameras ADD COLUMN merge_check_interval TEXT`,
			`ALTER TABLE cameras ADD COLUMN merge_window_size TEXT`,
			`ALTER TABLE cameras ADD COLUMN merge_batch_limit INTEGER`,
			`ALTER TABLE cameras ADD COLUMN merge_min_segment_age TEXT`,
			`ALTER TABLE cameras ADD COLUMN merge_min_segments_to_merge INTEGER`,
		}
		for _, col := range mergeColumns {
			_, _ = d.db.ExecContext(ctx, col)
		}
	}
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='5' WHERE key='schema_version'")
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_recordings_merged ON recordings(merged)")
	// Migration v5 → v6: add ONVIF columns
	var onvifColExists int
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='onvif_endpoint'`).Scan(&onvifColExists)
	if onvifColExists == 0 {
		_, _ = d.db.ExecContext(ctx, "ALTER TABLE cameras ADD COLUMN onvif_endpoint TEXT DEFAULT ''")
		_, _ = d.db.ExecContext(ctx, "ALTER TABLE cameras ADD COLUMN profile_token TEXT DEFAULT ''")
	}
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='6' WHERE key='schema_version'")
	// Migration v6 → v7: add stream_encoding column
	var streamEncColExists int
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='stream_encoding'`).Scan(&streamEncColExists)
	if streamEncColExists == 0 {
		_, _ = d.db.ExecContext(ctx, "ALTER TABLE cameras ADD COLUMN stream_encoding TEXT DEFAULT ''")
	}
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='7' WHERE key='schema_version'")
	// Migration v7 → v8: add archive columns
	var archivedColExists int
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='archived'`).Scan(&archivedColExists)
	if archivedColExists == 0 {
		_, _ = d.db.ExecContext(ctx, "ALTER TABLE cameras ADD COLUMN archived INTEGER DEFAULT 0")
		_, _ = d.db.ExecContext(ctx, "ALTER TABLE cameras ADD COLUMN archived_at DATETIME DEFAULT NULL")
		_, _ = d.db.ExecContext(ctx, "ALTER TABLE cameras ADD COLUMN archive_retention_days INTEGER DEFAULT 0")
		_, _ = d.db.ExecContext(ctx, "ALTER TABLE recordings ADD COLUMN archived INTEGER DEFAULT 0")
		_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_cameras_archived ON cameras(archived)")
		_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_recordings_archived ON recordings(archived)")
	}
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='8' WHERE key='schema_version'")
	// Migration v8 → v9: feature_flags table for protocol toggles
	featSQL := `CREATE TABLE IF NOT EXISTS feature_flags (
		key TEXT PRIMARY KEY,
		value BOOLEAN NOT NULL DEFAULT FALSE,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := d.db.ExecContext(ctx, featSQL); err != nil {
		return err
	}
	// Insert default protocol toggles if they don't exist
	_, _ = d.db.ExecContext(ctx, `INSERT OR IGNORE INTO feature_flags (key, value) VALUES
		('protocol.xiaomi', 1),
		('protocol.rtsp', 1),
		('protocol.http', 1),
		('protocol.onvif', 1);`)
	// Migration v8 → v9: compound index for ListRecordings query pattern
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_recordings_camera_time ON recordings(camera_id, started_at, ended_at, archived)")
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='9' WHERE key='schema_version'")

	// Migration v9 → v10: camera_health_events table
	healthSQL := `CREATE TABLE IF NOT EXISTS camera_health_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		camera_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		status TEXT NOT NULL,
		message TEXT DEFAULT '',
		metadata TEXT DEFAULT '{}',
		created_at TEXT DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
	);`
	if _, err := d.db.ExecContext(ctx, healthSQL); err != nil {
		return err
	}
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_health_events_camera_id ON camera_health_events(camera_id)")
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_health_events_created_at ON camera_health_events(created_at)")
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='10' WHERE key='schema_version'")

	// Migration v10 → v12: transcoding_tasks table (transcoding feature removed;
	// migration retained only to keep schema_version monotonic for existing DBs).
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='12' WHERE key='schema_version'")

	// Migration: add encoding column if missing
	d.db.Exec("ALTER TABLE cameras ADD COLUMN encoding TEXT NOT NULL DEFAULT ''")
	// Migration: add rtsp_transport column if missing
	d.db.Exec("ALTER TABLE cameras ADD COLUMN rtsp_transport TEXT NOT NULL DEFAULT 'tcp'")
	// Migration: normalize legacy protocol values + populate encoding
	// Migration v12 → v13: add merge_status TEXT column to recordings
	var mergeStatusColExists int
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='merge_status'`).Scan(&mergeStatusColExists)
	if mergeStatusColExists == 0 {
		_, _ = d.db.ExecContext(ctx, `ALTER TABLE recordings ADD COLUMN merge_status TEXT NOT NULL DEFAULT 'pending'`)
		// Backfill: merged=1 → 'merged', merged=0 → 'pending'
		_, _ = d.db.ExecContext(ctx, `UPDATE recordings SET merge_status = 'merged' WHERE merged = 1`)
		_, _ = d.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_recordings_merge_status ON recordings(merge_status)`)
	}
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='13' WHERE key='schema_version'")

	// Migration: normalize legacy protocol values + populate encoding
	d.migrateEncodings()

	// Migration v13 → v14: (transcoding_tasks framerate column — transcoding feature
	// removed; version bump retained to keep schema_version monotonic).
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='14' WHERE key='schema_version'")

	// Migration v14 → v15: stream_bindings table for stream-camera binding
	streamBindingsSQL := `CREATE TABLE IF NOT EXISTS stream_bindings (
		stream_id TEXT NOT NULL,
		camera_id TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (stream_id),
		FOREIGN KEY (camera_id) REFERENCES cameras(id)
	);`
	if _, err := d.db.ExecContext(ctx, streamBindingsSQL); err != nil {
		return err
	}
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_stream_bindings_camera ON stream_bindings(camera_id)")
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='15' WHERE key='schema_version'")

	// Migration v15 → v16: add reconnection tracking columns to recordings
	var reconnectedAtColExists int
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='reconnected_at'`).Scan(&reconnectedAtColExists)
	if reconnectedAtColExists == 0 {
		_, _ = d.db.ExecContext(ctx, `ALTER TABLE recordings ADD COLUMN reconnected_at DATETIME`)
		_, _ = d.db.ExecContext(ctx, `ALTER TABLE recordings ADD COLUMN gap_reason TEXT DEFAULT ''`)
	}
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='16' WHERE key='schema_version'")

	// Migration v16 → v17: stream_history and stream_bans tables
	streamHistorySQL := `CREATE TABLE IF NOT EXISTS stream_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		stream_id TEXT NOT NULL,
		app_name TEXT DEFAULT 'live',
		protocol TEXT DEFAULT '',
		remote_addr TEXT DEFAULT '',
		session_id TEXT DEFAULT '',
		started_at DATETIME NOT NULL,
		ended_at DATETIME,
		duration_sec REAL DEFAULT 0,
		bytes_read INTEGER DEFAULT 0,
		bytes_written INTEGER DEFAULT 0
	);`
	if _, err := d.db.ExecContext(ctx, streamHistorySQL); err != nil {
		return err
	}
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_stream_history_stream ON stream_history(stream_id)")
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_stream_history_started ON stream_history(started_at)")
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_stream_history_session ON stream_history(session_id)")

	streamBansSQL := `CREATE TABLE IF NOT EXISTS stream_bans (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		stream_id TEXT NOT NULL UNIQUE,
		reason TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME
	);`
	if _, err := d.db.ExecContext(ctx, streamBansSQL); err != nil {
		return err
	}
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_stream_bans_stream ON stream_bans(stream_id)")
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='17' WHERE key='schema_version'")

	// Migration v17 → v18: per-camera extras JSON (timelapse, transcoding, etc.)
	var extrasColExists int
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='extras_json'`).Scan(&extrasColExists)
	if extrasColExists == 0 {
		_, _ = d.db.ExecContext(ctx, `ALTER TABLE cameras ADD COLUMN extras_json TEXT DEFAULT ''`)
	}
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='18' WHERE key='schema_version'")

	// Migration v18 → v19: unified NVR event center.
	eventsSQL := `CREATE TABLE IF NOT EXISTS events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		camera_id TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL,
		type TEXT NOT NULL,
		severity TEXT NOT NULL DEFAULT 'info',
		status TEXT NOT NULL DEFAULT 'open',
		message TEXT DEFAULT '',
		metadata TEXT DEFAULT '{}',
		recording_id TEXT DEFAULT '',
		snapshot_path TEXT DEFAULT '',
		started_at DATETIME NOT NULL,
		ended_at DATETIME,
		acknowledged_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := d.db.ExecContext(ctx, eventsSQL); err != nil {
		return err
	}
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_events_camera_time ON events(camera_id, started_at DESC)")
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_events_source_type ON events(source, type)")
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_events_status ON events(status)")
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_events_recording ON events(recording_id)")
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='19' WHERE key='schema_version'")

	// Migration v19 → v20: users table for RBAC
	if err := d.CreateUserTable(ctx); err != nil {
		return err
	}
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='20' WHERE key='schema_version'")

	// Migration v20 → v21: platform events for cascade history
	if err := d.initPlatformEvents(); err != nil {
		return err
	}
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='21' WHERE key='schema_version'")

	// Migration v21 → v22: add profile_name column
	var profileNameColExists int
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='profile_name'`).Scan(&profileNameColExists)
	if profileNameColExists == 0 {
		_, _ = d.db.ExecContext(ctx, "ALTER TABLE cameras ADD COLUMN profile_name TEXT DEFAULT ''")
	}
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='22' WHERE key='schema_version'")

	// Migration v22 → v23: per-camera recording_mode + schedules, migrate recording plans.
	if err := d.migrateRecordingMode(ctx); err != nil {
		return err
	}
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='23' WHERE key='schema_version'")

	// Migration v23 → v24: relay_tasks table for relay push tasks
	relayTasksSQL := `CREATE TABLE IF NOT EXISTS relay_tasks (
		id TEXT PRIMARY KEY,
		stream_id TEXT NOT NULL,
		target_url TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'stopped',
		error_msg TEXT DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		started_at DATETIME,
		stopped_at DATETIME
	);`
	if _, err := d.db.ExecContext(ctx, relayTasksSQL); err != nil {
		return err
	}
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_relay_tasks_stream ON relay_tasks(stream_id)")
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_relay_tasks_status ON relay_tasks(status)")

	// relay_task_stats table for historical statistics
	relayStatsSQL := `CREATE TABLE IF NOT EXISTS relay_task_stats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id TEXT NOT NULL,
		timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		video_fps REAL DEFAULT 0,
		video_bitrate INTEGER DEFAULT 0,
		audio_fps REAL DEFAULT 0,
		audio_bitrate INTEGER DEFAULT 0,
		total_bytes INTEGER DEFAULT 0,
		FOREIGN KEY (task_id) REFERENCES relay_tasks(id) ON DELETE CASCADE
	);`
	if _, err := d.db.ExecContext(ctx, relayStatsSQL); err != nil {
		return err
	}
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_relay_stats_task ON relay_task_stats(task_id)")
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_relay_stats_time ON relay_task_stats(timestamp)")
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='24' WHERE key='schema_version'")

	// Migration v24 → v25: operation logs for user and system audit trails.
	operationLogsSQL := `CREATE TABLE IF NOT EXISTS operation_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER,
		username TEXT NOT NULL DEFAULT '',
		actor_type TEXT NOT NULL DEFAULT 'system',
		action TEXT NOT NULL,
		resource TEXT NOT NULL DEFAULT '',
		resource_id TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'success',
		message TEXT NOT NULL DEFAULT '',
		metadata TEXT NOT NULL DEFAULT '{}',
		ip_address TEXT NOT NULL DEFAULT '',
		user_agent TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := d.db.ExecContext(ctx, operationLogsSQL); err != nil {
		return err
	}
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_operation_logs_created_at ON operation_logs(created_at DESC)")
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_operation_logs_action ON operation_logs(action)")
	_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_operation_logs_username ON operation_logs(username)")
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='25' WHERE key='schema_version'")

	return nil

}
func (d *DB) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

func (d *DB) migrateEncodings() {
	rows, err := d.db.Query("SELECT id, protocol FROM cameras WHERE encoding = ''")
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, protocol string
		if err := rows.Scan(&id, &protocol); err != nil {
			continue
		}
		proto, enc, err := model.ParseLegacyProtocol(protocol)
		if err != nil {
			continue
		}
		// Only update if protocol actually changed (was a combined format)
		if proto != protocol {
			d.db.Exec("UPDATE cameras SET protocol = ?, encoding = ? WHERE id = ?", proto, enc, id)
		} else {
			// Same protocol (onvif or already normalized) — just set encoding if available
			if enc != "" {
				d.db.Exec("UPDATE cameras SET encoding = ? WHERE id = ?", enc, id)
			}
		}
	}
}

// Backup creates a backup of the database using VACUUM INTO.
func (d *DB) Backup(ctx context.Context, destPath string) error {
	_, err := d.db.ExecContext(ctx, "VACUUM INTO ?", destPath)
	return err
}

// sqliteTimeFormat is the format used to store timestamps in SQLite.
// Uses UTC without timezone suffix, compatible with SQLite's datetime() for string comparison.
const sqliteTimeFormat = "2006-01-02 15:04:05.999999999"

// timeToDB converts time.Time to a SQLite-compatible string value.
// Returns nil for zero time (which SQLite stores as NULL).
func timeToDB(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(sqliteTimeFormat)
}

// formatTime formats a time.Time as a SQLite-compatible UTC string.
// Returns empty string for zero time.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(sqliteTimeFormat)
}

// parseTime parses a SQLite timestamp string back into time.Time (UTC).
// Supports multiple formats for backward compatibility with legacy data.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	// Canonical format (our new format)
	if t, err := time.Parse(sqliteTimeFormat, s); err == nil {
		return t, nil
	}
	// Without fractional seconds (SQLite CURRENT_TIMESTAMP)
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t, nil
	}
	// RFC3339 variants
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	// Legacy Go time.Time.String() format with monotonic clock:
	// "2006-01-02 15:04:05.999999999 -0700 MST m=+123.456"
	cleaned := s
	if idx := strings.Index(cleaned, " m=+"); idx != -1 {
		cleaned = cleaned[:idx]
	}
	// Strip timezone name (e.g., "CST") after offset: "+0800 CST" → "+0800"
	fields := strings.Fields(cleaned)
	if len(fields) >= 4 && len(fields[2]) == 5 && (fields[2][0] == '+' || fields[2][0] == '-') {
		cleaned = fields[0] + " " + fields[1] + " " + fields[2]
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999 -0700",
		"2006-01-02 15:04:05 -0700",
	} {
		if t, err := time.Parse(layout, cleaned); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse time: %q", s)
}

// scanTime converts a sql.NullString to time.Time using parseTime.
// Returns zero time for NULL or empty values.
func scanTime(ns sql.NullString) time.Time {
	if !ns.Valid || ns.String == "" {
		return time.Time{}
	}
	t, err := parseTime(ns.String)
	if err != nil {
		logger.Warn("scanTime: failed to parse time string", "value", ns.String, "error", err)
		return time.Time{}
	}
	return t
}

// Nullable helper functions for per-camera merge config.

func nullBoolToPtr(v sql.NullBool) *bool {
	if !v.Valid {
		return nil
	}
	b := v.Bool
	return &b
}

func nullStringToPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func nullInt64ToPtr(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	i := int(v.Int64)
	return &i
}

func ptrToNullBool(v *bool) sql.NullBool {
	if v == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Valid: true, Bool: *v}
}

func ptrToNullString(v *string) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	return sql.NullString{Valid: true, String: *v}
}

func ptrToNullInt64(v *int) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Valid: true, Int64: int64(*v)}
}
