package api

import (
    "net/http"
    "sync"
    "time"

    "github.com/lalmax-pro/lalmax-nvr/internal/media"
)

// trafficHistoryCap retains 24 hours at the existing 5-second stream sampler interval.
// These are measured media-session rates, not host NIC byte counters.
const trafficHistoryCap = 24 * 60 * 60 / 5

type trafficSample struct {
    Timestamp int64 `json:"ts"`
    InKbps int64 `json:"in_kbps"`
    OutKbps int64 `json:"out_kbps"`
    Publishers int `json:"publishers"`
    Subscribers int `json:"subscribers"`
    UnknownIn int `json:"unknown_in"`
    UnknownOut int `json:"unknown_out"`
}

type trafficHistory struct {
    mu sync.RWMutex
    points [trafficHistoryCap]trafficSample
    next int
    count int
}

func (h *trafficHistory) add(s trafficSample) {
    h.mu.Lock()
    defer h.mu.Unlock()
    h.points[h.next] = s
    h.next = (h.next + 1) % trafficHistoryCap
    if h.count < trafficHistoryCap { h.count++ }
}

func (h *trafficHistory) since(cutoff int64, stride int64) []trafficSample {
    h.mu.RLock()
    defer h.mu.RUnlock()
    out := make([]trafficSample, 0)
    start := (h.next - h.count + trafficHistoryCap) % trafficHistoryCap
    var lastBucket int64 = -1
    for i := 0; i < h.count; i++ {
        p := h.points[(start+i)%trafficHistoryCap]
        if p.Timestamp < cutoff { continue }
        bucket := p.Timestamp / stride
        if len(out) == 0 || bucket != lastBucket {
            out = append(out, p)
            lastBucket = bucket
        } else {
            out[len(out)-1] = p
        }
    }
    return out
}

func (h *Handler) sampleTrafficHistory(streams []media.StreamInfo, now int64) {
    if h.trafficHistory == nil { return }
    p := trafficSample{Timestamp: now}
    for _, s := range streams {
        if !s.Active { continue }
        if s.Publisher != nil {
            p.Publishers++
            rate := s.Publisher.ReadBitrateKbits
            if rate <= 0 { rate = s.Publisher.BitrateKbits }
            if rate > 0 { p.InKbps += int64(rate) } else { p.UnknownIn++ }
        }
        for _, sub := range s.Subscribers {
            if media.IsInternalRecorderSession(sub.Protocol, sub.SessionID) { continue }
            p.Subscribers++
            if sub.WriteBitrateKbits > 0 {
                p.OutKbps += int64(sub.WriteBitrateKbits)
            } else { p.UnknownOut++ }
        }
    }
    h.trafficHistory.add(p)
}

func (h *Handler) handleTrafficHistory(w http.ResponseWriter, r *http.Request) {
    if h.trafficHistory == nil {
        writeJSON(w, http.StatusOK, []trafficSample{})
        return
    }
    period := 15 * time.Minute
    switch r.URL.Query().Get("period") {
    case "5m": period = 5 * time.Minute
    case "15m": period = 15 * time.Minute
    case "30m": period = 30 * time.Minute
    case "1h": period = time.Hour
    case "6h": period = 6 * time.Hour
    case "24h": period = 24 * time.Hour
    }
    stride := int64(5)
    if period >= time.Hour { stride = 30 }
    if period >= 6*time.Hour { stride = 60 }
    if period >= 24*time.Hour { stride = 120 }
    writeJSON(w, http.StatusOK, h.trafficHistory.since(time.Now().Add(-period).Unix(), stride))
}
