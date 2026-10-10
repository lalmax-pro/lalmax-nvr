package voip

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/dtls/v3"
	"github.com/q191201771/lal/pkg/base"
	maxvoip "github.com/q191201771/lalmax/voip"
	"github.com/q191201771/naza/pkg/nazalog"
)

func testSIPCertificate(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "sip-cert.pem"), filepath.Join(dir, "sip-key.pem")
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("could not trust test certificate")
	}
	return certPath, keyPath, roots
}

// Each case uses a real listener and a peer connection, including certificate
// verification for TLS, WSS and signaling DTLS. Media is independently sent over UDP.
func TestSIPCallAcrossTransports(t *testing.T) {
	cert, key, roots := testSIPCertificate(t)
	for _, kind := range []TransportType{TransportUDP, TransportTCP, TransportTLS, TransportWS, TransportWSS, TransportDTLS} {
		t.Run(string(kind), func(t *testing.T) {
			mediaSocket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			mediaPort := mediaSocket.LocalAddr().(*net.UDPAddr).Port
			mediaSocket.Close()
			cfg := Config{SipListenAddr: "127.0.0.1:0", SipIP: "127.0.0.1", MediaIP: "127.0.0.1", MediaPortMin: mediaPort, MediaPortMax: mediaPort,
				AuthEnable: true, Users: []User{{Username: "1001", Password: "transport-test"}}, SipTlsCertFile: cert, SipTlsKeyFile: key}
			switch kind {
			case TransportTCP:
				cfg.SipTcpListenAddr = "127.0.0.1:0"
			case TransportTLS:
				cfg.SipTlsListenAddr = "127.0.0.1:0"
			case TransportDTLS:
				cfg.SipDtlsListenAddr = "127.0.0.1:0"
			case TransportWS:
				cfg.SipWsListenAddr = "127.0.0.1:0"
			case TransportWSS:
				cfg.SipWssListenAddr = "127.0.0.1:0"
			}
			added, received, removed := make(chan *maxvoip.PubSession, 4), make(chan base.AvPacket, 8), make(chan struct{}, 4)
			s, err := NewServer(ServerConfig{Config: cfg, OnPubSession: func(pub *maxvoip.PubSession) error {
				pub.SetOnAvPacket(func(pkt base.AvPacket) { received <- pkt })
				added <- pub
				return nil
			}, OnDelSession: func(*maxvoip.PubSession) error { removed <- struct{}{}; return nil }})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			send, read, closePeer := transportPeer(t, s, kind, roots)
			defer closePeer()
			makeRequest := func(method string, cseq int) *Message {
				m, err := ParseMessage(request(method, "transport-call"))
				if err != nil {
					t.Fatal(err)
				}
				m.SetHeader("Via", fmt.Sprintf("SIP/2.0/%s 127.0.0.1:9999;branch=z9hG4bK-%d", kind, cseq))
				m.SetHeader("From", "<sip:1001@127.0.0.1>;tag=caller")
				m.SetHeader("Contact", "<sip:1001@127.0.0.1:9999>")
				m.SetHeader("CSeq", fmt.Sprintf("%d %s", cseq, method))
				return m
			}
			exchangeMessage := func(m *Message, want int) *Message {
				send(serializePeerRequest(m))
				answer, err := ParseMessage(read())
				if err != nil {
					t.Fatal(err)
				}
				if answer.StatusCode != want {
					t.Fatalf("%s: status=%d, want %d", m.Method, answer.StatusCode, want)
				}
				return answer
			}
			authorize := func(m *Message, challenge *Message, password string) {
				p := parseDigestAuthorization(challenge.GetHeader("WWW-Authenticate"))
				p["username"], p["uri"], p["nc"], p["cnonce"] = "1001", m.RequestURI, "00000001", "transport-peer"
				response := expectedDigestResponse(string(m.Method), p["realm"], password, p)
				m.SetHeader("Authorization", fmt.Sprintf(`Digest username="1001", realm="%s", nonce="%s", uri="%s", qop=auth, nc=00000001, cnonce="transport-peer", response="%s"`, p["realm"], p["nonce"], m.RequestURI, response))
			}
			register := makeRequest("REGISTER", 1)
			challenge := exchangeMessage(register, 401)
			register = makeRequest("REGISTER", 2)
			authorize(register, challenge, "wrong-password")
			exchangeMessage(register, 401)
			register = makeRequest("REGISTER", 3)
			authorize(register, challenge, "transport-test")
			exchangeMessage(register, 200)
			if !s.registrar.IsRegistered("1001") {
				t.Fatal("authenticated registration missing")
			}
			invite := makeRequest("INVITE", 10)
			challenge = exchangeMessage(invite, 401)
			failedAck := makeRequest("ACK", 10)
			failedAck.SetHeader("To", challenge.To())
			send(serializePeerRequest(failedAck))
			invite = makeRequest("INVITE", 11)
			authorize(invite, challenge, "transport-test")
			answer := exchangeMessage(invite, 200)
			contactURI := strings.Trim(answer.GetHeader("Contact"), "<>")
			contactAddress := strings.TrimPrefix(strings.TrimPrefix(contactURI, "sips:"), "sip:")
			contactAddress = strings.Split(contactAddress, ";")[0]
			if _, port, err := net.SplitHostPort(contactAddress); err != nil || port == "" || port == "0" {
				t.Fatalf("unusable Contact: %q", contactURI)
			}
			var pub *maxvoip.PubSession
			select {
			case pub = <-added:
			case <-time.After(3 * time.Second):
				t.Fatal("publisher missing")
			}
			retry := exchangeMessage(invite, 200)
			if answer.Body != retry.Body || answer.To() != retry.To() || len(added) != 0 {
				t.Fatal("INVITE retransmission replaced the call")
			}
			ack := makeRequest("ACK", 11)
			ack.RequestURI = contactURI
			ack.SetHeader("To", answer.To())
			send(serializePeerRequest(ack))
			exchangeMessage(makeRequest("OPTIONS", 12), 200) // Barrier after ACK on the same connection.
			if d := s.dialogManager.Get("transport-call"); d == nil || d.State != DialogStateEstablished {
				t.Fatal("ACK did not establish the dialog")
			}
			rtp, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: pub.AudioPort()})
			if err != nil {
				t.Fatal(err)
			}
			for seq := uint16(1); seq <= 3; seq++ {
				pkt := make([]byte, 172)
				pkt[0], pkt[1] = 0x80, 8
				binary.BigEndian.PutUint16(pkt[2:], seq)
				binary.BigEndian.PutUint32(pkt[4:], uint32(seq)*160)
				binary.BigEndian.PutUint32(pkt[8:], 42)
				if _, err := rtp.Write(pkt); err != nil {
					t.Fatal(err)
				}
			}
			rtp.Close()
			select {
			case pkt := <-received:
				if pkt.PayloadType != base.AvPacketPtG711A || len(pkt.Payload) != 160 {
					t.Fatal("unexpected media")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("RTP did not reach publisher")
			}
			bye := makeRequest("BYE", 13)
			bye.RequestURI = contactURI
			bye.SetHeader("To", answer.To())
			exchangeMessage(bye, 200)
			waitRemoved(t, removed)
			if s.dialogManager.Get("transport-call") != nil {
				t.Fatal("dialog leaked")
			}
			if _, err := s.portAllocator.Alloc(); err != nil {
				t.Fatalf("port leaked: %v", err)
			}
			exchangeMessage(bye, 200) // A lost final response must remain replayable.
			register = makeRequest("REGISTER", 4)
			authorize(register, challenge, "transport-test")
			register.SetHeader("Expires", "0")
			exchangeMessage(register, 200)
			if s.registrar.IsRegistered("1001") {
				t.Fatal("unregistration failed")
			}
		})
	}
}

