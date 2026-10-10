package voip

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/q191201771/lal/pkg/base"
	maxrtc "github.com/q191201771/lalmax/rtc"
	maxvoip "github.com/q191201771/lalmax/voip"
	"github.com/q191201771/lalmax/voip/media"
	"github.com/q191201771/lalmax/voip/sdp"
)

type DialResult struct {
	CallID    string      `json:"call_id"`
	State     DialogState `json:"state"`
	TalkToken string      `json:"talk_token"`
}
type outboundCall struct {
	id, token, user                                        string
	authUsername, authPassword                             string
	authAttempts                                           int
	peer                                                   TransportAddr
	request, response                                      *Message
	raw                                                    string
	codecs                                                 []sdp.Payload
	security                                               string
	key                                                    []byte
	port, rtcpPort                                         int
	state                                                  DialogState
	reason                                                 string
	created, deadline, next, lease, retainUntil            time.Time
	interval                                               time.Duration
	provisional, cancelled, cancelSent, byeSent, attaching bool
	remoteCSeq                                             int
	endedAt                                                time.Time
	answeredAt                                             time.Time
	finalReceived, finalSent                               uint32
	bridgeMu                                               sync.Mutex
	pub                                                    *maxvoip.PubSession
	browser                                                *maxrtc.TalkSession
}

