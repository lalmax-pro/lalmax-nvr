package voip

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/q191201771/lal/pkg/base"
	maxrtc "github.com/q191201771/lalmax/rtc"
	maxvoip "github.com/q191201771/lalmax/voip"
	"github.com/q191201771/naza/pkg/nazalog"
	"github.com/q191201771/lalmax/voip/sdp"
	rtpmedia "github.com/q191201771/lalmax/voip/media"
)

func m2AudioPub(t *testing.T, dtmf bool) *maxvoip.PubSession {
	t.Helper()
	n := sdp.Negotiated{Audio: &sdp.Payload{PayloadType: 8, Codec: base.AvPacketPtG711A, ClockRate: 8000, CodecName: "PCMA"}}
	if dtmf {
		n.DTMF = &sdp.Payload{PayloadType: 101, ClockRate: 8000, CodecName: "telephone-event"}
	}
	return m2PubSession(t, n)
}

func m2TalkDialog(t *testing.T, s *Server, callID string, pub *maxvoip.PubSession) *Dialog {
	t.Helper()
	s.dialogManager.Create(callID, "caller", "server", "1001", "1002", "127.0.0.1:9")
	s.dialogManager.UpdateState(callID, DialogStateEstablished)
	if pub != nil {
		s.dialogManager.SetPubSession(callID, pub)
	}
	s.dialogManager.SetTalk(callID, newInboundTalk())
	return s.dialogManager.Get(callID)
}

func TestM2VoipInboundTalkBranches(t *testing.T) {
	s := m2UnitServer(t)
	pub := m2AudioPub(t, false)

	if _, err := s.ClaimInboundTalk("missing"); err == nil {
		t.Fatal("missing dialog claimed")
	}
	// A dialog without a talk session cannot be claimed.
	s.dialogManager.Create("no-talk", "f", "t", "1001", "1002", "peer")
	s.dialogManager.UpdateState("no-talk", DialogStateEstablished)
	s.dialogManager.SetPubSession("no-talk", pub)
	if _, err := s.ClaimInboundTalk("no-talk"); err == nil {
		t.Fatal("dialog without talk claimed")
	}

	d := m2TalkDialog(t, s, "talk", pub)
	token, err := s.ClaimInboundTalk("talk")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.ClaimInboundTalk("talk"); err != nil || again != token {
		t.Fatalf("token not reused: %q %v", again, err)
	}
	d.talk.attached = true
	if _, err := s.ClaimInboundTalk("talk"); err == nil {
		t.Fatal("attached talk claimed")
	}
	d.talk.attached = false
	// An expired token is replaced.
	d.talk.tokenExpires = time.Now().Add(-time.Second)
	fresh, err := s.ClaimInboundTalk("talk")
	if err != nil || fresh == token {
		t.Fatalf("expired token not replaced: %q %v", fresh, err)
	}

	// Attach error branches.
	if _, err := s.AttachInboundTalk(context.Background(), "missing", "token", ""); err == nil {
		t.Fatal("missing dialog attached")
	}
	if _, err := s.AttachInboundTalk(context.Background(), "talk", "wrong", ""); err == nil {
		t.Fatal("invalid ticket attached")
	}
	d.talk.tokenExpires = time.Now().Add(-time.Second)
	if _, err := s.AttachInboundTalk(context.Background(), "talk", d.talk.token, ""); err == nil {
		t.Fatal("expired ticket attached")
	}
	d.talk.tokenExpires = time.Now().Add(time.Minute)
	d.talk.attached = true
	if _, err := s.AttachInboundTalk(context.Background(), "talk", d.talk.token, ""); err == nil {
		t.Fatal("attached talk reattached")
	}
	d.talk.attached = false
	d.talk.attaching = true
	if _, err := s.AttachInboundTalk(context.Background(), "talk", d.talk.token, ""); err == nil {
		t.Fatal("attaching talk reattached")
	}
	d.talk.attaching = false
	s.newTalkSession = func(context.Context, string, sdp.Payload, func(*rtp.Packet), func()) (*maxrtc.TalkSession, string, error) {
		return nil, "", fmt.Errorf("negotiation failed")
	}
	if _, err := s.AttachInboundTalk(context.Background(), "talk", d.talk.token, ""); err == nil || !strings.Contains(err.Error(), "negotiation failed") {
		t.Fatalf("negotiation error = %v", err)
	}
	if d.talk.attaching {
		t.Fatal("attaching flag not reset after error")
	}

	// Keep/Detach require an attached browser and the matching ticket.
	if s.KeepInboundTalk("missing", "token") || s.KeepInboundTalk("talk", "wrong") {
		t.Fatal("keep accepted invalid state")
	}
	if s.DetachInboundTalk("missing", "token") || s.DetachInboundTalk("talk", "wrong") {
		t.Fatal("detach accepted invalid state")
	}
	d.talk.token = "ticket"
	d.talk.attached = true
	d.talk.lease = time.Now().Add(-time.Second)
	if !s.KeepInboundTalk("talk", "ticket") {
		t.Fatal("attached talk keep rejected")
	}
	if !s.DetachInboundTalk("talk", "ticket") || d.talk.attached {
		t.Fatal("detach did not reset the talk")
	}
	d.talk.token = "ticket"
	d.talk.attached = true
	s.detachInboundTalk("missing", "ticket")
	s.detachInboundTalk("talk", "wrong")
	s.detachInboundTalk("talk", "ticket")
	if d.talk.attached {
		t.Fatal("detachInboundTalk did not reset the talk")
	}

	// Expiry sweeps reset attached leases and unused tickets.
	attached := m2TalkDialog(t, s, "lease", nil)
	attached.talk.attached = true
	attached.talk.lease = time.Now().Add(-time.Second)
	unused := m2TalkDialog(t, s, "unused", nil)
	unused.talk.token = "stale"
	unused.talk.tokenExpires = time.Now().Add(-time.Second)
	noTalk := s.dialogManager.Create("no-talk-2", "f", "t", "a", "b", "peer")
	_ = noTalk
	s.runInboundTalk(time.Now())
	if attached.talk.attached || attached.talk.token != "" {
		t.Fatal("attached lease not reset")
	}
	if unused.talk.token != "" {
		t.Fatal("expired ticket not cleared")
	}
}