func transportPeer(t *testing.T, s *Server, kind TransportType, roots *x509.CertPool) (func(string), func() string, func()) {
	t.Helper()
	var conn net.Conn
	var err error
	secure := &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}
	switch kind {
	case TransportUDP:
		conn, err = net.Dial("udp", s.conn.LocalAddr().String())
	case TransportTCP:
		conn, err = net.Dial("tcp", s.tcpTransport.listener.Addr().String())
	case TransportTLS:
		conn, err = tls.Dial("tcp", s.tlsTransport.listener.Addr().String(), secure)
	case TransportDTLS:
		var dc *dtls.Conn
		dc, err = dtls.Dial("udp", s.dtlsTransport.listener.Addr().(*net.UDPAddr), &dtls.Config{RootCAs: roots, ServerName: "localhost"})
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = dc.HandshakeContext(ctx)
			cancel()
			if err != nil {
				dc.Close()
			} else {
				conn = dc
			}
		}
	case TransportWS, TransportWSS:
		address, scheme := "", "ws"
		if kind == TransportWS {
			address = s.wsTransport.listener.Addr().String()
		} else {
			scheme, address = "wss", s.wssTransport.listener.Addr().String()
		}
		dialer := websocket.Dialer{Subprotocols: []string{"sip"}, TLSClientConfig: secure, HandshakeTimeout: 5 * time.Second}
		ws, _, err := dialer.Dial(scheme+"://"+address+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		if ws.Subprotocol() != "sip" {
			t.Fatal("SIP websocket subprotocol missing")
		}
		return func(raw string) {
				t.Helper()
				ws.SetWriteDeadline(time.Now().Add(3 * time.Second))
				if err := ws.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
					t.Fatal(err)
				}
			},
			func() string {
				t.Helper()
				ws.SetReadDeadline(time.Now().Add(3 * time.Second))
				_, b, err := ws.ReadMessage()
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}, func() { ws.Close() }
	}
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	return func(raw string) {
			t.Helper()
			conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
			if _, err := conn.Write([]byte(raw)); err != nil {
				t.Fatal(err)
			}
		},
		func() string {
			t.Helper()
			conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			if kind == TransportTCP || kind == TransportTLS {
				raw, err := (&TCPTransport{}).readSIPMessage(reader)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			b := make([]byte, 8192)
			n, err := conn.Read(b)
			if err != nil {
				t.Fatal(err)
			}
			return string(b[:n])
		}, func() { conn.Close() }
}

