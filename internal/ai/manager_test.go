package ai

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/ai/multimodal"
	"github.com/lalmax-pro/lalmax-nvr/internal/ai/webhook"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/stretchr/testify/require"
)

type memoryStore struct {
	mu           sync.Mutex
	detections   []DetectionResult
	analyses     []multimodal.AnalysisResult
	detectionErr error
	analysisErr  error
	detected     chan struct{}
	analyzed     chan struct{}
}

func (s *memoryStore) InsertAIDetection(_ context.Context, result DetectionResult) error {
	s.mu.Lock()
	s.detections = append(s.detections, result)
	s.mu.Unlock()
	if s.detected != nil {
		select {
		case s.detected <- struct{}{}:
		default:
		}
	}
	return s.detectionErr
}

func (s *memoryStore) InsertAIAnalysis(_ context.Context, result interface{}) error {
	if analysis, ok := result.(multimodal.AnalysisResult); ok {
		s.mu.Lock()
		s.analyses = append(s.analyses, analysis)
		s.mu.Unlock()
	}
	if s.analyzed != nil {
		select {
		case s.analyzed <- struct{}{}:
		default:
		}
	}
	return s.analysisErr
}

type blockingAnalyzer struct {
	mu      sync.Mutex
	n       int
	started chan struct{}
	release chan struct{}
	err     error
}

func (a *blockingAnalyzer) Analyze(ctx context.Context, _ []byte, _ string) (*multimodal.AnalysisResult, error) {
	a.mu.Lock()
	a.n++
	a.mu.Unlock()
	if a.started != nil {
		select {
		case a.started <- struct{}{}:
		default:
		}
	}
	if a.release != nil {
		select {
		case <-a.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if a.err != nil {
		return nil, a.err
	}
	return &multimodal.AnalysisResult{Analysis: "人员 x1 正常", Labels: []string{"人员"}}, nil
}

func (a *blockingAnalyzer) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}

func (a *blockingAnalyzer) IsAvailable() bool { return true }
func (a *blockingAnalyzer) Close() error      { return nil }

func enabledConfig() config.AIConfig {
	return config.AIConfig{
		Enabled:            true,
		Backend:            "webhook",
		InferenceTimeoutMs: 200,
		Multimodal: &config.AIMultimodalConfig{
			Enabled:          true,
			Provider:         "local",
			AnalysisPrompt:   "描述风险",
			AnalysisInterval: "1h",
			Providers: map[string]config.AIProviderConfig{
				"local": {Provider: "openai", APIKey: "key", Endpoint: "http://127.0.0.1:9", Model: "vision"},
			},
		},
	}
}

func imageDataURL() string {
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("jpeg"))
}

