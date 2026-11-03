package observability

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// s2TraceRepo is a scriptable storage.TraceRepository.
type s2TraceRepo struct {
	mu       sync.Mutex
	spans    []storage.TraceSpanRecord
	storeErr error
	pruneErr error
	pruneAt  []time.Time
	pruneMax []int
}

func (r *s2TraceRepo) StoreTraceSpan(_ context.Context, record storage.TraceSpanRecord) error {
	if r.storeErr != nil {
		return r.storeErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spans = append(r.spans, record)
	return nil
}

func (r *s2TraceRepo) ListTraces(context.Context, int, int) ([]storage.TraceSummary, error) {
	return nil, nil
}

func (r *s2TraceRepo) GetTraceSpans(context.Context, string) ([]storage.TraceSpanRecord, error) {
	return nil, nil
}

func (r *s2TraceRepo) PruneTraceSpans(_ context.Context, before time.Time, maxTraces int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneAt = append(r.pruneAt, before)
	r.pruneMax = append(r.pruneMax, maxTraces)
	return r.pruneErr
}

func (r *s2TraceRepo) spanCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.spans)
}

func (r *s2TraceRepo) spanByName(name string) *storage.TraceSpanRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.spans {
		if r.spans[i].Name == name {
			return &r.spans[i]
		}
	}
	return nil
}

// s2RecordingSpan starts a recording span that can be handed to OnEnd.
func s2RecordingSpan(t *testing.T) sdktrace.ReadOnlySpan {
	t.Helper()
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(resource.Empty()))
	_, span := tp.Tracer("s2-test").Start(context.Background(), "probe")
	readOnly, ok := span.(sdktrace.ReadWriteSpan)
	require.True(t, ok, "SDK spans implement ReadWriteSpan")
	return readOnly
}

func TestS2NewTraceStoreProcessorDefaults(t *testing.T) {
	repo := &s2TraceRepo{}
	enabled := &atomic.Bool{}
	enabled.Store(true)
	p := newTraceStoreProcessor(repo, 0, 0, enabled)
	require.Equal(t, 24*time.Hour, p.retention)
	require.Equal(t, 10000, p.maxTraces)
	require.NoError(t, p.ForceFlush(context.Background()))
	require.Len(t, repo.pruneMax, 1, "the processor prunes once on startup")
	require.NoError(t, p.Shutdown(context.Background()))

	// Values above the floor are preserved.
	p = newTraceStoreProcessor(repo, time.Hour, 500, enabled)
	require.Equal(t, time.Hour, p.retention)
	require.Equal(t, 500, p.maxTraces)
	require.NoError(t, p.Shutdown(context.Background()))
}

func TestS2TraceStoreOnEndStoresSpan(t *testing.T) {
	repo := &s2TraceRepo{}
	enabled := &atomic.Bool{}
	enabled.Store(true)
	p := newTraceStoreProcessor(repo, time.Hour, 500, enabled)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(p),
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", "lalmax-nvr"),
			attribute.String("service.version", "1.2.3"),
		)),
	)
	tracer := tp.Tracer("s2-test")
	parentCtx, parent := tracer.Start(context.Background(), "parent.op")
	_, child := tracer.Start(parentCtx, "handle.request",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "GET"),
			attribute.String("http.password", "hunter2"),
			attribute.String("http.url", "http://secret/path"),
			attribute.String("url.path", "/api/health"),
			attribute.String("db.statement", "select 1"),
			attribute.String("long", string(make([]byte, 600))),
		),
	)
	child.AddEvent("cache.miss")
	child.AddLink(trace.Link{
		SpanContext: parent.SpanContext(),
		Attributes:  []attribute.KeyValue{attribute.String("link.kind", "batch")},
	})
	child.SetStatus(codes.Error, "boom")
	child.End()
	parent.End()

	require.NoError(t, p.ForceFlush(context.Background()))
	require.Equal(t, 2, repo.spanCount())

	record := repo.spanByName("handle.request")
	require.NotNil(t, record)
	require.Equal(t, "lalmax-nvr", record.Service)
	require.Equal(t, "server", record.Kind)
	require.Equal(t, "boom", record.Status)
	require.Equal(t, int(codes.Error), record.StatusCode)
	require.Equal(t, parent.SpanContext().SpanID().String(), record.ParentSpanID)
	require.Contains(t, record.Attributes, "http.method")
	require.NotContains(t, record.Attributes, "http.password")
	require.NotContains(t, record.Attributes, "http.url")
	require.NotContains(t, record.Attributes, "db.statement")
	require.Contains(t, record.Attributes, "url.path")
	require.Contains(t, record.Attributes, "resource.service.version")
	require.Contains(t, record.Events, "cache.miss")
	require.Contains(t, record.Links, "link.kind")
}

