package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestS2LogRingNilReceiver(t *testing.T) {
	var r *LogRing
	require.Equal(t, 0, r.Capacity())
	require.Equal(t, 0, r.Size())
	r.Append(LogEntry{Message: "ignored"})
	require.Nil(t, r.Snapshot(LogRingFilter{}))
	ch := r.Subscribe()
	require.NotNil(t, ch)
	_, open := <-ch
	require.False(t, open, "subscribe on a nil ring returns a closed channel")
	r.Unsubscribe(nil)
	r.Unsubscribe(make(chan LogEntry))
}

func TestS2LogRingCapacityFloor(t *testing.T) {
	r := NewLogRing(0)
	require.Equal(t, defaultLogRingCapacity, r.Capacity())
	r = NewLogRing(-5)
	require.Equal(t, defaultLogRingCapacity, r.Capacity())
	r = NewLogRing(3)
	require.Equal(t, 3, r.Capacity())
	require.Equal(t, 0, r.Size())
}

func TestS2LogRingSnapshotFilters(t *testing.T) {
	r := NewLogRing(4)
	now := time.Now()
	r.Append(LogEntry{Time: now.Add(-3 * time.Hour), Level: "INFO", Message: "old"})
	r.Append(LogEntry{Time: now.Add(-2 * time.Hour), Level: "WARN", Message: "warned", Attrs: map[string]any{"camera": "cam1"}})
	r.Append(LogEntry{Time: now.Add(-time.Hour), Level: "ERROR", Message: "boom", Attrs: map[string]any{"code": 500}})
	r.Append(LogEntry{Time: now, Level: "DEBUG", Message: "chatty"})
	require.Equal(t, 4, r.Size())

	// The default minimum level is INFO, so the DEBUG record is filtered out.
	require.Len(t, r.Snapshot(LogRingFilter{}), 3)
	require.Len(t, r.Snapshot(LogRingFilter{MinLevel: ParseLogLevel("debug")}), 4)

	// AfterSeq skips already-delivered entries.
	snapshot := r.Snapshot(LogRingFilter{AfterSeq: 2})
	require.Len(t, snapshot, 1)
	require.Equal(t, "boom", snapshot[0].Message)

	// Since drops older entries.
	require.Len(t, r.Snapshot(LogRingFilter{Since: now.Add(-90 * time.Minute)}), 1)

	// Minimum level filtering.
	require.Len(t, r.Snapshot(LogRingFilter{MinLevel: ParseLogLevel("warn")}), 2)

	// Query matches message, level and attribute keys/values.
	require.Len(t, r.Snapshot(LogRingFilter{Query: "CAM1"}), 1)
	require.Len(t, r.Snapshot(LogRingFilter{Query: "boom"}), 1)
	require.Len(t, r.Snapshot(LogRingFilter{Query: "code"}), 1)
	require.Len(t, r.Snapshot(LogRingFilter{Query: "500"}), 1)
	require.Len(t, r.Snapshot(LogRingFilter{Query: "chatty", MinLevel: ParseLogLevel("debug")}), 1)
	require.Len(t, r.Snapshot(LogRingFilter{Query: "error"}), 1, "query also matches the level name")
	require.Empty(t, r.Snapshot(LogRingFilter{Query: "nothing-matches"}))

	// Limit keeps the newest matching entries.
	limited := r.Snapshot(LogRingFilter{Limit: 2})
	require.Len(t, limited, 2)
	require.Equal(t, "warned", limited[0].Message)
	require.Equal(t, "boom", limited[1].Message)

	// Wrapping past capacity keeps chronological order.
	small := NewLogRing(2)
	small.Append(LogEntry{Message: "a"})
	small.Append(LogEntry{Message: "b"})
	small.Append(LogEntry{Message: "c"})
	got := small.Snapshot(LogRingFilter{})
	require.Len(t, got, 2)
	require.Equal(t, "b", got[0].Message)
	require.Equal(t, "c", got[1].Message)
}

