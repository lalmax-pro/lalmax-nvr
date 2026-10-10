package voip

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/dtls/v3"

	"github.com/q191201771/naza/pkg/nazalog"
)

// TransportType represents SIP transport protocol
type TransportType string

const (
	TransportUDP  TransportType = "UDP"
	TransportTCP  TransportType = "TCP"
	TransportTLS  TransportType = "TLS"
	TransportDTLS TransportType = "DTLS-UDP"
	TransportWS   TransportType = "WS"
	TransportWSS  TransportType = "WSS"
)

// TransportAddr represents a remote address with transport type
type TransportAddr struct {
	Type TransportType
	Addr string // host:port format
}

func (ta TransportAddr) String() string {
	return fmt.Sprintf("%s/%s", ta.Addr, ta.Type)
}

// Transport interface abstracts different SIP transports
type Transport interface {
	Send(data []byte, addr TransportAddr) error
	Close() error
}

// UDPTransport handles UDP transport
type UDPTransport struct {
	conn *net.UDPConn
	log  nazalog.Logger
}

func NewUDPTransport(conn *net.UDPConn, log nazalog.Logger) *UDPTransport {
	return &UDPTransport{
		conn: conn,
		log:  log,
	}
}

func (t *UDPTransport) Send(data []byte, addr TransportAddr) error {
	udpAddr, err := net.ResolveUDPAddr("udp", addr.Addr)
	if err != nil {
		return fmt.Errorf("resolve udp addr failed: %w", err)
	}
	_, err = t.conn.WriteToUDP(data, udpAddr)
	return err
}

func (t *UDPTransport) Close() error {
	if t.conn != nil {
		return t.conn.Close()
	}
	return nil
}

// TCPTransport handles TCP transport with connection pooling
type TCPTransport struct {
	admissionMu    sync.Mutex
	listener       net.Listener
	deadlineSetter interface {
		SetDeadline(time.Time) error
	}
	dial          func(addr string) (net.Conn, error)
	transportType TransportType
	connections   sync.Map // key: remote addr string, value: *tcpConnection
	onMessage     func(rawMsg string, addr TransportAddr)
	closeOnce     sync.Once
	stopChan      chan struct{}
	wg            sync.WaitGroup
	log           nazalog.Logger
}

type tcpConnection struct {
	conn       net.Conn
	remoteAddr string
	lastActive time.Time
	mu         sync.Mutex
}

func NewTCPTransport(listener *net.TCPListener, onMessage func(string, TransportAddr), log nazalog.Logger) *TCPTransport {
	t := &TCPTransport{
		listener:       listener,
		deadlineSetter: listener,
		dial: func(addr string) (net.Conn, error) {
			return net.DialTimeout("tcp", addr, 5*time.Second)
		},
		transportType: TransportTCP,
		onMessage:     onMessage,
		stopChan:      make(chan struct{}),
		log:           log,
	}

	// Start accept loop
	t.wg.Add(1)
	go t.acceptLoop()

	// Start connection cleanup loop
	t.wg.Add(1)
	go t.cleanupLoop()

	return t
}

func NewTLSTransport(listener *net.TCPListener, tlsConfig *tls.Config, onMessage func(string, TransportAddr), log nazalog.Logger) *TCPTransport {
	tlsListener := tls.NewListener(listener, tlsConfig)
	t := &TCPTransport{
		listener:       tlsListener,
		deadlineSetter: listener,
		dial: func(addr string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: 5 * time.Second}
			return tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
		},
		transportType: TransportTLS,
		onMessage:     onMessage,
		stopChan:      make(chan struct{}),
		log:           log,
	}

	t.wg.Add(1)
	go t.acceptLoop()

	t.wg.Add(1)
	go t.cleanupLoop()

	return t
}

// NewDialOnlyTCPTransport creates a client transport for upstream SIP peers.
// TLS uses the supplied config and verifies the peer certificate by default.
func NewDialOnlyTCPTransport(transportType TransportType, tlsConfig *tls.Config, onMessage func(string, TransportAddr), log nazalog.Logger) (*TCPTransport, error) {
	t := &TCPTransport{
		transportType: transportType,
		onMessage:     onMessage,
		stopChan:      make(chan struct{}),
		log:           log,
	}
	switch transportType {
	case TransportTCP:
		t.dial = func(addr string) (net.Conn, error) {
			return net.DialTimeout("tcp", addr, 5*time.Second)
		}
	case TransportTLS:
		if tlsConfig == nil {
			return nil, fmt.Errorf("TLS client config is required")
		}
		t.dial = func(addr string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: 5 * time.Second}
			return tls.DialWithDialer(dialer, "tcp", addr, tlsConfig.Clone())
		}
	default:
		return nil, fmt.Errorf("dial-only stream transport must be TCP or TLS")
	}
	t.wg.Add(1)
	go t.cleanupLoop()
	return t, nil
}

