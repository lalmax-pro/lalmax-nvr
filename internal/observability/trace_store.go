package observability

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const traceWriteQueueSize = 2048

type traceStoreProcessor struct {
	store     storage.TraceRepository
	retention time.Duration
	maxTraces int
	enabled   *atomic.Bool
	queue     chan traceWriteItem
	done      chan struct{}
	mu        sync.Mutex
	closed    bool
}

type traceWriteItem struct {
	span    *storage.TraceSpanRecord
	barrier chan struct{}
}

func newTraceStoreProcessor(store storage.TraceRepository, retention time.Duration, maxTraces int, enabled *atomic.Bool) *traceStoreProcessor {
	if retention <= 0 {
		retention = 24 * time.Hour
	}
	if maxTraces < 100 {
		maxTraces = 10000
	}
	p := &traceStoreProcessor{store: store, retention: retention, maxTraces: maxTraces, enabled: enabled, queue: make(chan traceWriteItem, traceWriteQueueSize), done: make(chan struct{})}
	go p.run()
	return p
}

func (p *traceStoreProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

func (p *traceStoreProcessor) OnEnd(span sdktrace.ReadOnlySpan) {
	if p == nil || p.store == nil || p.enabled == nil || !p.enabled.Load() || !span.SpanContext().IsValid() {
		return
	}
	record := recordSpan(span)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	select {
	case p.queue <- traceWriteItem{span: &record}:
	default:
		slog.Warn("local trace store queue full; dropping span", "trace_id", record.TraceID, "span_name", record.Name)
	}
}

func (p *traceStoreProcessor) ForceFlush(ctx context.Context) error {
	barrier := make(chan struct{})
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	// A pre-cancelled context must not accidentally report success merely
	// because the queue and cancellation are both ready in the select below.
	if err := ctx.Err(); err != nil {
		p.mu.Unlock()
		return err
	}
	select {
	case p.queue <- traceWriteItem{barrier: barrier}:
		p.mu.Unlock()
	case <-ctx.Done():
		p.mu.Unlock()
		return ctx.Err()
	}
	select {
	case <-barrier:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *traceStoreProcessor) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.queue)
	}
	p.mu.Unlock()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *traceStoreProcessor) run() {
	defer close(p.done)
	cleanup := time.NewTicker(10 * time.Minute)
	defer cleanup.Stop()
	p.prune()
	for {
		select {
		case item, ok := <-p.queue:
			if !ok {
				return
			}
			if item.barrier != nil {
				close(item.barrier)
				continue
			}
			if item.span != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := p.store.StoreTraceSpan(ctx, *item.span); err != nil {
					slog.Warn("persist local trace span failed", "error", err)
				}
				cancel()
			}
		case <-cleanup.C:
			p.prune()
		}
	}
}

func (p *traceStoreProcessor) prune() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.store.PruneTraceSpans(ctx, time.Now().Add(-p.retention), p.maxTraces); err != nil {
		slog.Warn("expire local trace spans failed", "error", err)
	}
}

func recordSpan(span sdktrace.ReadOnlySpan) storage.TraceSpanRecord {
	sc := span.SpanContext()
	parentID := ""
	if parent := span.Parent(); parent.IsValid() {
		parentID = parent.SpanID().String()
	}
	status := span.Status()
	attrs := safeAttributes(span.Attributes())
	resourceAttrs := safeAttributes(span.Resource().Attributes())
	for key, value := range resourceAttrs {
		if _, exists := attrs["resource."+key]; !exists {
			attrs["resource."+key] = value
		}
	}
	events := make([]map[string]any, 0, len(span.Events()))
	for _, event := range span.Events() {
		events = append(events, map[string]any{"name": event.Name, "time_unix_nano": event.Time.UnixNano(), "attributes": safeAttributes(event.Attributes)})
	}
	links := make([]map[string]any, 0, len(span.Links()))
	for _, link := range span.Links() {
		links = append(links, map[string]any{"trace_id": link.SpanContext.TraceID().String(), "span_id": link.SpanContext.SpanID().String(), "attributes": safeAttributes(link.Attributes)})
	}
	attrsJSON, _ := json.Marshal(attrs)
	eventsJSON, _ := json.Marshal(events)
	linksJSON, _ := json.Marshal(links)
	service := ""
	if value, ok := resourceAttrs["service.name"].(string); ok {
		service = value
	}
	return storage.TraceSpanRecord{
		TraceID: sc.TraceID().String(), SpanID: sc.SpanID().String(), ParentSpanID: parentID,
		Name: span.Name(), Service: service, Kind: span.SpanKind().String(), Status: status.Description,
		StatusCode: int(status.Code), StartUnixNS: span.StartTime().UnixNano(), EndUnixNS: span.EndTime().UnixNano(),
		Attributes: string(attrsJSON), Events: string(eventsJSON), Links: string(linksJSON),
	}
}

func safeAttributes(values []attribute.KeyValue) map[string]any {
	result := make(map[string]any, len(values))
	for _, item := range values {
		key := strings.ToLower(string(item.Key))
		if strings.Contains(key, "password") || strings.Contains(key, "token") || strings.Contains(key, "secret") ||
			strings.Contains(key, "authorization") || strings.Contains(key, "cookie") || strings.Contains(key, "credential") ||
			(strings.Contains(key, "url") && key != "url.path" && key != "url.scheme") || key == "db.statement" {
			continue
		}
		if len(result) >= 64 {
			break
		}
		value := item.Value.AsInterface()
		if text, ok := value.(string); ok && len(text) > 512 {
			value = text[:512]
		}
		result[string(item.Key)] = value
	}
	return result
}

var _ sdktrace.SpanProcessor = (*traceStoreProcessor)(nil)