func TestS2LogRingSubscribeOverflowAndUnsubscribe(t *testing.T) {
	r := NewLogRing(8)
	ch := r.Subscribe()
	defer r.Unsubscribe(ch)

	// Fill the subscriber buffer beyond its capacity; extra events are dropped.
	for i := 0; i < 70; i++ {
		r.Append(LogEntry{Message: "flood"})
	}
	received := 0
	for {
		select {
		case <-ch:
			received++
			continue
		default:
		}
		break
	}
	require.Equal(t, 64, received, "overflow events are dropped instead of blocking")

	// Unsubscribe twice is safe.
	r.Unsubscribe(ch)
	r.Unsubscribe(ch)
	r.Append(LogEntry{Message: "after unsubscribe"})
	select {
	case entry := <-ch:
		require.Failf(t, "unexpected delivery after unsubscribe", "%+v", entry)
	default:
	}
}

func TestS2ParseLogLevel(t *testing.T) {
	require.Equal(t, slog.LevelDebug, ParseLogLevel("debug"))
	require.Equal(t, slog.LevelDebug, ParseLogLevel(" DEBUG "))
	require.Equal(t, slog.LevelWarn, ParseLogLevel("warn"))
	require.Equal(t, slog.LevelWarn, ParseLogLevel("warning"))
	require.Equal(t, slog.LevelError, ParseLogLevel("error"))
	require.Equal(t, slog.LevelInfo, ParseLogLevel("info"))
	require.Equal(t, slog.LevelInfo, ParseLogLevel("nonsense"))
}

func TestS2RingHandlerFlattenAttrs(t *testing.T) {
	r := NewLogRing(16)
	h := NewRingHandler(r, slog.LevelDebug)

	// Group prefixing plus every supported attribute kind.
	grouped := h.WithGroup("grp").WithAttrs([]slog.Attr{slog.String("static", "v")})
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "captured", 0)
	record.AddAttrs(
		slog.Bool("b", true),
		slog.Float64("f", 1.5),
		slog.Int64("i", 42),
		slog.Uint64("u", 7),
		slog.String("s", "str"),
		slog.Duration("d", 2*time.Second),
		slog.Time("t", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		slog.Group("g", slog.String("inner", "x")),
		slog.Any("err", errors.New("boom")),
		slog.Any("any", []string{"a", "b"}),
	)
	require.NoError(t, grouped.Handle(context.Background(), record))

	entries := r.Snapshot(LogRingFilter{})
	require.Len(t, entries, 1)
	attrs := entries[0].Attrs
	require.Equal(t, true, attrs["grp.b"])
	require.Equal(t, 1.5, attrs["grp.f"])
	require.Equal(t, int64(42), attrs["grp.i"])
	require.Equal(t, uint64(7), attrs["grp.u"])
	require.Equal(t, "str", attrs["grp.s"])
	require.Equal(t, "2s", attrs["grp.d"])
	require.Equal(t, "2026-01-02T03:04:05Z", attrs["grp.t"])
	require.Equal(t, "x", attrs["grp.g.inner"])
	require.Equal(t, "boom", attrs["grp.err"])
	require.Equal(t, "[a b]", attrs["grp.any"])
	require.Equal(t, "v", attrs["grp.static"])

	// A record without attributes stores no attribute map.
	h2 := NewRingHandler(r, slog.LevelInfo)
	bare := slog.NewRecord(time.Now(), slog.LevelInfo, "bare", 0)
	require.NoError(t, h2.Handle(context.Background(), bare))
	entries = r.Snapshot(LogRingFilter{})
	require.Nil(t, entries[len(entries)-1].Attrs)

	// An empty group name is a no-op.
	require.Same(t, h2, h2.WithGroup(""))
	// Enabled honours the configured level.
	require.True(t, h2.Enabled(context.Background(), slog.LevelInfo))
	require.False(t, h2.Enabled(context.Background(), slog.LevelDebug))

	// A nil ring falls back to the process-wide ring.
	require.NotNil(t, NewRingHandler(nil, slog.LevelInfo))
}