func TestS2TraceStoreOnEndGuards(t *testing.T) {
	span := s2RecordingSpan(t)

	enabled := &atomic.Bool{}
	enabled.Store(true)

	// Nil receiver, nil store, nil enabled flag and disabled store all no-op.
	var nilProcessor *traceStoreProcessor
	nilProcessor.OnEnd(span)

	repo := &s2TraceRepo{}
	withNilEnabled := newTraceStoreProcessor(repo, time.Hour, 500, nil)
	withNilEnabled.OnEnd(span)
	require.NoError(t, withNilEnabled.Shutdown(context.Background()))

	disabled := &atomic.Bool{}
	p := newTraceStoreProcessor(repo, time.Hour, 500, disabled)
	p.OnEnd(span)
	require.NoError(t, p.ForceFlush(context.Background()))
	require.Zero(t, repo.spanCount())
	require.NoError(t, p.Shutdown(context.Background()))

	// Spans arriving after shutdown are ignored.
	closed := newTraceStoreProcessor(repo, time.Hour, 500, enabled)
	require.NoError(t, closed.Shutdown(context.Background()))
	closed.OnEnd(span)
	require.Zero(t, repo.spanCount())
}

func TestS2TraceStoreForceFlushAndShutdown(t *testing.T) {
	repo := &s2TraceRepo{}
	enabled := &atomic.Bool{}
	enabled.Store(true)

	// A cancelled context aborts the flush.
	p := newTraceStoreProcessor(repo, time.Hour, 500, enabled)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, p.ForceFlush(ctx), context.Canceled)

	// Flushing a closed processor is a no-op.
	require.NoError(t, p.Shutdown(context.Background()))
	require.NoError(t, p.ForceFlush(context.Background()))
	// Shutting down twice is safe.
	require.NoError(t, p.Shutdown(context.Background()))

	// Storage errors are swallowed and shutdown still completes.
	failing := &s2TraceRepo{storeErr: errors.New("store exploded"), pruneErr: errors.New("prune exploded")}
	p = newTraceStoreProcessor(failing, time.Hour, 500, enabled)
	p.OnEnd(s2RecordingSpan(t))
	require.NoError(t, p.ForceFlush(context.Background()))
	require.NoError(t, p.Shutdown(context.Background()))
}

func TestS2TraceStorePrune(t *testing.T) {
	repo := &s2TraceRepo{}
	enabled := &atomic.Bool{}
	p := newTraceStoreProcessor(repo, 3*time.Hour, 700, enabled)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	require.NoError(t, p.ForceFlush(context.Background()))

	repo.mu.Lock()
	before := repo.pruneAt[0]
	maxTraces := repo.pruneMax[0]
	repo.mu.Unlock()
	require.Equal(t, 700, maxTraces)
	require.WithinDuration(t, time.Now().Add(-3*time.Hour), before, time.Minute)
}

func TestS2RecordSpanDefaults(t *testing.T) {
	// A root span without events, links or resource attributes.
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(resource.Empty()))
	_, span := tp.Tracer("s2-test").Start(context.Background(), "bare",
		trace.WithAttributes(
			attribute.String("TOKEN", "abc"),
			attribute.String("url.scheme", "https"),
		),
	)
	readOnly, ok := span.(sdktrace.ReadOnlySpan)
	require.True(t, ok, "SDK spans implement ReadOnlySpan")
	record := recordSpan(readOnly)
	require.Empty(t, record.ParentSpanID)
	require.Empty(t, record.Service)
	require.NotEmpty(t, record.TraceID)
	require.NotEmpty(t, record.SpanID)
	require.Equal(t, "[]", record.Events)
	require.Equal(t, "[]", record.Links)
	require.Contains(t, record.Attributes, "url.scheme")
	require.NotContains(t, record.Attributes, "TOKEN")
}

func TestS2SafeAttributes(t *testing.T) {
	values := []attribute.KeyValue{
		attribute.String("password", "x"),
		attribute.String("api_token", "x"),
		attribute.String("client_secret", "x"),
		attribute.String("authorization_header", "x"),
		attribute.String("set-cookie", "x"),
		attribute.String("aws.credential", "x"),
		attribute.String("http.url", "http://x"),
		attribute.String("url.path", "/ok"),
		attribute.String("url.scheme", "https"),
		attribute.String("db.statement", "select 1"),
		attribute.Int("retries", 3),
		attribute.String("long.text", string(make([]byte, 600))),
	}
	attrs := safeAttributes(values)
	require.Equal(t, "/ok", attrs["url.path"])
	require.Equal(t, "https", attrs["url.scheme"])
	require.Equal(t, int64(3), attrs["retries"])
	require.Len(t, attrs["long.text"], 512)
	for _, key := range []string{"password", "api_token", "client_secret", "authorization_header", "set-cookie", "aws.credential", "http.url", "db.statement"} {
		_, exists := attrs[key]
		require.False(t, exists, "sensitive attribute %q must be dropped", key)
	}

	// At most 64 attributes survive.
	many := make([]attribute.KeyValue, 0, 100)
	for i := 0; i < 100; i++ {
		many = append(many, attribute.Int("attr"+string(rune('a'+i%26))+string(rune('a'+i/26)), i))
	}
	require.LessOrEqual(t, len(safeAttributes(many)), 64)
}