func TestM2VoipSendDTMFBranches(t *testing.T) {
	s := m2UnitServer(t)
	if err := s.SendDTMF("missing", "1"); err == nil {
		t.Fatal("unknown call DTMF accepted")
	}
	s.outbound["dialing"] = &outboundCall{id: "dialing", state: "dialing"}
	if err := s.SendDTMF("dialing", "1"); err == nil {
		t.Fatal("dialing call DTMF accepted")
	}
	s.outbound["no-pub"] = &outboundCall{id: "no-pub", state: DialogStateEstablished}
	if err := s.SendDTMF("no-pub", "1"); err == nil {
		t.Fatal("call without media DTMF accepted")
	}
	s.outbound["no-dtmf"] = &outboundCall{id: "no-dtmf", state: DialogStateEstablished, pub: m2AudioPub(t, false)}
	if err := s.SendDTMF("no-dtmf", "1"); err == nil || !strings.Contains(err.Error(), "telephone-event") {
		t.Fatalf("no DTMF negotiation = %v", err)
	}
	s.outbound["dtmf"] = &outboundCall{id: "dtmf", state: DialogStateEstablished, pub: m2AudioPub(t, true)}
	if err := s.SendDTMF("dtmf", "1"); err != nil {
		t.Fatal(err)
	}

	s.dialogManager.Create("early", "f", "t", "1001", "1002", "peer")
	if err := s.SendDTMF("early", "1"); err == nil {
		t.Fatal("early inbound call DTMF accepted")
	}
	s.dialogManager.UpdateState("early", DialogStateEstablished)
	s.dialogManager.SetPubSession("early", m2AudioPub(t, false))
	if err := s.SendDTMF("early", "1"); err == nil || !strings.Contains(err.Error(), "telephone-event") {
		t.Fatalf("inbound no DTMF = %v", err)
	}
	s.dialogManager.SetPubSession("early", m2AudioPub(t, true))
	if err := s.SendDTMF("early", "1"); err != nil {
		t.Fatal(err)
	}
}

func m2UpdateBody() string {
	return "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 40000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\na=rtcp-mux\r\n"
}

