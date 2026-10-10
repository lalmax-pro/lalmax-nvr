package sdp

import (
	"errors"
	"strings"
	"testing"
)

func TestNegotiateAudioVideoTelephoneEvent(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"m=audio 40000 RTP/AVP 0 8 101",
		"a=rtpmap:0 PCMU/8000",
		"a=rtpmap:8 PCMA/8000",
		"a=rtpmap:101 telephone-event/8000",
		"a=fmtp:101 0-16",
		"m=video 40002 RTP/AVP 96",
		"a=rtpmap:96 H264/90000",
		"a=fmtp:96 packetization-mode=1;profile-level-id=42e01f",
		"",
	}, "\r\n")

	answer, negotiated, err := NegotiateOffer(offer, AnswerOptions{
		MediaIP:   "198.51.100.20",
		AudioPort: 41000,
		VideoPort: 41002,
	})
	if err != nil {
		t.Fatalf("NegotiateOffer error: %v", err)
	}
	if negotiated.Audio == nil || negotiated.Audio.PayloadType != 8 {
		t.Fatalf("expected PCMA audio, got %+v", negotiated.Audio)
	}
	if negotiated.Video == nil || negotiated.Video.PayloadType != 96 {
		t.Fatalf("expected H264 video, got %+v", negotiated.Video)
	}
	if negotiated.DTMF == nil || negotiated.DTMF.PayloadType != 101 {
		t.Fatalf("expected telephone-event, got %+v", negotiated.DTMF)
	}
	for _, want := range []string{
		"c=IN IP4 198.51.100.20",
		"m=audio 41000 RTP/AVP 8 101",
		"a=rtpmap:8 PCMA/8000",
		"a=recvonly",
		"m=video 41002 RTP/AVP 96",
		"a=rtpmap:96 H264/90000",
	} {
		if !strings.Contains(answer, want) {
			t.Fatalf("answer missing %q:\n%s", want, answer)
		}
	}
}

func TestNegotiateSingleAudioCodecUsesOfferedCodec(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"m=audio 40000 RTP/AVP 0 101",
		"a=rtpmap:0 PCMU/8000",
		"a=rtpmap:101 telephone-event/8000",
		"",
	}, "\r\n")

	answer, negotiated, err := NegotiateOffer(offer, AnswerOptions{
		MediaIP:   "198.51.100.20",
		AudioPort: 41000,
	})
	if err != nil {
		t.Fatalf("NegotiateOffer error: %v", err)
	}
	if negotiated.Audio == nil || negotiated.Audio.PayloadType != 0 || negotiated.Audio.CodecName != "PCMU" {
		t.Fatalf("expected PCMU audio, got %+v", negotiated.Audio)
	}
	for _, want := range []string{
		"m=audio 41000 RTP/AVP 0 101",
		"a=rtpmap:0 PCMU/8000",
	} {
		if !strings.Contains(answer, want) {
			t.Fatalf("answer missing %q:\n%s", want, answer)
		}
	}
}

func TestNegotiateVideoPrefersH264ThenAV1ThenHEVC(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"m=video 40002 RTP/AVP 96 97 98",
		"a=rtpmap:96 AV1/90000",
		"a=rtpmap:97 H265/90000",
		"a=rtpmap:98 H264/90000",
		"a=fmtp:98 packetization-mode=1;profile-level-id=42e01f",
		"",
	}, "\r\n")

	answer, negotiated, err := NegotiateOffer(offer, AnswerOptions{
		MediaIP:   "198.51.100.20",
		VideoPort: 41002,
	})
	if err != nil {
		t.Fatalf("NegotiateOffer error: %v", err)
	}
	if negotiated.Video == nil || negotiated.Video.PayloadType != 98 || negotiated.Video.CodecName != "H264" {
		t.Fatalf("expected H264 video, got %+v", negotiated.Video)
	}
	for _, want := range []string{
		"m=video 41002 RTP/AVP 98",
		"a=rtpmap:98 H264/90000",
		"a=fmtp:98 packetization-mode=1;profile-level-id=42e01f",
	} {
		if !strings.Contains(answer, want) {
			t.Fatalf("answer missing %q:\n%s", want, answer)
		}
	}
}

