package middleware

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const defaultLogRingCapacity = 8000

// LogEntry is one structured service-log record kept in the in-memory ring.
type LogEntry struct {
	Seq     uint64         `json:"seq"`
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"msg"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// LogRingFilter selects a snapshot of buffered service logs.
type LogRingFilter struct {
	MinLevel slog.Level
	Query    string
	AfterSeq uint64
	Since    time.Time
	Limit    int
}

// LogRing is a bounded, process-local buffer of recent slog records.
type LogRing struct {
	mu   sync.Mutex
	buf  []LogEntry
	cap  int
	head int
	size int
	seq  uint64
	subs map[chan LogEntry]struct{}
}

var defaultLogRing = NewLogRing(defaultLogRingCapacity)

// DefaultLogRing returns the process-wide service-log buffer attached by SetupLogger.
func DefaultLogRing() *LogRing {
	return defaultLogRing
}

// NewLogRing creates an isolated ring. Capacity is clamped to at least 1.
func NewLogRing(capacity int) *LogRing {
	if capacity < 1 {
		capacity = defaultLogRingCapacity
	}
	return &LogRing{
		buf:  make([]LogEntry, capacity),
		cap:  capacity,
		subs: make(map[chan LogEntry]struct{}),
	}
}

// Capacity is the maximum number of entries retained.
func (r *LogRing) Capacity() int {
	if r == nil {
		return 0
	}
	return r.cap
}

// Size is the number of entries currently retained.
func (r *LogRing) Size() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

// Append stores an entry and fans it out to live subscribers.
func (r *LogRing) Append(entry LogEntry) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.seq++
	entry.Seq = r.seq
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	r.buf[r.head] = entry
	r.head = (r.head + 1) % r.cap
	if r.size < r.cap {
		r.size++
	}
	subs := make([]chan LogEntry, 0, len(r.subs))
	for ch := range r.subs {
		subs = append(subs, ch)
	}
	r.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- entry:
		default:
		}
	}
}

// Snapshot returns matching entries in chronological order (oldest first).
func (r *LogRing) Snapshot(filter LogRingFilter) []LogEntry {
	if r == nil {
		return nil
	}
	if filter.Limit <= 0 {
		filter.Limit = 200
	}
	r.mu.Lock()
	n := r.size
	out := make([]LogEntry, 0, n)
	start := 0
	if r.size == r.cap {
		start = r.head
	}
	for i := 0; i < n; i++ {
		out = append(out, r.buf[(start+i)%r.cap])
	}
	r.mu.Unlock()

	matched := make([]LogEntry, 0, len(out))
	q := strings.ToLower(strings.TrimSpace(filter.Query))
	for _, entry := range out {
		if entry.Seq <= filter.AfterSeq {
			continue
		}
		if !filter.Since.IsZero() && entry.Time.Before(filter.Since) {
			continue
		}
		if !logLevelAtLeast(entry.Level, filter.MinLevel) {
			continue
		}
		if q != "" && !logEntryMatches(entry, q) {
			continue
		}
		matched = append(matched, entry)
	}
	if len(matched) > filter.Limit {
		matched = matched[len(matched)-filter.Limit:]
	}
	return matched
}

// Subscribe receives live copies of new entries. The channel is buffered;
// overflow drops the event rather than blocking logging.
func (r *LogRing) Subscribe() chan LogEntry {
	ch := make(chan LogEntry, 64)
	if r == nil {
		close(ch)
		return ch
	}
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return ch
}

// Unsubscribe stops live delivery. Safe to call once.
func (r *LogRing) Unsubscribe(ch chan LogEntry) {
	if r == nil || ch == nil {
		return
	}
	r.mu.Lock()
	_, ok := r.subs[ch]
	if ok {
		delete(r.subs, ch)
	}
	r.mu.Unlock()
	_ = ok
	// Do not close ch: a concurrent Append may still send after unregister.
}