func TestM2VoipRenegotiationBranches(t *testing.T) {
	s, added, _, _ := newTestServer(t, 5000, 15000)
	_, exchangePeer := sipPeer(t, s)
	invite := request("INVITE", "reneg")
	if got := exchangePeer(invite); !strings.Contains(got, "200 OK") {
		t.Fatal(got)
	}
	<-added
	s.handleMessage(inDialogRequest(s, "ACK", "reneg"), TransportAddr{Type: TransportUDP})
	s.requestMu.Lock()
	s.portAllocator = rtpmedia.NewPortAllocator(49000, 49100)
	s.requestMu.Unlock()

	if got := s.handleRenegotiation(&Message{Headers: map[string][]string{"Call-ID": {"unknown"}}}, TransportAddr{}); !strings.Contains(got, "481") {
		t.Fatalf("unknown dialog = %q", got)
	}
	stale := inDialogRequest(s, "UPDATE", "reneg")
	sm, _ := ParseMessage(stale)
	sm.SetHeader("CSeq", "1 UPDATE")
	if got := s.handleRenegotiation(sm, TransportAddr{Type: TransportUDP}); !strings.Contains(got, "500 CSeq Out of Order") {
		t.Fatalf("stale update = %q", got)
	}

	// Pending dialogs cannot renegotiate.
	s.dialogManager.UpdateState("reneg", DialogStateConfirmed)
	mkReneg := func(method Method, seq int, body string) *Message {
		raw := inDialogRequest(s, string(method), "reneg")
		m, err := ParseMessage(raw)
		if err != nil {
			t.Fatal(err)
		}
		m.SetHeader("CSeq", fmt.Sprintf("%d %s", seq, method))
		m.Body = body
		m.SetHeader("Content-Length", fmt.Sprint(len(body)))
		return m
	}
	s.requestMu.Lock()
	s.config.AuthEnable = true
	s.config.Users = []User{{Username: "alice", Password: "pass"}}
	s.requestMu.Unlock()
	if got := s.handleRenegotiation(mkReneg(MethodUpdate, 9, ""), TransportAddr{Type: TransportUDP}); !strings.Contains(got, "401 Unauthorized") {
		t.Fatalf("unauthenticated update = %q", got)
	}
	s.requestMu.Lock()
	s.config.AuthEnable = false
	s.requestMu.Unlock()
	if got := s.handleRenegotiation(mkReneg(MethodUpdate, 10, ""), TransportAddr{Type: TransportUDP}); !strings.Contains(got, "491 Request Pending") {
		t.Fatalf("pending update = %q", got)
	}
	s.dialogManager.UpdateState("reneg", DialogStateEstablished)

	// Empty UPDATE refreshes the target; an empty INVITE requires an offer.
	if got := s.handleRenegotiation(mkReneg(MethodUpdate, 11, ""), TransportAddr{Type: TransportUDP}); !strings.Contains(got, "200 OK") {
		t.Fatalf("empty update = %q", got)
	}
	if got := s.handleRenegotiation(mkReneg(MethodInvite, 12, ""), TransportAddr{Type: TransportUDP}); !strings.Contains(got, "488 SDP Offer Required") {
		t.Fatalf("empty invite = %q", got)
	}

	// Without a replacement hook, offers are refused.
	wm := mkReneg(MethodUpdate, 13, m2UpdateBody())
	if got := s.handleRenegotiation(wm, TransportAddr{Type: TransportUDP}); !strings.Contains(got, "488 Media Replacement Unavailable") {
		t.Fatalf("no replacement hook = %q", got)
	}

	// Invalid offers are refused without disturbing the call.
	s.requestMu.Lock()
	s.onReplaceSession = func(*maxvoip.PubSession, *maxvoip.PubSession) error { return nil }
	s.requestMu.Unlock()
	bm := mkReneg(MethodUpdate, 14, "invalid SDP")
	if got := s.handleRenegotiation(bm, TransportAddr{Type: TransportUDP}); !strings.Contains(got, "488 Media Negotiation Failed") {
		t.Fatalf("invalid offer = %q", got)
	}

	// A failing replacement keeps the old session.
	s.requestMu.Lock()
	s.onReplaceSession = func(*maxvoip.PubSession, *maxvoip.PubSession) error { return fmt.Errorf("replace failed") }
	s.requestMu.Unlock()
	nm := mkReneg(MethodUpdate, 15, m2UpdateBody())
	if got := s.handleRenegotiation(nm, TransportAddr{Type: TransportUDP}); !strings.Contains(got, "500 Media Replacement Failed") {
		t.Fatalf("replace failure = %q", got)
	}

	// A successful UPDATE swaps media and a re-INVITE waits for its ACK.
	s.requestMu.Lock()
	s.onReplaceSession = func(old, fresh *maxvoip.PubSession) error { return nil }
	s.requestMu.Unlock()
	um := mkReneg(MethodUpdate, 16, m2UpdateBody())
	if got := s.handleRenegotiation(um, TransportAddr{Type: TransportUDP}); !strings.Contains(got, "200 OK") {
		t.Fatalf("successful update = %q", got)
	}
	if got := s.handleRenegotiation(mkReneg(MethodInvite, 17, m2UpdateBody()), TransportAddr{Type: TransportUDP}); !strings.Contains(got, "200 OK") {
		t.Fatalf("successful re-invite = %q", got)
	}
	if s.dialogManager.Get("reneg").State != DialogStateConfirmed {
		t.Fatal("re-invite did not wait for ACK")
	}
}