func TestNegotiateVideoH265WhenUnsupportedAV1Offered(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"m=video 40002 RTP/AVP 96 97",
		"a=rtpmap:96 AV1/90000",
		"a=rtpmap:97 H265/90000",
		"",
	}, "\r\n")

	answer, negotiated, err := NegotiateOffer(offer, AnswerOptions{
		MediaIP:   "198.51.100.20",
		VideoPort: 41002,
	})
	if err != nil {
		t.Fatalf("NegotiateOffer error: %v", err)
	}
	if negotiated.Video == nil || negotiated.Video.PayloadType != 97 || negotiated.Video.CodecName != "H265" {
		t.Fatalf("expected H265 video, got %+v", negotiated.Video)
	}
	for _, want := range []string{
		"m=video 41002 RTP/AVP 97",
		"a=rtpmap:97 H265/90000",
	} {
		if !strings.Contains(answer, want) {
			t.Fatalf("answer missing %q:\n%s", want, answer)
		}
	}
}

func TestNegotiateSingleVideoCodecUsesOfferedCodec(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"m=video 40002 RTP/AVP 97",
		"a=rtpmap:97 H265/90000",
		"",
	}, "\r\n")

	answer, negotiated, err := NegotiateOffer(offer, AnswerOptions{
		MediaIP:   "198.51.100.20",
		VideoPort: 41002,
	})
	if err != nil {
		t.Fatalf("NegotiateOffer error: %v", err)
	}
	if negotiated.Video == nil || negotiated.Video.PayloadType != 97 || negotiated.Video.CodecName != "H265" {
		t.Fatalf("expected H265 video, got %+v", negotiated.Video)
	}
	for _, want := range []string{
		"m=video 41002 RTP/AVP 97",
		"a=rtpmap:97 H265/90000",
	} {
		if !strings.Contains(answer, want) {
			t.Fatalf("answer missing %q:\n%s", want, answer)
		}
	}
}

func TestNegotiateRejectsUnsupportedOffer(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"m=audio 40000 RTP/AVP 111",
		"a=rtpmap:111 unsupported/8000",
		"",
	}, "\r\n")
	_, _, err := NegotiateOffer(offer, AnswerOptions{MediaIP: "198.51.100.20", AudioPort: 41000})
	if !errors.Is(err, ErrUnsupportedSDP) {
		t.Fatalf("expected unsupported offer error, got %v", err)
	}
}

func TestNegotiateMixedUnsupportedAudioSupportedVideoRejectsAudioWithPayload(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"m=audio 40000 RTP/AVP 111",
		"a=rtpmap:111 unsupported/8000",
		"m=video 40002 RTP/AVP 96",
		"a=rtpmap:96 H264/90000",
		"",
	}, "\r\n")

	answer, negotiated, err := NegotiateOffer(offer, AnswerOptions{
		MediaIP:   "198.51.100.20",
		AudioPort: 41000,
		VideoPort: 41002,
	})
	if err != nil {
		t.Fatalf("NegotiateOffer error: %v", err)
	}
	if negotiated.Audio != nil {
		t.Fatalf("expected audio rejected, got %+v", negotiated.Audio)
	}
	if negotiated.Video == nil || negotiated.Video.PayloadType != 96 {
		t.Fatalf("expected H264 video, got %+v", negotiated.Video)
	}
	if !strings.Contains(answer, "m=audio 0 RTP/AVP 111") {
		t.Fatalf("answer missing valid rejected audio m-line:\n%s", answer)
	}
}

func TestNegotiateRejectsUnsupportedMediaKind(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"m=audio 40000 RTP/AVP 8",
		"a=rtpmap:8 PCMA/8000",
		"m=application 40004 RTP/AVP 101",
		"a=rtpmap:101 telephone-event/8000",
		"",
	}, "\r\n")

	answer, negotiated, err := NegotiateOffer(offer, AnswerOptions{
		MediaIP:   "198.51.100.20",
		AudioPort: 41000,
	})
	if err != nil {
		t.Fatalf("NegotiateOffer error: %v", err)
	}
	if negotiated.Audio == nil || negotiated.Audio.PayloadType != 8 {
		t.Fatalf("expected PCMA audio, got %+v", negotiated.Audio)
	}
	for _, want := range []string{
		"m=audio 41000 RTP/AVP 8",
		"m=application 0 RTP/AVP 101",
	} {
		if !strings.Contains(answer, want) {
			t.Fatalf("answer missing %q:\n%s", want, answer)
		}
	}
}