func serializePeerRequest(m *Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s SIP/2.0\r\n", m.Method, m.RequestURI)
	for name, values := range m.Headers {
		for _, value := range values {
			fmt.Fprintf(&b, "%s: %s\r\n", name, value)
		}
	}
	b.WriteString("\r\n")
	b.WriteString(m.Body)
	return b.String()
}

func TestSIPStreamFramingCoalescedMessages(t *testing.T) {
	first, second := request("INVITE", "split"), request("OPTIONS", "coalesced")
	reader := bufio.NewReader(strings.NewReader(first + second))
	transport := &TCPTransport{}
	for _, want := range []string{first, second} {
		got, err := transport.readSIPMessage(reader)
		if err != nil || got != want {
			t.Fatalf("SIP framing: err=%v, got %q", err, got)
		}
	}
}

// Shrink the server's idle deadline only after the first SIP message, keeping
// the real HTTP/WebSocket handshake intact while reproducing a read timeout.
type idleDeadlineListener struct {
	net.Listener
	shorten *atomic.Bool
}
type idleDeadlineConn struct {
	net.Conn
	shorten *atomic.Bool
}

func (l idleDeadlineListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return idleDeadlineConn{c, l.shorten}, nil
}
func (c idleDeadlineConn) SetReadDeadline(deadline time.Time) error {
	if !deadline.IsZero() && c.shorten.Load() {
		deadline = time.Now().Add(10 * time.Millisecond)
	}
	return c.Conn.SetReadDeadline(deadline)
}

