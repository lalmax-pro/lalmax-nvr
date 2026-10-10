package voip

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/q191201771/lal/pkg/base"
	maxvoip "github.com/q191201771/lalmax/voip"
)

func newTestServer(t *testing.T, ack, rtp int) (*Server, chan *maxvoip.PubSession, chan base.AvPacket, chan struct{}) {
	t.Helper()
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	port := socket.LocalAddr().(*net.UDPAddr).Port
	socket.Close()
	added := make(chan *maxvoip.PubSession, 8)
	packets := make(chan base.AvPacket, 32)
	removed := make(chan struct{}, 8)
	server, err := NewServer(ServerConfig{
		Config: Config{SipListenAddr: "127.0.0.1:0", MediaIP: "203.0.113.10", MediaPortMin: port, MediaPortMax: port, AckTimeoutMs: ack, RtpTimeoutMs: rtp},
		OnPubSession: func(session *maxvoip.PubSession) error {
			session.SetOnAvPacket(func(pkt base.AvPacket) {
				select {
				case packets <- pkt:
				default:
				}
			})
			added <- session
			return nil
		},
		OnDelSession: func(session *maxvoip.PubSession) error { removed <- struct{}{}; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	return server, added, packets, removed
}

func request(method, callID string) string {
	body := ""
	if method == "INVITE" {
		body = "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 40000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\na=rtcp-mux\r\n"
	}
	return fmt.Sprintf("%s sip:door@127.0.0.1 SIP/2.0\r\nVia: SIP/2.0/UDP 127.0.0.1:9999;branch=z9hG4bK-test\r\nFrom: <sip:alice@127.0.0.1>;tag=caller\r\nTo: <sip:door@127.0.0.1>\r\nCall-ID: %s\r\nCSeq: 1 %s\r\nContent-Type: application/sdp\r\nContent-Length: %d\r\n\r\n%s", method, callID, method, len(body), body)
}

func exchange(t *testing.T, s *Server, raw string) string {
	t.Helper()
	conn, err := net.DialUDP("udp", nil, s.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = conn.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8192)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return string(buf[:n])
}

func inDialogRequest(s *Server, method, callID string) string {
	m, _ := ParseMessage(request(method, callID))
	if d := s.dialogManager.Get(callID); d != nil {
		m.SetHeader("To", m.To()+";tag="+d.ToTag)
		seq := d.InviteCSeq
		if method != "ACK" {
			seq = d.RemoteCSeq + 1
		}
		m.SetHeader("CSeq", fmt.Sprintf("%d %s", seq, method))
	}
	return serializePeerRequest(m)
}

func waitRemoved(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(4 * time.Second):
		t.Fatal("session cleanup timed out")
	}
}

func TestInboundCallRTPRetransmissionAndBye(t *testing.T) {
	s, added, packets, removed := newTestServer(t, 5000, 15000)
	if got := exchange(t, s, request("REGISTER", "register")); !strings.Contains(got, "200 OK") {
		t.Fatal(got)
	}
	invite := request("INVITE", "call-rtp")
	answer := exchange(t, s, invite)
	if !strings.Contains(answer, "200 OK") || !strings.Contains(answer, "c=IN IP4 203.0.113.10") {
		t.Fatal(answer)
	}
	session := <-added
	if again := exchange(t, s, invite); again != answer {
		t.Fatalf("retransmission changed answer: %s", again)
	}
	if len(added) != 0 {
		t.Fatal("retransmission created another publisher")
	}
	s.handleMessage(inDialogRequest(s, "ACK", "call-rtp"), TransportAddr{Type: TransportUDP})
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: session.AudioPort()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for seq := uint16(1); seq <= 3; seq++ {
		pkt := make([]byte, 172)
		pkt[0] = 0x80
		pkt[1] = 8
		binary.BigEndian.PutUint16(pkt[2:], seq)
		binary.BigEndian.PutUint32(pkt[4:], uint32(seq)*160)
		binary.BigEndian.PutUint32(pkt[8:], 42)
		if _, err := conn.Write(pkt); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case pkt := <-packets:
		if pkt.PayloadType != base.AvPacketPtG711A || len(pkt.Payload) != 160 {
			t.Fatalf("unexpected packet: %+v", pkt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RTP did not reach publisher")
	}
	if got := exchange(t, s, inDialogRequest(s, "BYE", "call-rtp")); !strings.Contains(got, "200 OK") {
		t.Fatal(got)
	}
	waitRemoved(t, removed)
	if s.dialogManager.Get("call-rtp") != nil {
		t.Fatal("dialog leaked")
	}
	if _, err := s.portAllocator.Alloc(); err != nil {
		t.Fatalf("media port leaked: %v", err)
	}
}

func TestCallTimeoutsAndClose(t *testing.T) {
	for _, kind := range []string{"ack", "rtp", "close"} {
		t.Run(kind, func(t *testing.T) {
			ack, rtp := 5000, 15000
			if kind == "ack" {
				ack = 100
			}
			if kind == "rtp" {
				rtp = 100
			}
			s, added, _, removed := newTestServer(t, ack, rtp)
			if answer := exchange(t, s, request("INVITE", kind)); !strings.Contains(answer, "200 OK") {
				t.Fatal(answer)
			}
			<-added
			if kind == "rtp" {
				s.handleMessage(inDialogRequest(s, "ACK", kind), TransportAddr{Type: TransportUDP})
			}
			if kind == "close" {
				var wg sync.WaitGroup
				for i := 0; i < 4; i++ {
					wg.Add(1)
					go func() { defer wg.Done(); s.Close() }()
				}
				wg.Wait()
			}
			waitRemoved(t, removed)
			if len(s.dialogManager.List()) != 0 {
				t.Fatal("dialog leaked")
			}
		})
	}
}

func TestCancelAfterFinalInviteDoesNotHangUp(t *testing.T) {
	s, added, _, removed := newTestServer(t, 5000, 15000)
	if got := exchange(t, s, request("CANCEL", "unknown")); !strings.Contains(got, "481") {
		t.Fatal(got)
	}
	if got := exchange(t, s, request("INVITE", "cancel-final")); !strings.Contains(got, "200 OK") {
		t.Fatal(got)
	}
	<-added
	for _, established := range []bool{false, true} {
		if established {
			s.handleMessage(inDialogRequest(s, "ACK", "cancel-final"), TransportAddr{Type: TransportUDP})
		}
		if got := exchange(t, s, request("CANCEL", "cancel-final")); !strings.Contains(got, "200 OK") {
			t.Fatal(got)
		}
		if s.dialogManager.Get("cancel-final") == nil || len(removed) != 0 {
			t.Fatal("CANCEL ended a call after its final INVITE response")
		}
	}
	exchange(t, s, inDialogRequest(s, "BYE", "cancel-final"))
	waitRemoved(t, removed)
}

func TestCloseWithTCPRequestsInFlight(t *testing.T) {
	s, err := NewServer(ServerConfig{Config: Config{SipListenAddr: "127.0.0.1:0", SipTcpListenAddr: "127.0.0.1:0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	conn, err := net.Dial("tcp", s.tcpTransport.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(started)
		for i := 0; i < 20; i++ {
			if _, err := conn.Write([]byte(request("OPTIONS", "tcp"))); err != nil {
				return
			}
		}
	}()
	<-started
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("TCP shutdown deadlocked")
	}
	wg.Wait()
	// A send after close must not resurrect a transport handler.
	if err := s.tcpTransport.Send([]byte("ignored"), TransportAddr{Type: TransportTCP, Addr: s.tcpTransport.listener.Addr().String()}); err == nil {
		t.Fatal("closed transport accepted send")
	}
}