func TestM2VoipUpstreamResponseBranches(t *testing.T) {
	s, _ := m2UpstreamServer(t)
	u := s.upstream
	now := time.Now()
	respond := func(status int, headers map[string]string, addr TransportAddr) bool {
		u.pending = true
		u.cseq++
		return s.handleUpstreamResponse(u.m2Response(t, status, headers), addr, now)
	}
	peer := u.peer

	mismatchCall := u.m2Response(t, 200, nil)
	mismatchCall.SetHeader("Call-ID", "other")
	u.pending = true
	u.cseq++
	if s.handleUpstreamResponse(mismatchCall, peer, now) {
		t.Fatal("mismatched Call-ID accepted")
	}
	call := u.m2Response(t, 200, nil)
	u.pending = false
	if s.handleUpstreamResponse(call, peer, now) {
		t.Fatal("unsolicited response accepted")
	}
	u.pending = true
	if s.handleUpstreamResponse(call, TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:1"}, now) {
		t.Fatal("peer mismatch accepted")
	}
	wrongMethod := u.m2Response(t, 200, nil)
	wrongMethod.SetHeader("CSeq", fmt.Sprintf("%d OPTIONS", u.cseq))
	if s.handleUpstreamResponse(wrongMethod, peer, now) {
		t.Fatal("wrong method accepted")
	}
	wrongCSeq := u.m2Response(t, 200, nil)
	wrongCSeq.SetHeader("CSeq", "99 REGISTER")
	if s.handleUpstreamResponse(wrongCSeq, peer, now) {
		t.Fatal("wrong CSeq accepted")
	}

	if !respond(200, map[string]string{"Expires": "90"}, peer) {
		t.Fatal("200 registration rejected")
	}
	if u.state != "registered" || u.refreshAt.Sub(now) != 72*time.Second || u.expiresAt.Sub(now) != 90*time.Second {
		t.Fatalf("registered state = %+v", u)
	}

	if !respond(200, map[string]string{"Expires": "10"}, peer) || u.refreshAt.Sub(now) != 30*time.Second {
		t.Fatalf("minimum refresh = %v", u.refreshAt.Sub(now))
	}
	if !respond(200, map[string]string{"Expires": "oops"}, peer) || u.expiresAt.Sub(now) != time.Duration(s.config.PbxRegisterExpires)*time.Second {
		t.Fatalf("invalid expires fallback = %v", u.expiresAt.Sub(now))
	}
	if !respond(200, map[string]string{"Expires": "0"}, peer) || u.state != "unregistered" || !u.expiresAt.IsZero() {
		t.Fatalf("unregistered state = %+v", u)
	}

	if !respond(401, map[string]string{"WWW-Authenticate": `Digest realm="r"`}, peer) || !strings.Contains(u.lastError, "unsupported digest challenge") {
		t.Fatalf("missing nonce state = %+v", u)
	}
	if !respond(401, map[string]string{"WWW-Authenticate": `Digest realm="r", nonce="n", algorithm=MD5-sess`}, peer) || !strings.Contains(u.lastError, "algorithm") {
		t.Fatalf("md5-sess state = %+v", u)
	}
	if !respond(407, map[string]string{"Proxy-Authenticate": `Digest realm="r", nonce="n", algorithm=MD5, qop="auth"`}, peer) {
		t.Fatal("407 challenge rejected")
	}
	if !u.proxyAuth || u.state != "registering" || u.nonce != "n" {
		t.Fatalf("challenge state = %+v", u)
	}
	if !respond(500, nil, peer) || u.state != "failed" || !strings.Contains(u.lastError, "SIP 500") {
		t.Fatalf("failure state = %+v", u)
	}
	if !respond(401, map[string]string{"WWW-Authenticate": `Digest realm="r", nonce="n", algorithm=MD5`}, peer) || u.state != "registering" {
		t.Fatalf("md5 challenge state = %+v", u)
	}
}

type m2RecordingConn struct {
	net.Conn
	mu  sync.Mutex
	buf strings.Builder
}

func (c *m2RecordingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.buf.Write(p)
	c.mu.Unlock()
	return c.Conn.Write(p)
}

func (c *m2RecordingConn) recorded() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// m2PipeTransport returns a stream transport whose dialed connections record
// everything written to them, which lets tests inspect outbound REGISTERs
// without real PBX sockets.
func m2PipeTransport(t *testing.T, typ TransportType) (*TCPTransport, func() string) {
	t.Helper()
	recorder := &m2RecordingConn{}
	tt := &TCPTransport{
		transportType: typ,
		stopChan:      make(chan struct{}),
		log:           nazalog.GetGlobalLogger(),
		dial: func(string) (net.Conn, error) {
			client, server := net.Pipe()
			go func() {
				_, _ = io.Copy(io.Discard, server)
				_ = server.Close()
			}()
			recorder.Conn = client
			return recorder, nil
		},
	}
	t.Cleanup(func() { _ = tt.Close() })
	return tt, recorder.recorded
}

