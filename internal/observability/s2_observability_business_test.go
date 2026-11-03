package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// s2UseRecorder installs an in-memory tracer provider for span assertions.
func s2UseRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "s2-test"))),
	)
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	return recorder
}

func TestS2RunBusinessOperation(t *testing.T) {
	recorder := s2UseRecorder(t)

	// A successful operation returns its context and records an unset status.
	ctx, err := RunBusinessOperation(context.Background(), "camera.sync", func(ctx context.Context) error {
		require.NotNil(t, ctx)
		return nil
	}, attribute.String("nvr.camera_id", "cam1"))
	require.NoError(t, err)
	require.NotNil(t, ctx)
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "camera.sync", spans[0].Name())
	require.Equal(t, int(codes.Unset), int(spans[0].Status().Code))

	// Failures are recorded with a sanitized error type.
	boom := errors.New("rtsp://user:secret@camera/stream failed")
	_, err = RunBusinessOperation(context.Background(), "camera.sync", func(context.Context) error {
		return boom
	})
	require.ErrorIs(t, err, boom)
	spans = recorder.Ended()
	require.Len(t, spans, 2)
	found := false
	for _, attr := range spans[1].Attributes() {
		if string(attr.Key) == "error.type" {
			require.Equal(t, "*errors.errorString", attr.Value.AsString())
			found = true
		}
	}
	require.True(t, found, "failed operations record error.type")
	require.Equal(t, int(codes.Error), int(spans[1].Status().Code))
	require.NotContains(t, spans[1].Status().Description, "secret")
}

func TestS2StartHTTPClientSpan(t *testing.T) {
	recorder := s2UseRecorder(t)

	req := httptest.NewRequest(http.MethodPost, "http://internal.local:8081/api/v1/hooks", nil)
	ctx, span := StartHTTPClientSpan(context.Background(), req)
	require.NotNil(t, ctx)
	span.End()

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "HTTP POST", spans[0].Name())

	attrs := map[string]string{}
	for _, attr := range spans[0].Attributes() {
		if attr.Value.Type().String() == "STRING" {
			attrs[string(attr.Key)] = attr.Value.AsString()
		}
	}
	require.Equal(t, http.MethodPost, attrs["http.request.method"])
	require.Equal(t, "http", attrs["url.scheme"])
	require.Equal(t, "internal.local", attrs["server.address"])
	require.Equal(t, "/api/v1/hooks", attrs["url.path"])

	// The W3C trace context is propagated to the outbound request.
	require.Contains(t, req.Header.Get("traceparent"), span.SpanContext().TraceID().String())

	// A request without an explicit port omits server.port.
	req = httptest.NewRequest(http.MethodGet, "http://internal.local/health", nil)
	_, span = StartHTTPClientSpan(context.Background(), req)
	span.End()
	spans = recorder.Ended()
	require.Len(t, spans, 2)
	for _, attr := range spans[1].Attributes() {
		require.NotEqual(t, "server.port", string(attr.Key))
	}

	// An invalid port is ignored instead of failing the span.
	req = httptest.NewRequest(http.MethodGet, "http://internal.local/health", nil)
	req.URL.Host = "internal.local:not-a-port"
	_, span = StartHTTPClientSpan(context.Background(), req)
	span.End()
	require.Len(t, recorder.Ended(), 3)
}

func TestS2DetachedContextNil(t *testing.T) {
	require.NotNil(t, DetachedContext(nil))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	detached := DetachedContext(ctx)
	require.NoError(t, detached.Err(), "detached contexts survive parent cancellation")
}

func TestS2HTTPObserverAggregator(t *testing.T) {
	observer := NewHTTPObserver(nil)
	require.NotNil(t, observer.Aggregator())
	require.Same(t, observer.aggregator, observer.Aggregator())

	custom := NewAggregator()
	require.Same(t, custom, NewHTTPObserver(custom).Aggregator())
}

func TestS2ProviderSetLocalTraceEnabled(t *testing.T) {
	// Nil receivers are no-ops.
	var nilProvider *Provider
	nilProvider.SetLocalTraceEnabled(true)
	require.NoError(t, nilProvider.Shutdown(context.Background()))

	provider := &Provider{}
	require.False(t, provider.localTraceEnabled.Load())
	provider.SetLocalTraceEnabled(true)
	require.True(t, provider.localTraceEnabled.Load())
	provider.SetLocalTraceEnabled(false)
	require.False(t, provider.localTraceEnabled.Load())
}

func TestS2SignalEndpointParseError(t *testing.T) {
	// A control character makes url.Parse fail; the base is returned verbatim.
	require.Equal(t, "http://bad\x7f", signalEndpoint("http://bad\x7f", "traces"))
	require.Equal(t, "http://127.0.0.1:4318/v1/logs", signalEndpoint("http://127.0.0.1:4318", "logs"))
	require.Equal(t, "http://127.0.0.1:4318/base/v1/metrics", signalEndpoint("http://127.0.0.1:4318/base", "metrics"))
}
