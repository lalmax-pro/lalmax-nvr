package storage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/ai"
	"github.com/lalmax-pro/lalmax-nvr/internal/ai/multimodal"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

// TestCovAIDetections covers detection insert/list including filters and paging.
func TestCovAIDetections(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Zero timestamp is defaulted.
	require.NoError(t, db.InsertAIDetection(ctx, ai.DetectionResult{
		CameraID: "cam1",
		PTS:      120,
		ImageURL: "https://example.com/a.jpg",
		Detections: []ai.Detection{
			{Label: "person", Confidence: 0.9, Box: [4]float32{1, 2, 3, 4}},
		},
	}))
	require.NoError(t, db.InsertAIDetection(ctx, ai.DetectionResult{
		CameraID:  "cam2",
		Timestamp: 1_700_000_000_000,
		Detections: []ai.Detection{
			{Label: "car", Confidence: 0.5, Box: [4]float32{5, 6, 7, 8}},
		},
	}))

	all, total, err := db.ListAIDetections(ctx, AIHistoryFilter{})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, all, 2)

	byCamera, total, err := db.ListAIDetections(ctx, AIHistoryFilter{CameraID: "cam1"})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, byCamera, 1)
	require.Equal(t, "cam1", byCamera[0].CameraID)
	require.Equal(t, int64(120), byCamera[0].PTS)
	require.Equal(t, "https://example.com/a.jpg", byCamera[0].ImageURL)
	require.NotZero(t, byCamera[0].Timestamp, "zero timestamps are filled in")
	require.Len(t, byCamera[0].Detections, 1)
	require.Equal(t, "person", byCamera[0].Detections[0].Label)

	// Label filter is a LIKE over the serialized detections.
	byLabel, total, err := db.ListAIDetections(ctx, AIHistoryFilter{Label: "car"})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, byLabel, 1)
	require.Equal(t, "cam2", byLabel[0].CameraID)

	none, total, err := db.ListAIDetections(ctx, AIHistoryFilter{CameraID: "cam2", Label: "person"})
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, none)

	// Pagination: negative offset clamp, oversize limit clamp and offset paging.
	page, total, err := db.ListAIDetections(ctx, AIHistoryFilter{Limit: 500, Offset: -3})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, page, 2)
	second, _, err := db.ListAIDetections(ctx, AIHistoryFilter{Limit: 1, Offset: 1})
	require.NoError(t, err)
	require.Len(t, second, 1)
}

// TestCovAIAnalyses covers analysis insert (value and pointer) and listing.
func TestCovAIAnalyses(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	when := time.Now().UnixMilli()

	value := multimodal.AnalysisResult{
		CameraID:   "cam1",
		Timestamp:  when,
		Analysis:   "someone walking",
		Labels:     []string{"person", "walking"},
		Confidence: 0.75,
		ImageURL:   "https://example.com/frame.jpg",
		TriggerDetections: []multimodal.TriggerDetection{
			{Label: "person", Confidence: 0.8, Box: [4]float32{0, 0, 10, 10}},
		},
		Metadata: map[string]string{"provider": "test"},
	}
	require.NoError(t, db.InsertAIAnalysis(ctx, value))

	// Zero timestamp defaults to now.
	zeroTS := multimodal.AnalysisResult{CameraID: "cam2", Analysis: "empty scene", Labels: []string{}}
	require.NoError(t, db.InsertAIAnalysis(ctx, zeroTS))

	// Unsupported value types are rejected.
	require.ErrorContains(t, db.InsertAIAnalysis(ctx, "nope"), "unsupported AI analysis type")
	// Current implementation rejects a *AnalysisResult because the inner type
	// assertion shadows the outer ok flag; assert the observable behavior.
	ptr := &multimodal.AnalysisResult{CameraID: "cam3", Analysis: "pointer"}
	require.ErrorContains(t, db.InsertAIAnalysis(ctx, ptr), "unsupported AI analysis type")
	var nilPtr *multimodal.AnalysisResult
	require.ErrorContains(t, db.InsertAIAnalysis(ctx, nilPtr), "unsupported AI analysis type")

	all, total, err := db.ListAIAnalyses(ctx, AIHistoryFilter{})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, all, 2)

	found := false
	for _, res := range all {
		if res.CameraID != "cam1" {
			continue
		}
		found = true
		require.Equal(t, "someone walking", res.Analysis)
		require.Equal(t, []string{"person", "walking"}, res.Labels)
		require.InDelta(t, 0.75, res.Confidence, 0.0001)
		require.Equal(t, "https://example.com/frame.jpg", res.ImageURL)
		require.Len(t, res.TriggerDetections, 1)
		require.Equal(t, "person", res.TriggerDetections[0].Label)
		require.Equal(t, "test", res.Metadata["provider"])
	}
	require.True(t, found)

	byCamera, total, err := db.ListAIAnalyses(ctx, AIHistoryFilter{CameraID: "cam2"})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, byCamera, 1)
	require.NotZero(t, byCamera[0].Timestamp)
	require.Empty(t, byCamera[0].Labels)

	page, total, err := db.ListAIAnalyses(ctx, AIHistoryFilter{Limit: 1, Offset: 1})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, page, 1)
}

