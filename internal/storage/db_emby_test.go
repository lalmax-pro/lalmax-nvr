package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Emby recovery enumerates publications on every NVR start. A previously
// SQLite-specific collation prevented this query on PostgreSQL and MySQL.
func TestEmbyRecoveryListingSQLite(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "emby.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Init(context.Background()))
	checkEmbyListing(t, db)
}

func TestEmbyRecoveryListingExternalDrivers(t *testing.T) {
	for _, tc := range []struct {
		name string
		driver DatabaseDriver
		env string
	}{
		{"postgres", DriverPostgres, "LALMAX_TEST_POSTGRES_DSN"},
		{"mysql", DriverMySQL, "LALMAX_TEST_MYSQL_DSN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if dsn == "" {
				t.Skipf("set %s to test actual %s Emby recovery", tc.env, tc.name)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			db, err := Open(ctx, DatabaseOptions{
				Driver: tc.driver,
				DSN: dsn,
				MaxOpenConns: 2,
				MaxIdleConns: 1,
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			require.NoError(t, db.Init(ctx))
			checkEmbyListing(t, db)
		})
	}
}

func checkEmbyListing(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	// Emby recovery must work with no items, before any upstream connection.
	_, err := db.ListEmbyItems(ctx)
	require.NoError(t, err)

	idBase := fmt.Sprintf("test-emby-list-%d", time.Now().UnixNano())
	fixtures := []struct{
		name string
		id string
	}{
		{name: "zeta", id: idBase + "-z"},
		{name: "Alpha", id: idBase + "-a"},
		{name: "beta", id: idBase + "-b"},
	}
	ours := make(map[string]struct{}, len(fixtures))
	for _, fixture := range fixtures {
		id := fixture.id
		ours[id] = struct{}{}
		t.Cleanup(func() {
			if err := db.DeleteEmbyItem(context.Background(), id); err != nil {
				t.Errorf("cleanup Emby fixture %q: %v", id, err)
			}
		})
		require.NoError(t, db.SaveEmbyItem(ctx, EmbyItem{
			ID: id,
			EmbyItemID: id,
			Name: fixture.name,
			ItemType: "Movie",
			StreamID: "stream-" + id,
			PublishEnabled: true,
		}))
	}
	items, err := db.ListEmbyItems(ctx)
	require.NoError(t, err)
	var names []string
	for _, item := range items {
		if _, ok := ours[item.ID]; ok {
			names = append(names, item.Name)
			require.True(t, item.PublishEnabled)
		}
	}
	require.Equal(t, []string{"Alpha", "beta", "zeta"}, names)
}