func TestS2NewTeeHandlerVariants(t *testing.T) {
	// No valid handlers: a discard text handler is returned.
	empty := newTeeHandler(nil, nil)
	require.NotNil(t, empty)
	require.NoError(t, empty.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "x", 0)))

	// A single handler is returned unchanged.
	single := NewRingHandler(NewLogRing(4), slog.LevelInfo)
	require.Same(t, single, newTeeHandler(nil, single))

	// Multiple handlers fan out.
	a := NewLogRing(4)
	b := NewLogRing(4)
	tee := newTeeHandler(NewRingHandler(a, slog.LevelInfo), NewRingHandler(b, slog.LevelWarn))
	require.True(t, tee.Enabled(context.Background(), slog.LevelWarn))
	require.False(t, tee.Enabled(context.Background(), slog.LevelDebug))

	record := slog.NewRecord(time.Now(), slog.LevelInfo, "teed", 0)
	require.NoError(t, tee.Handle(context.Background(), record))
	require.Equal(t, 1, a.Size())
	require.Equal(t, 0, b.Size(), "handlers below the level must not receive the record")

	withAttrs := tee.WithAttrs([]slog.Attr{slog.String("k", "v")})
	slog.New(withAttrs).Info("with attrs")
	require.Equal(t, 2, a.Size())
	withGroup := tee.WithGroup("grp")
	slog.New(withGroup).Warn("with group")
	require.Equal(t, 3, a.Size())
	require.Equal(t, 1, b.Size())
}

func TestS2LevelFilterHandler(t *testing.T) {
	ring := NewLogRing(8)
	inner := NewRingHandler(ring, slog.LevelDebug)
	filtered := newLevelFilterHandler(inner, slog.LevelWarn)

	require.False(t, filtered.Enabled(context.Background(), slog.LevelInfo))
	require.True(t, filtered.Enabled(context.Background(), slog.LevelWarn))

	// Below-level records are silently discarded.
	require.NoError(t, filtered.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "dropped", 0)))
	require.Equal(t, 0, ring.Size())

	require.NoError(t, filtered.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelError, "kept", 0)))
	require.Equal(t, 1, ring.Size())

	// WithAttrs/WithGroup preserve filtering behaviour.
	withAttrs := filtered.WithAttrs([]slog.Attr{slog.String("k", "v")})
	require.NoError(t, withAttrs.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelWarn, "attrs", 0)))
	require.Equal(t, 2, ring.Size())
	withGroup := filtered.WithGroup("g")
	require.NoError(t, withGroup.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelWarn, "group", 0)))
	require.Equal(t, 3, ring.Size())

	// A handler that disables everything keeps Enabled false.
	disabled := newLevelFilterHandler(inner, slog.LevelError+4)
	require.False(t, disabled.Enabled(context.Background(), slog.LevelError))
}

func TestS2ResolveServiceLogPath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "lalmax-nvr.log")
	require.NoError(t, os.WriteFile(file, []byte("x\n"), 0o644))

	// An explicit environment override pointing at a regular file wins.
	t.Setenv("NVR_LOG_FILE", file)
	require.Equal(t, file, ResolveServiceLogPath())

	// A directory override is ignored; the search continues.
	t.Setenv("NVR_LOG_FILE", dir)
	require.NotEqual(t, dir, ResolveServiceLogPath())

	// Without any candidate the resolver returns an empty string.
	t.Setenv("NVR_LOG_FILE", "")
	empty := t.TempDir()
	t.Chdir(empty)
	require.Equal(t, "", ResolveServiceLogPath())

	// The working-directory candidate is resolved to an absolute path.
	require.NoError(t, os.MkdirAll(filepath.Join("logs"), 0o755))
	cwdFile := filepath.Join("logs", "lalmax-nvr.log")
	require.NoError(t, os.WriteFile(cwdFile, []byte("x\n"), 0o644))
	found := ResolveServiceLogPath()
	require.True(t, filepath.IsAbs(found))
	require.Equal(t, "lalmax-nvr.log", filepath.Base(found))
}

