package media

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildReverseMap(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]string
		expected map[string]string
	}{
		{
			name:     "nil input",
			input:    nil,
			expected: map[string]string{},
		},
		{
			name:     "empty input",
			input:    map[string]string{},
			expected: map[string]string{},
		},
		{
			name: "single mapping",
			input: map[string]string{
				"cam-1": "mystream",
			},
			expected: map[string]string{
				"mystream": "cam-1",
			},
		},
		{
			name: "multiple mappings",
			input: map[string]string{
				"cam-1": "stream1",
				"cam-2": "stream2",
			},
			expected: map[string]string{
				"stream1": "cam-1",
				"stream2": "cam-2",
			},
		},
		{
			name: "empty stream key skipped",
			input: map[string]string{
				"cam-1": "stream1",
				"cam-2": "",
			},
			expected: map[string]string{
				"stream1": "cam-1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildReverseMap(tt.input)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestRTMPIngestHandlerResolvesStream(t *testing.T) {
	sub := newMockRTMPSubscriber()

	var started []string
	handler := NewRTMPIngestHandler(sub, func(name string) (string, bool) {
		if name == "mystream" {
			return "cam-1", true
		}
		return "", false
	}, func(camID, stream string) {
		started = append(started, camID)
	}, nil)

	err := handler.Start(context.Background())
	require.NoError(t, err)

	sub.send(RTMPEvent{StreamID: "mystream", Protocol: "rtmp", Type: "pub_start"})
	sub.close()

	handler.Stop()

	require.Equal(t, []string{"cam-1"}, started)
}

func TestRTMPIngestHandlerIgnoresUnmapped(t *testing.T) {
	sub := newMockRTMPSubscriber()

	var started []string
	handler := NewRTMPIngestHandler(sub, func(name string) (string, bool) {
		return "", false
	}, func(camID, stream string) {
		started = append(started, camID)
	}, nil)

	err := handler.Start(context.Background())
	require.NoError(t, err)

	sub.send(RTMPEvent{StreamID: "unknown", Protocol: "rtmp", Type: "pub_start"})
	sub.close()

	handler.Stop()

	require.Empty(t, started)
}

func TestRTMPIngestHandlerStop(t *testing.T) {
	sub := newMockRTMPSubscriber()

	var stopped []string
	handler := NewRTMPIngestHandler(sub, func(name string) (string, bool) {
		return "cam-1", true
	}, nil, func(camID, stream string) {
		stopped = append(stopped, camID)
	})

	err := handler.Start(context.Background())
	require.NoError(t, err)

	sub.send(RTMPEvent{StreamID: "mystream", Protocol: "rtmp", Type: "pub_start"})
	sub.send(RTMPEvent{StreamID: "mystream", Protocol: "rtmp", Type: "pub_stop"})
	sub.close()

	handler.Stop()

	require.Equal(t, []string{"cam-1"}, stopped)
}

func TestRTMPIngestHandlerIgnoresNonRTMP(t *testing.T) {
	sub := newMockRTMPSubscriber()

	var started []string
	handler := NewRTMPIngestHandler(sub, func(name string) (string, bool) {
		return "cam-1", true
	}, func(camID, stream string) {
		started = append(started, camID)
	}, nil)

	err := handler.Start(context.Background())
	require.NoError(t, err)

	sub.send(RTMPEvent{StreamID: "mystream", Protocol: "rtsp", Type: "pub_start"})
	sub.close()

	handler.Stop()

	require.Empty(t, started)
}

func TestSRTIngestHandlerResolvesStream(t *testing.T) {
	sub := newMockSRTSubscriber()

	var started []string
	handler := NewSRTIngestHandler(sub, func(name string) (string, bool) {
		if name == "mystream" {
			return "cam-1", true
		}
		return "", false
	}, func(camID, stream string) {
		started = append(started, camID)
	}, nil)

	err := handler.Start(context.Background())
	require.NoError(t, err)

	sub.send(SRTEvent{StreamID: "mystream", Protocol: "srt", Type: "pub_start"})
	sub.close()

	handler.Stop()

	require.Equal(t, []string{"cam-1"}, started)
}

func TestSRTIngestHandlerIgnoresUnmapped(t *testing.T) {
	sub := newMockSRTSubscriber()

	var started []string
	handler := NewSRTIngestHandler(sub, func(name string) (string, bool) {
		return "", false
	}, func(camID, stream string) {
		started = append(started, camID)
	}, nil)

	err := handler.Start(context.Background())
	require.NoError(t, err)

	sub.send(SRTEvent{StreamID: "unknown", Protocol: "srt", Type: "pub_start"})
	sub.close()

	handler.Stop()

	require.Empty(t, started)
}

func TestSRTIngestHandlerStop(t *testing.T) {
	sub := newMockSRTSubscriber()

	var stopped []string
	handler := NewSRTIngestHandler(sub, func(name string) (string, bool) {
		return "cam-1", true
	}, nil, func(camID, stream string) {
		stopped = append(stopped, camID)
	})

	err := handler.Start(context.Background())
	require.NoError(t, err)

	sub.send(SRTEvent{StreamID: "mystream", Protocol: "srt", Type: "pub_start"})
	sub.send(SRTEvent{StreamID: "mystream", Protocol: "srt", Type: "pub_stop"})
	sub.close()

	handler.Stop()

	require.Equal(t, []string{"cam-1"}, stopped)
}

func TestSRTIngestHandlerIgnoresNonSRT(t *testing.T) {
	sub := newMockSRTSubscriber()

	var started []string
	handler := NewSRTIngestHandler(sub, func(name string) (string, bool) {
		return "cam-1", true
	}, func(camID, stream string) {
		started = append(started, camID)
	}, nil)

	err := handler.Start(context.Background())
	require.NoError(t, err)

	sub.send(SRTEvent{StreamID: "mystream", Protocol: "rtmp", Type: "pub_start"})
	sub.close()

	handler.Stop()

	require.Empty(t, started)
}

func TestWHIPIngestHandlerResolvesCustomize(t *testing.T) {
	sub := newMockWHIPSubscriber()

	var started []string
	handler := NewWHIPIngestHandler(sub, func(name string) (string, bool) {
		if name == "front-door" {
			return "front-door", true
		}
		return "", false
	}, func(camID, stream string) {
		started = append(started, camID)
	}, nil)

	err := handler.Start(context.Background())
	require.NoError(t, err)

	sub.send(WHIPEvent{StreamID: "front-door", Protocol: "customize", Type: "pub_start"})
	sub.close()

	handler.Stop()

	require.Equal(t, []string{"front-door"}, started)
}

func TestWHIPIngestHandlerIgnoresRTMP(t *testing.T) {
	sub := newMockWHIPSubscriber()

	var started []string
	handler := NewWHIPIngestHandler(sub, func(name string) (string, bool) {
		return "cam-1", true
	}, func(camID, stream string) {
		started = append(started, camID)
	}, nil)

	err := handler.Start(context.Background())
	require.NoError(t, err)

	sub.send(WHIPEvent{StreamID: "mystream", Protocol: "rtmp", Type: "pub_start"})
	sub.close()

	handler.Stop()

	require.Empty(t, started)
}

type mockRTMPSubscriber struct {
	events    chan RTMPEvent
	closeOnce sync.Once
}

func newMockRTMPSubscriber() *mockRTMPSubscriber {
	return &mockRTMPSubscriber{events: make(chan RTMPEvent, 16)}
}

func (m *mockRTMPSubscriber) SubscribeRTMPEvents(ctx context.Context) (<-chan RTMPEvent, error) {
	go func() {
		<-ctx.Done()
		m.close()
	}()
	return m.events, nil
}

func (m *mockRTMPSubscriber) send(ev RTMPEvent) {
	m.events <- ev
}

func (m *mockRTMPSubscriber) close() {
	m.closeOnce.Do(func() { close(m.events) })
}

type mockSRTSubscriber struct {
	events    chan SRTEvent
	closeOnce sync.Once
}

func newMockSRTSubscriber() *mockSRTSubscriber {
	return &mockSRTSubscriber{events: make(chan SRTEvent, 16)}
}

func (m *mockSRTSubscriber) SubscribeSRTEvents(ctx context.Context) (<-chan SRTEvent, error) {
	go func() {
		<-ctx.Done()
		m.close()
	}()
	return m.events, nil
}

func (m *mockSRTSubscriber) send(ev SRTEvent) {
	m.events <- ev
}

func (m *mockSRTSubscriber) close() {
	m.closeOnce.Do(func() { close(m.events) })
}

type mockWHIPSubscriber struct {
	events    chan WHIPEvent
	closeOnce sync.Once
}

func newMockWHIPSubscriber() *mockWHIPSubscriber {
	return &mockWHIPSubscriber{events: make(chan WHIPEvent, 16)}
}

func (m *mockWHIPSubscriber) SubscribeWHIPEvents(ctx context.Context) (<-chan WHIPEvent, error) {
	go func() {
		<-ctx.Done()
		m.close()
	}()
	return m.events, nil
}

func (m *mockWHIPSubscriber) send(ev WHIPEvent) {
	m.events <- ev
}

func (m *mockWHIPSubscriber) close() {
	m.closeOnce.Do(func() { close(m.events) })
}