func (t *TCPTransport) acceptLoop() {
	defer t.wg.Done()

	for {
		select {
		case <-t.stopChan:
			return
		default:
		}

		if t.deadlineSetter != nil {
			t.deadlineSetter.SetDeadline(time.Now().Add(100 * time.Millisecond))
		}
		conn, err := t.listener.Accept()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			select {
			case <-t.stopChan:
				return
			default:
				t.log.Warnf("accept tcp connection failed: %v", err)
				continue
			}
		}

		remoteAddr := conn.RemoteAddr().String()
		t.log.Infof("new %s connection from %s", strings.ToLower(string(t.transportType)), remoteAddr)
		// Track accepted sockets before TLS handshake so Close can interrupt
		// peers that send an incomplete ClientHello.
		tcpConn := &tcpConnection{conn: conn, remoteAddr: remoteAddr, lastActive: time.Now()}
		t.admissionMu.Lock()
		select {
		case <-t.stopChan:
			t.admissionMu.Unlock()
			conn.Close()
			return
		default:
		}
		t.connections.Store(remoteAddr, tcpConn)
		t.admissionMu.Unlock()
		if tlsConn, ok := conn.(*tls.Conn); ok {
			tlsConn.SetDeadline(time.Now().Add(5 * time.Second))
			if err := tlsConn.Handshake(); err != nil {
				t.log.Warnf("tls handshake from %s failed: %v", remoteAddr, err)
				conn.Close()
				t.connections.CompareAndDelete(remoteAddr, tcpConn)
				continue
			}
			tlsConn.SetDeadline(time.Time{})

			state := tlsConn.ConnectionState()
			t.log.Debugf("tls handshake from %s completed: version=%s cipher=%s server_name=%q",
				remoteAddr, tlsVersionName(state.Version), tls.CipherSuiteName(state.CipherSuite), state.ServerName)
		}

		t.admissionMu.Lock()
		select {
		case <-t.stopChan:
			t.admissionMu.Unlock()
			conn.Close()
			return
		default:
		}
		t.wg.Add(1)
		go t.handleConnection(tcpConn)
		t.admissionMu.Unlock()
	}
}

func (t *TCPTransport) handleConnection(tc *tcpConnection) {
	defer t.wg.Done()
	defer func() {
		tc.conn.Close()
		t.connections.CompareAndDelete(tc.remoteAddr, tc)
		t.log.Infof("%s connection closed: %s", strings.ToLower(string(t.transportType)), tc.remoteAddr)
	}()

	reader := bufio.NewReader(tc.conn)
	for {
		select {
		case <-t.stopChan:
			return
		default:
		}

		// Set read deadline
		tc.conn.SetReadDeadline(time.Now().Add(5 * time.Minute))

		// Read until we have a complete SIP message
		msg, err := t.readSIPMessage(reader)
		if err != nil {
			if err == io.EOF {
				return
			}
			// A timeout may occur after a partial SIP header/body. Restarting
			// framing at the remaining bytes corrupts the stream; close the peer.
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				return
			}

			t.log.Warnf("read from %s connection failed: %v", strings.ToLower(string(t.transportType)), err)
			return
		}

		// Update last active time
		tc.mu.Lock()
		tc.lastActive = time.Now()
		tc.mu.Unlock()

		// Handle message
		if t.onMessage != nil {
			addr := TransportAddr{
				Type: t.transportType,
				Addr: tc.remoteAddr,
			}
			t.onMessage(msg, addr)
		}
	}
}

func tlsVersionName(version uint16) string {
	switch version {
	case tls.VersionTLS10:
		return "TLS1.0"
	case tls.VersionTLS11:
		return "TLS1.1"
	case tls.VersionTLS12:
		return "TLS1.2"
	case tls.VersionTLS13:
		return "TLS1.3"
	default:
		return fmt.Sprintf("0x%04x", version)
	}
}

