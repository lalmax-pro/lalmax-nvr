package voip

import (
	"strings"
	"testing"
)

func TestParseRegisterRequest(t *testing.T) {
	raw := strings.Join([]string{
		"REGISTER sip:192.168.1.100 SIP/2.0",
		"Via: SIP/2.0/UDP 192.168.1.101:5060;branch=z9hG4bK-1234",
		"From: <sip:alice@192.168.1.100>;tag=abc123",
		"To: <sip:alice@192.168.1.100>",
		"Call-ID: test-call-id-12345",
		"CSeq: 1 REGISTER",
		"Contact: <sip:alice@192.168.1.101:5060>",
		"Expires: 3600",
		"Content-Length: 0",
		"",
		"",
	}, "\r\n")

	msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !msg.IsRequest {
		t.Fatal("expected request")
	}
	if msg.Method != MethodRegister {
		t.Fatalf("expected REGISTER, got %s", msg.Method)
	}
	if msg.CallID() != "test-call-id-12345" {
		t.Fatalf("wrong call-id: %s", msg.CallID())
	}
	if ExtractUser(msg.From()) != "alice" {
		t.Fatalf("wrong from user: %s", ExtractUser(msg.From()))
	}
	if ExtractTag(msg.From()) != "abc123" {
		t.Fatalf("wrong from tag: %s", ExtractTag(msg.From()))
	}
	if ExtractCSeqNumber(msg.CSeq()) != 1 {
		t.Fatalf("wrong cseq number: %d", ExtractCSeqNumber(msg.CSeq()))
	}
	if ExtractCSeqMethod(msg.CSeq()) != MethodRegister {
		t.Fatalf("wrong cseq method: %s", ExtractCSeqMethod(msg.CSeq()))
	}
}

func TestParseInviteRequest(t *testing.T) {
	sdpBody := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.168.1.101",
		"s=-",
		"c=IN IP4 192.168.1.101",
		"t=0 0",
		"m=audio 40000 RTP/AVP 8",
		"a=rtpmap:8 PCMA/8000",
		"",
	}, "\r\n")

	raw := strings.Join([]string{
		"INVITE sip:bob@192.168.1.100 SIP/2.0",
		"Via: SIP/2.0/UDP 192.168.1.101:5060;branch=z9hG4bK-5678",
		"From: <sip:alice@192.168.1.100>;tag=from-tag-123",
		"To: <sip:bob@192.168.1.100>",
		"Call-ID: invite-call-id-67890",
		"CSeq: 1 INVITE",
		"Contact: <sip:alice@192.168.1.101:5060>",
		"Content-Type: application/sdp",
		"Content-Length: " + string(rune(len(sdpBody))),
		"",
		sdpBody,
	}, "\r\n")

	msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if msg.Method != MethodInvite {
		t.Fatalf("expected INVITE, got %s", msg.Method)
	}
	if msg.CallID() != "invite-call-id-67890" {
		t.Fatalf("wrong call-id: %s", msg.CallID())
	}
	if ExtractUser(msg.From()) != "alice" {
		t.Fatalf("wrong from user: %s", ExtractUser(msg.From()))
	}
	if ExtractUser(msg.To()) != "bob" {
		t.Fatalf("wrong to user: %s", ExtractUser(msg.To()))
	}
	if !strings.Contains(msg.Body, "m=audio") {
		t.Fatalf("missing SDP body")
	}
}

func TestBuildResponse(t *testing.T) {
	req := &Message{
		IsRequest:  true,
		Method:     MethodRegister,
		SipVersion: "SIP/2.0",
		Headers:    make(map[string][]string),
	}
	req.AddHeader("Via", "SIP/2.0/UDP 192.168.1.101:5060;branch=z9hG4bK-1234")
	req.AddHeader("From", "<sip:alice@192.168.1.100>;tag=abc123")
	req.AddHeader("To", "<sip:alice@192.168.1.100>")
	req.AddHeader("Call-ID", "test-call-id")
	req.AddHeader("CSeq", "1 REGISTER")

	resp := BuildResponse(req, 200, "OK", "")

	if !strings.Contains(resp, "SIP/2.0 200 OK") {
		t.Fatal("missing status line")
	}
	if !strings.Contains(resp, "Call-ID: test-call-id") {
		t.Fatal("missing Call-ID")
	}
	if !strings.Contains(resp, "CSeq: 1 REGISTER") {
		t.Fatal("missing CSeq")
	}
	if !strings.Contains(resp, "tag=") {
		t.Fatal("missing To tag in 200 response")
	}
}

func TestIsSIPKeepalive(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "empty", raw: "", want: true},
		{name: "single crlf", raw: "\r\n", want: true},
		{name: "double crlf", raw: "\r\n\r\n", want: true},
		{name: "lf only", raw: "\n", want: true},
		{name: "request", raw: "REGISTER sip:127.0.0.1 SIP/2.0\r\n\r\n", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSIPKeepalive(tt.raw); got != tt.want {
				t.Fatalf("isSIPKeepalive(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestExtractUser(t *testing.T) {
	tests := []struct {
		header   string
		expected string
	}{
		{"<sip:alice@192.168.1.100>", "alice"},
		{"<sip:bob@domain.com>;tag=123", "bob"},
		{"sip:charlie@example.org", "charlie"},
		{"Alice <sip:alice123@test.com>", "alice123"},
		{"<sip:1001@192.168.1.1:5060>", "1001"},
	}

	for _, tt := range tests {
		got := ExtractUser(tt.header)
		if got != tt.expected {
			t.Errorf("ExtractUser(%q) = %q, want %q", tt.header, got, tt.expected)
		}
	}
}

func TestExtractTag(t *testing.T) {
	tests := []struct {
		header   string
		expected string
	}{
		{"<sip:alice@192.168.1.100>;tag=abc123", "abc123"},
		{"<sip:bob@domain.com>;tag=xyz789;param=value", "xyz789"},
		{"<sip:charlie@example.org>", ""},
		{"Alice <sip:alice@test.com>;tag=12345", "12345"},
	}

	for _, tt := range tests {
		got := ExtractTag(tt.header)
		if got != tt.expected {
			t.Errorf("ExtractTag(%q) = %q, want %q", tt.header, got, tt.expected)
		}
	}
}
