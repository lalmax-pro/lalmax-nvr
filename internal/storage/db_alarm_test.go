package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func alarmTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(filepath.Join(t.TempDir(), "alarm.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Init(context.Background()))
	return db
}

func TestCreateAndListAlarms(t *testing.T) {
	ctx := context.Background()
	db := alarmTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)

	id1, err := db.CreateAlarm(ctx, &AlarmRow{
		DeviceID: "dev-a", ChannelID: "ch1", AlarmType: "motion", AlarmTime: now,
		Priority: 1, Method: "sip", Description: "first",
	})
	require.NoError(t, err)
	require.Greater(t, id1, int64(0))
	id2, err := db.CreateAlarm(ctx, &AlarmRow{
		DeviceID: "dev-a", ChannelID: "ch2", AlarmType: "tamper", AlarmTime: now.Add(time.Second),
		Priority: 2, Method: "sip", Description: "second",
	})
	require.NoError(t, err)
	require.Greater(t, id2, id1)
	_, err = db.CreateAlarm(ctx, &AlarmRow{
		DeviceID: "dev-b", ChannelID: "ch3", AlarmType: "offline", AlarmTime: now,
		Priority: 3, Method: "http", Description: "other",
	})
	require.NoError(t, err)

	all, total, err := db.ListAlarms(ctx, "", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Len(t, all, 3)

	filtered, total, err := db.ListAlarms(ctx, "dev-a", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, filtered, 2)
	require.Equal(t, "dev-a", filtered[0].DeviceID)

	page, total, err := db.ListAlarms(ctx, "dev-a", 1, 1)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, page, 1)

	empty, total, err := db.ListAlarms(ctx, "missing", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 0, total)
	require.Empty(t, empty)
}
