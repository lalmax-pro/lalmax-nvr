package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPoolConnectionsWaitForConcurrentWriter(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "pool.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, db.Init(ctx))
	var connections []*sql.Conn
	for i := 0; i < 4; i++ {
		c, err := db.DB().Conn(ctx)
		require.NoError(t, err)
		connections = append(connections, c)
		defer c.Close()
		for pragma, want := range map[string]int{"busy_timeout": 5000, "synchronous": 1, "cache_size": -2000} {
			var got int
			require.NoError(t, c.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got))
			require.Equal(t, want, got, "connection %d pragma %s", i, pragma)
		}
	}
	_, err = connections[0].ExecContext(ctx, "CREATE TABLE pool_writes (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	_, err = connections[0].ExecContext(ctx, "BEGIN IMMEDIATE")
	require.NoError(t, err)
	defer connections[0].ExecContext(context.Background(), "ROLLBACK")
	done := make(chan error, 1)
	go func() {
		_, err := connections[1].ExecContext(ctx, "INSERT INTO pool_writes VALUES (1)")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("writer did not wait for held transaction: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	_, err = connections[0].ExecContext(ctx, "COMMIT")
	require.NoError(t, err)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
