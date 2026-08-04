package api

import (
	"net/http"
	"strconv"
	"time"
)

func (h *Handler) handleAPIObservability(w http.ResponseWriter, r *http.Request) {
	window := parseObservabilityWindow(r.URL.Query().Get("window"), 5*time.Minute)
	series := parseObservabilityWindow(r.URL.Query().Get("series"), time.Hour)
	limit := 30
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = value
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, h.apiObserver.Aggregator().Snapshot(window, series, limit))
}

func parseObservabilityWindow(raw string, fallback time.Duration) time.Duration {
	switch raw {
	case "1m":
		return time.Minute
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "30m":
		return 30 * time.Minute
	case "1h":
		return time.Hour
	default:
		return fallback
	}
}
