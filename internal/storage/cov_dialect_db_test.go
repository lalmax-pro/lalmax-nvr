package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

// TestCovSplitSQLStatements covers quote, escape and separator handling.
func TestCovSplitSQLStatements(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{name: "empty", query: "   ;  ;  ", want: nil},
		{name: "simple", query: "SELECT 1", want: []string{"SELECT 1"}},
		{name: "two", query: "SELECT 1; SELECT 2;", want: []string{"SELECT 1", "SELECT 2"}},
		{name: "semicolon in single quote", query: "INSERT INTO t VALUES ('a;b'); SELECT 2",
			want: []string{"INSERT INTO t VALUES ('a;b')", "SELECT 2"}},
		{name: "doubled quote", query: "INSERT INTO t VALUES ('it''s;fine'); SELECT 2",
			want: []string{"INSERT INTO t VALUES ('it''s;fine')", "SELECT 2"}},
		{name: "backslash escape", query: `INSERT INTO t VALUES ('a\';b'); SELECT 2`,
			want: []string{`INSERT INTO t VALUES ('a\';b')`, "SELECT 2"}},
		{name: "double quoted identifier", query: `SELECT "a;b" FROM t; SELECT 2`,
			want: []string{`SELECT "a;b" FROM t`, "SELECT 2"}},
		{name: "backtick identifier", query: "SELECT `a;b` FROM t", want: []string{"SELECT `a;b` FROM t"}},
		{name: "semicolon at start", query: "; SELECT 1", want: []string{"SELECT 1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, splitSQLStatements(tt.query))
		})
	}
}

// TestCovAdaptSQLForMySQLAndPostgres covers the dialect rewrites that do not
// need a live MySQL/PostgreSQL server.
func TestCovAdaptSQLForMySQLAndPostgres(t *testing.T) {
	sqliteIdentity := "SELECT ? FROM t WHERE key = ?"
	require.Equal(t, sqliteIdentity, adaptSQLForDriver(sqliteIdentity, DriverSQLite))

	ddl := `CREATE TABLE IF NOT EXISTS sample (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		slug TEXT UNIQUE,
		code TEXT DEFAULT '',
		active BOOLEAN DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (id, name),
		FOREIGN KEY (name) REFERENCES other(id),
		UNIQUE (slug, code)
	);`
	my := adaptSQLForDriver(ddl, DriverMySQL)
	require.Contains(t, my, "BIGINT AUTO_INCREMENT PRIMARY KEY")
	require.Contains(t, my, "TINYINT(1)")
	require.NotContains(t, my, "BOOLEAN")
	require.Contains(t, my, "VARCHAR(191)")
	require.Contains(t, my, "DATETIME", "DATETIME is valid for MySQL")

	alter := "ALTER TABLE cameras ADD COLUMN note TEXT DEFAULT ''"
	require.Equal(t, "ALTER TABLE cameras ADD COLUMN note VARCHAR(191) DEFAULT ''", adaptSQLForDriver(alter, DriverMySQL))

	insertIgnore := "INSERT OR IGNORE INTO t (name) VALUES (?)"
	myInsert := adaptSQLForDriver(insertIgnore, DriverMySQL)
	require.NotContains(t, strings.ToUpper(myInsert), "OR IGNORE")
	require.Contains(t, myInsert, "INSERT IGNORE INTO")
	require.NotContains(t, strings.ToUpper(myInsert), "OR IGNORE")

	upsert := "INSERT INTO t (name) VALUES (?) ON CONFLICT(name) DO UPDATE SET name=excluded.name, updated_at=excluded.updated_at;"
	myUpsert := adaptSQLForDriver(upsert, DriverMySQL)
	require.Contains(t, myUpsert, "ON DUPLICATE KEY UPDATE")
	require.Contains(t, myUpsert, "name=VALUES(name)")
	require.Contains(t, myUpsert, "updated_at=VALUES(updated_at)")

	index := "CREATE INDEX IF NOT EXISTS idx_name ON t(name)"
	require.Equal(t, "CREATE INDEX idx_name ON t(name)", adaptSQLForDriver(index, DriverMySQL))

	epoch := "SELECT CAST((CAST(strftime('%s', substr(started_at, 1, 19)) AS INTEGER) / 60) * 60 AS BIGINT) FROM recordings"
	myEpoch := adaptSQLForDriver(epoch, DriverMySQL)
	require.Contains(t, myEpoch, "UNIX_TIMESTAMP(SUBSTRING(started_at, 1, 19))")
	require.Contains(t, myEpoch, "AS SIGNED")

	dateFormats := "SELECT strftime('%Y-%m', started_at), strftime('%Y-%m-%dT%H:00:00Z', started_at), datetime('now'), strftime('%Y-%m-%d %H:%M:%f', 'now')"
	myDates := adaptSQLForDriver(dateFormats, DriverMySQL)
	require.Contains(t, myDates, "DATE_FORMAT(started_at, '%Y-%m')")
	require.Contains(t, myDates, "CURRENT_TIMESTAMP")
	require.NotContains(t, myDates, "strftime('%Y-%m-%d %H:%M:%f', 'now')")

	pg := adaptSQLForDriver("SELECT ? FROM t WHERE id = ?", DriverPostgres)
	require.Equal(t, "SELECT ? FROM t WHERE id = ?", pg, "rebinding happens in dialectQuery")
	pgDDL := adaptSQLForDriver(ddl, DriverPostgres)
	require.Contains(t, pgDDL, "BIGSERIAL PRIMARY KEY")
	require.Contains(t, pgDDL, "TEXT", "PostgreSQL keeps TEXT")

	// Identifier quoting for MySQL reserved words.
	require.Equal(t, "`key`", quoteMySQLKeyIdentifier("key"))
	require.Equal(t, "`KEY`", quoteMySQLKeyIdentifier("KEY"))
	require.Equal(t, "`loop`", quoteMySQLKeyIdentifier("loop"))
	require.Equal(t, "usage", quoteMySQLKeyIdentifier("usage"), "only key/loop are reserved here")
	require.Equal(t, "name", quoteMySQLKeyIdentifier("name"))
	require.Equal(t, "a`b", quoteMySQLKeyIdentifier("a`b"), "quoted sections are copied verbatim")
	require.Equal(t, `a\b`, quoteMySQLKeyIdentifier(`a\b`))
}