func TestWebSocketReadTimeoutClosesConnection(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var shorten atomic.Bool
	transport := NewWebSocketTransport(idleDeadlineListener{l, &shorten}, TransportWS, func(string, TransportAddr) { shorten.Store(true) }, nazalog.GetGlobalLogger())
	defer transport.Close()
	peer, _, err := websocket.DefaultDialer.Dial("ws://"+l.Addr().String()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if err := peer.WriteMessage(websocket.TextMessage, []byte(request("OPTIONS", "timeout"))); err != nil {
		t.Fatal(err)
	}
	peer.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err = peer.ReadMessage()
	if err == nil {
		t.Fatal("idle websocket was not closed")
	}
	if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("server kept retrying a broken websocket reader instead of closing")
	}
}

func TestTLSCloseInterruptsIncompleteHandshake(t *testing.T) {
	cert, key, _ := testSIPCertificate(t)
	s, err := NewServer(ServerConfig{Config: Config{SipListenAddr: "127.0.0.1:0", SipTlsListenAddr: "127.0.0.1:0", SipTlsCertFile: cert, SipTlsKeyFile: key}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	peer, err := net.Dial("tcp", s.tlsTransport.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	peer.Write([]byte{0x16, 0x03, 0x03, 0x00, 0x10, 0x01})
	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := s.tlsTransport.connections.Load(peer.LocalAddr().String()); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("TLS connection was not tracked while handshaking")
		}
		time.Sleep(time.Millisecond)
	}
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close waited for TLS handshake timeout")
	}
}

func TestSecureSIPTransportsRejectUntrustedCertificate(t *testing.T) {
	cert, key, _ := testSIPCertificate(t)
	s, err := NewServer(ServerConfig{Config: Config{SipListenAddr: "127.0.0.1:0", SipTlsListenAddr: "127.0.0.1:0", SipWssListenAddr: "127.0.0.1:0", SipDtlsListenAddr: "127.0.0.1:0", SipTlsCertFile: cert, SipTlsKeyFile: key}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	untrusted := &tls.Config{RootCAs: x509.NewCertPool(), ServerName: "localhost", MinVersion: tls.VersionTLS12}
	if c, err := tls.Dial("tcp", s.tlsTransport.listener.Addr().String(), untrusted); err == nil {
		c.Close()
		t.Fatal("TLS accepted untrusted certificate")
	}
	dialer := websocket.Dialer{TLSClientConfig: untrusted, HandshakeTimeout: time.Second}
	if c, _, err := dialer.Dial("wss://"+s.wssTransport.listener.Addr().String()+"/", nil); err == nil {
		c.Close()
		t.Fatal("WSS accepted untrusted certificate")
	}
	c, err := dtls.Dial("udp", s.dtlsTransport.listener.Addr().(*net.UDPAddr), &dtls.Config{RootCAs: x509.NewCertPool(), ServerName: "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.HandshakeContext(ctx); err == nil {
		t.Fatal("DTLS accepted untrusted certificate")
	}
}

func TestTCPPartialMessageTimeoutClosesConnection(t *testing.T) {
	server, peer := net.Pipe()
	defer peer.Close()
	var shorten atomic.Bool
	shorten.Store(true)
	transport := &TCPTransport{transportType: TransportTCP, stopChan: make(chan struct{}), log: nazalog.GetGlobalLogger(),
		onMessage: func(string, TransportAddr) { t.Error("partial message was dispatched") }}
	connection := &tcpConnection{conn: idleDeadlineConn{server, &shorten}, remoteAddr: "pipe", lastActive: time.Now()}
	transport.wg.Add(1)
	done := make(chan struct{})
	go func() { transport.handleConnection(connection); close(done) }()
	peer.Write([]byte("OPTIONS sip:partial@localhost SIP/2.0\r\nVia: "))
	select {
	case <-done:
	case <-time.After(time.Second):
		server.Close()
		t.Fatal("reader retried after partial message timeout")
	}
}