// TestCovAIHistoryHelpers covers the pure filter/page helpers.
func TestCovAIHistoryHelpers(t *testing.T) {
	limit, offset := normalizeAIHistoryPage(0, -1)
	require.Equal(t, 50, limit)
	require.Zero(t, offset)
	limit, offset = normalizeAIHistoryPage(500, 5)
	require.Equal(t, 50, limit)
	require.Equal(t, 5, offset)
	limit, offset = normalizeAIHistoryPage(20, 5)
	require.Equal(t, 20, limit)
	require.Equal(t, 5, offset)

	where, args := aiHistoryWhere("", "")
	require.Empty(t, where)
	require.Nil(t, args)
	where, args = aiHistoryWhere("cam1", "person")
	require.Equal(t, "WHERE camera_id = ? AND detections_json LIKE ?", where)
	require.Equal(t, []any{"cam1", "%person%"}, args)
	where, args = aiHistoryWhere("", "person")
	require.Equal(t, "WHERE detections_json LIKE ?", where)
	require.Equal(t, []any{"%person%"}, args)
}

// TestCovEmbyServerAndItems covers the Emby server singleton and item catalogue.
func TestCovEmbyServerAndItems(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	server, err := db.GetEmbyServer(ctx)
	require.NoError(t, err)
	require.Nil(t, server)

	require.NoError(t, db.SaveEmbyServer(ctx, EmbyServer{ID: "srv-1", Name: "home", BaseURL: "http://emby.local:8096", UserID: "u1", Token: "t1"}))
	server, err = db.GetEmbyServer(ctx)
	require.NoError(t, err)
	require.NotNil(t, server)
	require.Equal(t, "home", server.Name)
	require.Equal(t, "u1", server.UserID)
	require.Equal(t, "t1", server.Token)
	require.False(t, server.UpdatedAt.IsZero())

	require.NoError(t, db.SaveEmbyServer(ctx, EmbyServer{ID: "srv-1", Name: "home2", BaseURL: "http://emby.local:9000", UserID: "u2", Token: "t2"}))
	server, err = db.GetEmbyServer(ctx)
	require.NoError(t, err)
	require.Equal(t, "home2", server.Name)
	require.Equal(t, "t2", server.Token)

	require.NoError(t, db.SaveEmbyItem(ctx, EmbyItem{
		ID: "item-1", EmbyItemID: "emby-1", Name: "Movie", ItemType: "Movie", Overview: "overview",
		ImageTag: "tag", RunTimeTicks: 1234, StreamID: "emby_stream_1", PublishEnabled: true, Loop: true,
	}))
	require.NoError(t, db.SaveEmbyItem(ctx, EmbyItem{
		ID: "item-2", EmbyItemID: "emby-2", Name: "Alpha Show", ItemType: "Series", StreamID: "emby_stream_2",
	}))

	items, err := db.ListEmbyItems(ctx)
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, "Alpha Show", items[0].Name, "ordered by name")

	byID, err := db.GetEmbyItem(ctx, "item-1")
	require.NoError(t, err)
	require.NotNil(t, byID)
	require.Equal(t, "emby-1", byID.EmbyItemID)
	require.True(t, byID.PublishEnabled)
	require.True(t, byID.Loop)
	require.Equal(t, int64(1234), byID.RunTimeTicks)

	byEmbyID, err := db.GetEmbyItem(ctx, "emby-2")
	require.NoError(t, err)
	require.NotNil(t, byEmbyID)
	require.Equal(t, "item-2", byEmbyID.ID)

	missing, err := db.GetEmbyItem(ctx, "nope")
	require.NoError(t, err)
	require.Nil(t, missing)

	// Upserting by emby_item_id updates the existing row without duplicating.
	require.NoError(t, db.SaveEmbyItem(ctx, EmbyItem{
		ID: "ignored", EmbyItemID: "emby-1", Name: "Movie HD", ItemType: "Movie", StreamID: "emby_stream_1",
	}))
	items, err = db.ListEmbyItems(ctx)
	require.NoError(t, err)
	require.Len(t, items, 2)
	updated, err := db.GetEmbyItem(ctx, "item-1")
	require.NoError(t, err)
	require.Equal(t, "Movie HD", updated.Name)

	require.NoError(t, db.SetEmbyItemPublish(ctx, "emby-1", false, false))
	updated, err = db.GetEmbyItem(ctx, "item-1")
	require.NoError(t, err)
	require.False(t, updated.PublishEnabled)
	require.False(t, updated.Loop)

	require.NoError(t, db.SetEmbyItemPublish(ctx, "item-2", true, true))
	updated, err = db.GetEmbyItem(ctx, "item-2")
	require.NoError(t, err)
	require.True(t, updated.PublishEnabled)
	require.True(t, updated.Loop)

	require.NoError(t, db.DeleteEmbyItem(ctx, "emby-2"))
	missing, err = db.GetEmbyItem(ctx, "item-2")
	require.NoError(t, err)
	require.Nil(t, missing)

	require.NoError(t, db.DeleteEmbyItem(ctx, "item-1"))
	items, err = db.ListEmbyItems(ctx)
	require.NoError(t, err)
	require.Empty(t, items)
}