// TestCovRebindDollarPlaceholders covers placeholder rewriting rules.
func TestCovRebindDollarPlaceholders(t *testing.T) {
	require.Equal(t, "SELECT $1, $2 FROM t WHERE id = $3", rebindDollarPlaceholders("SELECT ?, ? FROM t WHERE id = ?"))
	require.Equal(t, "SELECT '?' , $1", rebindDollarPlaceholders("SELECT '?' , ?"))
	require.Equal(t, `SELECT "?" , $1`, rebindDollarPlaceholders(`SELECT "?" , ?`))
	require.Equal(t, "SELECT `?`, $1", rebindDollarPlaceholders("SELECT `?`, ?"))
	require.Equal(t, "SELECT 'it''s ?', $1", rebindDollarPlaceholders("SELECT 'it''s ?', ?"))
	require.Equal(t, `SELECT 'a\'?' , $1`, rebindDollarPlaceholders(`SELECT 'a\'?' , ?`))
	require.Equal(t, "SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11", rebindDollarPlaceholders("SELECT ?,?,?,?,?,?,?,?,?,?,?"))
	require.Equal(t, "SELECT 1", rebindDollarPlaceholders("SELECT 1"))
}

// TestCovSQLPlaceholderNumber covers single and multi-digit formatting.
func TestCovSQLPlaceholderNumber(t *testing.T) {
	require.Equal(t, "1", sqlPlaceholderNumber(1))
	require.Equal(t, "9", sqlPlaceholderNumber(9))
	require.Equal(t, "10", sqlPlaceholderNumber(10))
	require.Equal(t, "42", sqlPlaceholderNumber(42))
	require.Equal(t, "100", sqlPlaceholderNumber(100))
}

