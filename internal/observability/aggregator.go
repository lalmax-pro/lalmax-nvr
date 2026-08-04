package observability

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	bucketWidth   = 10 * time.Second
	bucketCount   = 360 // one hour at ten-second resolution
	maxRecentErrs = 100
)

var latencyBoundsMS = [...]float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

type Sample struct {
	Timestamp     time.Time
	Method        string
	Route         string
	Status        int
	Duration      time.Duration
	ResponseBytes int64
	TraceID       string
}

type routeKey struct {
	method string
	route  string
}

type aggregate struct {
	requests      uint64
	status4xx     uint64
	status5xx     uint64
	responseBytes uint64
	durationMS    float64
	maxMS         float64
	histogram     [len(latencyBoundsMS) + 1]uint64
}

func (a *aggregate) add(s Sample) {
	durationMS := float64(s.Duration) / float64(time.Millisecond)
	a.requests++
	if s.Status >= 400 && s.Status < 500 {
		a.status4xx++
	}
	if s.Status >= 500 {
		a.status5xx++
	}
	if s.ResponseBytes > 0 {
		a.responseBytes += uint64(s.ResponseBytes)
	}
	a.durationMS += durationMS
	if durationMS > a.maxMS {
		a.maxMS = durationMS
	}
	a.histogram[histogramIndex(durationMS)]++
}

func (a *aggregate) merge(other aggregate) {
	a.requests += other.requests
	a.status4xx += other.status4xx
	a.status5xx += other.status5xx
	a.responseBytes += other.responseBytes
	a.durationMS += other.durationMS
	if other.maxMS > a.maxMS {
		a.maxMS = other.maxMS
	}
	for i := range a.histogram {
		a.histogram[i] += other.histogram[i]
	}
}

func histogramIndex(durationMS float64) int {
	for i, bound := range latencyBoundsMS {
		if durationMS <= bound {
			return i
		}
	}
	return len(latencyBoundsMS)
}

func (a aggregate) percentile(q float64) float64 {
	if a.requests == 0 {
		return 0
	}
	target := uint64(float64(a.requests)*q + 0.999999)
	if target == 0 {
		target = 1
	}
	var seen uint64
	for i, count := range a.histogram {
		seen += count
		if seen >= target {
			if i < len(latencyBoundsMS) {
				return latencyBoundsMS[i]
			}
			return a.maxMS
		}
	}
	return a.maxMS
}

type timeBucket struct {
	start  int64
	total  aggregate
	routes map[routeKey]aggregate
}

type Aggregator struct {
	mu           sync.RWMutex
	buckets      [bucketCount]timeBucket
	recentErrors []RecentError
	inFlight     atomic.Int64
	now          func() time.Time
}

func NewAggregator() *Aggregator {
	return &Aggregator{now: time.Now}
}

func (a *Aggregator) Begin() {
	a.inFlight.Add(1)
}

func (a *Aggregator) End() {
	a.inFlight.Add(-1)
}

func (a *Aggregator) Observe(s Sample) {
	if s.Timestamp.IsZero() {
		s.Timestamp = a.now()
	}
	start := s.Timestamp.Unix() / int64(bucketWidth/time.Second) * int64(bucketWidth/time.Second)
	idx := int((start / int64(bucketWidth/time.Second)) % bucketCount)

	a.mu.Lock()
	defer a.mu.Unlock()

	b := &a.buckets[idx]
	if b.start != start {
		*b = timeBucket{start: start, routes: make(map[routeKey]aggregate)}
	}
	b.total.add(s)
	key := routeKey{method: s.Method, route: s.Route}
	routeStats := b.routes[key]
	routeStats.add(s)
	b.routes[key] = routeStats

	if s.Status >= 500 {
		a.recentErrors = append(a.recentErrors, RecentError{
			Timestamp:  s.Timestamp.Unix(),
			Method:     s.Method,
			Route:      s.Route,
			Status:     s.Status,
			DurationMS: round1(float64(s.Duration) / float64(time.Millisecond)),
			TraceID:    s.TraceID,
		})
		if len(a.recentErrors) > maxRecentErrs {
			copy(a.recentErrors, a.recentErrors[len(a.recentErrors)-maxRecentErrs:])
			a.recentErrors = a.recentErrors[:maxRecentErrs]
		}
	}
}

