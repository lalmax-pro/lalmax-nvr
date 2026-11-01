package voip

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"testing"

	maxvoip "github.com/q191201771/lalmax/voip"
	"github.com/q191201771/lalmax/voip/media"
	"github.com/q191201771/naza/pkg/nazalog"
)

func m2ServerConfig() Config {
	return Config{SipListenAddr: "127.0.0.1:0", MediaIP: "127.0.0.1", MediaPortMin: 46000, MediaPortMax: 46100}
}

func m2FreeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	_ = conn.Close()
	return port
}

func m2OccupiedUDPPort(t *testing.T) (int, net.PacketConn) {
	t.Helper()
	conn, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	return conn.LocalAddr().(*net.UDPAddr).Port, conn
}

func TestM2VoipNewServerBranches(t *testing.T) {
	t.Run("invalid config", func(t *testing.T) {
		cfg := m2ServerConfig()
		cfg.MediaPortMin = 1
		if _, err := NewServer(ServerConfig{Config: cfg}); err == nil {
			t.Fatal("invalid media port range accepted")
		}
	})

	t.Run("srtp certificate", func(t *testing.T) {
		cfg := m2ServerConfig()
		cfg.SrtpEnable = true
		s, err := NewServer(ServerConfig{Config: cfg})
		if err != nil {
			t.Fatal(err)
		}
		if s.dtlsFingerprint == "" {
			t.Fatal("srtp server has no DTLS fingerprint")
		}
		s.Dispose()
		if s.Status().Enabled {
			t.Fatal("disposed server still enabled")
		}
	})

	t.Run("udp resolve error", func(t *testing.T) {
		cfg := m2ServerConfig()
		cfg.SipListenAddr = "bad:0"
		if _, err := NewServer(ServerConfig{Config: cfg}); err == nil {
			t.Fatal("invalid UDP address accepted")
		}
	})

	t.Run("udp listen error", func(t *testing.T) {
		port, holder := m2OccupiedUDPPort(t)
		defer holder.Close()
		cfg := m2ServerConfig()
		cfg.SipListenAddr = fmt.Sprintf("127.0.0.1:%d", port)
		if _, err := NewServer(ServerConfig{Config: cfg}); err == nil {
			t.Fatal("occupied UDP port accepted")
		}
	})

	t.Run("tcp resolve and listen errors", func(t *testing.T) {
		cfg := m2ServerConfig()
		cfg.SipTcpListenAddr = "bad:0"
		if _, err := NewServer(ServerConfig{Config: cfg}); err == nil {
			t.Fatal("invalid TCP address accepted")
		}
		holder, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer holder.Close()
		cfg = m2ServerConfig()
		cfg.SipTcpListenAddr = holder.Addr().String()
		if _, err := NewServer(ServerConfig{Config: cfg}); err == nil {
			t.Fatal("occupied TCP port accepted")
		}
	})

	t.Run("tls errors", func(t *testing.T) {
		cfg := m2ServerConfig()
		cfg.SipTlsListenAddr = "127.0.0.1:0"
		cfg.SipTlsCertFile = "/nonexistent/cert.pem"
		cfg.SipTlsKeyFile = "/nonexistent/key.pem"
		if _, err := NewServer(ServerConfig{Config: cfg}); err == nil {
			t.Fatal("missing TLS certificate accepted")
		}

		cert, key, _ := testSIPCertificate(t)
		cfg = m2ServerConfig()
		cfg.SipTlsListenAddr = "bad:0"
		cfg.SipTlsCertFile, cfg.SipTlsKeyFile = cert, key
		if _, err := NewServer(ServerConfig{Config: cfg}); err == nil {
			t.Fatal("invalid TLS address accepted")
		}

		holder, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer holder.Close()
		cfg = m2ServerConfig()
		cfg.SipTlsListenAddr = holder.Addr().String()
		cfg.SipTlsCertFile, cfg.SipTlsKeyFile = cert, key
		if _, err := NewServer(ServerConfig{Config: cfg}); err == nil {
			t.Fatal("occupied TLS port accepted")
		}
	})

	t.Run("dtls errors", func(t *testing.T) {
		cfg := m2ServerConfig()
		cfg.SipDtlsListenAddr = "127.0.0.1:0"
		cfg.SipTlsCertFile = "/nonexistent/cert.pem"
		cfg.SipTlsKeyFile = "/nonexistent/key.pem"
		if _, err := NewServer(ServerConfig{Config: cfg}); err == nil {
			t.Fatal("missing DTLS certificate accepted")
		}

		cert, key, _ := testSIPCertificate(t)
		cfg = m2ServerConfig()
		cfg.SipDtlsListenAddr = "bad:0"
		cfg.SipTlsCertFile, cfg.SipTlsKeyFile = cert, key
		if _, err := NewServer(ServerConfig{Config: cfg}); err == nil {
			t.Fatal("invalid DTLS address accepted")
		}

		port, holder := m2OccupiedUDPPort(t)
		defer holder.Close()
		cfg = m2ServerConfig()
		cfg.SipDtlsListenAddr = fmt.Sprintf("127.0.0.1:%d", port)
		cfg.SipTlsCertFile, cfg.SipTlsKeyFile = cert, key
		if _, err := NewServer(ServerConfig{Config: cfg}); err == nil {
			t.Fatal("occupied DTLS port accepted")
		}
	})

	t.Run("websocket errors", func(t *testing.T) {
		cfg := m2ServerConfig()
		cfg.SipWsListenAddr = "bad:0"
		if _, err := NewServer(ServerConfig{Config: cfg}); err == nil {
			t.Fatal("invalid WS address accepted")
		}

		cfg = m2ServerConfig()
		cfg.SipWssListenAddr = "127.0.0.1:0"
		cfg.SipTlsCertFile = "/nonexistent/cert.pem"
		cfg.SipTlsKeyFile = "/nonexistent/key.pem"
		if _, err := NewServer(ServerConfig{Config: cfg}); err == nil {
			t.Fatal("missing WSS certificate accepted")
		}

		cert, key, _ := testSIPCertificate(t)
		cfg = m2ServerConfig()
		cfg.SipWssListenAddr = "bad:0"
		cfg.SipTlsCertFile, cfg.SipTlsKeyFile = cert, key
		if _, err := NewServer(ServerConfig{Config: cfg}); err == nil {
			t.Fatal("invalid WSS address accepted")
		}
	})

	t.Run("upstream transport errors", func(t *testing.T) {
		cfg := m2ServerConfig()
		cfg.PbxServer = "127.0.0.1:5060"
		cfg.PbxTransport = "tls"
		cfg.PbxUsername, cfg.PbxPassword = "1001", "pass"
		cfg.PbxTLSCAFile = "/nonexistent/ca.pem"
		if _, err := NewServer(ServerConfig{Config: cfg}); err == nil {
			t.Fatal("missing PBX CA file accepted")
		}

		cfg = m2ServerConfig()
		cfg.PbxServer = "bad:1"
		cfg.PbxTransport = "udp"
		cfg.PbxUsername, cfg.PbxPassword = "1001", "pass"
		if _, err := NewServer(ServerConfig{Config: cfg}); err == nil {
			t.Fatal("unresolvable PBX server accepted")
		}
	})
}

