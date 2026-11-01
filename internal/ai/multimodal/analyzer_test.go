package multimodal

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAICompatibleAnalyzer(t *testing.T) {
	var sawAuth, sawImage bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization") == "Bearer secret"
		body, _ := io.ReadAll(r.Body)
		sawImage = strings.Contains(string(body), "data:image/jpeg;base64,")
		switch r.URL.Path {
		case "/ok/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"人员活动正常，无异常"}}]}`)
		case "/status/chat/completions":
			http.Error(w, "nope", http.StatusBadGateway)
		case "/bad-json/chat/completions":
			_, _ = io.WriteString(w, `{`)
		case "/api-error/chat/completions":
			_, _ = io.WriteString(w, `{"error":{"message":"quota"}}`)
		case "/empty/chat/completions":
			_, _ = io.WriteString(w, `{"choices":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	analyzer := NewOpenAICompatibleAnalyzer(ProviderConfig{Provider: "qwen", APIKey: "secret", Endpoint: server.URL + "/ok", VisionModel: "qwen-vl", Timeout: 2, MaxTokens: 10, Temperature: 0.2})
	analyzer.client = server.Client()
	require.True(t, analyzer.IsAvailable())
	result, err := analyzer.Analyze(context.Background(), []byte("jpeg"), "")
	require.NoError(t, err)
	require.Contains(t, result.Analysis, "人员")
	require.Equal(t, []string{"人员", "异常"}, result.Labels)
	require.Equal(t, "qwen", result.Metadata["provider"])
	require.Equal(t, "qwen-vl", result.Metadata["model"])
	require.True(t, sawAuth)
	require.True(t, sawImage)
	require.NoError(t, analyzer.Close())
	_, err = analyzer.Analyze(context.Background(), []byte("jpeg"), "again")
	require.ErrorContains(t, err, "closed")

	missingKey := NewOpenAICompatibleAnalyzer(ProviderConfig{Provider: "openai"})
	require.Equal(t, "https://api.openai.com/v1", missingKey.cfg.Endpoint)
	require.False(t, missingKey.IsAvailable())
	_, err = missingKey.Analyze(context.Background(), nil, "prompt")
	require.ErrorContains(t, err, "API key")

	missingEndpoint := NewOpenAICompatibleAnalyzer(ProviderConfig{Provider: "custom", APIKey: "secret"})
	missingEndpoint.cfg.Endpoint = ""
	_, err = missingEndpoint.Analyze(context.Background(), nil, "prompt")
	require.ErrorContains(t, err, "endpoint")

	missingModel := NewOpenAICompatibleAnalyzer(ProviderConfig{APIKey: "secret", Endpoint: server.URL})
	require.Equal(t, "openai-compatible", missingModel.providerName())
	require.False(t, missingModel.IsAvailable())
	_, err = missingModel.Analyze(context.Background(), nil, "prompt")
	require.ErrorContains(t, err, "model")

	for _, path := range []string{"/status", "/bad-json", "/api-error", "/empty"} {
		client := NewOpenAICompatibleAnalyzer(ProviderConfig{Provider: "openai", APIKey: "secret", Endpoint: server.URL + path, Model: "gpt"})
		client.client = server.Client()
		_, err = client.Analyze(context.Background(), []byte("jpeg"), "prompt")
		require.Error(t, err, path)
	}

	offline := NewOpenAICompatibleAnalyzer(ProviderConfig{APIKey: "secret", Endpoint: "http://127.0.0.1:1", Model: "gpt", Timeout: 1})
	_, err = offline.Analyze(context.Background(), []byte("jpeg"), "prompt")
	require.ErrorContains(t, err, "request failed")

	badURL := NewOpenAICompatibleAnalyzer(ProviderConfig{APIKey: "secret", Endpoint: "http://[::1", Model: "gpt"})
	_, err = badURL.Analyze(context.Background(), []byte("jpeg"), "prompt")
	require.ErrorContains(t, err, "create request")
}

func TestDeepSeekAnalyzer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer secret" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		case "/v1/chat/completions":
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"车辆经过，安全"}}]}`)
		case "/err/chat/completions":
			http.Error(w, "busy", http.StatusInternalServerError)
		case "/json/chat/completions":
			_, _ = io.WriteString(w, `{`)
		case "/api/chat/completions":
			_, _ = io.WriteString(w, `{"error":{"message":"rejected"}}`)
		case "/empty/chat/completions":
			_, _ = io.WriteString(w, `{"choices":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	analyzer := NewDeepSeekAnalyzer(DeepSeekConfig{ProviderConfig: ProviderConfig{APIKey: "secret", Endpoint: server.URL + "/v1", Timeout: 2, MaxTokens: 8, Temperature: 0.1}, VisionModel: "deepseek-vl"})
	analyzer.client = server.Client()
	require.True(t, analyzer.IsAvailable())
	result, err := analyzer.Analyze(context.Background(), []byte("jpeg"), "")
	require.NoError(t, err)
	require.Equal(t, []string{"车辆", "安全"}, result.Labels)
	require.Equal(t, "deepseek", result.Metadata["provider"])
	require.Equal(t, "deepseek-vl", result.Metadata["model"])
	require.NoError(t, analyzer.Close())
	_, err = analyzer.Analyze(context.Background(), []byte("jpeg"), "again")
	require.ErrorContains(t, err, "closed")

	defaults := NewDeepSeekAnalyzer(DeepSeekConfig{})
	require.Equal(t, "https://api.deepseek.com/v1", defaults.cfg.Endpoint)
	require.False(t, defaults.IsAvailable())
	_, err = defaults.Analyze(context.Background(), nil, "prompt")
	require.ErrorContains(t, err, "API key")

	unreachable := NewDeepSeekAnalyzer(DeepSeekConfig{ProviderConfig: ProviderConfig{APIKey: "secret", Endpoint: "http://127.0.0.1:1", Timeout: 1}})
	unreachable.client.Timeout = 0
	require.False(t, unreachable.IsAvailable())

	denied := NewDeepSeekAnalyzer(DeepSeekConfig{ProviderConfig: ProviderConfig{APIKey: "wrong", Endpoint: server.URL + "/v1"}})
	denied.client = server.Client()
	require.False(t, denied.IsAvailable())

	for _, path := range []string{"/err", "/json", "/api", "/empty"} {
		client := NewDeepSeekAnalyzer(DeepSeekConfig{ProviderConfig: ProviderConfig{APIKey: "secret", Endpoint: server.URL + path, Model: "deepseek-chat"}})
		client.client = server.Client()
		_, err = client.Analyze(context.Background(), []byte("jpeg"), "prompt")
		require.Error(t, err, path)
	}

	offline := NewDeepSeekAnalyzer(DeepSeekConfig{ProviderConfig: ProviderConfig{APIKey: "secret", Endpoint: "http://127.0.0.1:1", Model: "deepseek-chat", Timeout: 1}})
	_, err = offline.Analyze(context.Background(), []byte("jpeg"), "prompt")
	require.ErrorContains(t, err, "request failed")

	badURL := NewDeepSeekAnalyzer(DeepSeekConfig{ProviderConfig: ProviderConfig{APIKey: "secret", Endpoint: "http://[::1", Model: "chat"}})
	_, err = badURL.Analyze(context.Background(), []byte("jpeg"), "prompt")
	require.ErrorContains(t, err, "create request")

	badHealthURL := NewDeepSeekAnalyzer(DeepSeekConfig{ProviderConfig: ProviderConfig{APIKey: "secret", Endpoint: "http://[::1"}})
	require.False(t, badHealthURL.IsAvailable())
}