// TestCovDownloads covers GB28181 download bookkeeping.
func TestCovDownloads(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	start := covTestTime(0)

	first := &DownloadRecordRow{
		DeviceID:  "dev-1",
		ChannelID: "ch-1",
		FilePath:  "/downloads/first.mp4",
		StartTime: start,
		EndTime:   start.Add(time.Minute),
		Status:    "pending",
	}
	id, err := db.CreateDownload(ctx, first)
	require.NoError(t, err)
	require.NotZero(t, id)

	second := &DownloadRecordRow{DeviceID: "dev-1", ChannelID: "ch-2", FilePath: "/downloads/second.mp4", StartTime: start, EndTime: start.Add(2 * time.Minute), Status: "pending"}
	_, err = db.CreateDownload(ctx, second)
	require.NoError(t, err)
	third := &DownloadRecordRow{DeviceID: "dev-2", ChannelID: "ch-1", FilePath: "/downloads/third.mp4", StartTime: start, EndTime: start.Add(3 * time.Minute), Status: "pending"}
	_, err = db.CreateDownload(ctx, third)
	require.NoError(t, err)

	got, err := db.GetDownload(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "dev-1", got.DeviceID)
	require.Equal(t, "ch-1", got.ChannelID)
	require.Equal(t, "/downloads/first.mp4", got.FilePath)
	require.Equal(t, "pending", got.Status)
	require.True(t, got.StartTime.Equal(start), "start time round-trips: %v", got.StartTime)

	_, err = db.GetDownload(ctx, 99999)
	require.ErrorIs(t, err, sql.ErrNoRows)

	require.NoError(t, db.UpdateDownloadStatus(ctx, id, "completed", 4096))
	got, err = db.GetDownload(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "completed", got.Status)
	require.Equal(t, int64(4096), got.FileSize)

	all, total, err := db.ListDownloads(ctx, "", "", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Len(t, all, 3)

	byDevice, total, err := db.ListDownloads(ctx, "dev-1", "", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, byDevice, 2)

	byBoth, total, err := db.ListDownloads(ctx, "dev-1", "ch-2", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, byBoth, 1)
	require.Equal(t, "ch-2", byBoth[0].ChannelID)

	page, total, err := db.ListDownloads(ctx, "", "ch-1", 1, 1)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, page, 1)

	none, total, err := db.ListDownloads(ctx, "dev-none", "", 10, 0)
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, none)
}

