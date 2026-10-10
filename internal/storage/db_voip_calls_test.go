package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVoIPCallHistoryPersistsAndPages(t *testing.T) {
	t.Helper()
	db, err := New(filepath.Join(t.TempDir(), "calls.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	started := time.Date(2026, 10, 10, 1, 2, 3, 0, time.UTC)
	first := VoIPCall{CallID: "call-1", Direction: "outbound", FromUser: "6001", ToUser: "6002", Outcome: "completed",
		StartedAt: started, AnsweredAt: started.Add(time.Second), EndedAt: started.Add(17 * time.Second), DurationSecond: 16,
		Transport: "tcp", AudioCodec: "PCMA", StreamID: "voip_6002_call-1"}
	second := VoIPCall{CallID: "call-2", Direction: "inbound", FromUser: "1001", ToUser: "nvr", Outcome: "missed",
		StartedAt: started.Add(time.Minute), EndedAt: started.Add(2 * time.Minute), FailureReason: "no answer"}
	require.NoError(t, db.SaveVoIPCall(ctx, first))
	require.NoError(t, db.SaveVoIPCall(ctx, second))

	items, total, err := db.ListVoIPCalls(ctx, 1, 0)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, items, 1)
	require.Equal(t, "call-2", items[0].CallID)

	first.Outcome = "failed"
	first.FailureReason = "media timeout"
	require.NoError(t, db.SaveVoIPCall(ctx, first))
	items, total, err = db.ListVoIPCalls(ctx, 10, 1)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, items, 1)
	require.Equal(t, "call-1", items[0].CallID)
	require.Equal(t, "failed", items[0].Outcome)
	require.Equal(t, "media timeout", items[0].FailureReason)
	require.True(t, items[0].AnsweredAt.Equal(first.AnsweredAt))
}
