package multimodal

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type analyzerTransport func(*http.Request) (*http.Response, error)

func (f analyzerTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestAnalyzersRejectNonFiniteTemperatureBeforeHTTP(t *testing.T) {
	cfg := ProviderConfig{APIKey: "key", Endpoint: "https://unused.invalid", Model: "vision", Temperature: float32(math.Inf(1))}
	transport := analyzerTransport(func(*http.Request) (*http.Response, error) {
		t.Error("invalid request must fail before transport")
		return nil, context.Canceled
	})
	openai := NewOpenAICompatibleAnalyzer(cfg)
	openai.client.Transport = transport
	deepseek := NewDeepSeekAnalyzer(DeepSeekConfig{ProviderConfig: cfg})
	deepseek.client.Transport = transport
	for name, analyzer := range map[string]Analyzer{"openai": openai, "deepseek": deepseek} {
		t.Run(name, func(t *testing.T) {
			result, err := analyzer.Analyze(context.Background(), nil, "prompt")
			require.Nil(t, result)
			require.ErrorContains(t, err, "marshal request")
		})
	}
}

func TestAnalyzerDefaultRequestPayloadsWithoutNetwork(t *testing.T) {
	for _, provider := range []string{"openai", "deepseek"} {
		t.Run(provider, func(t *testing.T) {
			cfg := ProviderConfig{Provider: provider, APIKey: "key", Endpoint: "https://unused.invalid", Model: "vision"}
			var analyzer Analyzer
			client := &http.Client{Transport: analyzerTransport(func(req *http.Request) (*http.Response, error) {
				require.Equal(t, http.MethodPost, req.Method)
				require.Equal(t, "/chat/completions", req.URL.Path)
				require.Equal(t, "Bearer key", req.Header.Get("Authorization"))
				require.Equal(t, "application/json", req.Header.Get("Content-Type"))
				var payload deepSeekRequest
				require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
				require.Equal(t, "vision", payload.Model)
				require.Equal(t, 2000, payload.MaxTokens)
				require.Equal(t, float32(.7), payload.Temperature)
				require.False(t, payload.Stream)
				require.Len(t, payload.Messages, 1)
				require.Equal(t, DefaultPrompt, payload.Messages[0].Content[0].Text)
				require.Equal(t, "data:image/jpeg;base64,YWJj", payload.Messages[0].Content[1].ImageURL.URL)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"quiet"}}]}`)), Header: make(http.Header)}, nil
			})}
			if provider == "openai" {
				a := NewOpenAICompatibleAnalyzer(cfg)
				a.client = client
				analyzer = a
			} else {
				a := NewDeepSeekAnalyzer(DeepSeekConfig{ProviderConfig: cfg})
				a.client = client
				analyzer = a
			}
			result, err := analyzer.Analyze(context.Background(), []byte("abc"), "")
			require.NoError(t, err)
			require.Equal(t, "quiet", result.Analysis)
			require.Empty(t, result.Labels)
			require.Equal(t, "vision", result.Metadata["model"])
		})
	}
}
