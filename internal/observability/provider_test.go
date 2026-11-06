package observability

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	otellogglobal "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	logspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logsrecordpb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/proto"
)

type capturedOTLPRequest struct {
	path          string
	method        string
	contentType   string
	authorization string
	body          []byte
}

func TestSignalEndpoint(t *testing.T) {
	tests := []struct {
		base   string
		signal string
		want   string
	}{
		{"http://localhost:4318", "traces", "http://localhost:4318/v1/traces"},
		{"https://otel.example.com/prefix/", "metrics", "https://otel.example.com/prefix/v1/metrics"},
	}
	for _, tt := range tests {
		if got := signalEndpoint(tt.base, tt.signal); got != tt.want {
			t.Fatalf("signalEndpoint(%q, %q) = %q, want %q", tt.base, tt.signal, got, tt.want)
		}
	}
}

func TestCloneHeaders(t *testing.T) {
	original := map[string]string{"Authorization": "Bearer token"}
	cloned := cloneHeaders(original)
	cloned["Authorization"] = "changed"
	if original["Authorization"] != "Bearer token" {
		t.Fatal("cloneHeaders mutated its source")
	}
}

func TestProviderExportsTracesMetricsAndPropagatesContext(t *testing.T) {
	// Provider registration is process-global, so keep this test serial and
	// restore the previous globals when it finishes.
	previousTracer := otel.GetTracerProvider()
	previousMeter := otel.GetMeterProvider()
	previousLogger := otellogglobal.GetLoggerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	previousSlogLogger := slog.Default()
	t.Cleanup(func() {
		otel.SetTracerProvider(previousTracer)
		otel.SetMeterProvider(previousMeter)
		otellogglobal.SetLoggerProvider(previousLogger)
		otel.SetTextMapPropagator(previousPropagator)
		slog.SetDefault(previousSlogLogger)
	})

	requests := make(chan capturedOTLPRequest, 64)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- capturedOTLPRequest{
			path: r.URL.Path, method: r.Method, contentType: r.Header.Get("Content-Type"),
			authorization: r.Header.Get("Authorization"), body: body,
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	appLogger := slog.New(otelslog.NewHandler("provider-test"))
	slog.SetDefault(appLogger)
	provider, err := NewProvider(context.Background(), "test-version", ProviderConfig{
		Enabled:               true,
		Endpoint:              collector.URL + "/collector/",
		ServiceName:           "nvr-test",
		TracesEnabled:         true,
		MetricsEnabled:        true,
		LogsEnabled:           true,
		Headers:               map[string]string{"Authorization": "Bearer test-token"},
		Timeout:               time.Second,
		MetricsExportInterval: 20 * time.Millisecond,
		LogsExportInterval:    20 * time.Millisecond,
	})
	require.NoError(t, err)

	router := chi.NewRouter()
	router.Use(NewHTTPObserver(NewAggregator()).Middleware)
	router.Get("/api/cameras/{id}", func(w http.ResponseWriter, r *http.Request) {
		appLogger.ErrorContext(r.Context(), "camera request failed", "camera.id", "cam-1", "failure.kind", "upstream_timeout")
		w.WriteHeader(http.StatusInternalServerError)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/cameras/cam-1", nil)
	req.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	router.ServeHTTP(httptest.NewRecorder(), req)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, provider.Shutdown(shutdownCtx))

	seen := map[string]capturedOTLPRequest{}
	deadline := time.After(time.Second)
	for len(seen) < 3 {
		select {
		case got := <-requests:
			seen[got.path] = got
		case <-deadline:
			t.Fatalf("collector did not receive both signals; received %v", seen)
		}
	}

	traceRequest, ok := seen["/collector/v1/traces"]
	require.True(t, ok, "trace signal should use the configured base path")
	require.Equal(t, http.MethodPost, traceRequest.method)
	require.Contains(t, traceRequest.contentType, "application/x-protobuf")
	require.Equal(t, "Bearer test-token", traceRequest.authorization)
	var traceData tracepb.ExportTraceServiceRequest
	require.NoError(t, proto.Unmarshal(traceRequest.body, &traceData))
	require.NotEmpty(t, traceData.ResourceSpans)
	require.NotEmpty(t, traceData.ResourceSpans[0].ScopeSpans)
	require.NotEmpty(t, traceData.ResourceSpans[0].ScopeSpans[0].Spans)
	span := traceData.ResourceSpans[0].ScopeSpans[0].Spans[0]
	require.Equal(t, "0123456789abcdef0123456789abcdef", strings.ToLower(hexString(span.TraceId)))
	require.Equal(t, "0123456789abcdef", strings.ToLower(hexString(span.ParentSpanId)))
	require.Equal(t, "GET /api/cameras/{id}", span.Name)
	require.Equal(t, int32(2), int32(span.Status.Code), "HTTP 500 should mark the span as an error")
	requireResourceAttribute(t, traceData.ResourceSpans[0].Resource.Attributes, "service.name", "nvr-test")
	requireResourceAttribute(t, traceData.ResourceSpans[0].Resource.Attributes, "service.version", "test-version")
	instanceID := resourceAttribute(t, traceData.ResourceSpans[0].Resource.Attributes, "service.instance.id")
	require.NotEmpty(t, instanceID)

	metricRequest, ok := seen["/collector/v1/metrics"]
	require.True(t, ok, "metric signal should use the configured base path")
	require.Equal(t, http.MethodPost, metricRequest.method)
	require.Contains(t, metricRequest.contentType, "application/x-protobuf")
	require.Equal(t, "Bearer test-token", metricRequest.authorization)
	var metricData metricspb.ExportMetricsServiceRequest
	require.NoError(t, proto.Unmarshal(metricRequest.body, &metricData))
	require.NotEmpty(t, metricData.ResourceMetrics)
	require.NotEmpty(t, metricData.ResourceMetrics[0].ScopeMetrics)
	requireResourceAttribute(t, metricData.ResourceMetrics[0].Resource.Attributes, "service.name", "nvr-test")
	require.Equal(t, instanceID, resourceAttribute(t, metricData.ResourceMetrics[0].Resource.Attributes, "service.instance.id"))
	metricNames := make(map[string]bool)
	for _, scope := range metricData.ResourceMetrics[0].ScopeMetrics {
		for _, item := range scope.Metrics {
			metricNames[item.Name] = true
		}
	}
	require.True(t, metricNames["http.server.request.duration"])
	require.True(t, metricNames["http.server.response.body.size"])
	require.True(t, metricNames["go.goroutine.count"])
	require.True(t, metricNames["go.schedule.duration"])

	logRequest, ok := seen["/collector/v1/logs"]
	require.True(t, ok, "log signal should use the configured base path")
	require.Equal(t, http.MethodPost, logRequest.method)
	require.Contains(t, logRequest.contentType, "application/x-protobuf")
	require.Equal(t, "Bearer test-token", logRequest.authorization)
	var logData logspb.ExportLogsServiceRequest
	require.NoError(t, proto.Unmarshal(logRequest.body, &logData))
	require.NotEmpty(t, logData.ResourceLogs)
	require.NotEmpty(t, logData.ResourceLogs[0].ScopeLogs)
	require.Equal(t, instanceID, resourceAttribute(t, logData.ResourceLogs[0].Resource.Attributes, "service.instance.id"))
	require.NotEmpty(t, logData.ResourceLogs[0].ScopeLogs[0].LogRecords)
	logRecords := logData.ResourceLogs[0].ScopeLogs[0].LogRecords
	if len(logRecords) < 2 {
		var bodies []string
		for _, record := range logRecords {
			bodies = append(bodies, record.Body.GetStringValue())
		}
		t.Fatalf("application error and HTTP failure summary should both be exported; got %v", bodies)
	}
	var appError, requestFailure *logsrecordpb.LogRecord
	for i := range logRecords {
		switch logRecords[i].Body.GetStringValue() {
		case "camera request failed":
			appError = logRecords[i]
		case "HTTP request failed":
			requestFailure = logRecords[i]
		}
	}
	require.NotNil(t, appError)
	require.NotNil(t, requestFailure)
	require.Equal(t, "0123456789abcdef0123456789abcdef", strings.ToLower(hexString(appError.TraceId)))
	require.NotEmpty(t, appError.SpanId, "log emitted in a server span should be correlated to that span")
	require.Equal(t, "ERROR", appError.SeverityText)
	require.Equal(t, "0123456789abcdef0123456789abcdef", strings.ToLower(hexString(requestFailure.TraceId)))
	requireResourceAttribute(t, logData.ResourceLogs[0].Resource.Attributes, "service.name", "nvr-test")
	logAttributes := make(map[string]string)
	for _, attr := range appError.Attributes {
		logAttributes[attr.Key] = attr.Value.GetStringValue()
	}
	require.Equal(t, "cam-1", logAttributes["camera.id"])
	require.Equal(t, "upstream_timeout", logAttributes["failure.kind"])
	failureAttributes := make(map[string]string)
	for _, attr := range requestFailure.Attributes {
		failureAttributes[attr.Key] = attr.Value.GetStringValue()
	}
	require.Equal(t, "GET", failureAttributes["http.request.method"])
	require.Equal(t, "/api/cameras/{id}", failureAttributes["http.route"])
	statusCodeFound := false
	for _, attr := range requestFailure.Attributes {
		if attr.Key == "http.response.status_code" {
			require.Equal(t, int64(500), attr.Value.GetIntValue())
			statusCodeFound = true
			break
		}
	}
	require.True(t, statusCodeFound, "HTTP failure log should include its response status")
}

func TestProviderDisabledDoesNotExport(t *testing.T) {
	previousTracer := otel.GetTracerProvider()
	previousMeter := otel.GetMeterProvider()
	previousLogger := otellogglobal.GetLoggerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(previousTracer)
		otel.SetMeterProvider(previousMeter)
		otellogglobal.SetLoggerProvider(previousLogger)
		otel.SetTextMapPropagator(previousPropagator)
	})

	provider, err := NewProvider(context.Background(), "test-version", ProviderConfig{
		Enabled: false,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, provider.Shutdown(context.Background())) }()
	require.NotNil(t, otel.GetTracerProvider())
	require.NotNil(t, otel.GetMeterProvider())
	require.IsType(t, propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}), otel.GetTextMapPropagator())
}