func m2UpstreamServer(t *testing.T) (*Server, func() string) {
	t.Helper()
	s := m2UnitServer(t)
	s.config.PbxServer = "127.0.0.1:5060"
	s.config.PbxTransport = "udp"
	s.config.PbxUsername = "1001"
	s.config.PbxPassword = "pass"
	s.config.PbxDomain = "pbx.test"
	s.config.PbxRegisterExpires = 3600
	s.listenAddr = "0.0.0.0:5070"
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = sender.Close()
	})
	s.udpTransport = NewUDPTransport(sender, s.log)
	read := func() string {
		buf := make([]byte, 8192)
		if err := listener.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		n, _, err := listener.ReadFromUDP(buf)
		if err != nil {
			t.Fatal(err)
		}
		return string(buf[:n])
	}
	s.upstream = &upstreamRegistration{
		peer: TransportAddr{Type: TransportUDP, Addr: listener.LocalAddr().String()},
		state: "registering", callID: "up-call@lalmax-nvr", fromTag: "from-tag", requestedExpires: 3600,
	}
	return s, read
}

func (u *upstreamRegistration) m2Response(t *testing.T, status int, headers map[string]string) *Message {
	t.Helper()
	m, err := ParseMessage(fmt.Sprintf("SIP/2.0 %d Test\r\nVia: SIP/2.0/UDP 127.0.0.1:5070;branch=b\r\nFrom: <sip:1001@pbx.test>;tag=%s\r\nTo: <sip:1001@pbx.test>;tag=pbx\r\nCall-ID: %s\r\nCSeq: %d REGISTER\r\n\r\n", status, u.fromTag, u.callID, u.cseq))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		m.SetHeader(k, v)
	}
	return m
}

func TestM2VoipUpstreamRegistrationBranches(t *testing.T) {
	t.Run("run state machine", func(t *testing.T) {
		bare := m2UnitServer(t)
		bare.runUpstreamRegistration(time.Now())

		s, read := m2UpstreamServer(t)
		s.upstream.pending = true
		s.upstream.deadline = time.Now().Add(time.Minute)
		s.runUpstreamRegistration(time.Now())
		if !s.upstream.pending {
			t.Fatal("pending registration timed out early")
		}
		s.upstream.deadline = time.Now().Add(-time.Second)
		s.runUpstreamRegistration(time.Now())
		if s.upstream.state != "failed" || s.upstream.lastError != "PBX registration timed out" {
			t.Fatalf("timeout state = %+v", s.upstream)
		}
		now := time.Now()
		s.upstream.nextAttempt = time.Time{}
		s.runUpstreamRegistration(now)
		if !strings.Contains(read(), "REGISTER sip:pbx.test") {
			t.Fatal("registration not retried after timeout")
		}
	})

	t.Run("transport missing", func(t *testing.T) {
		s, _ := m2UpstreamServer(t)
		s.upstream.peer.Type = TransportWS
		s.sendUpstreamRegister(s.upstream, 3600, time.Now())
		if s.upstream.state != "failed" || !strings.Contains(s.upstream.lastError, "transport is unavailable") {
			t.Fatalf("missing transport = %+v", s.upstream)
		}
	})

	t.Run("digest errors", func(t *testing.T) {
		s, read := m2UpstreamServer(t)
		s.upstream.nonce, s.upstream.realm, s.upstream.qop = "nonce", "realm", "bogus"
		s.sendUpstreamRegister(s.upstream, 3600, time.Now())
		if s.upstream.state != "failed" {
			t.Fatalf("bad qop state = %+v", s.upstream)
		}

		s.upstream.qop = "auth"
		s.upstream.proxyAuth = true
		s.upstream.nonce = "nonce"
		s.sendUpstreamRegister(s.upstream, 3600, time.Now())
		request := read()
		if !strings.Contains(request, "Proxy-Authorization: Digest") {
			t.Fatalf("proxy authorization missing: %q", request)
		}
	})

	t.Run("normal and unregister", func(t *testing.T) {
		s, read := m2UpstreamServer(t)
		s.sendUpstreamRegister(s.upstream, 3600, time.Now())
		request := read()
		if !strings.Contains(request, "REGISTER sip:pbx.test SIP/2.0") || !strings.Contains(request, "Via: SIP/2.0/UDP 127.0.0.1:5070") {
			t.Fatalf("register request = %q", request)
		}
		if !s.upstream.pending || s.upstream.state != "registering" {
			t.Fatalf("pending state = %+v", s.upstream)
		}

		s.sendUpstreamRegister(s.upstream, 0, time.Now())
		if got := read(); !strings.Contains(got, "Expires: 0") {
			t.Fatalf("unregister request = %q", got)
		}
		if s.upstream.state != "unregistering" {
			t.Fatalf("unregister state = %s", s.upstream.state)
		}

		s.upstream.peer.Type = TransportWS
		s.sendUpstreamRegister(s.upstream, 60, time.Now())
		if s.upstream.state != "failed" {
			t.Fatalf("send failure state = %+v", s.upstream)
		}
	})

	t.Run("contact defaults and tcp tls", func(t *testing.T) {
		s, read := m2UpstreamServer(t)
		s.listenAddr = ""
		s.config.SipIP = ""
		s.sendUpstreamRegister(s.upstream, 3600, time.Now())
		if !strings.Contains(read(), "Contact: <sip:1001@127.0.0.1") {
			t.Fatalf("contact fallback = %q", read())
		}

		tcpTransport, tcpRead := m2PipeTransport(t, TransportTCP)
		s.pbxTCPTransport = tcpTransport
		s.upstream.peer.Type = TransportTCP
		s.upstream.nonce = ""
		s.sendUpstreamRegister(s.upstream, 3600, time.Now())
		if got := tcpRead(); !strings.Contains(got, "REGISTER sip:pbx.test;transport=tcp") {
			t.Fatalf("tcp register = %q", got)
		}

		tlsTransport, tlsRead := m2PipeTransport(t, TransportTLS)
		s.pbxTLSTransport = tlsTransport
		s.upstream.peer.Type = TransportTLS
		s.sendUpstreamRegister(s.upstream, 3600, time.Now())
		if got := tlsRead(); !strings.Contains(got, "REGISTER sips:pbx.test") || !strings.Contains(got, "Contact: <sips:1001@") {
			t.Fatalf("tls register = %q", got)
		}
	})
}

