package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHomeAssistantIdentitySurvivesReopenAndBackup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nvr.db")
	db, err := New(path)
	require.NoError(t, err)
	require.NoError(t, db.Init(ctx))
	// A second initialization must be idempotent and preserve the generated ID.
	require.NoError(t, db.Init(ctx))
	cfg, err := db.GetHomeAssistantSettings(ctx)
	require.NoError(t, err)
	identity := cfg.InstanceID
	require.False(t, cfg.Enabled)

	cfg.Enabled = true
	cfg.InstanceID = "must-not-replace"
	cfg.RTSPBaseURL = "rtsp://nvr.lan:15544"
	cfg.Streams = []HomeAssistantPublication{{StreamID: "push-1", AppName: "custom", Name: "Door"}}
	require.NoError(t, db.SaveHomeAssistantSettings(ctx, cfg))
	cfg, err = db.GetHomeAssistantSettings(ctx)
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.Equal(t, identity, cfg.InstanceID)
	require.Len(t, cfg.Streams, 1)

	cfg.Enabled = false
	require.NoError(t, db.SaveHomeAssistantSettings(ctx, cfg))
	cfg, err = db.GetHomeAssistantSettings(ctx)
	require.NoError(t, err)
	require.False(t, cfg.Enabled)
	require.Equal(t, identity, cfg.InstanceID)
	require.Len(t, cfg.Streams, 1)

	require.NoError(t, db.Close())
	db, err = New(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Init(ctx))
	require.NoError(t, db.Init(ctx))
	cfg, err = db.GetHomeAssistantSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, identity, cfg.InstanceID)
	require.False(t, cfg.Enabled)
	require.Len(t, cfg.Streams, 1)

	backup := filepath.Join(t.TempDir(), "backup.db")
	require.NoError(t, db.Backup(ctx, backup))
	restored, err := New(backup)
	require.NoError(t, err)
	t.Cleanup(func() { _ = restored.Close() })
	require.NoError(t, restored.Init(ctx))
	got, err := restored.GetHomeAssistantSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, cfg, got)
}

type captureDriver struct {
	conn *captureConn
}

func (d *captureDriver) Open(string) (driver.Conn, error) {
	d.conn = &captureConn{}
	return d.conn, nil
}

type captureConn struct {
	query string
	args  []driver.NamedValue
}

func (c *captureConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("prepare is not used") }
func (c *captureConn) Close() error                        { return nil }
func (c *captureConn) Begin() (driver.Tx, error)            { return nil, errors.New("transactions are not used") }
func (c *captureConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.query = query
	c.args = append([]driver.NamedValue(nil), args...)
	return captureResult{}, nil
}
func (c *captureConn) CheckNamedValue(*driver.NamedValue) error { return nil }

type captureResult struct{}

func (captureResult) LastInsertId() (int64, error) { return 0, nil }
func (captureResult) RowsAffected() (int64, error) { return 1, nil }

func TestHomeAssistantUsesIntegerBindingsForExternalDialects(t *testing.T) {
	for _, tt := range []struct {
		name   string
		driver DatabaseDriver
	}{
		{name: "postgres", driver: DriverPostgres},
		{name: "mysql", driver: DriverMySQL},
	} {
		t.Run(tt.name, func(t *testing.T) {
			capture := &captureDriver{}
			driverName := fmt.Sprintf("ha_capture_%d", time.Now().UnixNano())
			sql.Register(driverName, capture)
			sqlDB, err := sql.Open(driverName, "")
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = sqlDB.Close() })
			db := &DB{driver: tt.driver, db: sqlDB}
			ctx := context.Background()

			require.NoError(t, db.createHomeAssistantTable(ctx))
			require.NotNil(t, capture.conn)
			require.Len(t, capture.conn.args, 5)
			require.Equal(t, int(0), capture.conn.args[2].Value,
				"bootstrap enabled value must be an integer for %s", tt.driver)
			if tt.driver == DriverPostgres {
				require.Contains(t, capture.conn.query, "INSERT INTO")
				require.Contains(t, capture.conn.query, "ON CONFLICT DO NOTHING")
				require.Contains(t, capture.conn.query, "$1")
			} else {
				require.Contains(t, capture.conn.query, "INSERT IGNORE INTO")
				require.Contains(t, capture.conn.query, "?")
			}

			cfg := HomeAssistantSettings{Enabled: true, RTSPBaseURL: "rtsp://nvr", Streams: []HomeAssistantPublication{}}
			require.NoError(t, db.SaveHomeAssistantSettings(ctx, cfg))
			require.Len(t, capture.conn.args, 4)
			require.Equal(t, int(1), capture.conn.args[0].Value,
				"settings enabled value must be an integer for %s", tt.driver)
			if tt.driver == DriverPostgres {
				require.Contains(t, capture.conn.query, "$1")
			}
		})
	}
}

func TestHomeAssistantDialectSQLDoesNotChangeIntegerSemantics(t *testing.T) {
	for _, tt := range []DatabaseDriver{DriverPostgres, DriverMySQL} {
		t.Run(string(tt), func(t *testing.T) {
			db := &DB{driver: tt}
			query := db.dialectQuery("UPDATE home_assistant_settings SET enabled = ? WHERE id = ?")
			if tt == DriverPostgres {
				require.Equal(t, "UPDATE home_assistant_settings SET enabled = $1 WHERE id = $2", query)
			} else {
				require.Equal(t, "UPDATE home_assistant_settings SET enabled = ? WHERE id = ?", query)
			}
		})
	}
}

var _ driver.ExecerContext = (*captureConn)(nil)
var _ driver.NamedValueChecker = (*captureConn)(nil)