func logEntryMatches(entry LogEntry, q string) bool {
	if strings.Contains(strings.ToLower(entry.Message), q) {
		return true
	}
	if strings.Contains(strings.ToLower(entry.Level), q) {
		return true
	}
	for k, v := range entry.Attrs {
		if strings.Contains(strings.ToLower(k), q) {
			return true
		}
		if strings.Contains(strings.ToLower(fmt.Sprint(v)), q) {
			return true
		}
	}
	return false
}

func logLevelAtLeast(level string, min slog.Level) bool {
	return ParseLogLevel(level) >= min
}

// ParseLogLevel maps API/UI level names onto slog levels. Unknown values are INFO.
func ParseLogLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// ringHandler captures slog records into a LogRing.
type ringHandler struct {
	ring  *LogRing
	level slog.Level
	attrs []slog.Attr
	group string
}

// NewRingHandler returns an slog.Handler that writes into ring.
func NewRingHandler(ring *LogRing, level slog.Level) slog.Handler {
	if ring == nil {
		ring = DefaultLogRing()
	}
	return &ringHandler{ring: ring, level: level}
}

func (h *ringHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *ringHandler) Handle(_ context.Context, r slog.Record) error {
	fields := make(map[string]any, len(h.attrs)+r.NumAttrs())
	prefix := h.group
	for _, a := range h.attrs {
		flattenLogAttr(prefix+a.Key, a.Value, fields)
	}
	r.Attrs(func(a slog.Attr) bool {
		flattenLogAttr(prefix+a.Key, a.Value, fields)
		return true
	})
	if len(fields) == 0 {
		fields = nil
	}
	h.ring.Append(LogEntry{
		Time:    r.Time,
		Level:   r.Level.String(),
		Message: r.Message,
		Attrs:   fields,
	})
	return nil
}

func (h *ringHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &ringHandler{ring: h.ring, level: h.level, attrs: merged, group: h.group}
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &ringHandler{ring: h.ring, level: h.level, attrs: h.attrs, group: h.group + name + "."}
}

func flattenLogAttr(key string, v slog.Value, m map[string]any) {
	switch v.Kind() {
	case slog.KindBool:
		m[key] = v.Bool()
	case slog.KindFloat64:
		m[key] = v.Float64()
	case slog.KindInt64:
		m[key] = v.Int64()
	case slog.KindUint64:
		m[key] = v.Uint64()
	case slog.KindString:
		m[key] = v.String()
	case slog.KindDuration:
		m[key] = v.Duration().String()
	case slog.KindTime:
		m[key] = v.Time().UTC().Format(time.RFC3339Nano)
	case slog.KindGroup:
		for _, ga := range v.Group() {
			flattenLogAttr(key+"."+ga.Key, ga.Value, m)
		}
	case slog.KindAny:
		if err, ok := v.Any().(error); ok && err != nil {
			m[key] = err.Error()
			return
		}
		m[key] = fmt.Sprint(v.Any())
	}
}

type teeHandler struct {
	handlers []slog.Handler
}

func newTeeHandler(handlers ...slog.Handler) slog.Handler {
	var valid []slog.Handler
	for _, h := range handlers {
		if h != nil {
			valid = append(valid, h)
		}
	}
	if len(valid) == 0 {
		return slog.NewTextHandler(io.Discard, nil)
	}
	if len(valid) == 1 {
		return valid[0]
	}
	return &teeHandler{handlers: valid}
}

func (t *teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range t.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (t *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range t.handlers {
		if h.Enabled(ctx, r.Level) {
			_ = h.Handle(ctx, r)
		}
	}
	return nil
}

func (t *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clones := make([]slog.Handler, len(t.handlers))
	for i, h := range t.handlers {
		clones[i] = h.WithAttrs(attrs)
	}
	return newTeeHandler(clones...)
}

func (t *teeHandler) WithGroup(name string) slog.Handler {
	clones := make([]slog.Handler, len(t.handlers))
	for i, h := range t.handlers {
		clones[i] = h.WithGroup(name)
	}
	return newTeeHandler(clones...)
}
