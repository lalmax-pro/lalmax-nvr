package gb28181

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func hubClientCount(h *WSHub) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func TestWSHubHandleWebSocketDeliversEventsAndUnregisters(t *testing.T) {
	h := NewWSHub()
	runDone := make(chan struct{})
	go func() { h.Run(); close(runDone) }()
	server := httptest.NewServer(http.HandlerFunc(h.HandleWebSocket))
	defer server.Close()

	// A plain HTTP request cannot upgrade and must not create a client.
	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 0, hubClientCount(h))

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	require.Eventually(t, func() bool { return hubClientCount(h) == 1 }, 2*time.Second, 10*time.Millisecond)

	h.Broadcast(Event{Type: EventDeviceOnline, Data: map[string]interface{}{"device_id": "34020000001320000001"}})
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, payload, err := conn.ReadMessage()
	require.NoError(t, err)
	var event Event
	require.NoError(t, json.Unmarshal(payload, &event))
	require.Equal(t, EventDeviceOnline, event.Type)
	require.NotZero(t, event.Timestamp)

	// The client side of the read pump consumes inbound frames.
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte("ignored")))

	// Closing the socket unregisters the client exactly once.
	conn.Close()
	require.Eventually(t, func() bool { return hubClientCount(h) == 0 }, 2*time.Second, 10*time.Millisecond)

	h.Stop()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("WSHub.Run did not stop")
	}
}

func TestWSHubStopClosesRegisteredClientPumps(t *testing.T) {
	h := NewWSHub()
	runDone := make(chan struct{})
	go func() { h.Run(); close(runDone) }()
	server := httptest.NewServer(http.HandlerFunc(h.HandleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()
	require.Eventually(t, func() bool { return hubClientCount(h) == 1 }, 2*time.Second, 10*time.Millisecond)

	// Stopping the hub closes every client's send channel; the write pump then
	// emits a close frame and exits, and the read pump observes ctx.Done.
	h.Stop()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("WSHub.Run did not stop with a registered client")
	}
	require.Equal(t, 0, hubClientCount(h))

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
}

func TestWSHubHandleWebSocketAfterStopClosesImmediately(t *testing.T) {
	h := NewWSHub()
	h.Stop() // no Run loop: HandleWebSocket must take the ctx.Done branch
	server := httptest.NewServer(http.HandlerFunc(h.HandleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, _, err = conn.ReadMessage()
	require.Error(t, err)
	require.Equal(t, 0, hubClientCount(h))
}