// TestCovSQLQueryClassifiers covers the index/alter detectors.
func TestCovSQLQueryClassifiers(t *testing.T) {
	require.True(t, isCreateIndexQuery("CREATE INDEX idx ON t(c)"))
	require.False(t, isCreateIndexQuery("create unique index if not exists idx on t(c)"),
		"only plain CREATE INDEX is recognised for duplicate-key suppression")
	require.False(t, isCreateIndexQuery("CREATE TABLE t (id INTEGER)"))
	require.True(t, isAddColumnQuery("ALTER TABLE t ADD COLUMN c TEXT"))
	require.True(t, isAddColumnQuery("  alter table t add column c text"))
	require.False(t, isAddColumnQuery("ALTER TABLE t RENAME TO u"))
	require.False(t, isAddColumnQuery("SELECT 1"))

	require.True(t, isSQLWordStart('k'))
	require.True(t, isSQLWordStart('_'))
	require.True(t, isSQLWordStart('Z'))
	require.False(t, isSQLWordStart('1'))
	require.False(t, isSQLWordStart('-'))
	require.True(t, isSQLWordPart('a'))
	require.True(t, isSQLWordPart('_'))
	require.True(t, isSQLWordPart('1'))
	require.False(t, isSQLWordPart('-'))
}

// TestCovMySQLTextIndexPrefixesNoMatch covers the early return without a DB.
func TestCovMySQLTextIndexPrefixesNoMatch(t *testing.T) {
	db := &DB{}
	query, changed := db.mysqlTextIndexPrefixes(context.Background(), "SELECT 1")
	require.False(t, changed)
	require.Equal(t, "SELECT 1", query)
}

// TestCovExecContextAddColumnSuppression verifies duplicate-column DDL is
// treated as success while real errors still surface.
func TestCovExecContextAddColumnSuppression(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_, err := db.execContext(ctx, "ALTER TABLE cameras ADD COLUMN cov_note TEXT DEFAULT ''")
	require.NoError(t, err)
	_, err = db.execContext(ctx, "ALTER TABLE cameras ADD COLUMN cov_note TEXT DEFAULT ''")
	require.NoError(t, err, "duplicate column errors are suppressed")

	_, err = db.execContext(ctx, "SELECT * FROM table_that_does_not_exist")
	require.ErrorContains(t, err, "table_that_does_not_exist")

	// Existing tables are left alone by "already exists".
	_, err = db.execContext(ctx, "CREATE TABLE IF NOT EXISTS cameras (id TEXT)")
	require.NoError(t, err)
}

