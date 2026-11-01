package voip

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/q191201771/naza/pkg/nazalog"
)

type m2AcceptTimeout struct{}

func (m2AcceptTimeout) Error() string   { return "accept timeout" }
func (m2AcceptTimeout) Timeout() bool   { return true }
func (m2AcceptTimeout) Temporary() bool { return true }

type m2FakeListener struct {
	accept func() (net.Conn, error)
}

func (l *m2FakeListener) Accept() (net.Conn, error) { return l.accept() }
func (l *m2FakeListener) Close() error             { return nil }
func (l *m2FakeListener) Addr() net.Addr           { return &net.TCPAddr{} }
func (l *m2FakeListener) SetDeadline(time.Time) error {
	return nil
}

func TestM2VoipTransportHelpers(t *testing.T) {
	if got := ParseViaTransport("SIP/2.0/UDP 127.0.0.1:5060"); got != TransportUDP {
		t.Fatalf("udp via = %s", got)
	}
	if got := ParseViaTransport("SIP/2.0/TCP host"); got != TransportTCP {
		t.Fatalf("tcp via = %s", got)
	}
	if got := ParseViaTransport("SIP/2.0/TLS host"); got != TransportTLS {
		t.Fatalf("tls via = %s", got)
	}
	if got := ParseViaTransport("SIP/2.0/DTLS-UDP host"); got != TransportDTLS {
		t.Fatalf("dtls via = %s", got)
	}
	if got := ParseViaTransport("SIP/2.0/WS host"); got != TransportWS {
		t.Fatalf("ws via = %s", got)
	}
	if got := ParseViaTransport("SIP/2.0/WSS host"); got != TransportWSS {
		t.Fatalf("wss via = %s", got)
	}
	if got := ParseViaTransport("SIP/2.0/SCTP host"); got != TransportUDP {
		t.Fatalf("unknown via = %s", got)
	}
	if got := ParseViaTransport("SIP/2.0"); got != TransportUDP {
		t.Fatalf("short protocol via = %s", got)
	}
	if got := ParseViaTransport(""); got != TransportUDP {
		t.Fatalf("empty via = %s", got)
	}

	if got := ExtractViaAddress("SIP/2.0/UDP 192.0.2.1:5060;branch=x"); got != "192.0.2.1:5060" {
		t.Fatalf("via address = %q", got)
	}
	if got := ExtractViaAddress("SIP/2.0/UDP"); got != "" {
		t.Fatalf("short via address = %q", got)
	}
	if got := ExtractViaAddress("SIP/2.0/UDP 192.0.2.1:5060"); got != "192.0.2.1:5060" {
		t.Fatalf("plain via address = %q", got)
	}

	if got := (TransportAddr{Type: TransportTCP, Addr: "127.0.0.1:5060"}).String(); got != "127.0.0.1:5060/TCP" {
		t.Fatalf("transport addr = %q", got)
	}
}

func TestM2VoipTransportSendErrors(t *testing.T) {
	udp := &UDPTransport{}
	if err := udp.Send([]byte("x"), TransportAddr{Type: TransportUDP, Addr: "bad address"}); err == nil {
		t.Fatal("invalid UDP address accepted")
	}
	if err := udp.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := NewDialOnlyTCPTransport(TransportTLS, nil, nil, nazalog.GetGlobalLogger()); err == nil {
		t.Fatal("dial-only TLS without config accepted")
	}
	if _, err := NewDialOnlyTCPTransport(TransportWS, nil, nil, nazalog.GetGlobalLogger()); err == nil {
		t.Fatal("dial-only WS accepted")
	}
}

func TestM2VoipTCPAcceptLoopBranches(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		fake := &m2FakeListener{accept: func() (net.Conn, error) { return nil, m2AcceptTimeout{} }}
		transport := &TCPTransport{listener: fake, deadlineSetter: fake, stopChan: make(chan struct{}), log: nazalog.GetGlobalLogger()}
		transport.wg.Add(1)
		go transport.acceptLoop()
		time.Sleep(10 * time.Millisecond)
		close(transport.stopChan)
		transport.wg.Wait()
	})

	t.Run("error after stop", func(t *testing.T) {
		release := make(chan struct{})
		entered := make(chan struct{}, 1)
		fake := &m2FakeListener{accept: func() (net.Conn, error) {
			entered <- struct{}{}
			<-release
			return nil, errors.New("accept failed")
		}}
		transport := &TCPTransport{listener: fake, deadlineSetter: fake, stopChan: make(chan struct{}), log: nazalog.GetGlobalLogger()}
		transport.wg.Add(1)
		go transport.acceptLoop()
		<-entered
		close(transport.stopChan)
		close(release)
		transport.wg.Wait()
	})

	t.Run("connection after stop", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		release := make(chan struct{})
		entered := make(chan struct{}, 1)
		fake := &m2FakeListener{accept: func() (net.Conn, error) {
			entered <- struct{}{}
			<-release
			return server, nil
		}}
		transport := &TCPTransport{listener: fake, deadlineSetter: fake, stopChan: make(chan struct{}), log: nazalog.GetGlobalLogger()}
		transport.wg.Add(1)
		go transport.acceptLoop()
		<-entered
		close(transport.stopChan)
		close(release)
		transport.wg.Wait()
	})
}

