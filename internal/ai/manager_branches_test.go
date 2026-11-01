package ai

import (
	"strings"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/ai/webhook"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAnalysisCooldownExpiresAndInvalidIntervalsUseDefault(t *testing.T) {
	for _, interval := range []string{"", "invalid", "0s", "-1s", "1s"} {
		t.Run(interval, func(t *testing.T) {
			manager := NewManager(config.AIConfig{Multimodal: &config.AIMultimodalConfig{AnalysisInterval: interval}})
			t.Cleanup(manager.StopAll)
			manager.lastAnalysis["camera"] = time.Now().Add(-time.Hour)
			require.False(t, manager.analysisCoolingDown("camera"), "expired entry must permit analysis")
			require.True(t, manager.analysisCoolingDown("camera"), "new entry must enforce cooldown")
			require.False(t, manager.analysisCoolingDown("other"), "cooldown must be per-camera")
			manager.lastAnalysis["camera"] = time.Now().Add(-5 * time.Second)
			require.Equal(t, interval != "1s", manager.analysisCoolingDown("camera"), "invalid intervals must retain the ten-second default")
		})
	}
}

func TestDetectionSummaryCountsLabelsWithoutMapOrderAssumptions(t *testing.T) {
	summary := detectionSummary([]webhook.Detection{{Label: "person"}, {Label: "car"}, {Label: "person"}})
	require.ElementsMatch(t, []string{"person x2", "car x1"}, strings.Split(summary, "，"))
}

func TestInvalidBase64ReleasesAnalysisSlot(t *testing.T) {
	manager := NewManager(enabledConfig())
	t.Cleanup(manager.StopAll)
	manager.triggerMultimodalAnalysis(webhook.DetectionResult{
		CameraID: "camera", ImageURL: "data:image/jpeg;base64,%%%",
		Detections: []webhook.Detection{{Label: "person"}},
	})
	require.Empty(t, manager.analysisSem, "decode failure must release acquired capacity")
	_, err := decodeImageURL("data:image/jpeg;base64,%%%")
	require.Error(t, err)
}

func TestPersistDetectionPreservesOptionalFields(t *testing.T) {
	manager := NewManager(config.AIConfig{})
	t.Cleanup(manager.StopAll)
	store := &memoryStore{}
	manager.SetStore(store)
	track := 17
	manager.persistDetection(webhook.DetectionResult{
		CameraID: "camera", PTS: 42, Timestamp: 123, ImageURL: "image",
		Detections: []webhook.Detection{
			{Label: "person", Confidence: .9, Box: [4]float32{.1, .2, .3, .4}, TrackID: &track, ZoneID: "door"},
			{Label: "car", Confidence: .8},
		},
	})
	require.Len(t, store.detections, 1)
	got := store.detections[0]
	require.Equal(t, "camera", got.CameraID)
	require.Equal(t, int64(42), got.PTS)
	require.Equal(t, int64(123), got.Timestamp)
	require.Equal(t, "image", got.ImageURL)
	require.Equal(t, Detection{Label: "person", Confidence: .9, Box: [4]float32{.1, .2, .3, .4}, TrackID: &track, ZoneID: "door"}, got.Detections[0])
	require.Nil(t, got.Detections[1].TrackID)
}
