package voip

import "testing"

func TestParseViaTransportTLS(t *testing.T) {
	got := ParseViaTransport("SIP/2.0/TLS 127.0.0.1:5061;branch=z9hG4bK-test")
	if got != TransportTLS {
		t.Fatalf("ParseViaTransport returned %s, want %s", got, TransportTLS)
	}
}

func TestParseViaTransportDTLSUDP(t *testing.T) {
	got := ParseViaTransport("SIP/2.0/DTLS-UDP 127.0.0.1:5061;branch=z9hG4bK-test")
	if got != TransportDTLS {
		t.Fatalf("ParseViaTransport returned %s, want %s", got, TransportDTLS)
	}
}
