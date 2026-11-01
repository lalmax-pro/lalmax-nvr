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

func migrationTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(filepath.Join(t.TempDir(), "migration.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Init(context.Background()))
	return db
}

func TestBackupAsSQLiteValidation(t *testing.T) {
	ctx := context.Background()
	db := migrationTestDB(t)

	err := db.backupAsSQLite(ctx, "")
	require.ErrorContains(t, err, "destination path must not be empty")

	existing := filepath.Join(t.TempDir(), "exists.db")
	require.NoError(t, os.WriteFile(existing, []byte("x"), 0o644))
	err = db.backupAsSQLite(ctx, existing)
	require.ErrorContains(t, err, "already exists")

	blockedParent := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(blockedParent, []byte("file"), 0o644))
	err = db.backupAsSQLite(ctx, filepath.Join(blockedParent, "backup.db"))
	require.Error(t, err)
	require.False(t, os.IsNotExist(err))
}

func TestBackupAsSQLiteCopiesRows(t *testing.T) {
	ctx := context.Background()
	db := migrationTestDB(t)
	require.NoError(t, db.UpsertCamera(ctx, "cam1", "Camera 1", "rtsp", "h264", "rtsp://127.0.0.1/live", "", "", true, "", "", ""))
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "rec1", CameraID: "cam1", FilePath: "/rec1.mp4", Format: model.FormatH264,
		StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}))

	dest := filepath.Join(t.TempDir(), "backup.db")
	require.NoError(t, db.backupAsSQLite(ctx, dest))

	restored, err := New(dest)
	require.NoError(t, err)
	t.Cleanup(func() { _ = restored.Close() })
	cam, err := restored.GetCamera(ctx, "cam1")
	require.NoError(t, err)
	require.NotNil(t, cam)
	require.Equal(t, "Camera 1", cam.Name)
	rec, err := restored.GetRecording(ctx, "rec1")
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, "cam1", rec.CameraID)
}

func TestMigrateToRejectsUnsupportedDestination(t *testing.T) {
	ctx := context.Background()
	db := migrationTestDB(t)
	_, err := db.MigrateTo(ctx, DriverSQLite, "ignored", nil)
	require.ErrorContains(t, err, "must be postgres or mysql")
	_, err = db.MigrateTo(ctx, "oracle", "ignored", nil)
	require.ErrorContains(t, err, "must be postgres or mysql")
}

func TestMigrateToDestinationOpenFailures(t *testing.T) {
	ctx := context.Background()
	db := migrationTestDB(t)
	_, err := db.MigrateTo(ctx, DriverPostgres, "postgres://127.0.0.1:1/nvr?connect_timeout=1", nil)
	require.Error(t, err)
	_, err = db.MigrateTo(ctx, DriverMySQL, "root:invalid@tcp(127.0.0.1:1)/nvr?timeout=1s", nil)
	require.Error(t, err)
}

func TestListTablesUnsupportedAndUnsafe(t *testing.T) {
	ctx := context.Background()
	db := migrationTestDB(t)

	_, err := listTables(ctx, db.db, "oracle")
	require.ErrorContains(t, err, "unsupported driver")

	_, err = db.db.ExecContext(ctx, `CREATE TABLE "bad-name" (id INTEGER)`)
	require.NoError(t, err)
	_, err = listTables(ctx, db.db, DriverSQLite)
	require.ErrorContains(t, err, "unsafe table identifier")

	_, err = listTables(ctx, db.db, DriverPostgres)
	require.Error(t, err)
	_, err = listTables(ctx, db.db, DriverMySQL)
	require.Error(t, err)
}

