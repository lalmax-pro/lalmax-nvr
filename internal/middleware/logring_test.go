package middleware

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLogRing_SnapshotOldestFirstAndLimit(t *testing.T) {
	t.Helper()
	t.Parallel()

	ring := NewLogRing(4)
	for i := 0; i < 6; i++ {
		ring.Append(LogEntry{Time: time.Unix(int64(i), 0).UTC(), Level: "INFO", Message: "m"})
	}
	got := ring.Snapshot(LogRingFilter{Limit: 10})
	require.Len(t, got, 4)
	require.Equal(t, uint64(3), got[0].Seq)
	require.Equal(t, uint64(6), got[3].Seq)

	got = ring.Snapshot(LogRingFilter{Limit: 2})
	require.Len(t, got, 2)
	require.Equal(t, uint64(5), got[0].Seq)
	require.Equal(t, uint64(6), got[1].Seq)
}

func TestLogRing_FilterLevelAndQuery(t *testing.T) {
	t.Helper()
	t.Parallel()

	ring := NewLogRing(16)
	ring.Append(LogEntry{Level: "INFO", Message: "camera started", Attrs: map[string]any{"camera_id": "cam-1"}})
	ring.Append(LogEntry{Level: "ERROR", Message: "failed to start recorder", Attrs: map[string]any{"camera_id": "cam-2"}})
	ring.Append(LogEntry{Level: "WARN", Message: "reconnect", Attrs: map[string]any{"stream_id": "obs"}})

	errors := ring.Snapshot(LogRingFilter{MinLevel: slog.LevelError, Limit: 20})
	require.Len(t, errors, 1)
	require.Equal(t, "ERROR", errors[0].Level)

	warns := ring.Snapshot(LogRingFilter{MinLevel: slog.LevelWarn, Limit: 20})
	require.Len(t, warns, 2)

	byID := ring.Snapshot(LogRingFilter{Query: "cam-2", Limit: 20})
	require.Len(t, byID, 1)
	require.Equal(t, "failed to start recorder", byID[0].Message)
}

func TestRingHandler_CapturesAttrs(t *testing.T) {
	t.Helper()
	t.Parallel()

	ring := NewLogRing(8)
	logger := slog.New(NewRingHandler(ring, slog.LevelInfo)).With("component", "camera")
	logger.Info("started", "camera_id", "cam-9")
	logger.Debug("hidden")

	got := ring.Snapshot(LogRingFilter{Limit: 10})
	require.Len(t, got, 1)
	require.Equal(t, "started", got[0].Message)
	require.Equal(t, "camera", got[0].Attrs["component"])
	require.Equal(t, "cam-9", got[0].Attrs["camera_id"])
}

func TestLogRing_Subscribe(t *testing.T) {
	t.Helper()
	t.Parallel()

	ring := NewLogRing(4)
	ch := ring.Subscribe()
	defer ring.Unsubscribe(ch)

	ring.Append(LogEntry{Level: "INFO", Message: "hello"})
	select {
	case got := <-ch:
		require.Equal(t, "hello", got.Message)
		require.Equal(t, uint64(1), got.Seq)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live log")
	}
}

func TestRingHandler_Enabled(t *testing.T) {
	t.Helper()
	t.Parallel()

	h := NewRingHandler(NewLogRing(2), slog.LevelWarn)
	require.False(t, h.Enabled(context.Background(), slog.LevelInfo))
	require.True(t, h.Enabled(context.Background(), slog.LevelError))
}