func TestM2VoipStatusEndpointsAndUpstream(t *testing.T) {
	s := m2UnitServer(t)
	s.registrar.Register("2002", "<sip:2002@127.0.0.1>", "ua", 3600)
	s.registrar.SetPeer("2002", TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:5062"})
	s.registrar.Register("1001", "<sip:1001@127.0.0.1>", "ua", 3600)
	s.registrar.SetPeer("1001", TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:5061"})
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	s.udpTransport = NewUDPTransport(conn, s.log)
	expires := time.Now().Add(time.Hour)
	s.upstream = &upstreamRegistration{peer: TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:5060"}, state: "registered", expiresAt: expires, lastError: "none"}
	s.config.PbxServer = "127.0.0.1:5060"
	s.config.PbxTransport = "udp"
	s.config.PbxUsername = "1001"

	status := s.Status()
	if len(status.Endpoints) != 2 || status.Endpoints[0].User != "1001" || status.Endpoints[1].User != "2002" {
		t.Fatalf("endpoints = %+v", status.Endpoints)
	}
	if !status.UpstreamRegistration.Configured || status.UpstreamRegistration.State != "registered" || status.UpstreamRegistration.ExpiresAt == nil {
		t.Fatalf("upstream status = %+v", status.UpstreamRegistration)
	}
}

func TestM2VoipTransactionBounds(t *testing.T) {
	s := m2UnitServer(t)
	req, _ := ParseMessage("REGISTER sip:x SIP/2.0\r\nVia: SIP/2.0/UDP 127.0.0.1:9;branch=b\r\nFrom: <sip:a@b>;tag=f\r\nTo: <sip:a@b>\r\nCall-ID: c\r\nCSeq: 1 REGISTER\r\n\r\n")
	response := BuildResponse(req, 200, "OK", "")
	for i := 0; i < 4097; i++ {
		s.transactions[fmt.Sprintf("k-%d", i)] = &serverTransaction{request: req, accepted: true}
	}
	s.rememberResponse(req, response, TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"})
	if len(s.transactions) != 4097 {
		t.Fatalf("transaction bound not enforced: %d", len(s.transactions))
	}

	// Invalid responses are ignored by both helpers.
	fresh := m2UnitServer(t)
	invite, _ := ParseMessage("INVITE sip:x SIP/2.0\r\nVia: SIP/2.0/UDP 127.0.0.1:9;branch=i\r\nFrom: <sip:a@b>;tag=f\r\nTo: <sip:a@b>\r\nCall-ID: inv\r\nCSeq: 1 INVITE\r\n\r\n")
	fresh.rememberResponse(invite, "", TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"})
	fresh.rememberResponse(invite, BuildResponse(invite, 180, "Ringing", ""), TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"})
	fresh.updateInviteResponse(&Dialog{Invite: invite}, "")

	// An ACK for an established dialog is ignored.
	s.dialogManager.Create("acked", "f", "t", "a", "b", "peer")
	s.dialogManager.SetInvite("acked", invite, TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"})
	s.dialogManager.UpdateState("acked", DialogStateEstablished)
	ack := &Message{IsRequest: true, Method: MethodAck, Headers: map[string][]string{
		"Call-ID": {"acked"}, "From": {"<sip:a@b>;tag=f"}, "To": {"<sip:c@d>;tag=t"}, "CSeq": {"1 ACK"},
	}}
	s.acknowledgeTransaction(ack)
}

func TestM2VoipServerRemainingBranches(t *testing.T) {
	t.Run("register missing user", func(t *testing.T) {
		s, _, _, _ := newTestServer(t, 5000, 15000)
		_, exchangePeer := sipPeer(t, s)
		raw := request("REGISTER", "bad-register")
		raw = strings.Replace(raw, "From: <sip:alice@127.0.0.1>;tag=caller", "From: <>;tag=caller", 1)
		if got := exchangePeer(raw); !strings.Contains(got, "400 Bad Request") {
			t.Fatalf("missing from user = %q", got)
		}
	})

	t.Run("text only offer", func(t *testing.T) {
		s, _, _, _ := newTestServer(t, 5000, 15000)
		_, exchangePeer := sipPeer(t, s)
		body := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=text 40000 RTP/AVP 99\r\na=rtpmap:99 text/plain\r\n"
		m, _ := ParseMessage(request("INVITE", "text-only"))
		m.Body = body
		m.SetHeader("Content-Length", fmt.Sprint(len(body)))
		if got := exchangePeer(serializePeerRequest(m)); !strings.Contains(got, "488 Media Negotiation Failed") {
			t.Fatalf("text offer = %q", got)
		}
	})

	t.Run("video srtp key failure", func(t *testing.T) {
		s, err := NewServer(ServerConfig{Config: Config{SipListenAddr: "127.0.0.1:0", MediaIP: "127.0.0.1", MediaPortMin: 47400, MediaPortMax: 47500, SrtpEnable: true, SrtpMandatory: true}})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		_, exchangePeer := sipPeer(t, s)
		body := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
			"m=audio 40000 RTP/SAVP 8\r\na=rtpmap:8 PCMA/8000\r\na=rtcp-mux\r\na=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:" + m2CryptoKey() + "\r\n" +
			"m=video 40002 RTP/SAVP 96\r\na=rtpmap:96 H264/90000\r\na=rtcp-mux\r\na=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:bad\r\n"
		m, _ := ParseMessage(request("INVITE", "video-key"))
		m.Body = body
		m.SetHeader("Content-Length", fmt.Sprint(len(body)))
		if got := exchangePeer(serializePeerRequest(m)); !strings.Contains(got, "500") {
			t.Fatalf("video key failure = %q", got)
		}
	})

	t.Run("dtls and bundle", func(t *testing.T) {
		s, err := NewServer(ServerConfig{Config: Config{
			SipListenAddr: "127.0.0.1:0", MediaIP: "127.0.0.1", MediaPortMin: 47600, MediaPortMax: 47700,
			SrtpEnable: true, SrtpMandatory: true, BundleEnable: true,
		}})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		_, exchangePeer := sipPeer(t, s)
		body := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
			"a=group:BUNDLE 0 1\r\n" +
			"a=fingerprint:sha-256 " + s.dtlsFingerprint + "\r\n" +
			"m=audio 40000 UDP/TLS/RTP/SAVPF 8\r\na=rtpmap:8 PCMA/8000\r\na=rtcp-mux\r\na=mid:0\r\na=setup:actpass\r\n" +
			"m=video 40002 UDP/TLS/RTP/SAVPF 96\r\na=rtpmap:96 H264/90000\r\na=rtcp-mux\r\na=mid:1\r\na=setup:actpass\r\n"
		m, _ := ParseMessage(request("INVITE", "dtls-bundle"))
		m.Body = body
		m.SetHeader("Content-Length", fmt.Sprint(len(body)))
		got := exchangePeer(serializePeerRequest(m))
		if !strings.Contains(got, "200 OK") {
			t.Fatalf("dtls bundle = %q", got)
		}
		if !strings.Contains(got, "a=group:BUNDLE 0 1") || strings.Count(got, "a=fingerprint:") != 2 {
			t.Fatalf("dtls bundle answer = %q", got)
		}
	})

	t.Run("handle message stopped", func(t *testing.T) {
		s := m2UnitServer(t)
		_ = s.Close()
		s.handleMessage(request("OPTIONS", "stopped"), TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:1"})
		waiting := m2UnitServer(t)
		waiting.ready = make(chan struct{})
		_ = waiting.Close()
		waiting.handleMessage(request("OPTIONS", "stopped"), TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:1"})
	})

	t.Run("terminate unknowns", func(t *testing.T) {
		s := m2UnitServer(t)
		s.terminate("missing", "failed", "no dialog")
		d := s.dialogManager.Create("with-terminate", "f", "t", "a", "b", "peer")
		called := false
		d.OnTerminate = func() { called = true }
		s.terminate("with-terminate", "completed", "done")
		if !called {
			t.Fatal("OnTerminate not invoked")
		}
	})

	t.Run("udp read loop error", func(t *testing.T) {
		s := m2UnitServer(t)
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		s.conn = conn
		_ = conn.Close()
		s.wg.Add(1)
		go s.runReceive()
		time.Sleep(20 * time.Millisecond)
		_ = s.Close()
	})

	t.Run("auth malformed", func(t *testing.T) {
		cfg := Config{SipListenAddr: "127.0.0.1:0", MediaIP: "127.0.0.1", MediaPortMin: 47800, MediaPortMax: 47900,
			AuthEnable: true, Realm: "lalmax-nvr", Users: []User{{Username: "1001", Password: "pass"}}}
		s, err := NewServer(ServerConfig{Config: cfg})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		_, exchangePeer := sipPeer(t, s)
		raw := request("REGISTER", "auth-malformed")
		m, _ := ParseMessage(raw)
		m.SetHeader("Authorization", "nonsense")
		if got := exchangePeer(serializePeerRequest(m)); !strings.Contains(got, "401") {
			t.Fatalf("malformed auth = %q", got)
		}
		m.SetHeader("Authorization", `Digest username="other", realm="lalmax-nvr", nonce="n", uri="sip:x", response="x"`)
		if got := exchangePeer(serializePeerRequest(m)); !strings.Contains(got, "401") {
			t.Fatalf("mismatched auth = %q", got)
		}
	})
}

func TestM2VoipOutboundMisc(t *testing.T) {
	t.Run("dial security default", func(t *testing.T) {
		s, err := NewServer(ServerConfig{Config: Config{SipListenAddr: "127.0.0.1:0", MediaIP: "127.0.0.1", MediaPortMin: 48000, MediaPortMax: 48100, SrtpEnable: true, SrtpMandatory: true}})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		s.requestMu.Lock()
		s.registrar.Register("1001", "<sip:1001@127.0.0.1:9>", "ua", 3600)
		s.registrar.SetPeer("1001", TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"})
		s.requestMu.Unlock()
		if _, err := s.Dial("1001", "", browserTalkOffer); err != nil {
			t.Fatal(err)
		}
		s.requestMu.Lock()
		for _, c := range s.outbound {
			if c.security != "dtls" {
				t.Fatalf("default security = %s", c.security)
			}
		}
		s.requestMu.Unlock()
	})

	t.Run("attach talk errors", func(t *testing.T) {
		s := m2UnitServer(t)
		if _, err := s.AttachTalk(context.Background(), "missing", "token", ""); err == nil {
			t.Fatal("unknown attach accepted")
		}
		call := &outboundCall{id: "attach", token: "token", state: "dialing"}
		s.outbound[call.id] = call
		if _, err := s.AttachTalk(context.Background(), call.id, "token", ""); err == nil {
			t.Fatal("dialing attach accepted")
		}
		call.state = DialogStateEstablished
		if _, err := s.AttachTalk(context.Background(), call.id, "wrong", ""); err == nil {
			t.Fatal("wrong token attach accepted")
		}
	})

	t.Run("call status fields", func(t *testing.T) {
		s := m2UnitServer(t)
		call := m2OutboundCall(t, s, "status-call")
		call.state = DialogStateEstablished
		call.pub = m2AudioPub(t, true)
		call.browser = nil
		started := time.Now().Add(-2 * time.Minute)
		call.created = started
		call.endedAt = started.Add(30 * time.Second)
		status := call.status(time.Now())
		if status.DurationSeconds != 30 || status.AudioCodec != "PCMA" || !status.DTMFAvailable || status.StreamID == "" {
			t.Fatalf("call status = %+v", status)
		}
	})

	t.Run("matching uri helpers", func(t *testing.T) {
		if !sameSIPURIIgnoringTransport("sip:a@b;transport=tcp", "sip:a@b") {
			t.Fatal("transport parameter not ignored")
		}
		if sameSIPURIIgnoringTransport("sip:a@b", "sip:c@b") {
			t.Fatal("different URIs matched")
		}
		if !validPBXExtension("1001-_+.a") || validPBXExtension(strings.Repeat("a", 129)) || validPBXExtension("bad extension") {
			t.Fatal("PBX extension validation wrong")
		}
		if got := contactAddress("<sip:1001@192.0.2.1:5060;transport=tcp>"); got != "1001@192.0.2.1:5060" {
			t.Fatalf("contact address = %q", got)
		}
	})
}
