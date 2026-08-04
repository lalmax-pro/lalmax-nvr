package observability

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	appmw "github.com/lalmax-pro/lalmax-nvr/internal/middleware"
)

type HTTPObserver struct {
	aggregator   *Aggregator
	tracer       trace.Tracer
	propagator   propagation.TextMapPropagator
	duration     metric.Float64Histogram
	responseSize metric.Int64Histogram
	active       metric.Int64UpDownCounter
}

func NewHTTPObserver(aggregator *Aggregator) *HTTPObserver {
	if aggregator == nil {
		aggregator = NewAggregator()
	}
	meter := otel.Meter("github.com/lalmax-pro/lalmax-nvr/http")
	duration, _ := meter.Float64Histogram("http.server.request.duration", metric.WithUnit("s"))
	responseSize, _ := meter.Int64Histogram("http.server.response.body.size", metric.WithUnit("By"))
	active, _ := meter.Int64UpDownCounter("http.server.active_requests", metric.WithUnit("{request}"))
	return &HTTPObserver{
		aggregator:   aggregator,
		tracer:       otel.Tracer("github.com/lalmax-pro/lalmax-nvr/http"),
		propagator:   otel.GetTextMapPropagator(),
		duration:     duration,
		responseSize: responseSize,
		active:       active,
	}
}

func (o *HTTPObserver) Aggregator() *Aggregator {
	return o.aggregator
}

func (o *HTTPObserver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !shouldObservePath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		ctx := o.propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		methodAttrs := metric.WithAttributes(attribute.String("http.request.method", r.Method))
		o.aggregator.Begin()
		o.active.Add(ctx, 1, methodAttrs)
		defer func() {
			o.aggregator.End()
			o.active.Add(ctx, -1, methodAttrs)
		}()

		ctx, span := o.tracer.Start(ctx, r.Method, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.String("url.scheme", requestScheme(r)),
		))
		defer span.End()

		started := time.Now()
		recorder := &appmw.StatusRecorder{ResponseWriter: w, Status: http.StatusOK}
		var panicValue any
		func() {
			defer func() { panicValue = recover() }()
			next.ServeHTTP(recorder, r.WithContext(ctx))
		}()
		if panicValue != nil {
			recorder.Status = http.StatusInternalServerError
		}
		duration := time.Since(started)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		span.SetName(r.Method + " " + route)
		span.SetAttributes(
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", recorder.Status),
		)
		if recorder.Status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(recorder.Status))
		}

		attrs := metric.WithAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", recorder.Status),
		)
		o.duration.Record(ctx, duration.Seconds(), attrs)
		o.responseSize.Record(ctx, int64(recorder.Bytes), attrs)

		traceID := ""
		if sc := span.SpanContext(); sc.IsValid() {
			traceID = sc.TraceID().String()
		}
		o.aggregator.Observe(Sample{
			Timestamp:     time.Now(),
			Method:        r.Method,
			Route:         route,
			Status:        recorder.Status,
			Duration:      duration,
			ResponseBytes: int64(recorder.Bytes),
			TraceID:       traceID,
		})
		if panicValue != nil {
			panic(panicValue)
		}
	})
}

func shouldObservePath(path string) bool {
	if !strings.HasPrefix(path, "/api/") {
		return false
	}
	switch path {
	case "/api/health", "/api/health/cameras", "/api/readyz":
		return false
	}
	if strings.HasPrefix(path, "/api/observability/") {
		return false
	}
	if strings.HasPrefix(path, "/api/cameras/") && strings.Contains(path, "/stream") {
		return false
	}
	if strings.HasSuffix(path, "/talk/ws") {
		return false
	}
	return true
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		if i := strings.IndexByte(forwarded, ','); i >= 0 {
			forwarded = forwarded[:i]
		}
		return strings.ToLower(strings.TrimSpace(forwarded))
	}
	return "http"
}
