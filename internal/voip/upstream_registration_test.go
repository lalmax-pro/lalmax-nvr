package voip

import (
	"net"
	"strings"
	"testing"
	"time"
)

func readPBXMessage(t *testing.T, conn *net.UDPConn) (*Message, *net.UDPAddr) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8192)
	n, addr, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseMessage(string(buf[:n]))
	if err != nil {
		t.Fatalf("parse NVR request: %v", err)
	}
	return m, addr
}

func sendPBXResponse(t *testing.T, conn *net.UDPConn, peer *net.UDPAddr, raw string) {
	t.Helper()
	if _, err := conn.WriteToUDP([]byte(raw), peer); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamPBXDigestRegistrationAndExtensionDial(t *testing.T) {
	pbx, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pbx.Close()
	pbxAddr := pbx.LocalAddr().String()
	s, err := NewServer(ServerConfig{Config: Config{SipListenAddr: "127.0.0.1:0", SipIP: "127.0.0.1", MediaIP: "127.0.0.1", MediaPortMin: 43000, MediaPortMax: 43020,
		PbxServer: pbxAddr, PbxDomain: "pbx.example.test", PbxUsername: "nvr-account", PbxPassword: "pbx-secret", PbxRegisterExpires: 600}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	register, peer := readPBXMessage(t, pbx)
	if register.Method != MethodRegister || register.GetHeader("Authorization") != "" {
		t.Fatalf("expected initial unauthenticated REGISTER, got %+v", register)
	}
	challenge := insertHeaderBeforeContentLength(BuildResponse(register, 401, "Unauthorized", ""), "WWW-Authenticate", `Digest realm="pbx.example.test", nonce="register-nonce", qop="auth", algorithm=MD5`)
	sendPBXResponse(t, pbx, peer, challenge)

	register, peer = readPBXMessage(t, pbx)
	params := parseDigestAuthorization(register.GetHeader("Authorization"))
	if params == nil || params["username"] != "nvr-account" || params["uri"] != "sip:pbx.example.test" {
		t.Fatalf("missing authenticated REGISTER: %q", register.GetHeader("Authorization"))
	}
	if got, want := params["response"], expectedDigestResponse("REGISTER", "pbx.example.test", "pbx-secret", params); got != want {
		t.Fatalf("REGISTER digest mismatch: got %s want %s", got, want)
	}
	sendPBXResponse(t, pbx, peer, insertHeaderBeforeContentLength(BuildResponse(register, 200, "OK", ""), "Expires", "600"))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && s.Status().UpstreamRegistration.State != "registered" {
		time.Sleep(10 * time.Millisecond)
	}
	if state := s.Status().UpstreamRegistration.State; state != "registered" {
		t.Fatalf("PBX did not register: %+v", s.Status().UpstreamRegistration)
	}

	call, err := s.Dial("204", "rtp", browserTalkOffer)
	if err != nil {
		t.Fatal(err)
	}
	invite, peer := readPBXMessage(t, pbx)
	if invite.Method != MethodInvite || invite.RequestURI != "sip:204@pbx.example.test" {
		t.Fatalf("wrong extension INVITE: %s %s", invite.Method, invite.RequestURI)
	}
	challenge = insertHeaderBeforeContentLength(BuildResponseWithContact(invite, 407, "Proxy Authentication Required", "", "pbx", ""), "Proxy-Authenticate", `Digest realm="pbx.example.test", nonce="invite-nonce", qop="auth", algorithm=MD5`)
	sendPBXResponse(t, pbx, peer, challenge)
	if ack, _ := readPBXMessage(t, pbx); ack.Method != MethodAck {
		t.Fatalf("expected ACK for non-2xx challenge, got %s", ack.Method)
	}
	invite, peer = readPBXMessage(t, pbx)
	params = parseDigestAuthorization(invite.GetHeader("Proxy-Authorization"))
	if invite.Method != MethodInvite || params == nil || params["username"] != "nvr-account" {
		t.Fatalf("expected authenticated retry INVITE, request=%+v", invite)
	}
	if got, want := params["response"], expectedDigestResponse("INVITE", params["realm"], "pbx-secret", params); got != want {
		t.Fatalf("INVITE digest mismatch: got %s want %s params=%+v requestURI=%q auth=%q", got, want, params, invite.RequestURI, invite.GetHeader("Proxy-Authorization"))
	}
	if invite.CSeq() == register.CSeq() {
		t.Fatalf("INVITE retry did not advance CSeq: %s", invite.CSeq())
	}
	sendPBXResponse(t, pbx, peer, BuildResponseWithContact(invite, 486, "Busy Here", "", "pbx", ""))
	if ack, _ := readPBXMessage(t, pbx); ack.Method != MethodAck {
		t.Fatalf("expected ACK after busy response, got %s", ack.Method)
	}
	for _, c := range s.Status().Calls {
		if c.CallID == call.CallID && c.State != "failed" {
			t.Fatalf("busy response should end the outbound call as failed: %+v", c)
		}
	}
}

func TestPBXConfigValidation(t *testing.T) {
	for _, cfg := range []Config{
		{PbxServer: "pbx.example.test:5060"},
		{PbxServer: "pbx.example.test", PbxUsername: "nvr", PbxPassword: "secret"},
		{PbxServer: "pbx.example.test:5060", PbxUsername: "nvr", PbxPassword: "secret", PbxTransport: "sctp"},
		{PbxServer: "pbx.example.test:5060", PbxUsername: "nvr", PbxPassword: "secret", PbxTransport: "tcp", PbxTLSCAFile: "ca.pem"},
		{PbxServer: "pbx.example.test:70000", PbxUsername: "nvr", PbxPassword: "secret"},
	} {
		cfg.Normalize()
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected invalid PBX config: %+v", cfg)
		}
	}
	cfg := Config{PbxServer: "pbx.example.test:5060", PbxUsername: "nvr", PbxPassword: "secret"}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid PBX config rejected: %v", err)
	}
	if cfg.PbxDomain != "pbx.example.test" || !strings.Contains(cfg.PbxServer, ":5060") {
		t.Fatalf("PBX defaults not normalized: %+v", cfg)
	}
	if cfg.PbxTransport != "udp" {
		t.Fatalf("PBX transport default = %q, want udp", cfg.PbxTransport)
	}
	cfg.PbxTransport = " TCP "
	cfg.Normalize()
	if err := cfg.Validate(); err != nil || cfg.PbxTransport != "tcp" {
		t.Fatalf("PBX transport normalization failed: transport=%q err=%v", cfg.PbxTransport, err)
	}
}
