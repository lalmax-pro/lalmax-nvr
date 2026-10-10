package sdp

import (
	"strings"
	"testing"
)

func TestTalkAnswerRejectsIncompatibleMedia(t *testing.T) {
	codecs := []Payload{{PayloadType: 8, CodecName: "PCMA", ClockRate: 8000}}
	answer := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 41000 RTP/AVP 8\r\na=sendrecv\r\na=rtpmap:8 PCMA/8000\r\n"
	if _, address, err := ValidateTalkAnswer(answer, codecs, "rtp"); err != nil || address != "127.0.0.1:41000" {
		t.Fatalf("valid answer: %s, %v", address, err)
	}
	telephoneEvent := strings.Replace(answer, "RTP/AVP 8", "RTP/AVP 8 101", 1) + "a=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-16\r\n"
	negotiated, _, err := ValidateTalkAnswer(telephoneEvent, codecs, "rtp")
	if err != nil || negotiated.DTMF == nil || negotiated.DTMF.PayloadType != 101 {
		t.Fatalf("telephone-event was not negotiated: %+v, %v", negotiated.DTMF, err)
	}
	offer := BuildTalkOffer(codecs, AnswerOptions{MediaIP: "127.0.0.1", AudioPort: 40000}, "rtp")
	if !strings.Contains(offer, "m=audio 40000 RTP/AVP 8 101") || !strings.Contains(offer, "telephone-event/8000") {
		t.Fatalf("outbound offer lacks DTMF capability: %s", offer)
	}
	cases := map[string]string{
		"held":                strings.Replace(answer, "sendrecv", "inactive", 1),
		"one direction":       strings.Replace(answer, "sendrecv", "sendonly", 1),
		"rejected track":      strings.Replace(answer, "audio 41000", "audio 0", 1),
		"invalid port":        strings.Replace(answer, "audio 41000", "audio 70000", 1),
		"changed encoding":    strings.Replace(answer, "PCMA/8000", "PCMU/8000", 1),
		"changed clock":       strings.Replace(answer, "PCMA/8000", "PCMA/48000", 1),
		"unoffered payload":   strings.Replace(answer, "RTP/AVP 8", "RTP/AVP 8 111", 1),
		"security change":     strings.Replace(answer, "RTP/AVP", "RTP/SAVP", 1),
		"rtcp mux":            answer + "a=rtcp-mux\r\n",
		"extra video":         answer + "m=video 41002 RTP/AVP 96\r\n",
		"unspecified address": strings.ReplaceAll(answer, "127.0.0.1", "0.0.0.0"),
		"ipv6":                strings.ReplaceAll(answer, "IP4 127.0.0.1", "IP6 ::1"),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ValidateTalkAnswer(raw, codecs, "rtp"); err == nil {
				t.Fatal("incompatible terminal answer accepted")
			}
		})
	}
	for _, security := range []string{"sdes", "dtls"} {
		if _, _, err := ValidateTalkAnswer(answer, codecs, security); err == nil {
			t.Fatalf("accepted plaintext downgrade for %s", security)
		}
	}
	if err := ValidateBrowserTalk(strings.Replace(answer, "sendrecv", "recvonly", 1), codecs[0]); err == nil {
		t.Fatal("browser without microphone accepted")
	}
}