func postDetection(t *testing.T, manager *Manager, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/ai/detect", strings.NewReader(body))
	rec := httptest.NewRecorder()
	manager.GetReceiver().HandleHTTP()(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestDisabledManager(t *testing.T) {
	manager := NewManager(config.AIConfig{})
	require.Nil(t, manager.GetReceiver())
	require.Nil(t, manager.GetMultimodalManager())
	status := manager.Status()
	require.Equal(t, "disabled", status.Backend)
	require.False(t, status.Available)
	manager.StopAll()
}

func TestEnabledManagerWithoutMultimodal(t *testing.T) {
	manager := NewManager(config.AIConfig{Enabled: true})
	require.NotNil(t, manager.GetReceiver())
	require.Nil(t, manager.GetMultimodalManager())
	status := manager.Status()
	require.Equal(t, "webhook", status.Backend)
	require.True(t, status.Available)
	require.Empty(t, status.Reason)

	store := &memoryStore{detected: make(chan struct{}, 1), detectionErr: errors.New("db down")}
	manager.SetStore(store)
	seen := make(chan webhook.DetectionResult, 1)
	id := manager.OnDetection(func(result webhook.DetectionResult) { seen <- result })
	require.False(t, manager.UnregisterCallback("missing"))

	track := 4
	postDetection(t, manager, `{"camera_id":"cam","pts":3,"timestamp":8,"detections":[{"label":"car","confidence":0.5,"box":[0,0,1,1],"track_id":4},{"label":"car","confidence":0.4,"box":[0,0,1,1]}]}`)
	select {
	case result := <-seen:
		require.Equal(t, "cam", result.CameraID)
		require.Equal(t, &track, result.Detections[0].TrackID)
	case <-time.After(time.Second):
		t.Fatal("manager callback was not called")
	}
	select {
	case <-store.detected:
	case <-time.After(time.Second):
		t.Fatal("detection was not persisted")
	}
	require.True(t, manager.UnregisterCallback(id))
	manager.StopAll()
}

func TestMultimodalAnalysisPersistsAndRespectsCooldown(t *testing.T) {
	manager := NewManager(enabledConfig())
	analyzer := &blockingAnalyzer{}
	manager.GetMultimodalManager().RegisterAnalyzer("local", analyzer)
	require.NoError(t, manager.GetMultimodalManager().SetProvider("local"))
	status := manager.Status()
	require.Contains(t, status.Reason, "多模态分析已启用")

	store := &memoryStore{analyzed: make(chan struct{}, 1), analysisErr: errors.New("analysis db")}
	manager.SetStore(store)
	body := `{"camera_id":"cam","pts":11,"image_url":"` + imageDataURL() + `","detections":[{"label":"person","confidence":0.8,"box":[0,0,1,1]}]}`
	postDetection(t, manager, body)
	select {
	case <-store.analyzed:
	case <-time.After(time.Second):
		t.Fatal("analysis was not persisted")
	}
	store.mu.Lock()
	require.Equal(t, "cam", store.analyses[0].CameraID)
	require.Equal(t, "webhook", store.analyses[0].Metadata["trigger"])
	require.Contains(t, store.analyses[0].Metadata["objects"], "person x1")
	store.mu.Unlock()

	postDetection(t, manager, body)
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 1, analyzer.calls())

	require.False(t, manager.analysisCoolingDown(""))
	manager.cfg.Multimodal.AnalysisInterval = "not-a-duration"
	require.False(t, manager.analysisCoolingDown("other"))
	require.True(t, manager.analysisCoolingDown("other"))
	manager.StopAll()
}

func TestMultimodalAnalysisFailureAndInvalidImage(t *testing.T) {
	cfg := enabledConfig()
	cfg.InferenceTimeoutMs = 0
	cfg.Multimodal.AnalysisPrompt = ""
	cfg.Multimodal.AnalysisInterval = "1ms"
	manager := NewManager(cfg)
	analyzer := &blockingAnalyzer{err: errors.New("model down")}
	manager.GetMultimodalManager().RegisterAnalyzer("local", analyzer)
	require.NoError(t, manager.GetMultimodalManager().SetProvider("local"))

	postDetection(t, manager, `{"camera_id":"cam","image_url":"`+imageDataURL()+`","detections":[{"label":"person","confidence":1,"box":[0,0,1,1]}]}`)
	require.Eventually(t, func() bool { return analyzer.calls() == 1 }, time.Second, 10*time.Millisecond)

	postDetection(t, manager, `{"camera_id":"cam-2","image_url":"data:image/jpeg","detections":[{"label":"person","confidence":1,"box":[0,0,1,1]}]}`)
	postDetection(t, manager, `{"camera_id":"cam-3","image_url":"http://example/a.jpg","detections":[{"label":"person","confidence":1,"box":[0,0,1,1]}]}`)
	time.Sleep(30 * time.Millisecond)

	_, err := decodeImageURL("")
	require.Error(t, err)
	require.Equal(t, "无", detectionSummary(nil))
	manager.persistAnalysis(multimodal.AnalysisResult{CameraID: "none"})
	manager.StopAll()
}

func TestMultimodalQueueDropsWhenFull(t *testing.T) {
	manager := NewManager(enabledConfig())
	release := make(chan struct{})
	analyzer := &blockingAnalyzer{started: make(chan struct{}, 4), release: release}
	manager.GetMultimodalManager().RegisterAnalyzer("local", analyzer)
	require.NoError(t, manager.GetMultimodalManager().SetProvider("local"))

	for i, camera := range []string{"a", "b", "c"} {
		postDetection(t, manager, `{"camera_id":"`+camera+`","image_url":"`+imageDataURL()+`","detections":[{"label":"person","confidence":1,"box":[0,0,1,1]}]}`)
		if i < 2 {
			select {
			case <-analyzer.started:
			case <-time.After(time.Second):
				t.Fatal("analysis did not start")
			}
		}
	}
	time.Sleep(30 * time.Millisecond)
	require.Equal(t, 2, analyzer.calls())
	close(release)
	manager.StopAll()
}

func TestBrokenMultimodalProviderStillStarts(t *testing.T) {
	manager := NewManager(config.AIConfig{
		Enabled: true,
		Backend: "browser",
		Multimodal: &config.AIMultimodalConfig{
			Enabled:  true,
			Provider: "missing",
			Providers: map[string]config.AIProviderConfig{
				"broken": {Provider: "not-a-provider"},
			},
		},
	})
	require.NotNil(t, manager.GetMultimodalManager())
	require.False(t, manager.GetMultimodalManager().IsAvailable())
	status := manager.Status()
	require.Equal(t, "browser", status.Backend)
	require.Empty(t, status.Reason)
	manager.triggerMultimodalAnalysis(webhook.DetectionResult{})
	manager.StopAll()
}
