package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

func covSeedBackupSource(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "cam-1", "Front door", "rtsp", "h264", "rtsp://cam-1", "user", "pass", true, "", "", ""))
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "rec-1", CameraID: "cam-1", StreamID: "live/cam-1", FilePath: "/r/rec-1.mp4",
		Format: model.FormatH264, StartedAt: covTestTime(0), EndedAt: covTestTime(time.Minute),
		Duration: 60, FileSize: 1024, FrameCount: 30,
	}))
	_, err := db.InsertEvent(ctx, model.Event{CameraID: "cam-1", Source: "mqtt", Type: "motion", Message: "hello"})
	require.NoError(t, err)
	require.NoError(t, db.InsertStreamHistory(ctx, &StreamHistory{StreamID: "live/cam-1", SessionID: "sess", StartedAt: covTestTime(0)}))
}

// TestCovBackupAsSQLite covers the SQLite→SQLite copy used for database backups.
func TestCovBackupAsSQLite(t *testing.T) {
	ctx := context.Background()
	src := newTestDB(t)
	covSeedBackupSource(t, src)

	destPath := filepath.Join(t.TempDir(), "backup.db")
	require.NoError(t, src.backupAsSQLite(ctx, destPath))

	dst, err := New(destPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dst.Close() })
	require.NoError(t, dst.Init(ctx))

	cameras, err := dst.ListCameras(ctx)
	require.NoError(t, err)
	require.Len(t, cameras, 1)
	require.Equal(t, "Front door", cameras[0].Name)

	stored, err := dst.GetRecording(ctx, "rec-1")
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, int64(1024), stored.FileSize)
	require.Equal(t, "live/cam-1", stored.StreamID)

	events, total, err := dst.ListEvents(ctx, EventsFilter{})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, events, 1)

	history, totalHistory, err := dst.ListStreamHistory(ctx, "live/cam-1", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 1, totalHistory)
	require.Len(t, history, 1)

	// Feature flags carry BOOLEAN values that must survive the copy.
	var flags int
	require.NoError(t, dst.queryRowContext(ctx, `SELECT COUNT(*) FROM feature_flags`).Scan(&flags))
	var srcFlags int
	require.NoError(t, src.queryRowContext(ctx, `SELECT COUNT(*) FROM feature_flags`).Scan(&srcFlags))
	require.Equal(t, srcFlags, flags)

	// Validation errors.
	require.ErrorContains(t, src.backupAsSQLite(ctx, ""), "must not be empty")
	require.ErrorContains(t, src.backupAsSQLite(ctx, destPath), "already exists")

	// A destination in a missing directory cannot be created.
	require.Error(t, src.backupAsSQLite(ctx, filepath.Join(t.TempDir(), "missing", "sub", "backup.db")))

	// The source connection must be usable.
	closed := newTestDB(t)
	require.NoError(t, closed.Close())
	require.Error(t, closed.backupAsSQLite(ctx, filepath.Join(t.TempDir(), "closed.db")))
	_, statErr := os.Stat(filepath.Join(t.TempDir(), "closed.db"))
	require.True(t, os.IsNotExist(statErr), "failed backups remove their partial file")
}