func serializeRequest(m *Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s SIP/2.0\r\n", m.Method, m.RequestURI)
	for _, key := range []string{"Via", "From", "To", "Call-ID", "CSeq", "Contact", "Max-Forwards", "Expires", "Route", "Authorization", "Proxy-Authorization", "Content-Type"} {
		for _, v := range m.GetHeaderAll(key) {
			fmt.Fprintf(&b, "%s: %s\r\n", key, v)
		}
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n%s", len(m.Body), m.Body)
	return b.String()
}

func contactAddress(contact string) string {
	uri := headerURI(contact)
	address := strings.TrimPrefix(strings.TrimPrefix(uri, "sips:"), "sip:")
	return strings.Split(address, ";")[0]
}

// Dial only calls an online registration through its observed signaling flow.
// It never holds the request lock while waiting for a terminal to answer.
func (s *Server) Dial(user, security, browserOffer string) (DialResult, error) {
	codecs, err := sdp.TalkCodecs(browserOffer)
	if err != nil {
		return DialResult{}, err
	}
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	select {
	case <-s.stopChan:
		return DialResult{}, fmt.Errorf("VoIP is stopped")
	default:
	}
	if security == "" {
		security = "rtp"
		if s.config.SrtpMandatory {
			security = "dtls"
		}
	}
	if security != "rtp" && security != "sdes" && security != "dtls" {
		return DialResult{}, fmt.Errorf("invalid media security")
	}
	if (security != "rtp" && !s.config.SrtpEnable) || (security == "rtp" && s.config.SrtpMandatory) {
		return DialResult{}, fmt.Errorf("media security is not allowed by VoIP settings")
	}
	reg := s.registrar.Get(user)
	var peer TransportAddr
	var requestURI, toHeader, fromUser, authUsername, authPassword string
	if reg != nil && reg.RemoteAddr != "" {
		peer = TransportAddr{Type: reg.Transport, Addr: reg.RemoteAddr}
		requestURI, toHeader = headerURI(reg.Contact), "<"+headerURI(reg.Contact)+">"
		fromUser = "nvr"
	} else if s.upstream != nil && s.upstream.state == "registered" {
		if !validPBXExtension(user) {
			return DialResult{}, fmt.Errorf("invalid PBX extension")
		}
		peer = s.upstream.peer
		requestURI = pbxExtensionURI(user, s.config.PbxDomain, peer.Type)
		toHeader = "<" + requestURI + ">"
		fromUser = s.config.PbxUsername
		authUsername, authPassword = s.config.PbxUsername, s.config.PbxPassword
	} else {
		return DialResult{}, fmt.Errorf("SIP user is offline and PBX registration is unavailable")
	}
	active := 0
	for _, c := range s.outbound {
		if c.state != "ended" && c.state != "failed" {
			active++
			if c.user == user {
				return DialResult{}, fmt.Errorf("terminal already has an outgoing call")
			}
		}
	}
	if active >= 32 || len(s.outbound) >= 256 {
		return DialResult{}, fmt.Errorf("too many outgoing calls")
	}
	port, rtcpPort, err := s.portAllocator.AllocMedia(false)
	if err != nil {
		return DialResult{}, err
	}
	now := time.Now()
	c := &outboundCall{id: generateTag() + "@lalmax-nvr", user: user, authUsername: authUsername, authPassword: authPassword, peer: peer, codecs: codecs, security: security, port: port, rtcpPort: rtcpPort, state: "dialing", created: now, deadline: now.Add(transactionLifetime), lease: now.Add(45 * time.Second), interval: sipT1}
	opt := sdp.AnswerOptions{MediaIP: s.config.MediaIP, AudioPort: port, AudioRTCPPort: rtcpPort, SessionID: uint64(now.UnixNano()), DTLSFingerprint: s.dtlsFingerprint}
	if opt.MediaIP == "" {
		opt.MediaIP = s.config.SipIP
	}
	if opt.MediaIP == "" {
		opt.MediaIP = s.getLocalIP()
	}
	if security == "sdes" {
		c.key, err = media.GenerateSrtpKey()
		if err != nil {
			s.portAllocator.Free(port)
			s.portAllocator.Free(rtcpPort)
			return DialResult{}, err
		}
		opt.AudioCrypto = media.BuildCryptoAttribute(1, "AES_CM_128_HMAC_SHA1_80", c.key)
	}
	target := headerURI(requestURI)
	if !strings.HasPrefix(target, "sip:") && !strings.HasPrefix(target, "sips:") {
		s.portAllocator.Free(port)
		s.portAllocator.Free(rtcpPort)
		return DialResult{}, fmt.Errorf("invalid registered Contact")
	}
	contact := s.buildContactURI(peer)
	localAddress := contactAddress(contact)
	if s.upstream != nil && peer == s.upstream.peer && (peer.Type == TransportTCP || peer.Type == TransportTLS) {
		requestURI = pbxExtensionURI(user, s.config.PbxDomain, peer.Type)
	} else {
		requestURI = headerURI(requestURI)
	}
	c.request = &Message{IsRequest: true, Method: MethodInvite, RequestURI: requestURI, Body: sdp.BuildTalkOffer(codecs, opt, security), Headers: map[string][]string{
		"Via": {fmt.Sprintf("SIP/2.0/%s %s;branch=z9hG4bK-%s;rport", peer.Type, localAddress, generateTag())}, "From": {"<sip:" + fromUser + "@" + localAddress + ">;tag=" + generateTag()}, "To": {toHeader}, "Call-ID": {c.id}, "CSeq": {"1 INVITE"}, "Contact": {contact}, "Max-Forwards": {"70"}, "Content-Type": {"application/sdp"}}}
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil {
		s.releaseOutbound(c)
		return DialResult{}, err
	}
	c.token = hex.EncodeToString(token[:])
	c.raw = serializeRequest(c.request)
	if peer.Type == TransportUDP || peer.Type == TransportDTLS {
		c.next = now.Add(sipT1)
	}
	s.outbound[c.id] = c
	if err := s.sendResponse(c.raw, peer); err != nil {
		s.releaseOutbound(c)
		delete(s.outbound, c.id)
		return DialResult{}, err
	}
	return DialResult{c.id, c.state, c.token}, nil
}

func (c *outboundCall) status(now time.Time) CallStatus {
	out := CallStatus{Direction: "outbound", CallID: c.id, FromUser: "nvr", ToUser: c.user, State: c.state, FailureReason: c.reason, StartedAt: c.created, DurationSeconds: int64(now.Sub(c.created).Seconds()), RemoteAddr: c.peer.Addr, Transport: c.peer.Type}
	c.bridgeMu.Lock()
	defer c.bridgeMu.Unlock()
	if !c.endedAt.IsZero() {
		out.DurationSeconds = int64(c.endedAt.Sub(c.created).Seconds())
	}
	out.AudioReceivedPackets, out.AudioSentPackets = c.finalReceived, c.finalSent
	out.BrowserReady = c.browser != nil
	if c.pub != nil {
		out.StreamID = c.pub.StreamName()
		negotiated := c.pub.Negotiated()
		if negotiated.Audio != nil {
			out.AudioCodec = negotiated.Audio.CodecName
		}
		out.DTMFAvailable = negotiated.DTMF != nil
		out.AudioPort = c.pub.AudioPort()
		out.AudioReceivedPackets, out.AudioSentPackets = c.pub.AudioPacketCounts()
	}
	return out
}

func (s *Server) AttachTalk(ctx context.Context, id, token, offer string) (string, error) {
	s.requestMu.Lock()
	c := s.outbound[id]
	if c == nil {
		s.requestMu.Unlock()
		return s.AttachInboundTalk(ctx, id, token, offer)
	}
	if subtle.ConstantTimeCompare([]byte(c.token), []byte(token)) != 1 || c.state != DialogStateEstablished {
		s.requestMu.Unlock()
		return "", fmt.Errorf("outgoing call is not connected")
	}
	c.bridgeMu.Lock()
	pub := c.pub
	already := c.browser != nil
	c.bridgeMu.Unlock()
	if already || c.attaching {
		s.requestMu.Unlock()
		return "", fmt.Errorf("call already has a browser")
	}
	c.attaching = true
	c.lease = time.Now().Add(45 * time.Second)
	codec := *pub.Negotiated().Audio
	s.requestMu.Unlock()
	browser, answer, err := s.newTalkSession(ctx, offer, codec, func(p *rtp.Packet) { _ = pub.WriteAudioRTP(p) }, func() { s.Hangup(id) })
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	c.attaching = false
	if err != nil {
		if c.state == DialogStateEstablished {
			s.cancelOutbound(c, "WebRTC negotiation failed")
		}
		return "", err
	}
	if s.outbound[id] != c || c.state != DialogStateEstablished {
		browser.Close()
		return "", fmt.Errorf("call ended during WebRTC negotiation")
	}
	c.bridgeMu.Lock()
	c.browser = browser
	c.bridgeMu.Unlock()
	c.lease = time.Now().Add(45 * time.Second)
	return answer, nil
}
func (s *Server) KeepTalk(id, token string) bool {
	s.requestMu.Lock()
	c := s.outbound[id]
	if c == nil {
		s.requestMu.Unlock()
		return s.KeepInboundTalk(id, token)
	}
	defer s.requestMu.Unlock()
	if subtle.ConstantTimeCompare([]byte(c.token), []byte(token)) != 1 || c.state == "ended" || c.state == "failed" {
		return false
	}
	c.lease = time.Now().Add(45 * time.Second)
	return true
}

func matchingOutboundResponse(m *Message, c *outboundCall, addr TransportAddr) bool {
	// SIP stacks may normalize the From URI in responses (for example, drop
	// the explicit port) while preserving its dialog tag. Match the tag and
	// transaction identifiers rather than requiring byte-for-byte headers.
	if addr != c.peer || ExtractTag(m.From()) != ExtractTag(c.request.From()) || m.CSeq() != c.request.CSeq() || !sameSIPURIIgnoringTransport(headerURI(m.To()), headerURI(c.request.To())) {
		return false
	}
	copy := *m
	copy.RequestURI = c.request.RequestURI
	return transactionKey(&copy, MethodInvite) == transactionKey(c.request, MethodInvite)
}

// sameSIPURIIgnoringTransport compares a dialog URI while allowing the
// transport parameter to be omitted from a response To header. Asterisk does
// this for TCP INVITE challenges: the request targets sip:user@host;transport=tcp
// but its 401 To header contains sip:user@host. The transport still comes from
// the established flow and is validated separately through addr.
func sameSIPURIIgnoringTransport(a, b string) bool {
	stripTransport := func(uri string) string {
		parts := strings.Split(uri, ";")
		kept := parts[:1]
		for _, parameter := range parts[1:] {
			name, _, _ := strings.Cut(strings.TrimSpace(parameter), "=")
			if !strings.EqualFold(strings.TrimSpace(name), "transport") {
				kept = append(kept, parameter)
			}
		}
		return strings.Join(kept, ";")
	}
	return stripTransport(a) == stripTransport(b)
}

func (s *Server) handleOutboundResponse(m *Message, addr TransportAddr) bool {
	c := s.outbound[m.CallID()]
	if c == nil {
		return false
	}
	if ExtractCSeqMethod(m.CSeq()) != MethodInvite {
		return false
	}
	if !matchingOutboundResponse(m, c, addr) {
		return true
	}
	if m.StatusCode < 100 || m.StatusCode > 699 {
		return true
	}
	if m.StatusCode < 200 {
		c.provisional = true
		c.next = time.Time{}
		if !c.cancelled && c.state == "dialing" && m.StatusCode >= 180 {
			c.state = "ringing"
		}
		if c.cancelled {
			s.sendCancel(c)
		}
		return true
	}
	if ExtractTag(m.To()) == "" {
		return true
	}
	c.next = time.Time{}
	if (m.StatusCode == 401 || m.StatusCode == 407) && c.authPassword != "" && c.authAttempts < 2 {
		challengeHeader := m.GetHeader("WWW-Authenticate")
		authName := "Authorization"
		if m.StatusCode == 407 {
			challengeHeader = m.GetHeader("Proxy-Authenticate")
			authName = "Proxy-Authorization"
		}
		challenge := parseDigestAuthorization(challengeHeader)
		if challenge != nil && (strings.EqualFold(challenge["algorithm"], "MD5") || challenge["algorithm"] == "") {
			header, err := buildDigestAuthorization("INVITE", c.request.RequestURI, c.authUsername, c.authPassword, challenge["realm"], challenge["nonce"], challenge["qop"])
			if err == nil {
				ack := *c.request
				ack.Headers = cloneHeaders(c.request.Headers)
				ack.Method = MethodAck
				ack.Body = ""
				ack.SetHeader("CSeq", fmt.Sprintf("%d ACK", ExtractCSeqNumber(c.request.CSeq())))
				ack.SetHeader("To", m.To())
				delete(ack.Headers, "Content-Type")
				s.sendResponse(serializeRequest(&ack), c.peer)

				retry := *c.request
				retry.Headers = cloneHeaders(c.request.Headers)
				retry.Method = MethodInvite
				retry.SetHeader("CSeq", fmt.Sprintf("%d INVITE", ExtractCSeqNumber(c.request.CSeq())+1))
				retry.SetHeader("Via", strings.Split(c.request.GetHeader("Via"), ";branch=")[0]+";branch=z9hG4bK-"+generateTag()+";rport")
				delete(retry.Headers, "Authorization")
				delete(retry.Headers, "Proxy-Authorization")
				retry.SetHeader(authName, header)
				c.request = &retry
				c.raw = serializeRequest(c.request)
				c.authAttempts++
				c.interval = sipT1
				c.deadline = time.Now().Add(transactionLifetime)
				if c.peer.Type == TransportUDP || c.peer.Type == TransportDTLS {
					c.next = time.Now().Add(sipT1)
				} else {
					c.next = time.Time{}
				}
				if err = s.sendResponse(c.raw, c.peer); err != nil {
					s.finishOutbound(c, "failed", err.Error())
				}
				return true
			}
		}
	}
	if m.StatusCode >= 300 {
		ack := *c.request
		ack.Headers = cloneHeaders(c.request.Headers)
		ack.Method = MethodAck
		ack.Body = ""
		ack.SetHeader("CSeq", fmt.Sprintf("%d ACK", ExtractCSeqNumber(c.request.CSeq())))
		ack.SetHeader("To", m.To())
		delete(ack.Headers, "Content-Type")
		s.sendResponse(serializeRequest(&ack), c.peer)
		if c.state != "ended" && c.state != "failed" {
			s.finishOutbound(c, "failed", fmt.Sprintf("SIP %d %s", m.StatusCode, m.StatusText))
		}
		return true
	}
	ack := s.outboundDialogRequest(c, m, MethodAck, ExtractCSeqNumber(c.request.CSeq()))
	s.sendResponse(serializeRequest(ack), c.peer)
	if c.response != nil {
		// Every repeated 2xx needs an ACK; extra forked dialogs are released.
		if ExtractTag(c.response.To()) != ExtractTag(m.To()) {
			s.sendResponse(serializeRequest(s.outboundDialogRequest(c, m, MethodBye, ExtractCSeqNumber(c.request.CSeq())+1)), c.peer)
		}
		return true
	}
	c.response = m
	if c.cancelled || c.state == "ended" || c.state == "failed" {
		s.sendOutboundBye(c)
		return true
	}
	n, remote, err := sdp.ValidateTalkAnswer(m.Body, c.codecs, c.security)
	if err != nil {
		s.sendOutboundBye(c)
		s.finishOutbound(c, "failed", err.Error())
		return true
	}
	var crypto *media.SrtpContext
	var dtls *media.DTLSSRTPConfig
	if n.AudioDTLS {
		dtls = &media.DTLSSRTPConfig{Certificate: s.dtlsCert, RemoteFingerprint: n.AudioFingerprint, Client: n.AudioSetup == "passive"}
	}
	if c.security == "sdes" {
		key, e := extractSrtpKeyMaterial(n.AudioCrypto)
		if e == nil {
			crypto, e = media.CreateSrtpContextPair(c.key, key, n.AudioCryptoSuite)
		}
		if e != nil {
			s.sendOutboundBye(c)
			s.finishOutbound(c, "failed", e.Error())
			return true
		}
	}
	// NewPubSession takes ownership of allocated ports even on construction failure.
	port, rtcpPort := c.port, c.rtcpPort
	c.port, c.rtcpPort = 0, 0
	remoteHost, _, splitErr := net.SplitHostPort(remote)
	if splitErr != nil {
		s.sendOutboundBye(c)
		s.finishOutbound(c, "failed", fmt.Sprintf("invalid remote RTP address: %v", splitErr))
		return true
	}
	pub, err := maxvoip.NewPubSession(maxvoip.PubSessionConfig{StreamName: sdp.StreamName(c.user, c.id), CallID: c.id, FromUser: c.user, ToUser: "nvr", MediaIP: "0.0.0.0", Negotiated: n, PortAllocator: s.portAllocator, AudioPort: port, AudioRTCPPort: rtcpPort, AudioSrtpCtx: crypto, AudioDTLSSRTP: dtls, RemoteAudioRTPAddress: remote, RemoteSourceIP: remoteHost, TimeoutMs: s.config.RtpTimeoutMs, OnAvPacket: func(base.AvPacket) {}, OnAudioRTP: func(p *rtp.Packet) {
		c.bridgeMu.Lock()
		browser := c.browser
		c.bridgeMu.Unlock()
		if browser != nil {
			_ = browser.WriteRTP(p)
		}
	}, OnTimeout: func() { s.Hangup(c.id) }})
	if err != nil {
		s.sendOutboundBye(c)
		s.finishOutbound(c, "failed", err.Error())
		return true
	}
	c.bridgeMu.Lock()
	c.pub = pub
	c.bridgeMu.Unlock()
	if s.onPubSession != nil {
		if err = s.onPubSession(pub); err != nil {
			s.sendOutboundBye(c)
			s.finishOutbound(c, "failed", err.Error())
			return true
		}
	}
	c.state = DialogStateEstablished
	c.answeredAt = time.Now()
	return true
}

func validPBXExtension(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.+", r)) {
			return false
		}
	}
	return true
}
func cloneHeaders(h map[string][]string) map[string][]string {
	out := map[string][]string{}
	for k, v := range h {
		out[k] = append([]string(nil), v...)
	}
	return out
}
func (s *Server) outboundDialogRequest(c *outboundCall, response *Message, method Method, seq int) *Message {
	target := headerURI(response.Contact())
	if target == "" {
		target = c.request.RequestURI
	}
	routes := []string{}
	for _, line := range response.GetHeaderAll("Record-Route") {
		for _, v := range strings.Split(line, ",") {
			routes = append(routes, strings.TrimSpace(v))
		}
	}
	for i, j := 0, len(routes)-1; i < j; i, j = i+1, j-1 {
		routes[i], routes[j] = routes[j], routes[i]
	}
	if len(routes) > 0 && !strings.Contains(strings.ToLower(routes[0]), ";lr") {
		first := headerURI(routes[0])
		routes = append(routes[1:], "<"+target+">")
		target = first
	}
	via := strings.Split(c.request.GetHeader("Via"), ";branch=")[0] + ";branch=z9hG4bK-" + generateTag() + ";rport"
	return &Message{IsRequest: true, Method: method, RequestURI: target, Headers: map[string][]string{"Via": {via}, "From": {c.request.From()}, "To": {response.To()}, "Call-ID": {c.id}, "CSeq": {fmt.Sprintf("%d %s", seq, method)}, "Max-Forwards": {"70"}, "Route": routes}}
}
func (s *Server) sendOutboundBye(c *outboundCall) {
	if c.response == nil || c.byeSent {
		return
	}
	c.byeSent = true
	req := s.outboundDialogRequest(c, c.response, MethodBye, ExtractCSeqNumber(c.request.CSeq())+1)
	now := time.Now()
	tx := &clientTransaction{request: req, raw: serializeRequest(req), peer: c.peer, expires: now.Add(transactionLifetime), interval: sipT1}
	if c.peer.Type == TransportUDP || c.peer.Type == TransportDTLS {
		tx.next = now.Add(sipT1)
	}
	s.clientTransactions[c.id] = tx
	s.sendResponse(tx.raw, c.peer)
}
func (s *Server) sendCancel(c *outboundCall) {
	if c.cancelSent || !c.provisional || c.response != nil {
		return
	}
	c.cancelSent = true
	req := *c.request
	req.Headers = cloneHeaders(c.request.Headers)
	req.Method = MethodCancel
	req.Body = ""
	req.SetHeader("CSeq", fmt.Sprintf("%d CANCEL", ExtractCSeqNumber(c.request.CSeq())))
	delete(req.Headers, "Content-Type")
	now := time.Now()
	tx := &clientTransaction{request: &req, raw: serializeRequest(&req), peer: c.peer, expires: now.Add(transactionLifetime), interval: sipT1}
	if c.peer.Type == TransportUDP || c.peer.Type == TransportDTLS {
		tx.next = now.Add(sipT1)
	}
	s.clientTransactions[c.id] = tx
	s.sendResponse(tx.raw, c.peer)
}
func (s *Server) cancelOutbound(c *outboundCall, reason string) {
	if c.state == "ended" || c.state == "failed" {
		return
	}
	c.cancelled = true
	if c.response != nil {
		s.sendOutboundBye(c)
	} else {
		s.sendCancel(c)
	}
	s.finishOutbound(c, "ended", reason)
}
func (s *Server) finishOutbound(c *outboundCall, state DialogState, reason string) {
	if c.state == "ended" || c.state == "failed" {
		return
	}
	c.state = state
	c.reason = reason
	c.endedAt = time.Now()
	c.retainUntil = c.endedAt.Add(40 * time.Second)
	if s.onCallEnded != nil {
		status := c.status(c.endedAt)
		outcome := "failed"
		if state == "ended" && c.response != nil {
			outcome = "completed"
		} else if reason == "terminal did not answer" {
			outcome = "missed"
		} else if strings.Contains(reason, "SIP 486") || strings.Contains(reason, "SIP 603") {
			outcome = "rejected"
		} else if c.response == nil {
			outcome = "cancelled"
		}
		s.onCallEnded(CallRecord{CallID: c.id, Direction: "outbound", FromUser: c.requestFromUser(), ToUser: c.user,
			Outcome: outcome, StartedAt: c.created, AnsweredAt: c.answeredAt, EndedAt: c.endedAt,
			DurationSecond: durationSeconds(c.answeredAt, c.endedAt), FailureReason: reason, RemoteAddr: c.peer.Addr,
			Transport: c.peer.Type, AudioCodec: status.AudioCodec, VideoCodec: status.VideoCodec, StreamID: status.StreamID})
	}
	s.releaseOutbound(c)
}