// readSIPMessage reads a complete SIP message from TCP stream
func (t *TCPTransport) readSIPMessage(reader *bufio.Reader) (string, error) {
	var sb strings.Builder
	var contentLength int = -1

	// Read headers line by line
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}

		sb.WriteString(line)

		// Check for Content-Length header
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &contentLength)
			}
		}

		// Check for end of headers (empty line)
		if line == "\r\n" || line == "\n" {
			break
		}
	}

	// Read body if content length is specified
	if contentLength > 0 {
		body := make([]byte, contentLength)
		_, err := io.ReadFull(reader, body)
		if err != nil {
			return "", err
		}
		sb.Write(body)
	}

	return sb.String(), nil
}

func (t *TCPTransport) Send(data []byte, addr TransportAddr) error {
	// Try to find existing connection
	if val, ok := t.connections.Load(addr.Addr); ok {
		tc := val.(*tcpConnection)
		tc.mu.Lock()
		defer tc.mu.Unlock()

		tc.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, err := tc.conn.Write(data)
		if err == nil {
			tc.lastActive = time.Now()
			return nil
		}
		// Connection failed, remove it
		t.connections.CompareAndDelete(addr.Addr, tc)
		tc.conn.Close()
	}

	// No existing connection, create new one
	conn, err := t.dial(addr.Addr)
	if err != nil {
		return fmt.Errorf("dial %s failed: %w", strings.ToLower(string(t.transportType)), err)
	}

	tc := &tcpConnection{
		conn:       conn,
		remoteAddr: addr.Addr,
		lastActive: time.Now(),
	}
	t.admissionMu.Lock()
	select {
	case <-t.stopChan:
		t.admissionMu.Unlock()
		conn.Close()
		return net.ErrClosed
	default:
	}
	t.connections.Store(addr.Addr, tc)

	// Start handling this connection
	t.wg.Add(1)
	go t.handleConnection(tc)
	t.admissionMu.Unlock()

	// Send data
	tc.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = tc.conn.Write(data)
	if err != nil {
		tc.conn.Close()
		t.connections.CompareAndDelete(addr.Addr, tc)
		return err
	}

	return nil
}

func (t *TCPTransport) cleanupLoop() {
	defer t.wg.Done()

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-t.stopChan:
			return
		case <-ticker.C:
			// Clean up idle connections
			now := time.Now()
			t.connections.Range(func(key, value interface{}) bool {
				tc := value.(*tcpConnection)
				tc.mu.Lock()
				idleTime := now.Sub(tc.lastActive)
				tc.mu.Unlock()

				if idleTime > 10*time.Minute {
					t.log.Infof("closing idle tcp connection: %s (idle: %v)", tc.remoteAddr, idleTime)
					tc.conn.Close()
					t.connections.Delete(key)
				}
				return true
			})
		}
	}
}

func (t *TCPTransport) Close() error {
	t.closeOnce.Do(func() {
		t.admissionMu.Lock()
		defer t.admissionMu.Unlock()
		close(t.stopChan)

		// Close all connections
		t.connections.Range(func(key, value interface{}) bool {
			tc := value.(*tcpConnection)
			tc.conn.Close()
			return true
		})

		// Close listener
		if t.listener != nil {
			t.listener.Close()
		}

	})
	t.wg.Wait()
	return nil
}

type DTLSTransport struct {
	admissionMu sync.Mutex
	closeOnce   sync.Once
	listener    net.Listener
	connections sync.Map // key: remote addr string, value: *dtlsConnection
	onMessage   func(rawMsg string, addr TransportAddr)
	stopChan    chan struct{}
	wg          sync.WaitGroup
	log         nazalog.Logger
}

type dtlsConnection struct {
	conn       net.Conn
	remoteAddr string
	lastActive time.Time
	mu         sync.Mutex
}

func NewDTLSTransport(listenAddr *net.UDPAddr, dtlsConfig *dtls.Config, onMessage func(string, TransportAddr), log nazalog.Logger) (*DTLSTransport, error) {
	listener, err := dtls.Listen("udp", listenAddr, dtlsConfig)
	if err != nil {
		return nil, err
	}

	t := &DTLSTransport{
		listener:  listener,
		onMessage: onMessage,
		stopChan:  make(chan struct{}),
		log:       log,
	}

	t.wg.Add(1)
	go t.acceptLoop()

	t.wg.Add(1)
	go t.cleanupLoop()

	return t, nil
}