// TestCovMigrationCopyHelpers covers the copy engine primitives with two
// SQLite databases; the SQL dialects are covered by the unit tests in
// cov_dialect_db_test.go.
func TestCovMigrationCopyHelpers(t *testing.T) {
	ctx := context.Background()
	src := newTestDB(t)
	covSeedBackupSource(t, src)
	dst := newTestDB(t)

	tables, err := listDatabaseTables(ctx, src)
	require.NoError(t, err)
	require.Contains(t, tables, "cameras")
	require.NotContains(t, tables, "sqlite_sequence")

	direct, err := listTables(ctx, src.db, DriverSQLite)
	require.NoError(t, err)
	require.Equal(t, tables, direct)

	_, err = listTables(ctx, src.db, DatabaseDriver("oracle"))
	require.ErrorContains(t, err, "unsupported driver")

	destTables, err := listDatabaseTables(ctx, dst)
	require.NoError(t, err)
	require.NotEmpty(t, destTables)

	columns, types, err := tableColumns(ctx, src, "cameras")
	require.NoError(t, err)
	require.Contains(t, columns, "id")
	require.Equal(t, "TEXT", types["id"])

	_, _, err = tableColumns(ctx, src, "bad-table;drop")
	require.ErrorContains(t, err, "unsafe")

	quoted, err := migrationQuoteIdentifier("cameras", DriverSQLite)
	require.NoError(t, err)
	require.Equal(t, `"cameras"`, quoted)
	quoted, err = migrationQuoteIdentifier("cameras", DriverMySQL)
	require.NoError(t, err)
	require.Equal(t, "`cameras`", quoted)
	_, err = migrationQuoteIdentifier("1bad", DriverSQLite)
	require.ErrorContains(t, err, "unsafe")

	require.Equal(t, migrationIdentity("sqlite://a", DriverSQLite), migrationIdentity("sqlite://a", DriverSQLite))
	require.NotEqual(t, migrationIdentity("sqlite://a", DriverSQLite), migrationIdentity("sqlite://b", DriverSQLite))
	require.NotEqual(t, migrationIdentity("sqlite://a", DriverSQLite), migrationIdentity("sqlite://a", DriverPostgres))

	// Clearing removes every row from the destination.
	require.NoError(t, clearDatabaseTables(ctx, dst.db, DriverSQLite, destTables))
	for _, table := range destTables {
		count, err := countTable(ctx, DriverSQLite, dst.db, table)
		require.NoError(t, err)
		require.Zero(t, count, "%s must be empty after clearing", table)
	}

	// Copy each interesting table and compare counts.
	copied, err := copyTable(ctx, src, DriverSQLite, dst.db, "cameras")
	require.NoError(t, err)
	require.Equal(t, int64(1), copied)
	copied, err = copyTable(ctx, src, DriverSQLite, dst.db, "recordings")
	require.NoError(t, err)
	require.Equal(t, int64(1), copied)
	copied, err = copyTable(ctx, src, DriverSQLite, dst.db, "events")
	require.NoError(t, err)
	require.Equal(t, int64(1), copied)
	copied, err = copyTable(ctx, src, DriverSQLite, dst.db, "feature_flags")
	require.NoError(t, err)
	require.Positive(t, copied)

	count, err := countTable(ctx, DriverSQLite, dst.db, "recordings")
	require.NoError(t, err)
	require.Equal(t, int64(1), count)

	stored, err := dst.GetRecording(ctx, "rec-1")
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, int64(1024), stored.FileSize)

	copied, err = copyTable(ctx, src, DriverSQLite, dst.db, "does_not_exist")
	require.NoError(t, err)
	require.Zero(t, copied, "a table without columns copies nothing")
	_, err = countTable(ctx, DriverSQLite, dst.db, "bad table")
	require.ErrorContains(t, err, "unsafe")

	// Resumable-target detection.
	_, err = isResumableMigrationTarget(ctx, dst, destTables, []string{migrationMarkerTable}, "id")
	require.ErrorContains(t, err, "reserved table")

	resumable, err := isResumableMigrationTarget(ctx, dst, []string{"cameras"}, []string{"cameras"}, "id")
	require.NoError(t, err)
	require.False(t, resumable, "a target without the marker table is not resumable")

	_, err = dst.execContext(ctx, `CREATE TABLE `+migrationMarkerTable+` (migration_id TEXT NOT NULL)`)
	require.NoError(t, err)
	existing, err := listDatabaseTables(ctx, dst)
	require.NoError(t, err)
	require.Contains(t, existing, migrationMarkerTable)

	// The destination table set (minus the marker) must match the source set.
	var sourceOnly []string
	for _, table := range existing {
		if table != migrationMarkerTable {
			sourceOnly = append(sourceOnly, table)
		}
	}
	resumable, err = isResumableMigrationTarget(ctx, dst, existing, sourceOnly, "id")
	require.NoError(t, err)
	require.False(t, resumable, "an empty marker table is not resumable")

	_, err = dst.execContext(ctx, `INSERT INTO `+migrationMarkerTable+` (migration_id) VALUES ('other-id')`)
	require.NoError(t, err)
	resumable, err = isResumableMigrationTarget(ctx, dst, existing, sourceOnly, "id")
	require.NoError(t, err)
	require.False(t, resumable, "a different migration id is not resumable")

	_, err = dst.execContext(ctx, `UPDATE `+migrationMarkerTable+` SET migration_id = 'id'`)
	require.NoError(t, err)
	resumable, err = isResumableMigrationTarget(ctx, dst, existing, sourceOnly, "id")
	require.NoError(t, err)
	require.True(t, resumable)

	resumable, err = isResumableMigrationTarget(ctx, dst, existing, []string{"cameras"}, "id")
	require.NoError(t, err)
	require.False(t, resumable, "destination tables missing from the source abort resumption")
}

// TestCovSQLiteBool covers the boolean conversion used while copying.
func TestCovSQLiteBool(t *testing.T) {
	require.Equal(t, true, sqliteBool(int64(1)))
	require.Equal(t, false, sqliteBool(int64(0)))
	require.Equal(t, true, sqliteBool(1))
	require.Equal(t, false, sqliteBool(0))
	require.Equal(t, true, sqliteBool([]byte("1")))
	require.Equal(t, true, sqliteBool([]byte("TRUE")))
	require.Equal(t, false, sqliteBool([]byte("0")))
	require.Equal(t, true, sqliteBool("true"))
	require.Equal(t, false, sqliteBool("0"))
	require.Equal(t, false, sqliteBool("text"))
	require.Nil(t, sqliteBool(nil))
}

// TestCovMigrateToValidation covers the destination guard rails.
func TestCovMigrateToValidation(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	_, err := db.MigrateTo(ctx, DriverSQLite, filepath.Join(t.TempDir(), "copy.db"), nil)
	require.ErrorContains(t, err, "must be postgres or mysql")

	_, err = db.MigrateTo(ctx, DriverPostgres, "", nil)
	require.ErrorContains(t, err, "DSN must not be empty")

	_, err = db.MigrateTo(ctx, DriverMySQL, "", nil)
	require.ErrorContains(t, err, "DSN must not be empty")

	// Unreachable destinations fail while connecting.
	_, err = db.MigrateTo(ctx, DriverPostgres, "postgres://127.0.0.1:1/none?sslmode=disable", nil)
	require.Error(t, err)

	// The source database stays active after a failed migration.
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "rec-after", CameraID: "cam-1", FilePath: "/r/after.mp4", Format: model.FormatH264, StartedAt: covTestTime(0),
	}))
	stored, err := db.GetRecording(ctx, "rec-after")
	require.NoError(t, err)
	require.NotNil(t, stored)
}
