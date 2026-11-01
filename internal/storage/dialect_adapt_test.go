package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdaptSQLForDriverSQLiteIsIdentity(t *testing.T) {
	query := "INSERT OR IGNORE INTO cameras (id) VALUES (?);"
	require.Equal(t, query, adaptSQLForDriver(query, DriverSQLite))
}

func TestAdaptSQLForDriverMySQL(t *testing.T) {
	create := adaptSQLForDriver(`CREATE TABLE cameras (
id INTEGER PRIMARY KEY AUTOINCREMENT,
name TEXT NOT NULL DEFAULT '',
token TEXT UNIQUE,
enabled BOOLEAN,
loop TEXT,
PRIMARY KEY (id),
FOREIGN KEY (token)
);`, DriverMySQL)
	require.Contains(t, create, "BIGINT AUTO_INCREMENT PRIMARY KEY")
	require.Contains(t, create, "TINYINT(1)")
	require.Contains(t, create, "`loop`")
	require.Contains(t, create, "PRIMARY KEY")
	require.NotContains(t, create, "INTEGER PRIMARY KEY AUTOINCREMENT")

	index := adaptSQLForDriver("CREATE INDEX IF NOT EXISTS idx_name ON cameras (name)", DriverMySQL)
	require.Equal(t, "CREATE INDEX idx_name ON cameras (name)", index)

	insert := adaptSQLForDriver("INSERT OR IGNORE INTO cameras (id) VALUES (?)", DriverMySQL)
	require.Equal(t, "INSERT IGNORE INTO cameras (id) VALUES (?)", insert)

	upsert := adaptSQLForDriver("INSERT INTO cameras (id, name) VALUES (?, ?) ON CONFLICT(id) DO UPDATE SET name = excluded.name", DriverMySQL)
	require.Contains(t, upsert, "ON DUPLICATE KEY UPDATE")
	require.Contains(t, upsert, "VALUES(name)")

	epoch := adaptSQLForDriver("SELECT CAST(strftime('%s', substr(r.started_at, 1, 19)) AS INTEGER) FROM recordings r", DriverMySQL)
	require.Contains(t, epoch, "UNIX_TIMESTAMP")
	require.Contains(t, adaptSQLForDriver("SELECT strftime('%Y-%m', started_at)", DriverMySQL), "DATE_FORMAT")
}

func TestAdaptSQLForDriverPostgres(t *testing.T) {
	create := adaptSQLForDriver("CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, created DATETIME)", DriverPostgres)
	require.Contains(t, create, "BIGSERIAL PRIMARY KEY")
	require.Contains(t, create, "TIMESTAMP")

	insert := adaptSQLForDriver("INSERT OR IGNORE INTO cameras (id) VALUES (?);", DriverPostgres)
	require.Contains(t, insert, "INSERT INTO cameras")
	require.Contains(t, insert, "ON CONFLICT DO NOTHING")
	require.True(t, insert[len(insert)-1] == ';')

	already := adaptSQLForDriver("INSERT OR IGNORE INTO cameras (id) VALUES (?) ON CONFLICT DO NOTHING", DriverPostgres)
	require.Contains(t, already, "ON CONFLICT DO NOTHING")
	require.Equal(t, 1, countSubstr(already, "ON CONFLICT DO NOTHING"))

	require.Contains(t, adaptSQLForDriver("SELECT strftime('%Y-%m', started_at)", DriverPostgres), "TO_CHAR")
	require.Contains(t, adaptSQLForDriver("SELECT datetime('now')", DriverPostgres), "CURRENT_TIMESTAMP")
}

func TestSplitSQLStatementsAndRebind(t *testing.T) {
	require.Equal(t, []string{
		"CREATE TABLE t (id INT)",
		"INSERT INTO t VALUES ('a;b')",
		`INSERT INTO t VALUES ("x;y")`,
	}, splitSQLStatements("CREATE TABLE t (id INT); INSERT INTO t VALUES ('a;b'); INSERT INTO t VALUES (\"x;y\");"))
	require.Empty(t, splitSQLStatements("   ;  ; "))
	require.Equal(t, []string{`INSERT INTO t VALUES ('it''s')`}, splitSQLStatements(`INSERT INTO t VALUES ('it''s');`))

	require.Equal(t, "SELECT * FROM t WHERE a = $1 AND b = $2", rebindDollarPlaceholders("SELECT * FROM t WHERE a = ? AND b = ?"))
	require.Equal(t, "SELECT '?' FROM t WHERE a = $1", rebindDollarPlaceholders("SELECT '?' FROM t WHERE a = ?"))
	rebound := rebindDollarPlaceholders(manyQuestionMarks(10))
	require.Contains(t, rebound, "$10")
	require.Contains(t, rebound, "$1 AND b = $2")
	require.Equal(t, 1, boolToDBInt(true))
	require.Equal(t, 0, boolToDBInt(false))
	require.True(t, isCreateIndexQuery("  CREATE INDEX idx ON t (id)"))
	require.False(t, isCreateIndexQuery("SELECT 1"))
	require.True(t, isAddColumnQuery("ALTER TABLE t ADD COLUMN name TEXT"))
	require.False(t, isAddColumnQuery("ALTER TABLE t DROP COLUMN name"))
}

func TestQuoteMySQLKeyIdentifier(t *testing.T) {
	require.Equal(t, "SELECT `key`, `loop` FROM t", quoteMySQLKeyIdentifier("SELECT key, loop FROM t"))
	require.Equal(t, "PRIMARY KEY (id)", quoteMySQLKeyIdentifier("PRIMARY KEY (id)"))
	require.Equal(t, "FOREIGN KEY (id)", quoteMySQLKeyIdentifier("FOREIGN KEY (id)"))
	require.Equal(t, "ON DUPLICATE KEY UPDATE name=VALUES(name)", quoteMySQLKeyIdentifier("ON DUPLICATE KEY UPDATE name=VALUES(name)"))
	require.Equal(t, "SELECT 'key' FROM t", quoteMySQLKeyIdentifier("SELECT 'key' FROM t"))
}

func TestEmptySQLResult(t *testing.T) {
	id, err := emptySQLResult{}.LastInsertId()
	require.NoError(t, err)
	require.Equal(t, int64(0), id)
	n, err := emptySQLResult{}.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(0), n)
}

func manyQuestionMarks(n int) string {
	q := "SELECT * FROM t WHERE a = ?"
	for i := 1; i < n; i++ {
		q += " AND b = ?"
	}
	return q
}

func countSubstr(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}