func TestS2ReadLogFileTailEdges(t *testing.T) {
	dir := t.TempDir()

	// Missing file.
	_, _, err := ReadLogFileTail(filepath.Join(dir, "absent.log"), LogRingFilter{})
	require.Error(t, err)

	// No trailing newline exercises the unterminated last line path.
	path := filepath.Join(dir, "no-newline.log")
	require.NoError(t, os.WriteFile(path, []byte("only line"), 0o644))
	entries, truncated, err := ReadLogFileTail(path, LogRingFilter{})
	require.NoError(t, err)
	require.False(t, truncated)
	require.Len(t, entries, 1)
	require.Equal(t, "only line", entries[0].Message)

	// Blank lines are skipped and a limit trims the oldest matches.
	path = filepath.Join(dir, "lines.log")
	body := "first\n\n   \nsecond\r\nthird\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	entries, truncated, err = ReadLogFileTail(path, LogRingFilter{Limit: 2})
	require.NoError(t, err)
	require.True(t, truncated, "trimming matches must mark the result truncated")
	require.Len(t, entries, 2)
	require.Equal(t, "second", entries[0].Message)
	require.Equal(t, "third", entries[1].Message)

	// No matches yields an empty (non-nil) slice.
	entries, _, err = ReadLogFileTail(path, LogRingFilter{Query: "no-such-text"})
	require.NoError(t, err)
	require.NotNil(t, entries)
	require.Empty(t, entries)

	// Files above the read cap return only their tail and are marked truncated.
	path = filepath.Join(dir, "big.log")
	f, err := os.Create(path)
	require.NoError(t, err)
	line := "time=2026-01-01T00:00:00Z level=INFO msg=bulk\n"
	_, err = f.WriteString(strings.Repeat(line, serviceLogReadCap/len(line)+10))
	require.NoError(t, err)
	_, err = f.WriteString("time=2026-01-01T00:00:01Z level=ERROR msg=tail-marker\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	entries, truncated, err = ReadLogFileTail(path, LogRingFilter{MinLevel: ParseLogLevel("error"), Limit: 5})
	require.NoError(t, err)
	require.True(t, truncated)
	require.Len(t, entries, 1)
	require.Equal(t, "tail-marker", entries[0].Message)

	// A capped read that starts inside a single long line has no boundary to skip.
	path = filepath.Join(dir, "one-line.log")
	f, err = os.Create(path)
	require.NoError(t, err)
	_, err = f.WriteString(strings.Repeat("a", serviceLogReadCap+128))
	require.NoError(t, err)
	require.NoError(t, f.Close())
	entries, truncated, err = ReadLogFileTail(path, LogRingFilter{Limit: 1})
	require.NoError(t, err)
	require.True(t, truncated)
	require.Len(t, entries, 1)
}

func TestS2ParseLogFileLineVariants(t *testing.T) {
	jsonLine := `{"time":"2026-01-02T03:04:05.123456789Z","level":"warn","msg":"json message","camera":"cam1"}`
	entry := ParseLogFileLine(jsonLine, 1)
	require.Equal(t, "WARN", entry.Level)
	require.Equal(t, "json message", entry.Message)
	require.Equal(t, "cam1", entry.Attrs["camera"])
	require.Equal(t, 2026, entry.Time.Year())

	// JSON without the well-known fields keeps the raw line as the message.
	entry = ParseLogFileLine(`{"camera":"cam1"}`, 2)
	require.Equal(t, "INFO", entry.Level)
	require.Equal(t, `{"camera":"cam1"}`, entry.Message)
	require.Equal(t, "cam1", entry.Attrs["camera"])

	// A line that only looks like JSON falls through to the plain branch.
	entry = ParseLogFileLine(`{not-json`, 3)
	require.Equal(t, "INFO", entry.Level)
	require.Equal(t, `{not-json`, entry.Message)

	// Structured text log with an invalid timestamp and an unknown duration key.
	entry = ParseLogFileLine(`time=bogus level=debug msg=hello extra=1`, 4)
	require.Equal(t, "DEBUG", entry.Level)
	require.Equal(t, "hello", entry.Message)
	require.Equal(t, "1", entry.Attrs["extra"])
	require.False(t, entry.Time.IsZero())

	// GIN lines map HTTP status onto levels.
	entry = ParseLogFileLine(`[GIN] 2026/09/22 - 15:20:48 | 404 | 1ms | 127.0.0.1 | GET "/x"`, 5)
	require.Equal(t, "WARN", entry.Level)
	entry = ParseLogFileLine(`[GIN] 2026/09/22 - 15:20:48 | abc | 1ms | 127.0.0.1 | GET "/x"`, 6)
	require.Equal(t, "INFO", entry.Level)
	entry = ParseLogFileLine(`[GIN] no separators here`, 7)
	require.Equal(t, "INFO", entry.Level)

	// Plain lines keep the raw text.
	entry = ParseLogFileLine("just text", 8)
	require.Equal(t, "just text", entry.Message)
	require.Equal(t, "INFO", entry.Level)
}

func TestS2LogFileEntryMatches(t *testing.T) {
	entry := LogEntry{Seq: 10, Level: "ERROR", Message: "disk full", Time: time.Now()}
	raw := "time=... level=ERROR msg=\"disk full\" camera=cam1"

	require.True(t, LogFileEntryMatches(entry, raw, LogRingFilter{}))
	require.False(t, LogFileEntryMatches(entry, raw, LogRingFilter{MinLevel: slog.LevelError + 1}))
	require.False(t, LogFileEntryMatches(entry, raw, LogRingFilter{Since: entry.Time.Add(time.Minute)}))
	require.False(t, LogFileEntryMatches(entry, raw, LogRingFilter{AfterSeq: 10}))
	require.True(t, LogFileEntryMatches(entry, raw, LogRingFilter{AfterSeq: 9}))
	require.True(t, LogFileEntryMatches(entry, raw, LogRingFilter{Query: "CAM1"}))
	require.True(t, LogFileEntryMatches(entry, raw, LogRingFilter{Query: "disk"}))
	require.False(t, LogFileEntryMatches(entry, raw, LogRingFilter{Query: "nomatch"}))
}

func TestS2SplitLogFields(t *testing.T) {
	// Quoted values keep their spaces, unterminated quotes are tolerated.
	fields := splitLogFields(`time=2026-01-01T00:00:00Z level=INFO msg="two words" extra=1`)
	require.Equal(t, [][2]string{
		{"time", "2026-01-01T00:00:00Z"},
		{"level", "INFO"},
		{"msg", "two words"},
		{"extra", "1"},
	}, fields)

	fields = splitLogFields(`msg="unterminated`)
	require.Equal(t, [][2]string{{"msg", "unterminated"}}, fields)

	// A key containing whitespace stops parsing.
	fields = splitLogFields(`a=1 bad key=2`)
	require.Equal(t, [][2]string{{"a", "1"}}, fields)

	// No key=value pairs at all.
	require.Empty(t, splitLogFields("plain text"))
	require.Empty(t, splitLogFields("   "))
	require.Empty(t, splitLogFields("=novalue"))
}

func TestS2StatusRecorderFlush(t *testing.T) {
	// A wrapper around a flushing writer forwards Flush.
	rec := newTestStatusRecorder(t)
	rec.Flush()

	// A writer without Flush is tolerated.
	plain := &StatusRecorder{ResponseWriter: s2PlainWriter{header: http.Header{}}}
	plain.Flush()
	rec.WriteHeader(http.StatusTeapot)
	require.Equal(t, http.StatusTeapot, rec.Status)
}

type s2PlainWriter struct{ header http.Header }

func (w s2PlainWriter) Header() http.Header         { return w.header }
func (w s2PlainWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w s2PlainWriter) WriteHeader(int)             {}

func TestS2SetupLoggerTeesIntoRing(t *testing.T) {
	logger := SetupLogger("debug", "json")
	require.NotNil(t, logger)
	before := DefaultLogRing().Size()
	logger.Warn("s2 ring capture", "camera", "cam1")
	entries := DefaultLogRing().Snapshot(LogRingFilter{Query: "s2 ring capture"})
	require.NotEmpty(t, entries)
	require.Greater(t, DefaultLogRing().Size(), 0)
	require.GreaterOrEqual(t, DefaultLogRing().Size(), before)
	require.NotNil(t, ComponentLogger("s2-component"))
}