func TestNegotiateRejectsUnsupportedMediaWithNonNumericFormat(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"m=audio 40000 RTP/AVP 8",
		"a=rtpmap:8 PCMA/8000",
		"m=application 9 UDP/DTLS/SCTP webrtc-datachannel",
		"",
	}, "\r\n")

	answer, negotiated, err := NegotiateOffer(offer, AnswerOptions{
		MediaIP:   "198.51.100.20",
		AudioPort: 41000,
	})
	if err != nil {
		t.Fatalf("NegotiateOffer error: %v", err)
	}
	if negotiated.Audio == nil || negotiated.Audio.PayloadType != 8 {
		t.Fatalf("expected PCMA audio, got %+v", negotiated.Audio)
	}
	for _, want := range []string{
		"m=audio 41000 RTP/AVP 8",
		"m=application 0 UDP/DTLS/SCTP webrtc-datachannel",
	} {
		if !strings.Contains(answer, want) {
			t.Fatalf("answer missing %q:\n%s", want, answer)
		}
	}
}

func TestNegotiateRejectsUnsupportedProtocol(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"m=audio 40000 RTP/SAVP 8",
		"a=rtpmap:8 PCMA/8000",
		"",
	}, "\r\n")

	_, _, err := NegotiateOffer(offer, AnswerOptions{MediaIP: "198.51.100.20", AudioPort: 41000})
	if !errors.Is(err, ErrUnsupportedSDP) {
		t.Fatalf("expected unsupported protocol error, got %v", err)
	}
}

func TestNegotiateSrtpSelectsSupportedCrypto(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"m=audio 40000 RTP/SAVP 96 8 100",
		"a=rtpmap:96 opus/48000/2",
		"a=rtpmap:8 PCMA/8000",
		"a=rtpmap:100 telephone-event/8000",
		"a=crypto:1 AEAD_AES_128_GCM inline:Ez6U1P0YHtoDZ78EYilAXjP56TM0QefYeMsmdg==",
		"a=crypto:2 AES_CM_128_HMAC_SHA1_80 inline:ARqAKJx07WOBG64jDkzKsCEqaS5wZqoHdL0VvEYN",
		"a=rtcp-mux",
		"",
	}, "\r\n")

	answer, negotiated, err := NegotiateOffer(offer, AnswerOptions{
		MediaIP:     "198.51.100.20",
		AudioPort:   41000,
		SrtpEnable:  true,
		AudioCrypto: "a=crypto:2 AES_CM_128_HMAC_SHA1_80 inline:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	})
	if err != nil {
		t.Fatalf("NegotiateOffer error: %v", err)
	}
	if !negotiated.AudioSecure {
		t.Fatalf("expected secure audio negotiation")
	}
	if negotiated.AudioCryptoTag != 2 || negotiated.AudioCryptoSuite != "AES_CM_128_HMAC_SHA1_80" {
		t.Fatalf("unexpected crypto selection: tag=%d suite=%s line=%q",
			negotiated.AudioCryptoTag, negotiated.AudioCryptoSuite, negotiated.AudioCrypto)
	}
	if !negotiated.AudioRTCPMux {
		t.Fatalf("expected rtcp-mux negotiation")
	}
	for _, want := range []string{
		"m=audio 41000 RTP/SAVP 96 100",
		"a=rtcp-mux",
		"a=crypto:2 AES_CM_128_HMAC_SHA1_80 inline:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	} {
		if !strings.Contains(answer, want) {
			t.Fatalf("answer missing %q:\n%s", want, answer)
		}
	}
}

func TestNegotiateDTLSSrtpAudio(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"m=audio 54872 UDP/TLS/RTP/SAVP 8 101",
		"a=rtpmap:8 PCMA/8000",
		"a=rtpmap:101 telephone-event/8000",
		"a=setup:actpass",
		"a=fingerprint:SHA-256 11:22:33:44",
		"a=rtcp-mux",
		"",
	}, "\r\n")

	answer, negotiated, err := NegotiateOffer(offer, AnswerOptions{
		MediaIP:         "198.51.100.20",
		AudioPort:       41000,
		SrtpEnable:      true,
		DTLSFingerprint: "SHA-256 AA:BB:CC:DD",
	})
	if err != nil {
		t.Fatalf("NegotiateOffer error: %v", err)
	}
	if !negotiated.AudioSecure || !negotiated.AudioDTLS {
		t.Fatalf("expected DTLS-SRTP audio negotiation")
	}
	if negotiated.AudioFingerprint != "SHA-256 11:22:33:44" {
		t.Fatalf("unexpected remote fingerprint: %q", negotiated.AudioFingerprint)
	}
	for _, want := range []string{
		"m=audio 41000 UDP/TLS/RTP/SAVP 8 101",
		"a=setup:passive",
		"a=fingerprint:SHA-256 AA:BB:CC:DD",
		"a=rtcp-mux",
	} {
		if !strings.Contains(answer, want) {
			t.Fatalf("answer missing %q:\n%s", want, answer)
		}
	}
}

