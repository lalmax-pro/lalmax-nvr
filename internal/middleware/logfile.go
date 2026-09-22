package middleware

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const serviceLogReadCap = 2 << 20 // last 2MiB of lalmax-nvr.log

// ResolveServiceLogPath finds logs/lalmax-nvr.log written by the start script.
// NVR_LOG_FILE overrides the search. An empty result means the file is absent.
func ResolveServiceLogPath() string {
	if path := strings.TrimSpace(os.Getenv("NVR_LOG_FILE")); path != "" {
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
	}
	var candidates []string
	candidates = append(candidates, filepath.Join("logs", "lalmax-nvr.log"))
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "logs", "lalmax-nvr.log"),
			filepath.Join(dir, "..", "logs", "lalmax-nvr.log"),
		)
	}
	for _, path := range candidates {
		st, err := os.Stat(path)
		if err == nil && !st.IsDir() {
			abs, err := filepath.Abs(path)
			if err != nil {
				return path
			}
			return abs
		}
	}
	return ""
}

// ReadLogFileTail returns the newest matching lines from a log file.
// Seq is the byte offset of each line so a follower can continue after it.
func ReadLogFileTail(path string, filter LogRingFilter) ([]LogEntry, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	size := st.Size()
	start := int64(0)
	truncated := false
	if size > serviceLogReadCap {
		start = size - serviceLogReadCap
		truncated = true
	}
	buf := make([]byte, size-start)
	n, err := f.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, truncated, err
	}
	buf = buf[:n]
	if start > 0 {
		if i := strings.IndexByte(string(buf), '\n'); i >= 0 {
			buf = buf[i+1:]
			start += int64(i + 1)
		}
	}
	text := string(buf)
	if filter.Limit <= 0 {
		filter.Limit = 200
	}
	var matched []LogEntry
	offset := start
	for len(text) > 0 {
		line := text
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			line = text[:i]
			text = text[i+1:]
		} else {
			text = ""
		}
		lineOff := offset
		offset += int64(len(line) + 1)
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		entry := ParseLogFileLine(line, uint64(lineOff))
		if !LogFileEntryMatches(entry, line, filter) {
			continue
		}
		matched = append(matched, entry)
	}
	if len(matched) > filter.Limit {
		truncated = true
		matched = matched[len(matched)-filter.Limit:]
	}
	if matched == nil {
		matched = []LogEntry{}
	}
	return matched, truncated, nil
}

// ParseLogFileLine turns one lalmax-nvr.log line into a service-log entry.
func ParseLogFileLine(line string, seq uint64) LogEntry {
	line = strings.TrimRight(line, "\r")
	if strings.HasPrefix(line, "{") {
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err == nil {
			entry := LogEntry{Seq: seq, Level: "INFO", Message: line, Time: time.Now()}
			if msg, ok := raw["msg"].(string); ok {
				entry.Message = msg
			}
			if level, ok := raw["level"].(string); ok && level != "" {
				entry.Level = strings.ToUpper(level)
			}
			if ts, ok := raw["time"].(string); ok {
				if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
					entry.Time = t
				}
			}
			delete(raw, "msg")
			delete(raw, "level")
			delete(raw, "time")
			if len(raw) > 0 {
				entry.Attrs = raw
			}
			return entry
		}
	}
	if strings.HasPrefix(line, "time=") {
		fields := splitLogFields(line)
		entry := LogEntry{Seq: seq, Level: "INFO", Message: line, Time: time.Now()}
		attrs := map[string]any{}
		for _, kv := range fields {
			switch kv[0] {
			case "time":
				if t, err := time.Parse(time.RFC3339Nano, kv[1]); err == nil {
					entry.Time = t
				}
			case "level":
				entry.Level = strings.ToUpper(kv[1])
			case "msg":
				entry.Message = kv[1]
			default:
				attrs[kv[0]] = kv[1]
			}
		}
		if len(attrs) > 0 {
			entry.Attrs = attrs
		}
		return entry
	}
	if strings.HasPrefix(line, "[GIN]") {
		entry := LogEntry{Seq: seq, Level: "INFO", Message: line, Time: time.Now()}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "[GIN]"))
		if i := strings.Index(rest, " | "); i > 0 {
			if t, err := time.ParseInLocation("2006/01/02 - 15:04:05", strings.TrimSpace(rest[:i]), time.Local); err == nil {
				entry.Time = t
			}
			parts := strings.Split(rest, "|")
			if len(parts) >= 2 {
				status, err := strconv.Atoi(strings.TrimSpace(parts[1]))
				if err == nil {
					switch {
					case status >= 500:
						entry.Level = "ERROR"
					case status >= 400:
						entry.Level = "WARN"
					}
				}
			}
		}
		return entry
	}
	return LogEntry{Seq: seq, Level: "INFO", Message: line, Time: time.Now()}
}

func LogFileEntryMatches(entry LogEntry, raw string, filter LogRingFilter) bool {
	if ParseLogLevel(entry.Level) < filter.MinLevel {
		return false
	}
	if !filter.Since.IsZero() && entry.Time.Before(filter.Since) {
		return false
	}
	if filter.AfterSeq > 0 && entry.Seq <= filter.AfterSeq {
		return false
	}
	q := strings.ToLower(strings.TrimSpace(filter.Query))
	if q == "" {
		return true
	}
	return strings.Contains(strings.ToLower(raw), q) || strings.Contains(strings.ToLower(entry.Message), q)
}

func splitLogFields(line string) [][2]string {
	var out [][2]string
	i := 0
	for i < len(line) {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		if i >= len(line) {
			break
		}
		eq := strings.IndexByte(line[i:], '=')
		if eq <= 0 {
			break
		}
		key := line[i : i+eq]
		if strings.ContainsAny(key, " \t") {
			break
		}
		i += eq + 1
		if i < len(line) && line[i] == '"' {
			j := i + 1
			for j < len(line) && line[j] != '"' {
				j++
			}
			val := ""
			if j <= len(line) {
				val = line[i+1 : min(j, len(line))]
			}
			if j < len(line) && line[j] == '"' {
				j++
			}
			out = append(out, [2]string{key, val})
			i = j
			continue
		}
		j := i
		for j < len(line) && line[j] != ' ' {
			j++
		}
		out = append(out, [2]string{key, line[i:j]})
		i = j
	}
	return out
}
