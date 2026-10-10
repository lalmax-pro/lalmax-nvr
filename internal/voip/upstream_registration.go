package voip

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type upstreamRegistration struct {
	peer                  TransportAddr
	state, lastError      string
	callID, fromTag       string
	cseq                  int
	nonce, realm, qop     string
	proxyAuth             bool
	pending               bool
	deadline, nextAttempt time.Time
	expiresAt, refreshAt  time.Time
	requestedExpires      int
}

func pbxExtensionURI(extension, domain string, transport TransportType) string {
	switch transport {
	case TransportTCP:
		return fmt.Sprintf("sip:%s@%s;transport=tcp", extension, domain)
	case TransportTLS:
		return fmt.Sprintf("sips:%s@%s", extension, domain)
	default:
		return fmt.Sprintf("sip:%s@%s", extension, domain)
	}
}

func (s *Server) initUpstreamTransport() error {
	if s.config.PbxServer == "" {
		return nil
	}
	switch TransportType(strings.ToUpper(s.config.PbxTransport)) {
	case TransportUDP:
		return nil
	case TransportTCP:
		transport, err := NewDialOnlyTCPTransport(TransportTCP, nil, s.handleMessage, s.log)
		if err != nil {
			return fmt.Errorf("initialize PBX TCP transport: %w", err)
		}
		s.pbxTCPTransport = transport
	case TransportTLS:
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
		if s.config.PbxTLSServerName != "" {
			tlsConfig.ServerName = s.config.PbxTLSServerName
		}
		if s.config.PbxTLSCAFile != "" {
			caPEM, err := os.ReadFile(s.config.PbxTLSCAFile)
			if err != nil {
				return fmt.Errorf("read PBX TLS CA file: %w", err)
			}
			roots, err := x509.SystemCertPool()
			if err != nil || roots == nil {
				roots = x509.NewCertPool()
			}
			if !roots.AppendCertsFromPEM(caPEM) {
				return fmt.Errorf("PBX TLS CA file contains no valid certificates")
			}
			tlsConfig.RootCAs = roots
		}
		transport, err := NewDialOnlyTCPTransport(TransportTLS, tlsConfig, s.handleMessage, s.log)
		if err != nil {
			return fmt.Errorf("initialize PBX TLS transport: %w", err)
		}
		s.pbxTLSTransport = transport
	default:
		return fmt.Errorf("unsupported PBX transport %q", s.config.PbxTransport)
	}
	return nil
}

func (s *Server) initUpstreamRegistration() error {
	if s.config.PbxServer == "" {
		return nil
	}
	peer := TransportAddr{Type: TransportType(strings.ToUpper(s.config.PbxTransport)), Addr: s.config.PbxServer}
	if peer.Type == TransportUDP {
		resolved, err := net.ResolveUDPAddr("udp4", s.config.PbxServer)
		if err != nil {
			return fmt.Errorf("resolve PBX server: %w", err)
		}
		peer.Addr = resolved.String()
	}
	domain := strings.TrimSpace(s.config.PbxDomain)
	if domain == "" {
		host, _, _ := net.SplitHostPort(s.config.PbxServer)
		domain = host
		s.config.PbxDomain = domain
	}
	s.upstream = &upstreamRegistration{
		peer:             peer,
		state:            "registering",
		callID:           generateTag() + "@lalmax-nvr",
		fromTag:          generateTag(),
		requestedExpires: s.config.PbxRegisterExpires,
		nextAttempt:      time.Now(),
	}
	return nil
}

func (s *Server) upstreamStatus(now time.Time) UpstreamRegistrationStatus {
	if s.upstream == nil {
		return UpstreamRegistrationStatus{Configured: false, State: "disabled"}
	}
	u := s.upstream
	var expiresAt *time.Time
	if !u.expiresAt.IsZero() {
		value := u.expiresAt
		expiresAt = &value
	}
	return UpstreamRegistrationStatus{Configured: true, Server: s.config.PbxServer, Transport: strings.ToUpper(s.config.PbxTransport), Username: s.config.PbxUsername,
		State: u.state, ExpiresAt: expiresAt, LastError: u.lastError}
}

func (s *Server) runUpstreamRegistration(now time.Time) {
	u := s.upstream
	if u == nil {
		return
	}
	if u.pending {
		if now.Before(u.deadline) {
			return
		}
		u.pending = false
		u.state = "failed"
		u.lastError = "PBX registration timed out"
		u.nextAttempt = now.Add(10 * time.Second)
	}
	if !u.nextAttempt.IsZero() && now.Before(u.nextAttempt) {
		return
	}
	s.sendUpstreamRegister(u, s.config.PbxRegisterExpires, now)
}

