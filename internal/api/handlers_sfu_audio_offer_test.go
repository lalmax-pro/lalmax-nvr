package api

import (
	"strings"
	"testing"
)

func TestSFUOfferSupportsOpus(t *testing.T) {
	base := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111\r\nc=IN IP4 0.0.0.0\r\na=rtpmap:111 opus/48000/2\r\na=recvonly\r\n"
	if !sfuOfferSupportsOpus([]byte(base)) {
		t.Fatal("valid receive-only Opus offer rejected")
	}
	for _, invalid := range []string{
		strings.Replace(base, "a=recvonly", "a=sendonly", 1),
		strings.Replace(base, "a=recvonly", "a=inactive", 1),
		strings.Replace(base, "m=audio 9", "m=audio 0", 1),
		strings.Replace(base, "SAVPF 111", "SAVPF 112", 1),
		strings.Replace(base, "opus/48000/2", "opus/16000/1", 1),
		"not SDP",
	} {
		if sfuOfferSupportsOpus([]byte(invalid)) {
			t.Fatalf("accepted invalid offer: %q", invalid)
		}
	}
}
