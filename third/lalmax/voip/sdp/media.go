package sdp

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"

	"github.com/q191201771/lal/pkg/base"
)

type AnswerOptions struct {
	SessionID       uint64
	SessionVersion  uint64
	MediaIP         string
	AudioPort       int
	VideoPort       int
	AudioRTCPPort   int
	VideoRTCPPort   int
	SrtpEnable      bool   // 是否启用SRTP
	BundleEnable    bool   // 是否启用RTP/RTCP BUNDLE
	DTLSFingerprint string // DTLS-SRTP本端证书fingerprint（a=fingerprint之后的值）
	AudioCrypto     string // audio的a=crypto属性（如果已生成）
	VideoCrypto     string // video的a=crypto属性（如果已生成）
}

type Payload struct {
	PayloadType int
	Codec       base.AvPacketPt
	CodecName   string
	ClockRate   int
	Fmtp        string
}

type Negotiated struct {
	AudioHeld        bool
	VideoHeld        bool
	Audio            *Payload
	Video            *Payload
	DTMF             *Payload
	AudioSecure      bool // Audio是否使用SRTP
	VideoSecure      bool // Video是否使用SRTP
	AudioRTCPMux     bool // Audio是否使用RTCP mux
	VideoRTCPMux     bool // Video是否使用RTCP mux
	AudioRTCPAddress string
	VideoRTCPAddress string
	VideoPLI         bool
	Bundle           bool   // Audio和Video是否使用同一个RTP/RTCP端口
	AudioMID         string // Audio BUNDLE MID
	VideoMID         string // Video BUNDLE MID
	AudioDTLS        bool   // Audio是否使用DTLS-SRTP
	VideoDTLS        bool   // Video是否使用DTLS-SRTP
	AudioSetup       string // Audio DTLS setup属性
	VideoSetup       string // Video DTLS setup属性
	AudioFingerprint string // Audio远端DTLS fingerprint
	VideoFingerprint string // Video远端DTLS fingerprint
	AudioCrypto      string // Audio选中的crypto参数（来自offer）
	VideoCrypto      string // Video选中的crypto参数（来自offer）
	AudioCryptoTag   int    // Audio选中的crypto tag
	VideoCryptoTag   int    // Video选中的crypto tag
	AudioCryptoSuite string // Audio选中的crypto suite
	VideoCryptoSuite string // Video选中的crypto suite
}

type mediaSection struct {
	Kind        string
	Port        int
	Protocol    string
	Formats     []string
	Payloads    []int
	Attrs       map[int]Payload
	Direction   string
	Crypto      []string // a=crypto 属性列表
	Setup       string   // a=setup
	Fingerprint string   // a=fingerprint
	MID         string   // a=mid
	Bundle      bool     // 是否在offer的a=group:BUNDLE中
	ZRTPHash    string   // a=zrtp-hash
	RTCPMux     bool     // a=rtcp-mux
	Address     string
	RTCPPort    int
	RTCPAddress string
	Feedback    map[int]bool
}