func TestIsResumableMigrationTarget(t *testing.T) {
	ctx := context.Background()
	dest := migrationTestDB(t)
	migrationID := migrationIdentity(dest.path, DriverPostgres)

	ok, err := isResumableMigrationTarget(ctx, dest, nil, []string{migrationMarkerTable}, migrationID)
	require.ErrorContains(t, err, "reserved table")
	require.False(t, ok)

	ok, err = isResumableMigrationTarget(ctx, dest, []string{"cameras", "other"}, []string{"cameras"}, migrationID)
	require.NoError(t, err)
	require.False(t, ok)

	ok, err = isResumableMigrationTarget(ctx, dest, []string{"cameras"}, []string{"cameras"}, migrationID)
	require.NoError(t, err)
	require.False(t, ok)

	_, err = dest.execContext(ctx, `CREATE TABLE lalmax_migration_meta (migration_id TEXT PRIMARY KEY NOT NULL)`)
	require.NoError(t, err)
	ok, err = isResumableMigrationTarget(ctx, dest, []string{migrationMarkerTable, "cameras"}, []string{"cameras"}, migrationID)
	require.NoError(t, err)
	require.False(t, ok)

	_, err = dest.execContext(ctx, `INSERT INTO lalmax_migration_meta (migration_id) VALUES (?)`, "other-id")
	require.NoError(t, err)
	ok, err = isResumableMigrationTarget(ctx, dest, []string{migrationMarkerTable, "cameras"}, []string{"cameras"}, migrationID)
	require.NoError(t, err)
	require.False(t, ok)

	_, err = dest.execContext(ctx, `UPDATE lalmax_migration_meta SET migration_id=?`, migrationID)
	require.NoError(t, err)
	ok, err = isResumableMigrationTarget(ctx, dest, []string{migrationMarkerTable, "cameras"}, []string{"cameras"}, migrationID)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestTableColumnsCountAndClear(t *testing.T) {
	ctx := context.Background()
	db := migrationTestDB(t)
	require.NoError(t, db.UpsertCamera(ctx, "cam1", "Camera 1", "rtsp", "h264", "rtsp://127.0.0.1/live", "", "", true, "", "", ""))

	cols, types, err := tableColumns(ctx, db, "cameras")
	require.NoError(t, err)
	require.NotEmpty(t, cols)
	require.Contains(t, types, "id")

	_, _, err = tableColumns(ctx, db, "bad-name")
	require.Error(t, err)

	tx, err := db.beginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	n, err := countTable(ctx, DriverSQLite, tx, "cameras")
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	_, err = countTable(ctx, DriverSQLite, tx, "bad-name")
	require.Error(t, err)

	require.NoError(t, clearDatabaseTables(ctx, tx, DriverSQLite, []string{migrationMarkerTable, "cameras"}))
	n, err = countTable(ctx, DriverSQLite, tx, "cameras")
	require.NoError(t, err)
	require.Equal(t, int64(0), n)
	require.Error(t, clearDatabaseTables(ctx, tx, DriverSQLite, []string{"bad-name"}))
}

func TestCopyTableAndEmptyColumns(t *testing.T) {
	ctx := context.Background()
	source := migrationTestDB(t)
	dest := migrationTestDB(t)
	require.NoError(t, source.UpsertCamera(ctx, "cam1", "Camera 1", "rtsp", "h264", "rtsp://127.0.0.1/live", "", "", true, "", "", ""))

	_, err := source.db.ExecContext(ctx, `CREATE TABLE extra_bool (id TEXT PRIMARY KEY, flag BOOLEAN, name TEXT)`)
	require.NoError(t, err)
	_, err = dest.db.ExecContext(ctx, `CREATE TABLE extra_bool (id TEXT PRIMARY KEY, flag BOOLEAN, name TEXT)`)
	require.NoError(t, err)
	for i := 0; i < 51; i++ {
		_, err = source.db.ExecContext(ctx, `INSERT INTO extra_bool (id, flag, name) VALUES (?, ?, ?)`, i, i%2, "row")
		require.NoError(t, err)
	}

	tx, err := dest.beginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	copied, err := copyTable(ctx, source, DriverSQLite, tx, "cameras")
	require.NoError(t, err)
	require.Equal(t, int64(1), copied)

	copied, err = copyTable(ctx, source, DriverSQLite, tx, "extra_bool")
	require.NoError(t, err)
	require.Equal(t, int64(51), copied)

	_, err = copyTable(ctx, source, DriverSQLite, tx, "bad-name")
	require.Error(t, err)
}

func TestResetPostgresSequencesUnsafeIdentifier(t *testing.T) {
	ctx := context.Background()
	db := migrationTestDB(t)
	tx, err := db.beginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	require.Error(t, resetPostgresSequences(ctx, tx, []string{"bad-name"}))
}

func TestListDatabaseTables(t *testing.T) {
	ctx := context.Background()
	db := migrationTestDB(t)
	tables, err := listDatabaseTables(ctx, db)
	require.NoError(t, err)
	require.Contains(t, tables, "cameras")
	require.Contains(t, tables, "recordings")
}