type LatencySummary struct {
	AverageMS float64 `json:"avg_ms"`
	P50MS     float64 `json:"p50_ms"`
	P95MS     float64 `json:"p95_ms"`
	P99MS     float64 `json:"p99_ms"`
	MaxMS     float64 `json:"max_ms"`
}

type Summary struct {
	Requests        uint64         `json:"requests"`
	RPS             float64        `json:"rps"`
	InFlight        int64          `json:"in_flight"`
	Status4xx       uint64         `json:"status_4xx"`
	Status5xx       uint64         `json:"status_5xx"`
	ClientErrorRate float64        `json:"client_error_rate"`
	ErrorRate       float64        `json:"error_rate"`
	ResponseBytes   uint64         `json:"response_bytes"`
	Latency         LatencySummary `json:"latency"`
}

type SeriesPoint struct {
	Timestamp int64   `json:"timestamp"`
	Requests  uint64  `json:"requests"`
	RPS       float64 `json:"rps"`
	Status4xx uint64  `json:"status_4xx"`
	Status5xx uint64  `json:"status_5xx"`
	ErrorRate float64 `json:"error_rate"`
	P95MS     float64 `json:"p95_ms"`
}

type RouteStats struct {
	Method          string  `json:"method"`
	Route           string  `json:"route"`
	Requests        uint64  `json:"requests"`
	RPS             float64 `json:"rps"`
	Status4xx       uint64  `json:"status_4xx"`
	Status5xx       uint64  `json:"status_5xx"`
	ClientErrorRate float64 `json:"client_error_rate"`
	ErrorRate       float64 `json:"error_rate"`
	AverageMS       float64 `json:"avg_ms"`
	P95MS           float64 `json:"p95_ms"`
	P99MS           float64 `json:"p99_ms"`
	MaxMS           float64 `json:"max_ms"`
	ResponseBytes   uint64  `json:"response_bytes"`
}

type RecentError struct {
	Timestamp  int64   `json:"timestamp"`
	Method     string  `json:"method"`
	Route      string  `json:"route"`
	Status     int     `json:"status"`
	DurationMS float64 `json:"duration_ms"`
	TraceID    string  `json:"trace_id,omitempty"`
}

type DashboardResponse struct {
	GeneratedAt   int64         `json:"generated_at"`
	WindowSeconds int64         `json:"window_seconds"`
	Summary       Summary       `json:"summary"`
	Series        []SeriesPoint `json:"series"`
	Routes        []RouteStats  `json:"routes"`
	RecentErrors  []RecentError `json:"recent_errors"`
}