func TestM2VoipFormatSIPHost(t *testing.T) {
	if got := formatSIPHost("2001:db8::1"); got != "[2001:db8::1]" {
		t.Fatalf("IPv6 host = %q", got)
	}
	if got := formatSIPHost("127.0.0.1"); got != "127.0.0.1" {
		t.Fatalf("IPv4 host = %q", got)
	}
	if got := formatSIPHost("example.test"); got != "example.test" {
		t.Fatalf("name host = %q", got)
	}
}

func TestM2VoipSendResponseBranches(t *testing.T) {
	s := m2UnitServer(t)
	s.udpTransport = nil
	if err := s.sendResponse("SIP/2.0 200 OK\r\n\r\n", TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"}); err == nil {
		t.Fatal("UDP without transport succeeded")
	}
	for _, typ := range []TransportType{TransportDTLS, TransportWS, TransportWSS, TransportType("SCTP")} {
		if err := s.sendResponse("SIP/2.0 200 OK\r\n\r\n", TransportAddr{Type: typ, Addr: "127.0.0.1:9"}); err == nil {
			t.Fatalf("%s without transport succeeded", typ)
		}
	}

	// PBX dial-only fallbacks are used when no server transport is configured.
	tcp, err := NewDialOnlyTCPTransport(TransportTCP, nil, s.handleMessage, s.log)
	if err != nil {
		t.Fatal(err)
	}
	s.pbxTCPTransport = tcp
	t.Cleanup(func() { _ = tcp.Close() })
	if err := s.sendResponse("SIP/2.0 200 OK\r\n\r\n", TransportAddr{Type: TransportTCP, Addr: "127.0.0.1:1"}); err == nil {
		t.Fatal("PBX TCP fallback unexpectedly succeeded")
	}
	tlsDial, err := NewDialOnlyTCPTransport(TransportTLS, &tls.Config{MinVersion: tls.VersionTLS12}, s.handleMessage, s.log)
	if err != nil {
		t.Fatal(err)
	}
	s.pbxTLSTransport = tlsDial
	t.Cleanup(func() { _ = tlsDial.Close() })
	peer := TransportAddr{Type: TransportTLS, Addr: "127.0.0.1:1"}
	s.upstream = &upstreamRegistration{peer: peer}
	if err := s.sendResponse("SIP/2.0 200 OK\r\n\r\n", peer); err == nil {
		t.Fatal("PBX TLS fallback unexpectedly succeeded")
	}
	s.upstream = nil
	if err := s.sendResponse("SIP/2.0 200 OK\r\n\r\n", TransportAddr{Type: TransportTLS, Addr: "127.0.0.1:1"}); err == nil {
		t.Fatal("TLS without transport unexpectedly succeeded")
	}

	// A real TLS transport is preferred over the PBX fallback.
	cert, key, _ := testSIPCertificate(t)
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	s.tlsTransport = NewTLSTransport(listener, &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}, s.handleMessage, s.log)
	t.Cleanup(func() { _ = s.tlsTransport.Close() })
	if err := s.sendResponse("SIP/2.0 200 OK\r\n\r\n", TransportAddr{Type: TransportTLS, Addr: "127.0.0.1:1"}); err == nil {
		t.Fatal("TLS send to unreachable peer unexpectedly succeeded")
	}

	// A real TCP transport is used for TCP addresses.
	tcpListener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	s.tcpTransport = NewTCPTransport(tcpListener, s.handleMessage, s.log)
	t.Cleanup(func() { _ = s.tcpTransport.Close() })
	if err := s.sendResponse("SIP/2.0 200 OK\r\n\r\n", TransportAddr{Type: TransportTCP, Addr: "127.0.0.1:1"}); err == nil {
		t.Fatal("TCP send to unreachable peer unexpectedly succeeded")
	}
}

