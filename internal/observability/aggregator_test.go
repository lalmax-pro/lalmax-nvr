package observability

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAggregatorSnapshot(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	a := NewAggregator()
	a.now = func() time.Time { return now }

	samples := []Sample{
		{Timestamp: now.Add(-20 * time.Second), Method: "GET", Route: "/api/cameras/{id}", Status: 200, Duration: 8 * time.Millisecond, ResponseBytes: 100},
		{Timestamp: now.Add(-15 * time.Second), Method: "GET", Route: "/api/cameras/{id}", Status: 404, Duration: 30 * time.Millisecond, ResponseBytes: 50},
		{Timestamp: now.Add(-5 * time.Second), Method: "POST", Route: "/api/cameras/{id}/start", Status: 500, Duration: 700 * time.Millisecond, TraceID: "abc123"},
	}
	for _, sample := range samples {
		a.Observe(sample)
	}
	a.Begin()

	got := a.Snapshot(time.Minute, time.Minute, 10)
	require.Equal(t, uint64(3), got.Summary.Requests)
	require.Equal(t, int64(1), got.Summary.InFlight)
	require.Equal(t, uint64(1), got.Summary.Status4xx)
	require.Equal(t, uint64(1), got.Summary.Status5xx)
	require.Equal(t, 0.3333, got.Summary.ErrorRate)
	require.Equal(t, 1000.0, got.Summary.Latency.P95MS)
	require.Len(t, got.Routes, 2)
	require.Equal(t, "/api/cameras/{id}", got.Routes[0].Route)
	require.Equal(t, uint64(2), got.Routes[0].Requests)
	require.Len(t, got.RecentErrors, 1)
	require.Equal(t, "abc123", got.RecentErrors[0].TraceID)
	require.NotEmpty(t, got.Series)
}

func TestAggregatorResetsExpiredBucket(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	a := NewAggregator()
	a.now = func() time.Time { return now }
	a.Observe(Sample{Timestamp: now.Add(-2 * time.Hour), Method: "GET", Route: "/old", Status: 500})
	a.Observe(Sample{Timestamp: now, Method: "GET", Route: "/new", Status: 200})

	got := a.Snapshot(time.Hour, time.Hour, 10)
	require.Equal(t, uint64(1), got.Summary.Requests)
	require.Len(t, got.Routes, 1)
	require.Equal(t, "/new", got.Routes[0].Route)
}

func TestAggregatorRouteLimit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	a := NewAggregator()
	a.now = func() time.Time { return now }
	a.Observe(Sample{Timestamp: now, Method: "GET", Route: "/a", Status: 200})
	a.Observe(Sample{Timestamp: now, Method: "GET", Route: "/b", Status: 200})

	got := a.Snapshot(time.Minute, time.Minute, 1)
	require.Len(t, got.Routes, 1)
}