// TestCovDialectTxReleasesLock covers the dialectTx once-guarded unlock.
func TestCovDialectTxReleasesLock(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	tx, err := db.beginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO schema_meta (key, value) VALUES ('cov-tx', '1')`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.ErrorIs(t, tx.Rollback(), sql.ErrTxDone)

	// The connection lock must be released, so normal reads work afterwards.
	var value string
	require.NoError(t, db.queryRowContext(ctx, `SELECT value FROM schema_meta WHERE key='cov-tx'`).Scan(&value))
	require.Equal(t, "1", value)

	tx2, err := db.beginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, tx2.Rollback())
	require.ErrorIs(t, tx2.Rollback(), sql.ErrTxDone)
}

// TestCovEventNotifier covers SetEventNotifier/NotifyEvent wiring.
func TestCovEventNotifier(t *testing.T) {
	db := newTestDB(t)

	// No notifier installed: nothing happens.
	db.NotifyEvent(model.Event{Type: "noop"})

	var got []model.Event
	db.SetEventNotifier(func(e model.Event) { got = append(got, e) })
	db.NotifyEvent(model.Event{Type: "first"})
	db.NotifyEvent(model.Event{Type: "second"})
	require.Len(t, got, 2)
	require.Equal(t, "first", got[0].Type)

	// Replacing the notifier swaps the receiver.
	var replaced int
	db.SetEventNotifier(func(e model.Event) { replaced++ })
	db.NotifyEvent(model.Event{Type: "third"})
	require.Equal(t, 1, replaced)
	require.Len(t, got, 2)
}

// TestCovMigrateEncodings covers legacy protocol normalisation.
func TestCovMigrateEncodings(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	seed := func(id, protocol, encoding string) {
		_, err := db.execContext(ctx,
			`INSERT INTO cameras (id, name, protocol, encoding, url, enabled) VALUES (?,?,'',?,'rtsp://x',1)`, id, id, encoding)
		require.NoError(t, err)
		_, err = db.execContext(ctx, `UPDATE cameras SET protocol=? WHERE id=?`, protocol, id)
		require.NoError(t, err)
	}
	seed("legacy-rtsp", "rtsp_h264", "")
	seed("legacy-http", "http_jpeg", "")
	seed("legacy-onvif", "onvif", "")
	seed("legacy-unknown", "mystery", "")
	seed("already-normalised", "rtsp", "")

	db.migrateEncodings()

	check := func(id, wantProtocol, wantEncoding string) {
		t.Helper()
		var protocol, encoding string
		require.NoError(t, db.queryRowContext(ctx, `SELECT protocol, encoding FROM cameras WHERE id=?`, id).Scan(&protocol, &encoding))
		require.Equal(t, wantProtocol, protocol, "protocol for %s", id)
		require.Equal(t, wantEncoding, encoding, "encoding for %s", id)
	}
	check("legacy-rtsp", "rtsp", "h264")
	check("legacy-http", "http", "jpeg")
	check("legacy-onvif", "onvif", "")
	check("legacy-unknown", "mystery", "")
	check("already-normalised", "rtsp", "")

	// A closed database is tolerated (migration is best effort).
	require.NoError(t, db.Close())
	db.migrateEncodings()
}

// TestCovOpenValidation covers Open option validation and DSN helpers.
func TestCovOpenValidation(t *testing.T) {
	ctx := context.Background()

	_, err := Open(ctx, DatabaseOptions{Driver: "", DSN: ""})
	require.ErrorContains(t, err, "DSN must not be empty")

	db, err := Open(ctx, DatabaseOptions{Driver: "", DSN: filepath.Join(t.TempDir(), "implicit.db")})
	require.NoError(t, err)
	require.Equal(t, DriverSQLite, db.driver)
	require.NoError(t, db.Close())

	_, err = Open(ctx, DatabaseOptions{Driver: "oracle", DSN: "anything"})
	require.ErrorContains(t, err, "unsupported database driver")

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opt.db")
	db, err = Open(ctx, DatabaseOptions{
		Driver: DriverSQLite, DSN: dbPath, MaxOpenConns: 2, MaxIdleConns: 1, ConnMaxLifetime: 1,
	})
	require.NoError(t, err)
	require.NoError(t, db.Ping(ctx))
	require.NoError(t, db.Close())

	// A directory is not a valid SQLite database file.
	_, err = Open(ctx, DatabaseOptions{Driver: DriverSQLite, DSN: dir})
	require.Error(t, err)

	// A file that is not a SQLite database fails while applying pragmas.
	garbage := filepath.Join(dir, "garbage.db")
	require.NoError(t, os.WriteFile(garbage, []byte("this is definitely not a sqlite database"), 0o644))
	_, err = Open(ctx, DatabaseOptions{Driver: DriverSQLite, DSN: garbage})
	require.Error(t, err, "pragma application fails for a non-database file")
}

// TestCovSQLiteDSNHelpers covers DSN assembly and pragma naming.
func TestCovSQLiteDSNHelpers(t *testing.T) {
	plain := sqliteDSN("/tmp/plain.db")
	require.True(t, strings.HasPrefix(plain, "/tmp/plain.db?"))
	require.Contains(t, plain, "_pragma=busy_timeout%285000%29")

	withQuery := sqliteDSN("/tmp/query.db?cache=shared")
	require.True(t, strings.HasPrefix(withQuery, "/tmp/query.db?cache=shared&_pragma="))
	require.Contains(t, withQuery, "&_pragma=")

	require.Equal(t, "journal_mode=WAL", pragmaName("journal_mode(WAL)"))
	require.Equal(t, "busy_timeout=5000", pragmaName("busy_timeout(5000)"))
	require.Equal(t, "automatic_index", pragmaName("automatic_index"))
}

// TestCovLegacySchemaMigration drives Init through the v1/v2/v3 upgrade path.
func TestCovLegacySchemaMigration(t *testing.T) {
	ctx := context.Background()

	// Starting at schema_version=1: metadata columns and retention_days are added.
	dir := t.TempDir()
	legacyPath := covLegacyDB(t, dir, "legacy-v1.db", "1", false)

	upgraded, err := Open(ctx, DatabaseOptions{Driver: DriverSQLite, DSN: legacyPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = upgraded.Close() })
	require.NoError(t, upgraded.Init(ctx))
	require.NoError(t, upgraded.Init(ctx), "second init is idempotent")

	var version string
	require.NoError(t, upgraded.queryRowContext(ctx, `SELECT value FROM schema_meta WHERE key='schema_version'`).Scan(&version))
	require.NotEqual(t, "1", version)

	var description, location string
	require.NoError(t, upgraded.queryRowContext(ctx, `SELECT COALESCE(description,''), COALESCE(location,'') FROM cameras WHERE id='cam1'`).
		Scan(&description, &location))
	require.Empty(t, description)

	// Starting at schema_version=2 with a legacy pinned column: pinned becomes merged.
	dir2 := t.TempDir()
	pinnedPath := covLegacyDB(t, dir2, "legacy-v2.db", "2", true)
	pinned, err := Open(ctx, DatabaseOptions{Driver: DriverSQLite, DSN: pinnedPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pinned.Close() })
	require.NoError(t, pinned.Init(ctx))

	var merged int
	require.NoError(t, pinned.queryRowContext(ctx, `SELECT merged FROM recordings WHERE id='rec-legacy'`).Scan(&merged))
	require.Equal(t, 1, merged, "legacy pinned value is migrated to merged")

	var pinnedCols int
	require.NoError(t, pinned.queryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='pinned'`).Scan(&pinnedCols))
	require.Zero(t, pinnedCols, "the pinned column is dropped after migration")

	// New recordings still work after the upgrade.
	rec := &model.Recording{ID: "rec-new", CameraID: "cam1", FilePath: "/new.mp4", Format: model.FormatH264, StartedAt: covTestTime(0)}
	require.NoError(t, pinned.InsertRecording(ctx, rec))
}

