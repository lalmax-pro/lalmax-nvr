package voip

import (
	"strings"
	"testing"
)

func TestBuildContactURIUsesConfiguredSipIP(t *testing.T) {
	s := &Server{
		config:     Config{SipIP: "192.168.1.100", MediaIP: "10.0.0.20"},
		listenAddr: "0.0.0.0:5070",
	}

	if got, want := s.buildContactURI(TransportAddr{Type: TransportUDP}), "<sip:192.168.1.100:5070>"; got != want {
		t.Fatalf("buildContactURI returned %q, want %q", got, want)
	}
}

func TestBuildContactURIDoesNotAdvertiseUnspecifiedIP(t *testing.T) {
	s := &Server{
		config:     Config{SipIP: "0.0.0.0"},
		listenAddr: "0.0.0.0:5070",
	}

	if got, want := s.buildContactURI(TransportAddr{Type: TransportUDP}), "<sip:127.0.0.1:5070>"; got != want {
		t.Fatalf("buildContactURI returned %q, want %q", got, want)
	}
}

func TestBuildContactURIUsesTransportSpecificPort(t *testing.T) {
	s := &Server{
		config: Config{
			SipIP:             "203.0.113.10",
			SipTcpListenAddr:  "0.0.0.0:5070",
			SipTlsListenAddr:  "0.0.0.0:5061",
			SipDtlsListenAddr: "0.0.0.0:5061",
			SipWsListenAddr:   "0.0.0.0:8088",
			SipWssListenAddr:  "0.0.0.0:8443",
		},
		listenAddr: "0.0.0.0:5070",
	}

	tests := []struct {
		name      string
		transport TransportType
		want      string
	}{
		{name: "udp", transport: TransportUDP, want: "<sip:203.0.113.10:5070>"},
		{name: "tcp", transport: TransportTCP, want: "<sip:203.0.113.10:5070;transport=tcp>"},
		{name: "tls", transport: TransportTLS, want: "<sips:203.0.113.10:5061>"},
		{name: "dtls", transport: TransportDTLS, want: "<sip:203.0.113.10:5061;transport=dtls-udp>"},
		{name: "ws", transport: TransportWS, want: "<sip:203.0.113.10:8088;transport=ws>"},
		{name: "wss", transport: TransportWSS, want: "<sip:203.0.113.10:8443;transport=wss>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.buildContactURI(TransportAddr{Type: tt.transport})
			if got != tt.want {
				t.Fatalf("buildContactURI returned %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAuthenticateRequestDigest(t *testing.T) {
	s := &Server{
		config: Config{
			AuthEnable: true,
			Realm:      "lalmax-nvr",
			Users:      []User{{Username: "1001", Password: "password123"}},
		},
		nonceStore: newDigestNonceStore(),
	}
	nonce := s.nonceStore.New()
	req := &Message{
		IsRequest:  true,
		Method:     MethodRegister,
		RequestURI: "sip:127.0.0.1:5070",
		Headers: map[string][]string{
			"From":    {"<sip:1001@127.0.0.1>;tag=1"},
			"To":      {"<sip:1001@127.0.0.1>"},
			"Call-ID": {"call-1"},
			"CSeq":    {"1 REGISTER"},
			"Via":     {"SIP/2.0/UDP 127.0.0.1:5000;branch=z9hG4bK-test"},
		},
	}
	params := map[string]string{
		"username": "1001",
		"realm":    "lalmax-nvr",
		"nonce":    nonce,
		"uri":      req.RequestURI,
		"qop":      "auth",
		"nc":       "00000001",
		"cnonce":   "abcdef",
	}
	response := expectedDigestResponse("REGISTER", "lalmax-nvr", "password123", params)
	req.SetHeader("Authorization", `Digest username="1001", realm="lalmax-nvr", nonce="`+nonce+`", uri="sip:127.0.0.1:5070", qop=auth, nc=00000001, cnonce="abcdef", response="`+response+`", algorithm=MD5`)

	ok, challenge := s.authenticateRequest(req, "1001")
	if !ok || challenge != "" {
		t.Fatalf("authenticateRequest returned ok=%v challenge=%q", ok, challenge)
	}
}

func TestAuthenticateRequestChallengesMissingAuthorization(t *testing.T) {
	s := &Server{
		config: Config{
			AuthEnable: true,
			Realm:      "lalmax-nvr",
			Users:      []User{{Username: "1001", Password: "password123"}},
		},
		nonceStore: newDigestNonceStore(),
	}
	req := &Message{
		IsRequest:  true,
		Method:     MethodRegister,
		RequestURI: "sip:127.0.0.1:5070",
		Headers: map[string][]string{
			"From":    {"<sip:1001@127.0.0.1>;tag=1"},
			"To":      {"<sip:1001@127.0.0.1>"},
			"Call-ID": {"call-1"},
			"CSeq":    {"1 REGISTER"},
			"Via":     {"SIP/2.0/UDP 127.0.0.1:5000;branch=z9hG4bK-test"},
		},
	}

	ok, challenge := s.authenticateRequest(req, "1001")
	if ok {
		t.Fatal("authenticateRequest returned ok=true, want challenge")
	}
	if !strings.Contains(challenge, "SIP/2.0 401 Unauthorized") || !strings.Contains(challenge, "WWW-Authenticate: Digest") {
		t.Fatalf("unexpected challenge: %q", challenge)
	}
}