func TestM2VoipHandleMessageBranches(t *testing.T) {
	s, added, _, _ := newTestServer(t, 5000, 15000)
	_ = added
	peer, exchangePeer := sipPeer(t, s)

	// Keepalives and unparseable messages are ignored.
	s.handleMessage("   \r\n", TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:1"})
	s.handleMessage("garbage", TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:1"})

	// Require header yields 420 Bad Extension with the unsupported option.
	requireRaw := request("OPTIONS", "require-call")
	requireRaw = strings.Replace(requireRaw, "Content-Type: application/sdp\r\n", "Require: 100rel\r\nContent-Type: application/sdp\r\n", 1)
	if got := exchangePeer(requireRaw); !strings.Contains(got, "420 Bad Extension") || !strings.Contains(got, "Unsupported: 100rel") {
		t.Fatalf("Require response = %q", got)
	}

	// Unknown methods yield 501.
	unknown := strings.Replace(request("OPTIONS", "unknown-method"), "OPTIONS sip:", "MESSAGE sip:", 1)
	unknown = strings.Replace(unknown, "CSeq: 1 OPTIONS", "CSeq: 1 MESSAGE", 1)
	if got := exchangePeer(unknown); !strings.Contains(got, "501 Not Implemented") {
		t.Fatalf("unknown method response = %q", got)
	}

	// A retransmission with a changed body is rejected.
	invite := request("INVITE", "changed-tx")
	if got := exchangePeer(invite); !strings.Contains(got, "200 OK") {
		t.Fatalf("invite = %q", got)
	}
	changed := strings.Replace(invite, "m=audio 40000 RTP/AVP 8", "m=audio 40000 RTP/AVP 8 0", 1)
	if got := exchangePeer(changed); !strings.Contains(got, "400 Changed Transaction Request") {
		t.Fatalf("changed transaction response = %q", got)
	}
	s.handleMessage(inDialogRequest(s, "ACK", "changed-tx"), TransportAddr{Type: TransportUDP, Addr: peer.LocalAddr().String()})

	// In-dialog OPTIONS: stale CSeq is rejected and a valid CSeq updates state.
	options := inDialogRequest(s, "OPTIONS", "changed-tx")
	m, _ := ParseMessage(options)
	m.SetHeader("CSeq", "1 OPTIONS")
	if got := exchangePeer(serializePeerRequest(m)); !strings.Contains(got, "500 CSeq Out of Order") {
		t.Fatalf("stale options = %q", got)
	}
	fresh := inDialogRequest(s, "OPTIONS", "changed-tx")
	fm, _ := ParseMessage(fresh)
	fm.SetHeader("CSeq", "3 OPTIONS")
	if got := exchangePeer(serializePeerRequest(fm)); !strings.Contains(got, "200 OK") {
		t.Fatalf("fresh options = %q", got)
	}
	if got := s.dialogManager.Get("changed-tx").RemoteCSeq; got != 3 {
		t.Fatalf("options did not update remote CSeq: %d", got)
	}

	// OPTIONS with an unknown To tag is 481.
	tagged := request("OPTIONS", "options-unknown")
	tm, _ := ParseMessage(tagged)
	tm.SetHeader("To", tm.To()+";tag=unknown")
	if got := exchangePeer(serializePeerRequest(tm)); !strings.Contains(got, "481") {
		t.Fatalf("unknown dialog options = %q", got)
	}
}

func TestM2VoipHandleInviteErrorBranches(t *testing.T) {
	s, _, _, _ := newTestServer(t, 5000, 15000)
	_, exchangePeer := sipPeer(t, s)

	noFromTag := strings.Replace(request("INVITE", "no-from-tag"), ";tag=caller", "", 1)
	if got := exchangePeer(noFromTag); !strings.Contains(got, "400 Bad Request") {
		t.Fatalf("missing from tag = %q", got)
	}

	noBody := request("INVITE", "no-body")
	noBody = strings.Replace(noBody, "Content-Type: application/sdp", "Content-Type: text/plain", 1)
	idx := strings.Index(noBody, "v=0")
	if idx < 0 {
		t.Fatal("invite body missing")
	}
	noBody = noBody[:idx]
	noBody = strings.Replace(noBody, "Content-Length: 1", "Content-Length: 0", 1)
	if got := exchangePeer(noBody); !strings.Contains(got, "488 SDP Offer Required") {
		t.Fatalf("empty body = %q", got)
	}

	withToTag := request("INVITE", "unknown-dialog")
	tm, _ := ParseMessage(withToTag)
	tm.SetHeader("To", tm.To()+";tag=x")
	if got := exchangePeer(serializePeerRequest(tm)); !strings.Contains(got, "481") {
		t.Fatalf("unknown dialog invite = %q", got)
	}

	invalidSDP := request("INVITE", "invalid-sdp")
	im, _ := ParseMessage(invalidSDP)
	im.Body = "invalid SDP"
	im.SetHeader("Content-Length", fmt.Sprint(len(im.Body)))
	if got := exchangePeer(serializePeerRequest(im)); !strings.Contains(got, "488 Media Negotiation Failed") {
		t.Fatalf("invalid SDP = %q", got)
	}

	zrtp := request("INVITE", "zrtp")
	zm, _ := ParseMessage(zrtp)
	zm.Body = "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 40000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\na=zrtp-hash:1.10 deadbeef\r\n"
	zm.SetHeader("Content-Length", fmt.Sprint(len(zm.Body)))
	if got := exchangePeer(serializePeerRequest(zm)); !strings.Contains(got, "488") {
		t.Fatalf("zrtp offer = %q", got)
	}

	noMediaIP := request("INVITE", "local-ip")
	s.requestMu.Lock()
	s.config.MediaIP = ""
	s.config.SipIP = ""
	s.requestMu.Unlock()
	if got := exchangePeer(noMediaIP); !strings.Contains(got, "200 OK") || !strings.Contains(got, "127.0.0.1") {
		t.Fatalf("local ip fallback = %q", got)
	}
}

func TestM2VoipHandleInvitePublishFailure(t *testing.T) {
	probe := m2FreeUDPPort(t)
	s, err := NewServer(ServerConfig{
		Config: Config{SipListenAddr: "127.0.0.1:0", MediaIP: "127.0.0.1", MediaPortMin: probe, MediaPortMax: probe + 4},
		OnPubSession: func(*maxvoip.PubSession) error {
			return fmt.Errorf("publish rejected")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	_, exchangePeer := sipPeer(t, s)
	if got := exchangePeer(request("INVITE", "publish-failure")); !strings.Contains(got, "500 Publish Failed") {
		t.Fatalf("publish failure response = %q", got)
	}
}

func TestM2VoipPrepareMediaFailures(t *testing.T) {
	t.Run("audio allocation", func(t *testing.T) {
		port, holder := m2OccupiedUDPPort(t)
		defer holder.Close()
		s, err := NewServer(ServerConfig{Config: Config{SipListenAddr: "127.0.0.1:0", MediaIP: "127.0.0.1", MediaPortMin: port, MediaPortMax: port}})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		_, exchangePeer := sipPeer(t, s)
		if got := exchangePeer(request("INVITE", "audio-alloc")); !strings.Contains(got, "503 Media Negotiation Failed") {
			t.Fatalf("audio allocation failure = %q", got)
		}
	})

	t.Run("video allocation", func(t *testing.T) {
		port := m2FreeUDPPort(t)
		s, err := NewServer(ServerConfig{Config: Config{SipListenAddr: "127.0.0.1:0", MediaIP: "127.0.0.1", MediaPortMin: port, MediaPortMax: port}})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		_, exchangePeer := sipPeer(t, s)
		body := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
			"m=audio 40000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\na=rtcp-mux\r\n" +
			"m=video 40002 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=rtcp-mux\r\n"
		invite := request("INVITE", "video-alloc")
		m, _ := ParseMessage(invite)
		m.Body = body
		m.SetHeader("Content-Length", fmt.Sprint(len(body)))
		if got := exchangePeer(serializePeerRequest(m)); !strings.Contains(got, "503 Media Negotiation Failed") {
			t.Fatalf("video allocation failure = %q", got)
		}
	})

	t.Run("srtp mandatory plain", func(t *testing.T) {
		s, err := NewServer(ServerConfig{Config: Config{SipListenAddr: "127.0.0.1:0", MediaIP: "127.0.0.1", MediaPortMin: 46200, MediaPortMax: 46300, SrtpEnable: true, SrtpMandatory: true}})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		_, exchangePeer := sipPeer(t, s)
		if got := exchangePeer(request("INVITE", "plain-srtp")); !strings.Contains(got, "488 Media Negotiation Failed") {
			t.Fatalf("plain RTP under mandatory SRTP = %q", got)
		}
	})

	t.Run("video non bundle", func(t *testing.T) {
		s, err := NewServer(ServerConfig{Config: Config{SipListenAddr: "127.0.0.1:0", MediaIP: "127.0.0.1", MediaPortMin: 46600, MediaPortMax: 46700}})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		_, exchangePeer := sipPeer(t, s)
		body := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
			"m=audio 40000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\na=rtcp-mux\r\n" +
			"m=video 40002 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=rtcp-mux\r\n"
		invite := request("INVITE", "video-non-bundle")
		m, _ := ParseMessage(invite)
		m.Body = body
		m.SetHeader("Content-Length", fmt.Sprint(len(body)))
		if got := exchangePeer(serializePeerRequest(m)); !strings.Contains(got, "200 OK") || !strings.Contains(got, "m=video") {
			t.Fatalf("video offer = %q", got)
		}
	})
}

func m2CryptoKey() string {
	return base64.StdEncoding.EncodeToString(make([]byte, 30))
}

func TestM2VoipSrtpNegotiation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "audio and video sdes",
			body: "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
				"m=audio 40000 RTP/SAVP 8\r\na=rtpmap:8 PCMA/8000\r\na=rtcp-mux\r\na=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:" + m2CryptoKey() + "\r\n" +
				"m=video 40002 RTP/SAVP 96\r\na=rtpmap:96 H264/90000\r\na=rtcp-mux\r\na=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:" + m2CryptoKey() + "\r\n",
			want: "200 OK",
		},
		{
			name: "audio invalid inline key",
			body: "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
				"m=audio 40000 RTP/SAVP 8\r\na=rtpmap:8 PCMA/8000\r\na=rtcp-mux\r\na=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:not-base64!\r\n",
			want: "500 Media Negotiation Failed",
		},
		{
			name: "audio short inline key",
			body: "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
				"m=audio 40000 RTP/SAVP 8\r\na=rtpmap:8 PCMA/8000\r\na=rtcp-mux\r\na=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:" + base64.StdEncoding.EncodeToString([]byte{1, 2, 3}) + "\r\n",
			want: "500 Media Negotiation Failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewServer(ServerConfig{Config: Config{
				SipListenAddr: "127.0.0.1:0", MediaIP: "127.0.0.1",
				MediaPortMin: 46400, MediaPortMax: 46500,
				SrtpEnable: true, SrtpMandatory: true,
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			_, exchangePeer := sipPeer(t, s)
			invite := request("INVITE", "srtp-call")
			m, _ := ParseMessage(invite)
			m.Body = tt.body
			m.SetHeader("Content-Type", "application/sdp")
			m.SetHeader("Content-Length", fmt.Sprint(len(m.Body)))
			if got := exchangePeer(serializePeerRequest(m)); !strings.Contains(got, tt.want) {
				t.Fatalf("srtp negotiation = %q, want %q", got, tt.want)
			}
		})
	}
}

func m2ManualPendingServer(t *testing.T, cfg Config, onPub func(*maxvoip.PubSession) error) (*Server, *Dialog) {
	t.Helper()
	s, err := NewServer(ServerConfig{Config: cfg, OnPubSession: onPub})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.dialogManager.Create("pending", "caller", "server", "1001", "1002", "127.0.0.1:9")
	invite, _ := ParseMessage(request("INVITE", "pending"))
	s.dialogManager.SetInvite("pending", invite, TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"})
	return s, s.dialogManager.Get("pending")
}

func TestM2VoipManualAnswerErrors(t *testing.T) {
	manualConfig := func() Config {
		cfg := Config{SipListenAddr: "127.0.0.1:0", MediaIP: "127.0.0.1", MediaPortMin: 46600, MediaPortMax: 46700, ManualAnswer: true, RingTimeoutMs: 5000}
		return cfg
	}

	s := m2UnitServer(t)
	if err := s.AnswerIncomingCall("missing"); err == nil {
		t.Fatal("non-manual answer accepted")
	}
	if err := s.RejectIncomingCall("missing"); err == nil {
		t.Fatal("non-manual reject accepted")
	}
	_ = s.Close()
	if err := s.AnswerIncomingCall("missing"); err == nil {
		t.Fatal("answer on stopped server accepted")
	}

	s, _ = m2ManualPendingServer(t, manualConfig(), nil)
	if err := s.AnswerIncomingCall("missing"); err == nil {
		t.Fatal("unknown call answered")
	}
	if err := s.RejectIncomingCall("missing"); err == nil {
		t.Fatal("unknown call rejected")
	}

	// Publishing failures dispose the prepared media and surface the error.
	s, _ = m2ManualPendingServer(t, manualConfig(), func(*maxvoip.PubSession) error { return fmt.Errorf("feed failed") })
	if err := s.AnswerIncomingCall("pending"); err == nil || !strings.Contains(err.Error(), "publish call media") {
		t.Fatalf("publish failure = %v", err)
	}

	// Media preparation failures surface the SIP status.
	s, _ = m2ManualPendingServer(t, manualConfig(), nil)
	occupiedPort, holder := m2OccupiedUDPPort(t)
	defer holder.Close()
	s.portAllocator = media.NewPortAllocator(occupiedPort, occupiedPort)
	if err := s.AnswerIncomingCall("pending"); err == nil || !strings.Contains(err.Error(), "SIP 503") {
		t.Fatalf("media failure = %v", err)
	}

	// A transport failure while sending the final response is returned.
	s, _ = m2ManualPendingServer(t, manualConfig(), nil)
	s.dialogManager.Create("ws", "caller", "server", "1001", "1002", "peer")
	wsInvite, _ := ParseMessage(request("INVITE", "ws"))
	s.dialogManager.SetInvite("ws", wsInvite, TransportAddr{Type: TransportWS, Addr: "ws-peer"})
	if err := s.AnswerIncomingCall("ws"); err == nil || !strings.Contains(err.Error(), "send SIP answer") {
		t.Fatalf("answer transport failure = %v", err)
	}

	s, _ = m2ManualPendingServer(t, manualConfig(), nil)
	s.dialogManager.Create("ws-reject", "caller", "server", "1001", "1002", "peer")
	wsReject, _ := ParseMessage(request("INVITE", "ws-reject"))
	s.dialogManager.SetInvite("ws-reject", wsReject, TransportAddr{Type: TransportWS, Addr: "ws-peer"})
	if err := s.RejectIncomingCall("ws-reject"); err == nil || !strings.Contains(err.Error(), "send SIP rejection") {
		t.Fatalf("reject transport failure = %v", err)
	}
}

func TestM2VoipAuthenticationBranches(t *testing.T) {
	cfg := Config{SipListenAddr: "127.0.0.1:0", MediaIP: "127.0.0.1", MediaPortMin: 46800, MediaPortMax: 46900,
		AuthEnable: true, Realm: "lalmax-nvr", Users: []User{{Username: "1001", Password: "pass"}}}
	s, err := NewServer(ServerConfig{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, exchangePeer := sipPeer(t, s)

	unknownUser := request("REGISTER", "auth-unknown")
	um, _ := ParseMessage(unknownUser)
	um.SetHeader("From", "<sip:9999@127.0.0.1>;tag=reg")
	if got := exchangePeer(serializePeerRequest(um)); !strings.Contains(got, "401 Unauthorized") {
		t.Fatalf("unknown user = %q", got)
	}

	badAuth := request("REGISTER", "auth-bad")
	bm, _ := ParseMessage(badAuth)
	bm.SetHeader("Authorization", "nonsense")
	if got := exchangePeer(serializePeerRequest(bm)); !strings.Contains(got, "401 Unauthorized") {
		t.Fatalf("bad auth = %q", got)
	}

	// A valid digest is accepted once and the replayed nonce is challenged.
	nonce, err := s.nonceStore.Issue()
	if err != nil {
		t.Fatal(err)
	}
	req, _ := ParseMessage(request("REGISTER", "auth-ok"))
	params := map[string]string{
		"username": "1001", "realm": "lalmax-nvr", "nonce": nonce, "uri": "sip:1001@127.0.0.1",
		"qop": "auth", "nc": "00000001", "cnonce": "abcdef", "algorithm": "MD5",
	}
	params["response"] = expectedDigestResponse("REGISTER", "lalmax-nvr", "pass", params)
	req.SetHeader("Authorization", fmt.Sprintf(`Digest username="1001", realm="lalmax-nvr", nonce="%s", uri="sip:1001@127.0.0.1", qop=auth, nc=00000001, cnonce="abcdef", response="%s", algorithm=MD5`, nonce, params["response"]))
	if ok, _ := s.authenticateRequest(req, "1001"); !ok {
		t.Fatal("valid digest rejected")
	}
	if ok, _ := s.authenticateRequest(req, "1001"); ok {
		t.Fatal("replayed nonce accepted")
	}

	// Unsupported digest algorithms and mismatched usernames are challenged.
	other, err := s.nonceStore.Issue()
	if err != nil {
		t.Fatal(err)
	}
	md5sess, _ := ParseMessage(request("REGISTER", "auth-alg"))
	md5sess.SetHeader("Authorization", fmt.Sprintf(`Digest username="1001", realm="lalmax-nvr", nonce="%s", uri="sip:1001@127.0.0.1", qop=auth, nc=00000001, cnonce="abcdef", response="deadbeef", algorithm=MD5-sess`, other))
	if ok, _ := s.authenticateRequest(md5sess, "1001"); ok {
		t.Fatal("unsupported digest algorithm accepted")
	}
	if ok, _ := s.authenticateRequest(req, "other-user"); ok {
		t.Fatal("username mismatch accepted")
	}
	_ = context.Background
	_ = nazalog.GetGlobalLogger()
}