func TestNegotiateDTLSSrtpBundleAudioVideo(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"a=group:BUNDLE as vs",
		"m=audio 54872 UDP/TLS/RTP/SAVP 96 100",
		"a=rtpmap:96 opus/48000/2",
		"a=rtpmap:100 telephone-event/8000",
		"a=setup:actpass",
		"a=fingerprint:SHA-256 11:22:33:44",
		"a=rtcp-mux",
		"a=mid:as",
		"m=video 54874 UDP/TLS/RTP/SAVP 98",
		"a=rtpmap:98 H264/90000",
		"a=setup:actpass",
		"a=fingerprint:SHA-256 11:22:33:44",
		"a=rtcp-mux",
		"a=mid:vs",
		"",
	}, "\r\n")

	answer, negotiated, err := NegotiateOffer(offer, AnswerOptions{
		MediaIP:         "198.51.100.20",
		AudioPort:       41000,
		VideoPort:       41002,
		SrtpEnable:      true,
		BundleEnable:    true,
		DTLSFingerprint: "SHA-256 AA:BB:CC:DD",
	})
	if err != nil {
		t.Fatalf("NegotiateOffer error: %v", err)
	}
	if !negotiated.Bundle {
		t.Fatalf("expected bundled negotiation")
	}
	for _, want := range []string{
		"a=group:BUNDLE as vs",
		"m=audio 41000 UDP/TLS/RTP/SAVP 96 100",
		"a=mid:as",
		"m=video 41000 UDP/TLS/RTP/SAVP 98",
		"a=mid:vs",
	} {
		if !strings.Contains(answer, want) {
			t.Fatalf("answer missing %q:\n%s", want, answer)
		}
	}
}

func TestNegotiateDTLSSrtpBundleDisabledUsesSeparatePorts(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"a=group:BUNDLE as vs",
		"m=audio 54872 UDP/TLS/RTP/SAVP 96",
		"a=rtpmap:96 opus/48000/2",
		"a=setup:actpass",
		"a=fingerprint:SHA-256 11:22:33:44",
		"a=rtcp-mux",
		"a=mid:as",
		"m=video 54874 UDP/TLS/RTP/SAVP 98",
		"a=rtpmap:98 H264/90000",
		"a=setup:actpass",
		"a=fingerprint:SHA-256 11:22:33:44",
		"a=rtcp-mux",
		"a=mid:vs",
		"",
	}, "\r\n")

	answer, negotiated, err := NegotiateOffer(offer, AnswerOptions{
		MediaIP:         "198.51.100.20",
		AudioPort:       41000,
		VideoPort:       41002,
		SrtpEnable:      true,
		DTLSFingerprint: "SHA-256 AA:BB:CC:DD",
	})
	if err != nil {
		t.Fatalf("NegotiateOffer error: %v", err)
	}
	if negotiated.Bundle {
		t.Fatalf("did not expect bundled negotiation")
	}
	if strings.Contains(answer, "a=group:BUNDLE") {
		t.Fatalf("did not expect BUNDLE answer:\n%s", answer)
	}
	if !strings.Contains(answer, "m=video 41002 UDP/TLS/RTP/SAVP 98") {
		t.Fatalf("expected separate video port:\n%s", answer)
	}
}

func TestNegotiateRejectsZRTPHash(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"m=audio 40000 RTP/AVP 8",
		"a=rtpmap:8 PCMA/8000",
		"a=zrtp-hash:1.10 1c58b08940f11a04aaf70ad62af3c8453d2a5d09660778ec9c95e561971cbf15",
		"",
	}, "\r\n")

	_, _, err := NegotiateOffer(offer, AnswerOptions{
		MediaIP:   "198.51.100.20",
		AudioPort: 41000,
	})
	if !errors.Is(err, ErrUnsupportedZRTP) {
		t.Fatalf("expected unsupported zrtp error, got %v", err)
	}
}

