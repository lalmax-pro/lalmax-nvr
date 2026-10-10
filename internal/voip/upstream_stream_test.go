package voip

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readStreamMessage(t *testing.T, reader *bufio.Reader, conn net.Conn) *Message {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	raw, err := (&TCPTransport{}).readSIPMessage(reader)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("parse SIP stream message: %v", err)
	}
	return msg
}

func writeStreamMessage(t *testing.T, conn net.Conn, raw string) {
	t.Helper()
	if err := conn.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
}

func testPBXCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "pbx.example.test"},
		DNSNames:     []string{"pbx.example.test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert, certPEM
}

func TestUpstreamPBXTCPAndTLSRegistrationAndExtensionDial(t *testing.T) {
	for _, transport := range []string{"tcp", "tls"} {
		t.Run(transport, func(t *testing.T) {
			tcpListener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			var listener net.Listener = tcpListener
			var caFile string
			if transport == "tls" {
				certificate, certPEM := testPBXCertificate(t)
				listener = tls.NewListener(tcpListener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
				caFile = filepath.Join(t.TempDir(), "pbx-ca.pem")
				if err := os.WriteFile(caFile, certPEM, 0600); err != nil {
					t.Fatal(err)
				}
			}
			defer listener.Close()
			if err := tcpListener.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}

			cfg := Config{
				SipListenAddr: "127.0.0.1:0", SipIP: "127.0.0.1", MediaIP: "127.0.0.1",
				MediaPortMin: 43000, MediaPortMax: 43020,
				PbxServer: listener.Addr().String(), PbxTransport: transport,
				PbxDomain: "pbx.example.test", PbxUsername: "nvr-account", PbxPassword: "pbx-secret", PbxRegisterExpires: 600,
			}
			if transport == "tls" {
				cfg.PbxTLSServerName = "pbx.example.test"
				cfg.PbxTLSCAFile = caFile
			}
			s, err := NewServer(ServerConfig{Config: cfg})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()

			conn, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			reader := bufio.NewReader(conn)
			register := readStreamMessage(t, reader, conn)
			if register.Method != MethodRegister || !strings.Contains(register.GetHeader("Via"), "SIP/2.0/"+strings.ToUpper(transport)) {
				t.Fatalf("wrong initial REGISTER: method=%s via=%q", register.Method, register.GetHeader("Via"))
			}
			if transport == "tcp" && register.RequestURI != "sip:pbx.example.test;transport=tcp" {
				t.Fatalf("unexpected TCP registrar URI: %s", register.RequestURI)
			}
			if transport == "tls" && register.RequestURI != "sips:pbx.example.test" {
				t.Fatalf("unexpected TLS registrar URI: %s", register.RequestURI)
			}
			if transport == "tls" && !strings.HasPrefix(register.GetHeader("Contact"), "<sips:") {
				t.Fatalf("TLS registration Contact must use the SIPS scheme, got %q", register.GetHeader("Contact"))
			}
			challenge := insertHeaderBeforeContentLength(BuildResponse(register, 401, "Unauthorized", ""), "WWW-Authenticate", `Digest realm="pbx.example.test", nonce="register-nonce", qop="auth", algorithm=MD5`)
			writeStreamMessage(t, conn, challenge)

			register = readStreamMessage(t, reader, conn)
			params := parseDigestAuthorization(register.GetHeader("Authorization"))
			if params == nil || params["username"] != "nvr-account" {
				t.Fatalf("REGISTER challenge was not answered: %q", register.GetHeader("Authorization"))
			}
			if got, want := params["response"], expectedDigestResponse("REGISTER", params["realm"], "pbx-secret", params); got != want {
				t.Fatalf("REGISTER digest mismatch: got %s want %s", got, want)
			}
			writeStreamMessage(t, conn, insertHeaderBeforeContentLength(BuildResponse(register, 200, "OK", ""), "Expires", "600"))

			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && s.Status().UpstreamRegistration.State != "registered" {
				time.Sleep(10 * time.Millisecond)
			}
			if got := s.Status().UpstreamRegistration; got.State != "registered" || !strings.EqualFold(got.Transport, transport) {
				t.Fatalf("PBX registration failed: %+v", got)
			}

			call, err := s.Dial("204", "rtp", browserTalkOffer)
			if err != nil {
				t.Fatal(err)
			}
			invite := readStreamMessage(t, reader, conn)
			if invite.Method != MethodInvite {
				t.Fatalf("expected INVITE, got %s", invite.Method)
			}
			if transport == "tcp" && invite.RequestURI != "sip:204@pbx.example.test;transport=tcp" {
				t.Fatalf("unexpected TCP extension URI: %s", invite.RequestURI)
			}
			if transport == "tls" && invite.RequestURI != "sips:204@pbx.example.test" {
				t.Fatalf("unexpected TLS extension URI: %s", invite.RequestURI)
			}
			writeStreamMessage(t, conn, insertHeaderBeforeContentLength(BuildResponseWithContact(invite, 407, "Proxy Authentication Required", "", "pbx", ""), "Proxy-Authenticate", `Digest realm="pbx.example.test", nonce="invite-nonce", qop="auth", algorithm=MD5`))
			if ack := readStreamMessage(t, reader, conn); ack.Method != MethodAck {
				t.Fatalf("expected ACK for 407, got %s", ack.Method)
			}
			invite = readStreamMessage(t, reader, conn)
			params = parseDigestAuthorization(invite.GetHeader("Proxy-Authorization"))
			if params == nil || invite.Method != MethodInvite {
				t.Fatalf("expected authenticated INVITE retry: %+v", invite)
			}
			writeStreamMessage(t, conn, BuildResponseWithContact(invite, 486, "Busy Here", "", "pbx", ""))
			if ack := readStreamMessage(t, reader, conn); ack.Method != MethodAck {
				t.Fatalf("expected ACK after busy response, got %s", ack.Method)
			}
			for _, status := range s.Status().Calls {
				if status.CallID == call.CallID && status.State != "failed" {
					t.Fatalf("busy response should fail outbound call: %+v", status)
				}
			}
		})
	}
}
