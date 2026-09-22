package api

import (
	"net/http/httptest"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/stretchr/testify/require"
)

func TestStreamDisplayName_PrefersExplicitName(t *testing.T) {
	require.Equal(t, "会议室", streamDisplayName(streamSummary{StreamID: "room-1", Name: "会议室"}))
	require.Equal(t, "Front Door", streamDisplayName(streamSummary{StreamID: "cam-1", CameraName: "Front Door", Managed: true}))
	require.Equal(t, "obs-1", streamDisplayName(streamSummary{StreamID: "obs-1"}))
}

func TestFilterStreamSummaries_SearchByCameraName(t *testing.T) {
	items := []streamSummary{
		{StreamID: "cam-1", CameraName: "Front Door", Managed: true},
		{StreamID: "obs-room", Managed: false},
	}

	filtered := filterStreamSummaries(items, "front", nil)
	require.Len(t, filtered, 1)
	require.Equal(t, "cam-1", filtered[0].StreamID)
}

func TestFilterStreamSummaries_ManagedOnly(t *testing.T) {
	items := []streamSummary{
		{StreamID: "cam-1", Managed: true},
		{StreamID: "obs-room", Managed: false},
	}

	managed := true
	filtered := filterStreamSummaries(items, "", &managed)
	require.Len(t, filtered, 1)
	require.True(t, filtered[0].Managed)
}

func TestPaginateStreamSummaries(t *testing.T) {
	items := []streamSummary{
		{StreamID: "a"},
		{StreamID: "b"},
		{StreamID: "c"},
	}

	page, total := paginateStreamSummaries(items, 2, 1)
	require.Equal(t, 3, total)
	require.Len(t, page, 2)
	require.Equal(t, "b", page[0].StreamID)
	require.Equal(t, "c", page[1].StreamID)
}

func TestParseStreamListParams(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/streams?q=lobby&managed=true&limit=5&offset=10", nil)
	search, managedFilter, limit, offset := parseStreamListParams(req)

	require.Equal(t, "lobby", search)
	require.NotNil(t, managedFilter)
	require.True(t, *managedFilter)
	require.Equal(t, 5, limit)
	require.Equal(t, 10, offset)
}

func TestSessionStatusesFromInfo_OmitsInternalRecorder(t *testing.T) {
	t.Helper()
	t.Parallel()

	got := sessionStatusesFromInfo([]media.SessionInfo{
		{SessionID: "nvr-record-cam-1-1", Protocol: "NVR-RECORD", Remote: "in-process"},
		{SessionID: "sub-hls", Protocol: "hls", Remote: "10.0.0.2"},
	})
	require.Len(t, got, 1)
	require.Equal(t, "hls", got[0].Protocol)

	require.Nil(t, sessionStatusesFromInfo([]media.SessionInfo{
		{SessionID: "nvr-record-only", Protocol: "nvr-record"},
	}))
}