func TestNegotiateSrtpRejectsUnsupportedCrypto(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"m=audio 40000 RTP/SAVP 8",
		"a=rtpmap:8 PCMA/8000",
		"a=crypto:1 AEAD_AES_128_GCM inline:Ez6U1P0YHtoDZ78EYilAXjP56TM0QefYeMsmdg==",
		"",
	}, "\r\n")

	_, _, err := NegotiateOffer(offer, AnswerOptions{
		MediaIP:    "198.51.100.20",
		AudioPort:  41000,
		SrtpEnable: true,
	})
	if !errors.Is(err, ErrUnsupportedSDP) {
		t.Fatalf("expected unsupported crypto error, got %v", err)
	}
}

func TestNegotiateHeldAudio(t *testing.T) {
	for _, direction := range []string{"recvonly", "inactive"} {
		t.Run(direction, func(t *testing.T) {
			offer := strings.Join([]string{
				"v=0",
				"o=- 1 1 IN IP4 192.0.2.10",
				"s=-",
				"c=IN IP4 192.0.2.10",
				"t=0 0",
				"m=audio 40000 RTP/AVP 8",
				"a=rtpmap:8 PCMA/8000",
				"a=" + direction,
				"",
			}, "\r\n")

			answer, negotiated, err := NegotiateOffer(offer, AnswerOptions{
				MediaIP:   "198.51.100.20",
				AudioPort: 41000,
			})
			if err != nil || negotiated.Audio == nil || !negotiated.Held() || !strings.Contains(answer, "a=inactive") {
				t.Fatalf("held negotiation: %v %+v %s", err, negotiated, answer)
			}
		})
	}
}

func TestNegotiateHeldSession(t *testing.T) {
	for _, direction := range []string{"recvonly", "inactive"} {
		t.Run(direction, func(t *testing.T) {
			offer := strings.Join([]string{
				"v=0",
				"o=- 1 1 IN IP4 192.0.2.10",
				"s=-",
				"c=IN IP4 192.0.2.10",
				"t=0 0",
				"a=" + direction,
				"m=audio 40000 RTP/AVP 8",
				"a=rtpmap:8 PCMA/8000",
				"",
			}, "\r\n")

			answer, negotiated, err := NegotiateOffer(offer, AnswerOptions{
				MediaIP:   "198.51.100.20",
				AudioPort: 41000,
			})
			if err != nil || negotiated.Audio == nil || !negotiated.Held() || !strings.Contains(answer, "a=inactive") {
				t.Fatalf("held negotiation: %v %+v %s", err, negotiated, answer)
			}
		})
	}
}

func TestNegotiateMediaDirectionOverridesSessionDirection(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"a=recvonly",
		"m=audio 40000 RTP/AVP 8",
		"a=sendonly",
		"a=rtpmap:8 PCMA/8000",
		"",
	}, "\r\n")

	_, negotiated, err := NegotiateOffer(offer, AnswerOptions{
		MediaIP:   "198.51.100.20",
		AudioPort: 41000,
	})
	if err != nil {
		t.Fatalf("NegotiateOffer error: %v", err)
	}
	if negotiated.Audio == nil || negotiated.Audio.PayloadType != 8 {
		t.Fatalf("expected media-level sendonly to negotiate PCMA, got %+v", negotiated.Audio)
	}
}

func TestNegotiateStaticPCMUWithoutRtpmap(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 192.0.2.10",
		"s=-",
		"c=IN IP4 192.0.2.10",
		"t=0 0",
		"m=audio 40000 RTP/AVP 0",
		"",
	}, "\r\n")

	answer, negotiated, err := NegotiateOffer(offer, AnswerOptions{
		MediaIP:   "198.51.100.20",
		AudioPort: 41000,
	})
	if err != nil {
		t.Fatalf("NegotiateOffer error: %v", err)
	}
	if negotiated.Audio == nil || negotiated.Audio.PayloadType != 0 || negotiated.Audio.CodecName != "PCMU" {
		t.Fatalf("expected static PCMU audio, got %+v", negotiated.Audio)
	}
	if !strings.Contains(answer, "a=rtpmap:0 PCMU/8000") {
		t.Fatalf("answer missing static PCMU rtpmap:\n%s", answer)
	}
}

func TestSanitizeStreamName(t *testing.T) {
	got := StreamName("alice+door", "abc/def:1234567890")
	want := "voip_alice-door_abc-def-1234"
	if got != want {
		t.Fatalf("stream name mismatch: got %q want %q", got, want)
	}
}