func NegotiateOffer(raw string, opt AnswerOptions) (string, Negotiated, error) {
	if opt.MediaIP == "" {
		opt.MediaIP = "127.0.0.1"
	}
	sections, err := parseOffer(raw)
	if err != nil {
		return "", Negotiated{}, err
	}
	for _, sec := range sections {
		if sec.ZRTPHash != "" {
			return "", Negotiated{}, ErrUnsupportedZRTP
		}
	}
	var negotiated Negotiated
	bundleOffered := offerHasAudioVideoBundle(sections)
	bundleRequested := opt.BundleEnable && bundleOffered
	var answerHeader []string
	answerHeader = append(answerHeader,
		"v=0",
		fmt.Sprintf("o=lalmax-nvr %d %d IN IP4 %s", opt.SessionID, opt.SessionVersion, opt.MediaIP),
		"s=lalmax-nvr voip",
		fmt.Sprintf("c=IN IP4 %s", opt.MediaIP),
		"t=0 0",
	)
	var mediaAnswer []string
	for _, sec := range sections {
		switch sec.Kind {
		case "audio":
			if !canNegotiate(sec) {
				mediaAnswer = append(mediaAnswer, rejectMediaLine(sec))
				continue
			}
			audio, dtmf := chooseAudio(sec)
			if audio == nil {
				mediaAnswer = append(mediaAnswer, rejectMediaLine(sec))
				continue
			}
			pts := []string{strconv.Itoa(audio.PayloadType)}
			if dtmf != nil {
				pts = append(pts, strconv.Itoa(dtmf.PayloadType))
			}

			// 确定协议：RTP/SAVP为SDES-SRTP，UDP/TLS/RTP/SAVP为DTLS-SRTP。
			protocol := "RTP/AVP"
			switch sec.Protocol {
			case "RTP/SAVP":
				if !opt.SrtpEnable {
					mediaAnswer = append(mediaAnswer, rejectMediaLine(sec))
					continue
				}
				crypto, ok := selectSupportedCrypto(sec.Crypto)
				if !ok {
					mediaAnswer = append(mediaAnswer, rejectMediaLine(sec))
					continue
				}
				protocol = "RTP/SAVP"
				negotiated.AudioSecure = true
				negotiated.AudioCrypto = crypto.Line
				negotiated.AudioCryptoTag = crypto.Tag
				negotiated.AudioCryptoSuite = crypto.Suite
			case "UDP/TLS/RTP/SAVP", "UDP/TLS/RTP/SAVPF":
				if !opt.SrtpEnable || opt.DTLSFingerprint == "" || sec.Fingerprint == "" || !canAnswerDTLSSetup(sec.Setup) {
					mediaAnswer = append(mediaAnswer, rejectMediaLine(sec))
					continue
				}
				protocol = sec.Protocol
				negotiated.AudioSecure = true
				negotiated.AudioDTLS = true
				negotiated.AudioSetup = sec.Setup
				negotiated.AudioFingerprint = sec.Fingerprint
			}
			negotiated.Audio = audio
			negotiated.AudioHeld = sec.Direction == "inactive" || sec.Direction == "recvonly" || sec.Address == "0.0.0.0"
			negotiated.DTMF = dtmf
			negotiated.AudioRTCPMux = sec.RTCPMux
			negotiated.AudioRTCPAddress = remoteRTCPAddress(sec)
			negotiated.AudioMID = sec.MID

			mediaAnswer = append(mediaAnswer,
				fmt.Sprintf("m=audio %d %s %s", opt.AudioPort, protocol, strings.Join(pts, " ")),
				fmt.Sprintf("a=rtpmap:%d %s/%d", audio.PayloadType, audio.CodecName, audio.ClockRate),
				answerDirection(sec),
			)
			if bundleRequested && sec.MID != "" {
				mediaAnswer = append(mediaAnswer, "a=mid:"+sec.MID)
			}
			if sec.RTCPMux {
				mediaAnswer = append(mediaAnswer, "a=rtcp-mux")
			} else if opt.AudioRTCPPort > 0 {
				mediaAnswer = append(mediaAnswer, fmt.Sprintf("a=rtcp:%d IN IP4 %s", opt.AudioRTCPPort, opt.MediaIP))
			}
			if negotiated.AudioDTLS {
				mediaAnswer = append(mediaAnswer,
					"a=setup:passive",
					"a=fingerprint:"+opt.DTLSFingerprint,
				)
			}

			// 添加 crypto 属性（如果是SRTP）
			if protocol == "RTP/SAVP" && opt.AudioCrypto != "" {
				mediaAnswer = append(mediaAnswer, opt.AudioCrypto)
			}

			// 添加 fmtp（如果有的话）
			if audio.Fmtp != "" {
				mediaAnswer = append(mediaAnswer, fmt.Sprintf("a=fmtp:%d %s", audio.PayloadType, audio.Fmtp))
			}
			if dtmf != nil {
				mediaAnswer = append(mediaAnswer,
					fmt.Sprintf("a=rtpmap:%d telephone-event/%d", dtmf.PayloadType, dtmf.ClockRate),
					fmt.Sprintf("a=fmtp:%d 0-16", dtmf.PayloadType),
				)
			}
		case "video":
			if !canNegotiate(sec) {
				mediaAnswer = append(mediaAnswer, rejectMediaLine(sec))
				continue
			}
			video := chooseVideo(sec)
			if video == nil {
				mediaAnswer = append(mediaAnswer, rejectMediaLine(sec))
				continue
			}

			// 确定协议：RTP/SAVP为SDES-SRTP，UDP/TLS/RTP/SAVP为DTLS-SRTP。
			protocol := "RTP/AVP"
			switch sec.Protocol {
			case "RTP/SAVP":
				if !opt.SrtpEnable {
					mediaAnswer = append(mediaAnswer, rejectMediaLine(sec))
					continue
				}
				crypto, ok := selectSupportedCrypto(sec.Crypto)
				if !ok {
					mediaAnswer = append(mediaAnswer, rejectMediaLine(sec))
					continue
				}
				protocol = "RTP/SAVP"
				negotiated.VideoSecure = true
				negotiated.VideoCrypto = crypto.Line
				negotiated.VideoCryptoTag = crypto.Tag
				negotiated.VideoCryptoSuite = crypto.Suite
			case "UDP/TLS/RTP/SAVP", "UDP/TLS/RTP/SAVPF":
				if !opt.SrtpEnable || opt.DTLSFingerprint == "" || sec.Fingerprint == "" || !canAnswerDTLSSetup(sec.Setup) {
					mediaAnswer = append(mediaAnswer, rejectMediaLine(sec))
					continue
				}
				protocol = sec.Protocol
				negotiated.VideoSecure = true
				negotiated.VideoDTLS = true
				negotiated.VideoSetup = sec.Setup
				negotiated.VideoFingerprint = sec.Fingerprint
			}
			negotiated.Video = video
			negotiated.VideoHeld = sec.Direction == "inactive" || sec.Direction == "recvonly" || sec.Address == "0.0.0.0"
			negotiated.VideoRTCPMux = sec.RTCPMux
			negotiated.VideoRTCPAddress = remoteRTCPAddress(sec)
			negotiated.VideoPLI = sec.Feedback[video.PayloadType] || sec.Feedback[-1]
			negotiated.VideoMID = sec.MID

			videoPort := opt.VideoPort
			if bundleRequested && negotiated.Audio != nil && negotiated.AudioDTLS && negotiated.VideoDTLS &&
				negotiated.AudioFingerprint == negotiated.VideoFingerprint {
				videoPort = opt.AudioPort
			}
			mediaAnswer = append(mediaAnswer,
				fmt.Sprintf("m=video %d %s %d", videoPort, protocol, video.PayloadType),
				fmt.Sprintf("a=rtpmap:%d %s/%d", video.PayloadType, video.CodecName, video.ClockRate),
				answerDirection(sec),
			)
			if video.Fmtp != "" {
				mediaAnswer = append(mediaAnswer, fmt.Sprintf("a=fmtp:%d %s", video.PayloadType, video.Fmtp))
			} else if video.Codec == base.AvPacketPtAvc {
				mediaAnswer = append(mediaAnswer, "a=fmtp:"+strconv.Itoa(video.PayloadType)+" packetization-mode=1")
			}
			if bundleRequested && sec.MID != "" {
				mediaAnswer = append(mediaAnswer, "a=mid:"+sec.MID)
			}
			if sec.RTCPMux {
				mediaAnswer = append(mediaAnswer, "a=rtcp-mux")
			} else if opt.VideoRTCPPort > 0 {
				mediaAnswer = append(mediaAnswer, fmt.Sprintf("a=rtcp:%d IN IP4 %s", opt.VideoRTCPPort, opt.MediaIP))
			}
			if negotiated.VideoPLI {
				mediaAnswer = append(mediaAnswer, fmt.Sprintf("a=rtcp-fb:%d nack pli", video.PayloadType))
			}
			if negotiated.VideoDTLS {
				mediaAnswer = append(mediaAnswer,
					"a=setup:passive",
					"a=fingerprint:"+opt.DTLSFingerprint,
				)
			}

			// 添加 crypto 属性（如果是SRTP）
			if protocol == "RTP/SAVP" && opt.VideoCrypto != "" {
				mediaAnswer = append(mediaAnswer, opt.VideoCrypto)
			}
		default:
			mediaAnswer = append(mediaAnswer, rejectMediaLine(sec))
		}
	}
	if negotiated.Audio == nil && negotiated.Video == nil {
		return "", Negotiated{}, ErrUnsupportedSDP
	}
	if bundleRequested && negotiated.Audio != nil && negotiated.Video != nil &&
		negotiated.AudioMID != "" && negotiated.VideoMID != "" &&
		negotiated.AudioRTCPMux && negotiated.VideoRTCPMux &&
		negotiated.AudioDTLS && negotiated.VideoDTLS &&
		negotiated.AudioFingerprint == negotiated.VideoFingerprint {
		negotiated.Bundle = true
		answerHeader = append(answerHeader, fmt.Sprintf("a=group:BUNDLE %s %s", negotiated.AudioMID, negotiated.VideoMID))
	}
	answer := append(answerHeader, mediaAnswer...)
	return strings.Join(answer, "\r\n") + "\r\n", negotiated, nil
}