func (s *Server) sendUpstreamRegister(u *upstreamRegistration, expires int, now time.Time) {
	var transport Transport
	switch u.peer.Type {
	case TransportUDP:
		transport = s.udpTransport
	case TransportTCP:
		transport = s.pbxTCPTransport
	case TransportTLS:
		transport = s.pbxTLSTransport
	}
	if transport == nil {
		u.state, u.lastError, u.nextAttempt = "failed", "PBX signaling transport is unavailable", now.Add(30*time.Second)
		return
	}
	u.cseq++
	viaAddress := s.listenAddr
	if host, port, err := net.SplitHostPort(viaAddress); err == nil {
		ip := net.ParseIP(host)
		if host == "" || (ip != nil && ip.IsUnspecified()) {
			host = s.config.SipIP
			if host == "" {
				host = s.getLocalIP()
			}
		}
		viaAddress = net.JoinHostPort(host, port)
	}
	contactHost := s.config.SipIP
	if contactHost == "" {
		contactHost = s.getLocalIP()
	}
	_, localPort, _ := net.SplitHostPort(s.listenAddr)
	contactScheme := "sip"
	if u.peer.Type == TransportTLS {
		contactScheme = "sips"
	}
	contact := fmt.Sprintf("<%s:%s@%s>", contactScheme, s.config.PbxUsername, net.JoinHostPort(contactHost, localPort))
	domain := s.config.PbxDomain
	uri := "sip:" + domain
	if u.peer.Type == TransportTCP {
		uri += ";transport=tcp"
	} else if u.peer.Type == TransportTLS {
		uri = "sips:" + domain
	}
	from := fmt.Sprintf("<sip:%s@%s>;tag=%s", s.config.PbxUsername, domain, u.fromTag)
	viaTransport := strings.ToUpper(string(u.peer.Type))
	msg := &Message{IsRequest: true, Method: MethodRegister, RequestURI: uri, Headers: map[string][]string{
		"Via":  {fmt.Sprintf("SIP/2.0/%s %s;branch=z9hG4bK-%s;rport", viaTransport, viaAddress, generateTag())},
		"From": {from}, "To": {fmt.Sprintf("<sip:%s@%s>", s.config.PbxUsername, domain)},
		"Call-ID": {u.callID}, "CSeq": {fmt.Sprintf("%d REGISTER", u.cseq)}, "Contact": {contact},
		"Expires": {strconv.Itoa(expires)}, "Max-Forwards": {"70"},
	}}
	if u.nonce != "" {
		header, err := buildDigestAuthorization("REGISTER", uri, s.config.PbxUsername, s.config.PbxPassword, u.realm, u.nonce, u.qop)
		if err != nil {
			u.state, u.lastError, u.nextAttempt = "failed", err.Error(), now.Add(30*time.Second)
			return
		}
		name := "Authorization"
		if u.proxyAuth {
			name = "Proxy-Authorization"
		}
		msg.SetHeader(name, header)
	}
	if err := transport.Send([]byte(serializeRequest(msg)), u.peer); err != nil {
		u.state, u.lastError, u.pending, u.nextAttempt = "failed", err.Error(), false, now.Add(10*time.Second)
		return
	}
	u.pending = true
	u.state = "registering"
	u.lastError = ""
	u.deadline = now.Add(8 * time.Second)
	u.nextAttempt = time.Time{}
	if expires == 0 {
		u.state = "unregistering"
	}
}

func (s *Server) handleUpstreamResponse(m *Message, addr TransportAddr, now time.Time) bool {
	u := s.upstream
	if u == nil || m.CallID() != u.callID || !u.pending || addr != u.peer || ExtractCSeqMethod(m.CSeq()) != MethodRegister || ExtractCSeqNumber(m.CSeq()) != u.cseq {
		return false
	}
	u.pending = false
	switch m.StatusCode {
	case 200:
		expires := s.config.PbxRegisterExpires
		if raw := m.GetHeader("Expires"); raw != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n >= 0 {
				expires = n
			}
		}
		if expires == 0 {
			u.state = "unregistered"
			u.expiresAt, u.refreshAt = time.Time{}, time.Time{}
		} else {
			u.state = "registered"
			u.expiresAt = now.Add(time.Duration(expires) * time.Second)
			refresh := time.Duration(expires*4/5) * time.Second
			if refresh < 30*time.Second {
				refresh = 30 * time.Second
			}
			u.refreshAt = now.Add(refresh)
			u.nextAttempt = u.refreshAt
		}
		u.lastError = ""
	case 401, 407:
		challengeHeader := m.GetHeader("WWW-Authenticate")
		u.proxyAuth = m.StatusCode == 407
		if u.proxyAuth {
			challengeHeader = m.GetHeader("Proxy-Authenticate")
		}
		challenge := parseDigestAuthorization(challengeHeader)
		if challenge == nil || challenge["nonce"] == "" || challenge["realm"] == "" {
			u.state, u.lastError, u.nextAttempt = "failed", "PBX returned an unsupported digest challenge", now.Add(30*time.Second)
			return true
		}
		u.nonce, u.realm, u.qop = challenge["nonce"], challenge["realm"], challenge["qop"]
		if !strings.Contains(strings.ToLower(challenge["algorithm"]), "md5") || strings.Contains(strings.ToLower(challenge["algorithm"]), "sess") {
			u.state, u.lastError, u.nextAttempt = "failed", "PBX digest algorithm is not supported", now.Add(30*time.Second)
			return true
		}
		u.nextAttempt = now
		u.state = "registering"
	default:
		u.state = "failed"
		u.lastError = fmt.Sprintf("PBX registration failed: SIP %d %s", m.StatusCode, m.StatusText)
		u.nextAttempt = now.Add(30 * time.Second)
	}
	return true
}
