package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestOTelMiddlewareExtractsTraceContextAndUsesRouteTemplate(t *testing.T) {
	previousGinMode := gin.Mode()
	t.Cleanup(func() { gin.SetMode(previousGinMode) })
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(otelMiddleware())
	router.GET("/api/group/:stream", func(c *gin.Context) { c.Status(http.StatusInternalServerError) })
	request := httptest.NewRequest(http.MethodGet, "/api/group/camera-secret?token=do-not-record", nil)
	request.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	router.ServeHTTP(httptest.NewRecorder(), request)

	var matches []sdktrace.ReadOnlySpan
	for _, candidate := range recorder.Ended() {
		if candidate.Name() == "GET /api/group/:stream" && candidate.SpanContext().TraceID().String() == "0123456789abcdef0123456789abcdef" {
			matches = append(matches, candidate)
		}
	}
	require.Len(t, matches, 1)
	span := matches[0]
	require.Equal(t, "GET /api/group/:stream", span.Name())
	require.Equal(t, "0123456789abcdef0123456789abcdef", span.SpanContext().TraceID().String())
	require.Equal(t, "0123456789abcdef", span.Parent().SpanID().String())
	require.Equal(t, sdktrace.Status{Code: codes.Error, Description: "Internal Server Error"}, span.Status())
	for _, attr := range span.Attributes() {
		require.NotContains(t, attr.Value.AsString(), "camera-secret")
		require.NotContains(t, attr.Value.AsString(), "do-not-record")
	}
}

func TestLifecycleHookCarriesTraceContextOverSSE(t *testing.T) {
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})

	events := make(chan HookEvent, 1)
	hub := &HttpNotify{
		serverId:    "embedded-test",
		subscribers: map[int64]chan HookEvent{1: events},
		plugins:     make(map[string]*hookPluginEntry),
	}
	hub.publish(HookEventGroupStart, HookGroupInfo{StreamName: "camera-1"})
	event := <-events
	wire := httptest.NewRecorder()
	require.NoError(t, writeHookEventSSE(wire, event))
	require.Contains(t, wire.Body.String(), "traceparent: ")
	traceparent := strings.Split(strings.Split(wire.Body.String(), "traceparent: ")[1], "\n")[0]
	traceCtx := propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{"traceparent": traceparent})
	require.True(t, trace.SpanContextFromContext(traceCtx).IsValid())

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "lalmax.on_group_start", spans[0].Name())
	require.Equal(t, trace.SpanContextFromContext(traceCtx).TraceID(), spans[0].SpanContext().TraceID())
}
