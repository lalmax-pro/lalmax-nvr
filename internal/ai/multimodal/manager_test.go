package multimodal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeAnalyzer struct {
	available bool
	result    *AnalysisResult
	err       error
	closeErr  error
	calls     int
}

func (a *fakeAnalyzer) Analyze(context.Context, []byte, string) (*AnalysisResult, error) {
	a.calls++
	if a.err != nil {
		return nil, a.err
	}
	copyResult := *a.result
	return &copyResult, nil
}

func (a *fakeAnalyzer) IsAvailable() bool { return a.available }
func (a *fakeAnalyzer) Close() error      { return a.closeErr }

func TestManagerRoutesAnalysisAndCallbacks(t *testing.T) {
	manager := NewManager()
	require.False(t, manager.IsAvailable())
	require.Error(t, manager.SetProvider("missing"))
	_, err := manager.Analyze(context.Background(), []byte("frame"), "prompt")
	require.Error(t, err)
	_, err = manager.AnalyzeWithProvider(context.Background(), "missing", nil, "")
	require.Error(t, err)

	unavailable := &fakeAnalyzer{}
	ready := &fakeAnalyzer{available: true, result: &AnalysisResult{Analysis: "人员正常"}}
	failing := &fakeAnalyzer{available: true, err: errors.New("upstream")}
	manager.RegisterAnalyzer("down", unavailable)
	manager.RegisterAnalyzer("ready", ready)
	manager.RegisterAnalyzer("failing", failing)

	require.NoError(t, manager.SetProvider("down"))
	require.Equal(t, "down", manager.GetProvider())
	require.False(t, manager.IsAvailable())
	_, err = manager.Analyze(context.Background(), []byte("frame"), "prompt")
	require.ErrorContains(t, err, "not available")
	_, err = manager.AnalyzeWithProvider(context.Background(), "down", []byte("frame"), "prompt")
	require.ErrorContains(t, err, "not available")

	require.NoError(t, manager.SetProvider("failing"))
	_, err = manager.Analyze(context.Background(), []byte("frame"), "prompt")
	require.EqualError(t, err, "upstream")
	_, err = manager.AnalyzeWithProvider(context.Background(), "failing", []byte("frame"), "prompt")
	require.EqualError(t, err, "upstream")

	require.NoError(t, manager.SetProvider("ready"))
	require.True(t, manager.IsAvailable())
	got, err := manager.Analyze(context.Background(), []byte("frame"), "prompt")
	require.NoError(t, err)
	require.Equal(t, "人员正常", got.Analysis)
	got, err = manager.AnalyzeWithProvider(context.Background(), "ready", []byte("frame"), "prompt")
	require.NoError(t, err)
	require.Equal(t, 2, ready.calls)

	status := manager.Status()
	require.Equal(t, "ready", status["active_provider"])
	providers := status["providers"].(map[string]bool)
	require.True(t, providers["ready"])
	require.False(t, providers["down"])

	results := make(chan AnalysisResult, 1)
	id := manager.OnResult(func(result AnalysisResult) { results <- result })
	require.False(t, manager.UnregisterCallback("missing"))
	manager.PublishResult(AnalysisResult{Analysis: "done"})
	select {
	case result := <-results:
		require.Equal(t, "done", result.Analysis)
	case <-time.After(time.Second):
		t.Fatal("result callback was not called")
	}
	require.True(t, manager.UnregisterCallback(id))

	failing.closeErr = errors.New("close failed")
	manager.Close()
	require.Empty(t, manager.analyzers)
	require.False(t, manager.IsAvailable())
}

func TestCreateAnalyzerSelectsProvider(t *testing.T) {
	deepseek, err := CreateAnalyzer(ProviderConfig{Provider: "deepseek", APIKey: "key"})
	require.NoError(t, err)
	require.IsType(t, &DeepSeekAnalyzer{}, deepseek)

	for _, provider := range []string{"openai", "qwen", "custom", "openai-compatible"} {
		analyzer, err := CreateAnalyzer(ProviderConfig{Provider: provider, APIKey: "key", Endpoint: "http://example", Model: "vision"})
		require.NoError(t, err, provider)
		require.IsType(t, &OpenAICompatibleAnalyzer{}, analyzer)
	}

	fallback, err := CreateAnalyzer(ProviderConfig{Provider: "gateway", Endpoint: "http://example", Model: "vision"})
	require.NoError(t, err)
	require.IsType(t, &OpenAICompatibleAnalyzer{}, fallback)

	_, err = CreateAnalyzer(ProviderConfig{Provider: "unknown"})
	require.ErrorContains(t, err, "unsupported provider")
}

func TestExtractLabels(t *testing.T) {
	require.Equal(t, []string{"人员", "异常"}, extractLabels("画面有人员，并存在异常"))
	require.Empty(t, extractLabels("quiet"))
	require.Empty(t, extractLabels(""))
	require.True(t, contains("abc", "abc"))
	require.False(t, contains("ab", "abc"))
}