func (t *DTLSTransport) acceptLoop() {
	defer t.wg.Done()

	for {
		conn, err := t.listener.Accept()
		if err != nil {
			select {
			case <-t.stopChan:
				return
			default:
				t.log.Warnf("accept sip dtls connection failed: %v", err)
				continue
			}
		}

		remoteAddr := conn.RemoteAddr().String()
		t.log.Infof("new sip dtls connection from %s", remoteAddr)

		dtlsConn := &dtlsConnection{
			conn:       conn,
			remoteAddr: remoteAddr,
			lastActive: time.Now(),
		}
		t.admissionMu.Lock()
		select {
		case <-t.stopChan:
			t.admissionMu.Unlock()
			conn.Close()
			return
		default:
		}
		t.connections.Store(remoteAddr, dtlsConn)

		t.wg.Add(1)
		go t.handleConnection(dtlsConn)
		t.admissionMu.Unlock()
	}
}

func (t *DTLSTransport) handleConnection(dc *dtlsConnection) {
	defer t.wg.Done()
	defer func() {
		dc.conn.Close()
		t.connections.CompareAndDelete(dc.remoteAddr, dc)
		t.log.Infof("sip dtls connection closed: %s", dc.remoteAddr)
	}()

	buf := make([]byte, 65535)
	for {
		select {
		case <-t.stopChan:
			return
		default:
		}

		dc.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, err := dc.conn.Read(buf)
		if err != nil {
			if err == io.EOF {
				return
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				dc.mu.Lock()
				idleTime := time.Since(dc.lastActive)
				dc.mu.Unlock()
				if idleTime > 5*time.Minute {
					t.log.Infof("sip dtls connection idle timeout: %s", dc.remoteAddr)
					return
				}
				continue
			}
			t.log.Warnf("read from sip dtls connection failed: %v", err)
			return
		}

		dc.mu.Lock()
		dc.lastActive = time.Now()
		dc.mu.Unlock()

		if t.onMessage != nil {
			addr := TransportAddr{
				Type: TransportDTLS,
				Addr: dc.remoteAddr,
			}
			t.onMessage(string(buf[:n]), addr)
		}
	}
}

func (t *DTLSTransport) Send(data []byte, addr TransportAddr) error {
	val, ok := t.connections.Load(addr.Addr)
	if !ok {
		return fmt.Errorf("sip dtls connection not found: %s", addr.Addr)
	}

	dc := val.(*dtlsConnection)
	dc.mu.Lock()
	defer dc.mu.Unlock()

	dc.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := dc.conn.Write(data)
	if err == nil {
		dc.lastActive = time.Now()
		return nil
	}
	t.connections.CompareAndDelete(addr.Addr, dc)
	dc.conn.Close()
	return err
}

func (t *DTLSTransport) cleanupLoop() {
	defer t.wg.Done()

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-t.stopChan:
			return
		case <-ticker.C:
			now := time.Now()
			t.connections.Range(func(key, value interface{}) bool {
				dc := value.(*dtlsConnection)
				dc.mu.Lock()
				idleTime := now.Sub(dc.lastActive)
				dc.mu.Unlock()

				if idleTime > 10*time.Minute {
					t.log.Infof("closing idle sip dtls connection: %s (idle: %v)", dc.remoteAddr, idleTime)
					dc.conn.Close()
					t.connections.Delete(key)
				}
				return true
			})
		}
	}
}

func (t *DTLSTransport) Close() error {
	t.closeOnce.Do(func() {
		t.admissionMu.Lock()
		defer t.admissionMu.Unlock()
		close(t.stopChan)

		t.connections.Range(func(_, value interface{}) bool {
			dc := value.(*dtlsConnection)
			dc.conn.Close()
			return true
		})

		if t.listener != nil {
			t.listener.Close()
		}

	})
	t.wg.Wait()
	return nil
}

type WebSocketTransport struct {
	admissionMu   sync.Mutex
	server        *http.Server
	listener      net.Listener
	transportType TransportType
	connections   sync.Map // key: remote addr string, value: *websocketConnection
	onMessage     func(rawMsg string, addr TransportAddr)
	closeOnce     sync.Once
	stopChan      chan struct{}
	wg            sync.WaitGroup
	log           nazalog.Logger
}

type websocketConnection struct {
	conn       *websocket.Conn
	remoteAddr string
	lastActive time.Time
	mu         sync.Mutex
}