func offerHasAudioVideoBundle(sections []mediaSection) bool {
	hasAudio := false
	hasVideo := false
	for _, sec := range sections {
		if !sec.Bundle {
			continue
		}
		switch sec.Kind {
		case "audio":
			hasAudio = true
		case "video":
			hasVideo = true
		}
	}
	return hasAudio && hasVideo
}

func remoteRTCPAddress(sec mediaSection) string {
	host, port := sec.Address, sec.RTCPPort
	if sec.RTCPMux {
		return ""
	} // Learn the symmetric RTP/RTCP peer on receipt.
	if sec.RTCPAddress != "" {
		host = sec.RTCPAddress
	}
	if port == 0 {
		port = sec.Port + 1
	}
	if ip := net.ParseIP(host); ip == nil || ip.To4() == nil || ip.IsUnspecified() || port > 65535 {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func canNegotiate(sec mediaSection) bool {
	// 支持 RTP/AVP (未加密)、RTP/SAVP (SDES-SRTP) 和 UDP/TLS/RTP/SAVP(F) (DTLS-SRTP)。
	if sec.Protocol != "RTP/AVP" &&
		sec.Protocol != "RTP/SAVP" &&
		sec.Protocol != "UDP/TLS/RTP/SAVP" &&
		sec.Protocol != "UDP/TLS/RTP/SAVPF" {
		return false
	}
	return sec.Port != 0 && (sec.Direction == "" || sec.Direction == "sendrecv" || sec.Direction == "sendonly" || sec.Direction == "recvonly" || sec.Direction == "inactive")
}

func canAnswerDTLSSetup(setup string) bool {
	return setup == "" || setup == "actpass" || setup == "active"
}

type cryptoInfo struct {
	Line  string
	Tag   int
	Suite string
}

func selectSupportedCrypto(lines []string) (cryptoInfo, bool) {
	for _, line := range lines {
		crypto, ok := parseCryptoInfo(line)
		if !ok {
			continue
		}
		switch crypto.Suite {
		case "AES_CM_128_HMAC_SHA1_80", "AES_CM_128_HMAC_SHA1_32":
			return crypto, true
		}
	}
	return cryptoInfo{}, false
}

func parseCryptoInfo(line string) (cryptoInfo, bool) {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "a=")
	if !strings.HasPrefix(line, "crypto:") {
		return cryptoInfo{}, false
	}
	fields := strings.Fields(strings.TrimPrefix(line, "crypto:"))
	if len(fields) < 3 {
		return cryptoInfo{}, false
	}
	tag, err := strconv.Atoi(fields[0])
	if err != nil {
		return cryptoInfo{}, false
	}
	return cryptoInfo{
		Line:  "a=" + line,
		Tag:   tag,
		Suite: fields[1],
	}, true
}

func rejectMediaLine(sec mediaSection) string {
	protocol := sec.Protocol
	if protocol == "" {
		protocol = "RTP/AVP"
	}
	format := "0"
	if len(sec.Formats) > 0 {
		format = sec.Formats[0]
	}
	return fmt.Sprintf("m=%s 0 %s %s", sec.Kind, protocol, format)
}

func parseOffer(raw string) ([]mediaSection, error) {
	lines := strings.Split(strings.ReplaceAll(raw, "\n", "\r\n"), "\r\n")
	var sections []mediaSection
	var cur *mediaSection
	var sessionDirection string
	var sessionSetup string
	var sessionFingerprint string
	var sessionAddress string
	bundleMIDs := map[string]bool{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "m=") {
			fields := strings.Fields(strings.TrimPrefix(line, "m="))
			if len(fields) < 4 {
				return nil, ErrInvalidSDP
			}
			port, err := strconv.Atoi(fields[1])
			if err != nil {
				return nil, ErrInvalidSDP
			}
			sec := mediaSection{
				Kind:        fields[0],
				Port:        port,
				Protocol:    fields[2],
				Formats:     fields[3:],
				Attrs:       map[int]Payload{},
				Direction:   sessionDirection,
				Setup:       sessionSetup,
				Fingerprint: sessionFingerprint,
				Address:     sessionAddress,
				Feedback:    map[int]bool{},
			}
			for _, format := range sec.Formats {
				pt, err := strconv.Atoi(format)
				if err != nil {
					continue
				}
				sec.Payloads = append(sec.Payloads, pt)
			}
			sections = append(sections, sec)
			cur = &sections[len(sections)-1]
			continue
		}
		if strings.HasPrefix(line, "c=") {
			f := strings.Fields(strings.TrimPrefix(line, "c="))
			if len(f) != 3 || f[0] != "IN" || f[1] != "IP4" {
				return nil, ErrInvalidSDP
			}
			if cur == nil {
				sessionAddress = f[2]
			} else {
				cur.Address = f[2]
			}
			continue
		}
		if !strings.HasPrefix(line, "a=") {
			continue
		}
		attr := strings.TrimPrefix(line, "a=")
		switch {
		case strings.HasPrefix(attr, "rtcp:"):
			if cur != nil {
				f := strings.Fields(strings.TrimPrefix(attr, "rtcp:"))
				if len(f) != 1 && len(f) != 4 {
					return nil, ErrInvalidSDP
				}
				p, err := strconv.Atoi(f[0])
				if err != nil || p < 1 || p > 65535 {
					return nil, ErrInvalidSDP
				}
				cur.RTCPPort = p
				if len(f) == 4 {
					if f[1] != "IN" || f[2] != "IP4" {
						return nil, ErrInvalidSDP
					}
					cur.RTCPAddress = f[3]
				}
			}
		case strings.HasPrefix(attr, "rtcp-fb:"):
			if cur != nil {
				f := strings.Fields(strings.TrimPrefix(attr, "rtcp-fb:"))
				if len(f) == 3 && f[1] == "nack" && f[2] == "pli" {
					pt := -1
					var err error
					if f[0] != "*" {
						pt, err = strconv.Atoi(f[0])
					}
					if err == nil {
						cur.Feedback[pt] = true
					}
				}
			}
		case strings.HasPrefix(attr, "group:BUNDLE"):
			for _, mid := range strings.Fields(strings.TrimSpace(strings.TrimPrefix(attr, "group:BUNDLE"))) {
				bundleMIDs[mid] = true
			}
		case attr == "sendonly" || attr == "sendrecv" || attr == "recvonly" || attr == "inactive":
			if cur == nil {
				sessionDirection = attr
			} else {
				cur.Direction = attr
			}
		case strings.HasPrefix(attr, "rtpmap:"):
			if cur == nil {
				continue
			}
			parseRtpmap(cur, strings.TrimPrefix(attr, "rtpmap:"))
		case strings.HasPrefix(attr, "fmtp:"):
			if cur == nil {
				continue
			}
			parseFmtp(cur, strings.TrimPrefix(attr, "fmtp:"))
		case strings.HasPrefix(attr, "crypto:"):
			if cur == nil {
				continue
			}
			// 保存完整的crypto属性行（包括a=前缀）
			cur.Crypto = append(cur.Crypto, "a="+attr)
		case strings.HasPrefix(attr, "setup:"):
			setup := strings.TrimSpace(strings.TrimPrefix(attr, "setup:"))
			if cur == nil {
				sessionSetup = setup
			} else {
				cur.Setup = setup
			}
		case strings.HasPrefix(attr, "fingerprint:"):
			fingerprint := strings.TrimSpace(strings.TrimPrefix(attr, "fingerprint:"))
			if cur == nil {
				sessionFingerprint = fingerprint
			} else {
				cur.Fingerprint = fingerprint
			}
		case strings.HasPrefix(attr, "mid:"):
			if cur != nil {
				cur.MID = strings.TrimSpace(strings.TrimPrefix(attr, "mid:"))
			}
		case strings.HasPrefix(attr, "zrtp-hash:"):
			if cur != nil {
				cur.ZRTPHash = strings.TrimSpace(strings.TrimPrefix(attr, "zrtp-hash:"))
			}
		case attr == "rtcp-mux":
			if cur != nil {
				cur.RTCPMux = true
			}
		}
	}
	if len(sections) == 0 {
		return nil, ErrInvalidSDP
	}
	for i := range sections {
		if sections[i].MID != "" && bundleMIDs[sections[i].MID] {
			sections[i].Bundle = true
		}
	}
	return sections, nil
}