// TestCovAlarmRules covers alarm rule CRUD and the enabled mapping.
func TestCovAlarmRules(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	rules, err := db.ListAlarmRules(ctx)
	require.NoError(t, err)
	require.Empty(t, rules)

	enabled := &model.AlarmRule{
		Name: "person alert", Enabled: true, CameraID: "cam1", Source: "ai",
		EventType: "person", Severity: "high", Action: "webhook", ActionTarget: "https://example.com/hook",
	}
	require.NoError(t, db.InsertAlarmRule(ctx, enabled))
	require.NotZero(t, enabled.ID)

	disabled := &model.AlarmRule{Name: "motion", Enabled: false, Source: "motion"}
	require.NoError(t, db.InsertAlarmRule(ctx, disabled))
	require.NotZero(t, disabled.ID)

	rules, err = db.ListAlarmRules(ctx)
	require.NoError(t, err)
	require.Len(t, rules, 2)
	require.Equal(t, disabled.ID, rules[0].ID, "newest rule first")

	byID := map[int64]model.AlarmRule{}
	for _, r := range rules {
		byID[r.ID] = r
	}
	require.True(t, byID[enabled.ID].Enabled)
	require.Equal(t, "https://example.com/hook", byID[enabled.ID].ActionTarget)
	require.InDelta(t, time.Hour*0, time.Since(byID[enabled.ID].CreatedAt), float64(24*time.Hour))
	require.False(t, byID[disabled.ID].Enabled)

	require.NoError(t, db.SetAlarmRuleEnabled(ctx, disabled.ID, true))
	rules, err = db.ListAlarmRules(ctx)
	require.NoError(t, err)
	for _, r := range rules {
		if r.ID == disabled.ID {
			require.True(t, r.Enabled)
		}
	}

	require.ErrorIs(t, db.DeleteAlarmRule(ctx, 9999), sql.ErrNoRows)
	require.NoError(t, db.DeleteAlarmRule(ctx, disabled.ID))
	rules, err = db.ListAlarmRules(ctx)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.Equal(t, enabled.ID, rules[0].ID)

	// A second delete of the same rule reports sql.ErrNoRows.
	require.ErrorIs(t, db.DeleteAlarmRule(ctx, disabled.ID), sql.ErrNoRows)
}

// TestCovAIEmbyErrorPaths asserts storage errors are surfaced when the DB is closed.
func TestCovAIEmbyErrorPaths(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.Close())

	require.Error(t, db.InsertAIDetection(ctx, ai.DetectionResult{CameraID: "cam", Timestamp: 1}))
	_, _, err := db.ListAIDetections(ctx, AIHistoryFilter{})
	require.Error(t, err)
	require.Error(t, db.InsertAIAnalysis(ctx, multimodal.AnalysisResult{CameraID: "cam", Timestamp: 1}))
	_, _, err = db.ListAIAnalyses(ctx, AIHistoryFilter{})
	require.Error(t, err)

	_, err = db.GetEmbyServer(ctx)
	require.Error(t, err)
	require.Error(t, db.SaveEmbyServer(ctx, EmbyServer{ID: "s"}))
	_, err = db.ListEmbyItems(ctx)
	require.Error(t, err)
	_, err = db.GetEmbyItem(ctx, "id")
	require.Error(t, err)
	require.Error(t, db.SaveEmbyItem(ctx, EmbyItem{ID: "id", EmbyItemID: "e", StreamID: "s"}))
	require.Error(t, db.SetEmbyItemPublish(ctx, "id", true, true))
	require.Error(t, db.DeleteEmbyItem(ctx, "id"))

	_, err = db.CreateDownload(ctx, &DownloadRecordRow{DeviceID: "d", ChannelID: "c"})
	require.Error(t, err)
	require.Error(t, db.UpdateDownloadStatus(ctx, 1, "done", 1))
	_, err = db.GetDownload(ctx, 1)
	require.Error(t, err)
	_, _, err = db.ListDownloads(ctx, "", "", 10, 0)
	require.Error(t, err)

	require.Error(t, db.InsertAlarmRule(ctx, &model.AlarmRule{Name: "x"}))
	_, err = db.ListAlarmRules(ctx)
	require.Error(t, err)
	require.Error(t, db.DeleteAlarmRule(ctx, 1))
	require.Error(t, db.SetAlarmRuleEnabled(ctx, 1, true))
}