type m2SinkConn struct {
	closed chan struct{}
	once   sync.Once
}

func newM2SinkConn() *m2SinkConn { return &m2SinkConn{closed: make(chan struct{})} }
func (c *m2SinkConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, errors.New("closed")
}
func (c *m2SinkConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *m2SinkConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}
func (c *m2SinkConn) LocalAddr() net.Addr                { return &net.TCPAddr{} }
func (c *m2SinkConn) RemoteAddr() net.Addr               { return &net.TCPAddr{} }
func (c *m2SinkConn) SetDeadline(time.Time) error        { return nil }
func (c *m2SinkConn) SetReadDeadline(time.Time) error    { return nil }
func (c *m2SinkConn) SetWriteDeadline(time.Time) error   { return nil }

func TestM2VoipTCPSendBranches(t *testing.T) {
	transport := &TCPTransport{
		transportType: TransportTCP,
		stopChan:      make(chan struct{}),
		log:           nazalog.GetGlobalLogger(),
		dial: func(addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "bad") {
				return nil, errors.New("dial failed")
			}
			return newM2SinkConn(), nil
		},
	}
	addr := TransportAddr{Type: TransportTCP, Addr: "bad:1"}
	if err := transport.Send([]byte("x"), addr); err == nil {
		t.Fatal("dial failure not reported")
	}

	okAddr := TransportAddr{Type: TransportTCP, Addr: "ok:1"}
	if err := transport.Send([]byte("SIP/2.0 200 OK\r\n\r\n"), okAddr); err != nil {
		t.Fatal(err)
	}
	if err := transport.Send([]byte("SIP/2.0 200 OK\r\n\r\n"), okAddr); err != nil {
		t.Fatal(err)
	}

	// A closed transport refuses new connections after dialing.
	_ = transport.Close()
	if err := transport.Send([]byte("x"), TransportAddr{Type: TransportTCP, Addr: "new:1"}); err == nil {
		t.Fatal("closed transport accepted a connection")
	}

	tlsTransport, err := NewDialOnlyTCPTransport(TransportTCP, nil, nil, nazalog.GetGlobalLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := tlsTransport.Send([]byte("x"), TransportAddr{Type: TransportTCP, Addr: "127.0.0.1:1"}); err == nil {
		t.Fatal("unreachable TCP peer accepted")
	}
	_ = tlsTransport.Close()
}

func TestM2VoipWebSocketBranches(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	transport := NewWebSocketTransport(listener, TransportWS, func(string, TransportAddr) {}, nazalog.GetGlobalLogger())
	baseURL := "http://" + listener.Addr().String()

	// A plain HTTP request cannot upgrade and is rejected.
	resp, err := http.Get(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("plain request status = %d", resp.StatusCode)
	}

	if err := transport.Send([]byte("x"), TransportAddr{Type: TransportWS, Addr: "missing"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing connection error = %v", err)
	}

	client, _, err := websocket.DefaultDialer.Dial("ws://"+listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	key := client.LocalAddr().String()
	transport.connections.Store(key, &websocketConnection{conn: client, remoteAddr: key, lastActive: time.Now()})
	_ = client.UnderlyingConn().Close()
	if err := transport.Send([]byte("x"), TransportAddr{Type: TransportWS, Addr: key}); err == nil {
		t.Fatal("closed websocket accepted a write")
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}

	// Closing the listener underneath Serve exercises the unexpected-error path.
	second, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	secondTransport := NewWebSocketTransport(second, TransportWSS, nil, nazalog.GetGlobalLogger())
	_ = second.Close()
	time.Sleep(20 * time.Millisecond)
	if err := secondTransport.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestM2VoipDTLSBranchErrors(t *testing.T) {
	if _, err := NewDTLSTransport(nil, nil, nil, nazalog.GetGlobalLogger()); err == nil {
		t.Fatal("DTLS without config accepted")
	}
	transport := &DTLSTransport{stopChan: make(chan struct{}), log: nazalog.GetGlobalLogger()}
	_ = transport.Close()
}