func parseRtpmap(sec *mediaSection, value string) {
	parts := strings.Fields(value)
	if len(parts) != 2 {
		return
	}
	pt, err := strconv.Atoi(parts[0])
	if err != nil {
		return
	}
	codecParts := strings.Split(parts[1], "/")
	if len(codecParts) < 2 {
		return
	}
	clock, err := strconv.Atoi(codecParts[1])
	if err != nil {
		return
	}
	name := strings.ToUpper(codecParts[0])
	p := sec.Attrs[pt]
	p.PayloadType = pt
	p.CodecName = name
	p.ClockRate = clock
	switch name {
	case "PCMA":
		p.Codec = base.AvPacketPtG711A
	case "PCMU":
		p.Codec = base.AvPacketPtG711U
	case "H264":
		p.Codec = base.AvPacketPtAvc
	case "H265", "HEVC":
		p.Codec = base.AvPacketPtHevc
	}
	sec.Attrs[pt] = p
}

func parseFmtp(sec *mediaSection, value string) {
	parts := strings.Fields(value)
	if len(parts) < 2 {
		return
	}
	pt, err := strconv.Atoi(parts[0])
	if err != nil {
		return
	}
	p := sec.Attrs[pt]
	p.PayloadType = pt
	p.Fmtp = strings.Join(parts[1:], " ")
	sec.Attrs[pt] = p
}