// covLegacyDB builds a pre-migration SQLite database and returns its path.
func covLegacyDB(t *testing.T, dir, name, version string, withPinned bool) string {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(dir, name)
	legacy, err := Open(ctx, DatabaseOptions{Driver: DriverSQLite, DSN: dbPath})
	require.NoError(t, err)
	stmts := []string{
		`CREATE TABLE cameras (id TEXT PRIMARY KEY, name TEXT NOT NULL, protocol TEXT NOT NULL, url TEXT NOT NULL, enabled INTEGER DEFAULT 1);`,
		`CREATE TABLE recordings (id TEXT PRIMARY KEY, camera_id TEXT NOT NULL, file_path TEXT NOT NULL, format TEXT NOT NULL,
			started_at DATETIME NOT NULL, ended_at DATETIME, duration REAL, file_size INTEGER DEFAULT 0, frame_count INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);`,
		`INSERT INTO cameras (id, name, protocol, url, enabled) VALUES ('cam1','Legacy','rtsp','rtsp://legacy',1);`,
		`INSERT INTO recordings (id, camera_id, file_path, format, started_at) VALUES ('rec-legacy','cam1','/r.mp4','h264','2024-01-01 00:00:00');`,
	}
	if withPinned {
		stmts = append(stmts,
			`ALTER TABLE recordings ADD COLUMN pinned INTEGER DEFAULT 0;`,
			`UPDATE recordings SET pinned=1 WHERE id='rec-legacy';`)
	}
	for _, stmt := range stmts {
		_, err := legacy.execContext(ctx, stmt)
		require.NoError(t, err)
	}
	_, err = legacy.execContext(ctx, `INSERT INTO schema_meta (key, value) VALUES ('schema_version', ?);`, version)
	require.NoError(t, err)
	require.NoError(t, legacy.Close())
	return dbPath
}