func durationSeconds(start, end time.Time) int64 {
	if start.IsZero() || end.Before(start) {
		return 0
	}
	return int64(end.Sub(start).Seconds())
}

func (c *outboundCall) requestFromUser() string {
	if c.authUsername != "" {
		return c.authUsername
	}
	return "nvr"
}
func (s *Server) releaseOutbound(c *outboundCall) {
	c.bridgeMu.Lock()
	pub, browser := c.pub, c.browser
	c.pub, c.browser = nil, nil
	c.bridgeMu.Unlock()
	if browser != nil {
		browser.Close()
	}
	if pub != nil {
		c.finalReceived, c.finalSent = pub.AudioPacketCounts()
		_ = pub.Dispose()
		if s.onDelSession != nil {
			_ = s.onDelSession(pub)
		}
	}
	if c.port > 0 {
		s.portAllocator.Free(c.port)
		c.port = 0
	}
	if c.rtcpPort > 0 {
		s.portAllocator.Free(c.rtcpPort)
		c.rtcpPort = 0
	}
}
func (s *Server) runOutbound(now time.Time) {
	for id, c := range s.outbound {
		if !c.retainUntil.IsZero() && now.After(c.retainUntil) {
			delete(s.outbound, id)
			continue
		}
		if c.state != "ended" && c.state != "failed" {
			if now.After(c.lease) {
				s.cancelOutbound(c, "browser disconnected")
				continue
			}
			if c.response == nil && now.After(c.deadline) {
				s.sendCancel(c)
				s.finishOutbound(c, "failed", "terminal did not answer")
				continue
			}
		}
		// Keep an unanswered cancelled INVITE alive until its final response so a
		// racing 2xx can always be ACKed and followed by BYE.
		if c.response == nil && !c.provisional && !c.next.IsZero() && !now.Before(c.next) && now.Before(c.deadline) {
			s.sendResponse(c.raw, c.peer)
			c.interval *= 2
			c.next = now.Add(c.interval)
		}
	}
}
func (s *Server) handleOutboundRequest(m *Message, peer TransportAddr) bool {
	c := s.outbound[m.CallID()]
	if c == nil {
		return false
	}
	if c.response == nil || peer != c.peer || ExtractTag(m.From()) != ExtractTag(c.response.To()) || ExtractTag(m.To()) != ExtractTag(c.request.From()) {
		if m.Method != MethodAck {
			s.sendResponse(BuildResponse(m, 481, "Call/Transaction Does Not Exist", ""), peer)
		}
		return true
	}
	if m.Method == MethodAck {
		return true
	}
	response := ""
	seq := ExtractCSeqNumber(m.CSeq())
	if seq <= c.remoteCSeq {
		response = BuildResponse(m, 500, "CSeq Out of Order", "")
	} else {
		c.remoteCSeq = seq
		switch m.Method {
		case MethodBye:
			s.finishOutbound(c, "ended", "ended by terminal")
			response = BuildResponse(m, 200, "OK", "")
		case MethodOptions:
			response = BuildResponse(m, 200, "OK", "")
		case MethodUpdate:
			if m.Body == "" {
				response = BuildResponse(m, 200, "OK", "")
			} else {
				response = BuildResponse(m, 488, "Talk Media Change Unsupported", "")
			}
		default:
			response = BuildResponse(m, 488, "Talk Media Change Unsupported", "")
		}
	}
	s.rememberResponse(m, response, peer)
	s.sendResponse(response, peer)
	return true
}