func chooseAudio(sec mediaSection) (*Payload, *Payload) {
	var opus, aac, pcma, pcmu, dtmf *Payload
	for _, pt := range sec.Payloads {
		if pt < 0 || pt > 127 || (sec.RTCPMux && pt >= 64 && pt <= 95) {
			continue
		}
		p := sec.Attrs[pt]
		codecName := strings.ToUpper(p.CodecName)
		switch {
		case strings.EqualFold(p.CodecName, "opus"):
			if p.Codec == 0 {
				p.Codec = base.AvPacketPtOpus
			}
			cp := p
			opus = &cp
		case strings.EqualFold(p.CodecName, "mpeg4-generic") || codecName == "AAC":
			if p.Codec == 0 {
				p.Codec = base.AvPacketPtAac
			}
			cp := p
			aac = &cp
		case codecName == "PCMA" || (codecName == "" && pt == int(base.AvPacketPtG711A)):
			if p.PayloadType == 0 {
				p.PayloadType = pt
			}
			if p.CodecName == "" {
				p.CodecName = "PCMA"
				p.ClockRate = 8000
				p.Codec = base.AvPacketPtG711A
			}
			cp := p
			pcma = &cp
		case codecName == "PCMU" || (codecName == "" && pt == int(base.AvPacketPtG711U)):
			if p.CodecName == "" {
				p.CodecName = "PCMU"
				p.ClockRate = 8000
				p.Codec = base.AvPacketPtG711U
			}
			cp := p
			pcmu = &cp
		case strings.EqualFold(p.CodecName, "telephone-event"):
			cp := p
			dtmf = &cp
		}
	}
	// 优先级：Opus > AAC > PCMA > PCMU
	if opus != nil {
		return opus, dtmf
	}
	if aac != nil {
		return aac, dtmf
	}
	if pcma != nil {
		return pcma, dtmf
	}
	return pcmu, dtmf
}

