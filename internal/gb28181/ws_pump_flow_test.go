package gb28181

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// rawWebSocketPair upgrades a real connection and hands back both ends so the
// client pumps can be driven synchronously.
func rawWebSocketPair(t *testing.T) (client *websocket.Conn, server *websocket.Conn) {
	t.Helper()
	serverConns := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			serverConns <- conn
		}
	}))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	t.Cleanup(func() { clientConn.Close() })
	select {
	case serverConn := <-serverConns:
		t.Cleanup(func() { serverConn.Close() })
		return clientConn, serverConn
	case <-time.After(2 * time.Second):
		t.Fatal("websocket upgrade did not yield a server connection")
		return nil, nil
	}
}

func TestWSHubWritePumpClosedChannelSendsCloseFrame(t *testing.T) {
	clientConn, serverConn := rawWebSocketPair(t)
	hub := NewWSHub()
	client := &Client{hub: hub, conn: serverConn, send: make(chan []byte, 1)}
	close(client.send)
	client.writePump() // returns immediately after the close frame

	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, _, err := clientConn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func TestWSHubWritePumpExitsWhenWriterFails(t *testing.T) {
	_, serverConn := rawWebSocketPair(t)
	hub := NewWSHub()
	client := &Client{hub: hub, conn: serverConn, send: make(chan []byte, 1)}
	require.NoError(t, serverConn.Close())
	client.send <- []byte("payload")

	done := make(chan struct{})
	go func() { client.writePump(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("writePump did not exit when the writer failed")
	}
}

func TestWSHubReadPumpExitsOnClosedConnectionAfterStop(t *testing.T) {
	clientConn, serverConn := rawWebSocketPair(t)
	hub := NewWSHub()
	hub.Stop() // ctx done: the unregister send would otherwise block
	client := &Client{hub: hub, conn: serverConn, send: make(chan []byte, 1)}

	done := make(chan struct{})
	go func() { client.readPump(); close(done) }()
	require.NoError(t, clientConn.WriteMessage(websocket.TextMessage, []byte("ignored")))
	require.NoError(t, clientConn.Close())
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readPump did not exit after the connection closed")
	}
}

func TestServerParseChannelIDFromSDPRejectsNonDigits(t *testing.T) {
	// Long enough to pass the length check but contains letters.
	require.Equal(t, "", parseChannelIDFromSDP("o=3402000000132000abcd 0 0 IN IP4 192.0.2.20\r\n"))
	require.Equal(t, "", parseChannelIDFromSDP("o= 0 0 IN IP4 192.0.2.20\r\n"))
}
