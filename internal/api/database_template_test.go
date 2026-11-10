package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// The fast fixture must have a complete schema and each test's writes must be
// private; migration behavior itself is covered by storage's own tests.
func TestAPIDatabaseTemplateIsolationAndSchema(t *testing.T) {
	first, _ := setupTestDB(t)
	second, _ := setupTestDB(t)
	fresh, err := storage.New(filepath.Join(t.TempDir(), "fresh.db"))
	require.NoError(t, err)
	defer fresh.Close()
	require.NoError(t, fresh.Init(context.Background()))
	schema := func(db *storage.DB) map[string]string {
		rows, err := db.DB().Query(`SELECT type || ':' || name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`)
		require.NoError(t, err)
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var name, sql string
			require.NoError(t, rows.Scan(&name, &sql))
			out[name] = sql
		}
		require.NoError(t, rows.Err())
		return out
	}
	require.Equal(t, schema(fresh), schema(first))
	require.Equal(t, schema(first), schema(second))
	var integrity string
	require.NoError(t, first.DB().QueryRow(`PRAGMA integrity_check`).Scan(&integrity))
	require.Equal(t, "ok", integrity)
	_, err = first.DB().Exec(`INSERT INTO schema_meta(key,value) VALUES ('fixture-isolation','one')`)
	require.NoError(t, err)
	var count int
	require.NoError(t, second.DB().QueryRow(`SELECT count(*) FROM schema_meta WHERE key = 'fixture-isolation'`).Scan(&count))
	require.Zero(t, count)
}
