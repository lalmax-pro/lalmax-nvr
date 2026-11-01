package streamhistory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	lalmaxserver "github.com/q191201771/lalmax/server"
	"github.com/stretchr/testify/require"
)

type errHistoryRepo struct {
	historyRepoFake
	insertErr error
}

func (f *errHistoryRepo) InsertStreamHistory(ctx context.Context, history *storage.StreamHistory) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	return f.historyRepoFake.InsertStreamHistory(ctx, history)
}

func TestClassifyStopReasonAndInternalHistory(t *testing.T) {
	reason, detail := classifyStopReason(pubStopPayload{Reason: "kicked", Message: "bye"}, "")
	require.Equal(t, "kicked", reason)
	require.Equal(t, "bye", detail)
	reason, detail = classifyStopReason(pubStopPayload{Error: "timeout"}, "")
	require.Equal(t, "upstream_error", reason)
	require.Equal(t, "timeout", detail)
	reason, detail = classifyStopReason(pubStopPayload{Message: "reset"}, "")
	require.Equal(t, "upstream_error", reason)
	require.Equal(t, "reset", detail)
	reason, detail = classifyStopReason(pubStopPayload{}, "")
	require.Equal(t, "unknown", reason)
	require.Empty(t, detail)
	reason, detail = classifyStopReason(pubStopPayload{}, "publisher_stopped")
	require.Equal(t, "publisher_stopped", reason)

	require.True(t, isIPTVStreamID(" IPTV_channel "))
	require.False(t, isIPTVStreamID("cam1"))
	require.True(t, isInternalHLSHistory("iptv_a", "customize"))
	require.True(t, isInternalHLSHistory("emby_a", "CUSTOMIZE"))
	require.False(t, isInternalHLSHistory("iptv_a", "rtmp"))
	require.False(t, isInternalHLSHistory("cam1", "customize"))
}

func TestInsertStartOnceDedupesAndReleasesOnError(t *testing.T) {
	repo := &errHistoryRepo{}
	manager := NewManager(repo, nil)
	history := &storage.StreamHistory{StreamID: "cam", SessionID: "sess-1", Protocol: "rtmp", StartedAt: time.Now()}
	require.NoError(t, manager.insertStartOnce(history))
	require.Len(t, repo.rows, 1)
	require.NoError(t, manager.insertStartOnce(history))
	require.Len(t, repo.rows, 1)

	repo.insertErr = errors.New("db down")
	other := &storage.StreamHistory{StreamID: "cam", SessionID: "sess-2", Protocol: "rtmp", StartedAt: time.Now()}
	require.ErrorContains(t, manager.insertStartOnce(other), "db down")
	manager.forgetActive("cam", "", "rtmp")
	manager.forgetActive("cam", "sess-2", "rtmp")
	repo.insertErr = nil
	require.NoError(t, manager.insertStartOnce(other))
	require.Len(t, repo.rows, 2)
}

func TestHookPluginRecordsStartStopAndMediaState(t *testing.T) {
	repo := &historyRepoFake{}
	manager := NewManager(repo, nil)
	plugin := &hookPlugin{mgr: manager}
	require.Equal(t, "stream-history", plugin.Name())

	startAt := time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC)
	startPayload, err := json.Marshal(map[string]string{
		"stream_name": "cam1", "app_name": "live", "protocol": "rtmp", "session_id": "s1",
	})
	require.NoError(t, err)
	require.NoError(t, plugin.OnHookEvent(lalmaxserver.HookEvent{
		Event:     lalmaxserver.HookEventPubStart,
		Timestamp: startAt.Format(time.RFC3339Nano),
		Payload:   startPayload,
	}))
	require.Len(t, repo.rows, 1)
	require.Equal(t, "cam1", repo.rows[0].StreamID)
	require.Equal(t, "s1", repo.rows[0].SessionID)

	stopPayload, err := json.Marshal(map[string]string{
		"stream_name": "cam1", "app_name": "live", "protocol": "rtmp", "session_id": "s1", "reason": "kicked",
	})
	require.NoError(t, err)
	require.NoError(t, plugin.OnHookEvent(lalmaxserver.HookEvent{
		Event:     lalmaxserver.HookEventPubStop,
		Timestamp: startAt.Add(time.Minute).Format(time.RFC3339Nano),
		Payload:   stopPayload,
	}))
	require.NotNil(t, repo.rows[0].EndedAt)
	require.Equal(t, "kicked", repo.rows[0].OfflineReason)

	require.NoError(t, plugin.OnHookEvent(lalmaxserver.HookEvent{
		Event:   lalmaxserver.HookEventStreamActive,
		Payload: []byte(`{"stream_name":"iptv_live"}`),
	}))
	require.NoError(t, plugin.OnHookEvent(lalmaxserver.HookEvent{
		Event:   lalmaxserver.HookEventGroupStop,
		Payload: []byte(`{"stream_name":"iptv_live"}`),
	}))
	require.GreaterOrEqual(t, len(repo.rows), 2)
	require.Equal(t, startAt, hookEventAt(lalmaxserver.HookEvent{Timestamp: startAt.Format(time.RFC3339Nano)}))
	require.False(t, hookEventAt(lalmaxserver.HookEvent{Timestamp: "not-a-time"}).IsZero())
}

func TestHandlePubStartSkipsInternalHLS(t *testing.T) {
	repo := &historyRepoFake{}
	manager := NewManager(repo, nil)
	internal, err := json.Marshal(map[string]string{
		"stream_name": "iptv_skip", "protocol": "customize", "session_id": "s",
	})
	require.NoError(t, err)
	manager.handlePubStart(media.Event{StreamID: "iptv_skip", Protocol: "customize", SessionID: "s", Raw: internal})
	manager.handlePubStart(media.Event{Raw: []byte("not-json")})
	require.Empty(t, repo.rows)
}
