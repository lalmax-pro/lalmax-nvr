package sdp

import (
	"fmt"
	"net"
	"strings"
)

// TalkCodecs returns browser-compatible codecs in preference order. SIP uses its
// own payload numbers; the bridge rewrites headers without decoding audio.
func TalkCodecs(browserOffer string) ([]Payload, error) {
	sections, err := parseOffer(browserOffer)
	if err != nil {
		return nil, err
	}
	var out []Payload
	for _, name := range []string{"opus", "PCMA", "PCMU"} {
		for _, sec := range sections {
			if sec.Kind != "audio" || sec.Port == 0 || sec.Direction == "recvonly" || sec.Direction == "inactive" {
				continue
			}
			for _, pt := range sec.Payloads {
				p := sec.Attrs[pt]
				if strings.EqualFold(p.CodecName, name) && ((name == "opus" && p.ClockRate == 48000) || (name != "opus" && p.ClockRate == 8000)) {
					selected := sec
					selected.Payloads = []int{pt}
					q, _ := chooseAudio(selected)
					if q != nil {
						q.Fmtp = ""
						switch name {
						case "opus":
							q.PayloadType = 111
						case "PCMA":
							q.PayloadType = 8
						case "PCMU":
							q.PayloadType = 0
						}
						out = append(out, *q)
					}
					break
				}
			}
			if len(out) > 0 && strings.EqualFold(out[len(out)-1].CodecName, name) {
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("talk requires Opus, PCMA or PCMU microphone audio")
	}
	return out, nil
}

func BuildTalkOffer(codecs []Payload, opt AnswerOptions, security string) string {
	protocol := "RTP/AVP"
	if security == "sdes" {
		protocol = "RTP/SAVP"
	}
	if security == "dtls" {
		protocol = "UDP/TLS/RTP/SAVP"
	}
	pts := []string{}
	for _, p := range codecs {
		pts = append(pts, fmt.Sprint(p.PayloadType))
	}
	pts = append(pts, "101") // telephone-event/8000 for browser keypad DTMF
	lines := []string{"v=0", fmt.Sprintf("o=lalmax-nvr %d 1 IN IP4 %s", opt.SessionID, opt.MediaIP), "s=lalmax-nvr talk", "c=IN IP4 " + opt.MediaIP, "t=0 0", fmt.Sprintf("m=audio %d %s %s", opt.AudioPort, protocol, strings.Join(pts, " ")), "a=sendrecv", fmt.Sprintf("a=rtcp:%d IN IP4 %s", opt.AudioRTCPPort, opt.MediaIP), "a=ptime:20"}
	for _, p := range codecs {
		mapping := fmt.Sprintf("a=rtpmap:%d %s/%d", p.PayloadType, p.CodecName, p.ClockRate)
		if strings.EqualFold(p.CodecName, "opus") {
			mapping += "/2"
		}
		lines = append(lines, mapping)
	}
	lines = append(lines, "a=rtpmap:101 telephone-event/8000", "a=fmtp:101 0-16")
	if security == "sdes" {
		lines = append(lines, opt.AudioCrypto)
	}
	if security == "dtls" {
		lines = append(lines, "a=setup:actpass", "a=fingerprint:"+opt.DTLSFingerprint)
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}

// ValidateTalkAnswer rejects changes to offered payloads, security or topology.
// A sendrecv audio leg is required for full-duplex talk.
func ValidateTalkAnswer(raw string, codecs []Payload, security string) (Negotiated, string, error) {
	sections, err := parseOffer(raw)
	if err != nil {
		return Negotiated{}, "", err
	}
	if len(sections) != 1 || sections[0].Kind != "audio" {
		return Negotiated{}, "", fmt.Errorf("expected one audio answer")
	}
	sec := sections[0]
	protocol := "RTP/AVP"
	if security == "sdes" {
		protocol = "RTP/SAVP"
	}
	if security == "dtls" {
		protocol = "UDP/TLS/RTP/SAVP"
	}
	if sec.Port <= 0 || sec.Port > 65535 || sec.Protocol != protocol || sec.ZRTPHash != "" || sec.RTCPMux || (sec.Direction != "sendrecv" && sec.Direction != "") || net.ParseIP(sec.Address).To4() == nil || sec.Address == "0.0.0.0" {
		return Negotiated{}, "", fmt.Errorf("terminal did not accept full-duplex audio with offered security")
	}
	allowed := map[int]Payload{}
	for _, p := range codecs {
		allowed[p.PayloadType] = p
	}
	var dtmf *Payload
	for _, pt := range sec.Payloads {
		if pt == 101 {
			got := sec.Attrs[pt]
			if !strings.EqualFold(got.CodecName, "telephone-event") || got.ClockRate != 8000 {
				return Negotiated{}, "", fmt.Errorf("answer changed telephone-event mapping")
			}
			selected := Payload{PayloadType: pt, CodecName: "telephone-event", ClockRate: got.ClockRate}
			dtmf = &selected
			continue
		}
		p, ok := allowed[pt]
		if !ok {
			return Negotiated{}, "", fmt.Errorf("answer selected unoffered payload %d", pt)
		}
		got := sec.Attrs[pt]
		if got.CodecName != "" && (!strings.EqualFold(got.CodecName, p.CodecName) || got.ClockRate != p.ClockRate) {
			return Negotiated{}, "", fmt.Errorf("answer changed payload mapping")
		}
	}
	audio, _ := chooseAudio(sec)
	if audio == nil {
		return Negotiated{}, "", fmt.Errorf("no compatible audio")
	}
	// Recover static payload metadata when rtpmap is omitted.
	p := allowed[audio.PayloadType]
	audio = &p
	n := Negotiated{Audio: audio, DTMF: dtmf, AudioRTCPAddress: remoteRTCPAddress(sec)}
	if security == "sdes" {
		c, ok := selectSupportedCrypto(sec.Crypto)
		if !ok || c.Tag != 1 || c.Suite != "AES_CM_128_HMAC_SHA1_80" {
			return Negotiated{}, "", fmt.Errorf("invalid SDES answer")
		}
		n.AudioSecure = true
		n.AudioCrypto = c.Line
		n.AudioCryptoTag = c.Tag
		n.AudioCryptoSuite = c.Suite
	}
	if security == "dtls" {
		if sec.Fingerprint == "" || (sec.Setup != "active" && sec.Setup != "passive") {
			return Negotiated{}, "", fmt.Errorf("invalid DTLS answer")
		}
		n.AudioSecure = true
		n.AudioDTLS = true
		n.AudioFingerprint = sec.Fingerprint
		n.AudioSetup = sec.Setup
	}
	return n, net.JoinHostPort(sec.Address, fmt.Sprint(sec.Port)), nil
}

func ValidateBrowserTalk(offer string, selected Payload) error {
	sections, err := parseOffer(offer)
	if err != nil {
		return err
	}
	if len(sections) != 1 || sections[0].Kind != "audio" || (sections[0].Direction != "sendrecv" && sections[0].Direction != "") {
		return fmt.Errorf("browser must offer full-duplex audio only")
	}
	codecs, err := TalkCodecs(offer)
	if err != nil {
		return err
	}
	for _, p := range codecs {
		if strings.EqualFold(p.CodecName, selected.CodecName) && p.ClockRate == selected.ClockRate {
			return nil
		}
	}
	return fmt.Errorf("browser does not support the selected terminal codec")
}