func chooseVideo(sec mediaSection) *Payload {
	var hevc, avc *Payload
	for _, pt := range sec.Payloads {
		if pt < 0 || pt > 127 || (sec.RTCPMux && pt >= 64 && pt <= 95) {
			continue
		}
		p := sec.Attrs[pt]
		switch p.Codec {
		case base.AvPacketPtHevc:
			cp := p
			hevc = &cp
		case base.AvPacketPtAvc:
			cp := p
			avc = &cp
		}
	}
	if avc != nil {
		return avc
	}
	return hevc
}

func StreamName(user string, callID string) string {
	user = sanitize(user)
	callID = sanitize(callID)
	if len(callID) > 12 {
		callID = callID[:12]
	}
	if user == "" {
		user = "unknown"
	}
	if callID == "" {
		callID = "call"
	}
	return "voip_" + user + "_" + callID
}

func sanitize(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := unicode.IsLetter(r) || unicode.IsDigit(r)
		if ok {
			b.WriteRune(unicode.ToLower(r))
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func answerDirection(sec mediaSection) string {
	if sec.Direction == "inactive" || sec.Direction == "recvonly" || sec.Address == "0.0.0.0" {
		return "a=inactive"
	}
	return "a=recvonly"
}
func (n Negotiated) Held() bool {
	return (n.Audio == nil || n.AudioHeld) && (n.Video == nil || n.VideoHeld)
}