func TestProviderSignalToggles(t *testing.T) {
	for _, test := range []struct {
		name           string
		tracesEnabled  bool
		metricsEnabled bool
		logsEnabled    bool
		wantPath       string
	}{
		{name: "traces only", tracesEnabled: true, wantPath: "/v1/traces"},
		{name: "metrics only", metricsEnabled: true, wantPath: "/v1/metrics"},
		{name: "logs only", logsEnabled: true, wantPath: "/v1/logs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			previousTracer := otel.GetTracerProvider()
			previousMeter := otel.GetMeterProvider()
			previousLogger := otellogglobal.GetLoggerProvider()
			previousPropagator := otel.GetTextMapPropagator()
			t.Cleanup(func() {
				otel.SetTracerProvider(previousTracer)
				otel.SetMeterProvider(previousMeter)
				otellogglobal.SetLoggerProvider(previousLogger)
				otel.SetTextMapPropagator(previousPropagator)
			})

			paths := make(chan string, 4)
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths <- r.URL.Path
				w.WriteHeader(http.StatusOK)
			}))
			defer collector.Close()

			provider, err := NewProvider(context.Background(), "test", ProviderConfig{
				Enabled: true, Endpoint: collector.URL, ServiceName: "nvr-test",
				TracesEnabled: test.tracesEnabled, MetricsEnabled: test.metricsEnabled,
				LogsEnabled: test.logsEnabled,
				Timeout:     time.Second, MetricsExportInterval: 20 * time.Millisecond,
				LogsExportInterval: 20 * time.Millisecond,
			})
			require.NoError(t, err)
			_, span := otel.Tracer("signal-toggle-test").Start(context.Background(), "test-span")
			span.End()
			if test.logsEnabled {
				slog.New(otelslog.NewHandler("signal-toggle-test")).Info("test log")
			}
			histogram, err := otel.Meter("signal-toggle-test").Float64Histogram("test.measurement")
			require.NoError(t, err)
			histogram.Record(context.Background(), 1)

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			require.NoError(t, provider.Shutdown(ctx))
			require.Equal(t, test.wantPath, <-paths)
			select {
			case unexpected := <-paths:
				t.Fatalf("disabled signal was exported to %s", unexpected)
			case <-time.After(30 * time.Millisecond):
			}
		})
	}
}

func requireResourceAttribute(t *testing.T, attrs []*commonpb.KeyValue, key, value string) {
	t.Helper()
	require.Equal(t, value, resourceAttribute(t, attrs, key))
}

func resourceAttribute(t *testing.T, attrs []*commonpb.KeyValue, key string) string {
	t.Helper()
	for _, attr := range attrs {
		if attr.Key == key {
			return attr.Value.GetStringValue()
		}
	}
	t.Fatalf("resource attribute %q not found", key)
	return ""
}

func hexString(value []byte) string {
	const digits = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for i, b := range value {
		encoded[i*2] = digits[b>>4]
		encoded[i*2+1] = digits[b&0x0f]
	}
	return string(encoded)
}