func (a *Aggregator) Snapshot(window, seriesWindow time.Duration, limit int) DashboardResponse {
	window = clampWindow(window)
	seriesWindow = clampWindow(seriesWindow)
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}

	now := a.now()
	nowUnix := now.Unix()
	windowCutoff := now.Add(-window).Unix()
	seriesCutoff := now.Add(-seriesWindow).Unix()

	a.mu.RLock()
	defer a.mu.RUnlock()

	var total aggregate
	routeTotals := make(map[routeKey]aggregate)
	seriesByStart := make(map[int64]aggregate)
	for i := range a.buckets {
		b := &a.buckets[i]
		if b.start == 0 || b.start > nowUnix {
			continue
		}
		if b.start >= windowCutoff {
			total.merge(b.total)
			for key, stats := range b.routes {
				routeTotal := routeTotals[key]
				routeTotal.merge(stats)
				routeTotals[key] = routeTotal
			}
		}
		if b.start >= seriesCutoff {
			seriesByStart[b.start] = b.total
		}
	}

	windowSeconds := window.Seconds()
	resp := DashboardResponse{
		GeneratedAt:   nowUnix,
		WindowSeconds: int64(windowSeconds),
		Summary:       summaryFromAggregate(total, windowSeconds, a.inFlight.Load()),
		Series:        make([]SeriesPoint, 0, int(seriesWindow/bucketWidth)+1),
		Routes:        make([]RouteStats, 0, len(routeTotals)),
		RecentErrors:  make([]RecentError, 0),
	}

	stepSeconds := int64(bucketWidth / time.Second)
	start := seriesCutoff / stepSeconds * stepSeconds
	end := nowUnix / stepSeconds * stepSeconds
	for ts := start; ts <= end; ts += stepSeconds {
		stats := seriesByStart[ts]
		resp.Series = append(resp.Series, SeriesPoint{
			Timestamp: ts,
			Requests:  stats.requests,
			RPS:       round2(float64(stats.requests) / bucketWidth.Seconds()),
			Status4xx: stats.status4xx,
			Status5xx: stats.status5xx,
			ErrorRate: rate(stats.status5xx, stats.requests),
			P95MS:     round1(stats.percentile(.95)),
		})
	}

	for key, stats := range routeTotals {
		average := float64(0)
		if stats.requests > 0 {
			average = stats.durationMS / float64(stats.requests)
		}
		resp.Routes = append(resp.Routes, RouteStats{
			Method:          key.method,
			Route:           key.route,
			Requests:        stats.requests,
			RPS:             round2(float64(stats.requests) / windowSeconds),
			Status4xx:       stats.status4xx,
			Status5xx:       stats.status5xx,
			ClientErrorRate: rate(stats.status4xx, stats.requests),
			ErrorRate:       rate(stats.status5xx, stats.requests),
			AverageMS:       round1(average),
			P95MS:           round1(stats.percentile(.95)),
			P99MS:           round1(stats.percentile(.99)),
			MaxMS:           round1(stats.maxMS),
			ResponseBytes:   stats.responseBytes,
		})
	}
	sort.Slice(resp.Routes, func(i, j int) bool {
		if resp.Routes[i].Requests == resp.Routes[j].Requests {
			if resp.Routes[i].Route == resp.Routes[j].Route {
				return resp.Routes[i].Method < resp.Routes[j].Method
			}
			return resp.Routes[i].Route < resp.Routes[j].Route
		}
		return resp.Routes[i].Requests > resp.Routes[j].Requests
	})
	if len(resp.Routes) > limit {
		resp.Routes = resp.Routes[:limit]
	}

	for i := len(a.recentErrors) - 1; i >= 0; i-- {
		if a.recentErrors[i].Timestamp >= windowCutoff {
			resp.RecentErrors = append(resp.RecentErrors, a.recentErrors[i])
		}
	}
	return resp
}

func summaryFromAggregate(stats aggregate, windowSeconds float64, inFlight int64) Summary {
	average := float64(0)
	if stats.requests > 0 {
		average = stats.durationMS / float64(stats.requests)
	}
	return Summary{
		Requests:        stats.requests,
		RPS:             round2(float64(stats.requests) / windowSeconds),
		InFlight:        inFlight,
		Status4xx:       stats.status4xx,
		Status5xx:       stats.status5xx,
		ClientErrorRate: rate(stats.status4xx, stats.requests),
		ErrorRate:       rate(stats.status5xx, stats.requests),
		ResponseBytes:   stats.responseBytes,
		Latency: LatencySummary{
			AverageMS: round1(average),
			P50MS:     round1(stats.percentile(.50)),
			P95MS:     round1(stats.percentile(.95)),
			P99MS:     round1(stats.percentile(.99)),
			MaxMS:     round1(stats.maxMS),
		},
	}
}

func clampWindow(window time.Duration) time.Duration {
	if window < time.Minute {
		return time.Minute
	}
	if window > time.Hour {
		return time.Hour
	}
	return window
}

func rate(part, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return round4(float64(part) / float64(total))
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }
func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
func round4(v float64) float64 { return float64(int64(v*10000+0.5)) / 10000 }
