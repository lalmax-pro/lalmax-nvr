package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
)

const (
	serviceLogsDefaultLimit = 200
	serviceLogsMaxLimit     = 2000
)

func (h *Handler) serviceLogRing() *middleware.LogRing {
	if h != nil && h.logRing != nil {
		return h.logRing
	}
	return middleware.DefaultLogRing()
}

func (h *Handler) handleListServiceLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := middleware.LogRingFilter{
		MinLevel: middleware.ParseLogLevel(q.Get("level")),
		Query:    q.Get("q"),
		Limit:    serviceLogsDefaultLimit,
	}
	if value := strings.TrimSpace(q.Get("after_seq")); value != "" {
		seq, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid after_seq parameter")
			return
		}
		filter.AfterSeq = seq
	}
	if value := strings.TrimSpace(q.Get("since")); value != "" {
		since, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since parameter")
			return
		}
		filter.Since = since
	}
	if value := q.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 0 {
			writeError(w, http.StatusBadRequest, "invalid limit parameter")
			return
		}
		filter.Limit = limit
	}
	if filter.Limit == 0 {
		filter.Limit = serviceLogsDefaultLimit
	}
	if filter.Limit > serviceLogsMaxLimit {
		filter.Limit = serviceLogsMaxLimit
	}

	if path := h.serviceLogFile(); path != "" {
		logs, truncated, err := middleware.ReadLogFileTail(path, filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read service log")
			return
		}
		info, _ := os.Stat(path)
		buffered := int64(0)
		if info != nil {
			buffered = info.Size()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"logs":      logs,
			"count":     len(logs),
			"capacity":  0,
			"buffered":  buffered,
			"truncated": truncated,
			"source":    "file",
			"path":      path,
		})
		return
	}

	ring := h.serviceLogRing()
	if ring == nil {
		writeError(w, http.StatusServiceUnavailable, "service logs unavailable")
		return
	}
	logs := ring.Snapshot(filter)
	if logs == nil {
		logs = []middleware.LogEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"logs":      logs,
		"count":     len(logs),
		"capacity":  ring.Capacity(),
		"buffered":  ring.Size(),
		"truncated": ring.Size() == ring.Capacity(),
		"source":    "memory",
		"path":      "",
	})
}

func (h *Handler) serviceLogFile() string {
	if h == nil {
		return ""
	}
	path := strings.TrimSpace(h.serviceLogPath)
	if path == "" {
		return ""
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return ""
	}
	return path
}

func (h *Handler) handleServiceLogsStream(w http.ResponseWriter, r *http.Request) {
	if path := h.serviceLogFile(); path != "" {
		h.streamServiceLogFile(w, r, path)
		return
	}
	ring := h.serviceLogRing()
	if ring == nil {
		writeError(w, http.StatusServiceUnavailable, "service logs unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	minLevel := middleware.ParseLogLevel(r.URL.Query().Get("level"))
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := ring.Subscribe()
	defer ring.Unsubscribe(ch)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	var closed atomic.Bool

	match := func(entry middleware.LogEntry) bool {
		if !logLevelAtLeast(entry.Level, minLevel) {
			return false
		}
		if query == "" {
			return true
		}
		blob := strings.ToLower(entry.Message + " " + entry.Level)
		for k, v := range entry.Attrs {
			blob += " " + strings.ToLower(k) + " " + strings.ToLower(fmt.Sprint(v))
		}
		return strings.Contains(blob, query)
	}

	for {
		select {
		case <-r.Context().Done():
			closed.Store(true)
			return
		case entry, ok := <-ch:
			if !ok || closed.Load() {
				return
			}
			if !match(entry) {
				continue
			}
			data, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (h *Handler) streamServiceLogFile(w http.ResponseWriter, r *http.Request, path string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read service log")
		return
	}
	defer f.Close()

	minLevel := middleware.ParseLogLevel(r.URL.Query().Get("level"))
	query := r.URL.Query().Get("q")
	offset, _ := f.Seek(0, io.SeekEnd)
	if after := strings.TrimSpace(r.URL.Query().Get("after_seq")); after != "" {
		if seq, err := strconv.ParseUint(after, 10, 64); err == nil {
			st, statErr := f.Stat()
			if statErr == nil && int64(seq) < st.Size() {
				offset = int64(seq)
			}
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	var pending string
	parsed := offset

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case <-poll.C:
			st, err := f.Stat()
			if err != nil {
				return
			}
			if st.Size() < offset {
				offset = 0
				parsed = 0
				pending = ""
			}
			if st.Size() == offset {
				continue
			}
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				return
			}
			buf := make([]byte, st.Size()-offset)
			n, err := io.ReadFull(f, buf)
			if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
				return
			}
			offset += int64(n)
			pending += string(buf[:n])
			for {
				i := strings.IndexByte(pending, '\n')
				if i < 0 {
					break
				}
				raw := pending[:i]
				seq := uint64(parsed)
				parsed += int64(i + 1)
				pending = pending[i+1:]
				line := strings.TrimRight(raw, "\r")
				if strings.TrimSpace(line) == "" {
					continue
				}
				entry := middleware.ParseLogFileLine(line, seq)
				filter := middleware.LogRingFilter{MinLevel: minLevel, Query: query, Limit: 1}
				if middleware.LogFileEntryMatches(entry, line, filter) {
					data, err := json.Marshal(entry)
					if err != nil {
						continue
					}
					fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
					flusher.Flush()
				}
			}
		}
	}
}

func logLevelAtLeast(level string, min slog.Level) bool {
	return middleware.ParseLogLevel(level) >= min
}