func NewWebSocketTransport(listener net.Listener, transportType TransportType, onMessage func(string, TransportAddr), log nazalog.Logger) *WebSocketTransport {
	t := &WebSocketTransport{
		listener:      listener,
		transportType: transportType,
		onMessage:     onMessage,
		stopChan:      make(chan struct{}),
		log:           log,
	}

	upgrader := websocket.Upgrader{
		Subprotocols: []string{"sip"},
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
	t.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.log.Warnf("upgrade sip websocket failed: %v", err)
			return
		}
		t.handleAccepted(conn)
	})}

	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		if err := t.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			t.log.Warnf("sip websocket server failed: %v", err)
		}
	}()

	return t
}

func (t *WebSocketTransport) handleAccepted(conn *websocket.Conn) {
	remoteAddr := conn.RemoteAddr().String()
	t.log.Infof("new sip %s connection from %s", strings.ToLower(string(t.transportType)), remoteAddr)
	wc := &websocketConnection{
		conn:       conn,
		remoteAddr: remoteAddr,
		lastActive: time.Now(),
	}
	t.admissionMu.Lock()
	select {
	case <-t.stopChan:
		t.admissionMu.Unlock()
		conn.Close()
		return
	default:
	}
	t.connections.Store(remoteAddr, wc)
	t.wg.Add(1)
	go t.handleConnection(wc)
	t.admissionMu.Unlock()
}

func (t *WebSocketTransport) handleConnection(wc *websocketConnection) {
	defer t.wg.Done()
	defer func() {
		wc.conn.Close()
		t.connections.CompareAndDelete(wc.remoteAddr, wc)
		t.log.Infof("sip websocket connection closed: %s", wc.remoteAddr)
	}()

	for {
		select {
		case <-t.stopChan:
			return
		default:
		}

		wc.conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		messageType, payload, err := wc.conn.ReadMessage()
		if err != nil {
			// Gorilla's reader cannot be reused after a read timeout. Continuing
			// would repeatedly return the same error and spin until idle expiry.
			return
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}

		wc.mu.Lock()
		wc.lastActive = time.Now()
		wc.mu.Unlock()

		if t.onMessage != nil {
			addr := TransportAddr{
				Type: t.transportType,
				Addr: wc.remoteAddr,
			}
			t.onMessage(string(payload), addr)
		}
	}
}

func (t *WebSocketTransport) Send(data []byte, addr TransportAddr) error {
	val, ok := t.connections.Load(addr.Addr)
	if !ok {
		return fmt.Errorf("sip websocket connection not found: %s", addr.Addr)
	}
	wc := val.(*websocketConnection)
	wc.mu.Lock()
	defer wc.mu.Unlock()

	wc.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := wc.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.connections.CompareAndDelete(addr.Addr, wc)
		wc.conn.Close()
		return err
	}
	wc.lastActive = time.Now()
	return nil
}

func (t *WebSocketTransport) Close() error {
	t.closeOnce.Do(func() {
		t.admissionMu.Lock()
		defer t.admissionMu.Unlock()
		close(t.stopChan)
		t.connections.Range(func(key, value interface{}) bool {
			wc := value.(*websocketConnection)
			wc.conn.Close()
			return true
		})
		if t.server != nil {
			t.server.Close()
		}
		if t.listener != nil {
			t.listener.Close()
		}
	})
	t.wg.Wait()
	return nil
}

// ParseViaTransport extracts transport type from Via header
// Example: "SIP/2.0/TCP 192.168.1.100:5060" -> TransportTCP
func ParseViaTransport(via string) TransportType {
	parts := strings.Fields(via)
	if len(parts) < 2 {
		return TransportUDP // default
	}

	// Format: SIP/2.0/TRANSPORT
	protocol := parts[0]
	protocolParts := strings.Split(protocol, "/")
	if len(protocolParts) < 3 {
		return TransportUDP // default
	}

	transport := strings.ToUpper(protocolParts[2])
	switch transport {
	case "TCP":
		return TransportTCP
	case "TLS":
		return TransportTLS
	case "DTLS", "DTLS-UDP":
		return TransportDTLS
	case "WS":
		return TransportWS
	case "WSS":
		return TransportWSS
	case "UDP":
		return TransportUDP
	default:
		return TransportUDP
	}
}

// ExtractViaAddress extracts host:port from Via header
// Example: "SIP/2.0/UDP 192.168.1.100:5060;branch=xxx" -> "192.168.1.100:5060"
func ExtractViaAddress(via string) string {
	parts := strings.Fields(via)
	if len(parts) < 2 {
		return ""
	}

	addr := parts[1]
	// Remove parameters after semicolon
	if idx := strings.Index(addr, ";"); idx >= 0 {
		addr = addr[:idx]
	}
	return addr
}
