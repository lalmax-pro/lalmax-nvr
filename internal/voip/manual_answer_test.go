package voip

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	maxvoip "github.com/q191201771/lalmax/voip"
)

func newManualAnswerServer(t *testing.T) (*Server, chan *maxvoip.PubSession) {
	t.Helper()
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()
	added := make(chan *maxvoip.PubSession, 2)
	s, err := NewServer(ServerConfig{Config: Config{SipListenAddr: "127.0.0.1:0", MediaIP: "127.0.0.1", MediaPortMin: port, MediaPortMax: port, ManualAnswer: true, RingTimeoutMs: 5000},
		OnPubSession: func(p *maxvoip.PubSession) error { added <- p; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, added
}

func TestManualIncomingCallRingTimeoutAndRegistrationStatus(t *testing.T) {
	s, _ := newManualAnswerServer(t)
	s.config.RingTimeoutMs = 1000
	_, exchangePeer := sipPeer(t, s)
	if response := exchangePeer(request("INVITE", "manual-timeout")); !strings.Contains(response, "180 Ringing") {
		t.Fatalf("expected ringing before timeout, got %s", response)
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.dialogManager.Get("manual-timeout") != nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s.dialogManager.Get("manual-timeout") != nil {
		t.Fatal("unanswered call did not time out")
	}

	data, err := json.Marshal(s.Status().UpstreamRegistration)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "expires_at") {
		t.Fatalf("unregistered PBX status should omit expiry, got %s", data)
	}
}

func sipPeer(t *testing.T, s *Server) (*net.UDPConn, func(string) string) {
	t.Helper()
	serverAddr := s.conn.LocalAddr().(*net.UDPAddr)
	peer, err := net.DialUDP("udp4", nil, serverAddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	if err = peer.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	exchange := func(raw string) string {
		if _, e := peer.Write([]byte(raw)); e != nil {
			t.Fatal(e)
		}
		buf := make([]byte, 8192)
		n, e := peer.Read(buf)
		if e != nil {
			t.Fatal(e)
		}
		return string(buf[:n])
	}
	return peer, exchange
}

func TestManualIncomingCallAnswerAndReject(t *testing.T) {
	s, added := newManualAnswerServer(t)
	peer, exchangePeer := sipPeer(t, s)
	invite := request("INVITE", "manual-answer")
	if response := exchangePeer(invite); !strings.Contains(response, "180 Ringing") {
		t.Fatalf("expected provisional ringing response, got %s", response)
	}
	status := s.Status()
	if len(status.Calls) != 1 || !status.Calls[0].IncomingPending || len(added) != 0 {
		t.Fatalf("call should be pending without media: %+v", status)
	}
	if err := s.AnswerIncomingCall("manual-answer"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8192)
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := peer.Read(buf)
	if err != nil || !strings.Contains(string(buf[:n]), "200 OK") {
		t.Fatalf("expected final answer, response=%q err=%v", buf[:n], err)
	}
	if len(added) != 1 {
		t.Fatal("accepting the call should publish its media")
	}
	s.handleMessage(inDialogRequest(s, "ACK", "manual-answer"), TransportAddr{Type: TransportUDP})
	if got := s.Status().Calls[0].State; got != DialogStateEstablished {
		t.Fatalf("answer did not establish dialog: %s", got)
	}

	peer, exchangePeer = sipPeer(t, s)
	if response := exchangePeer(request("INVITE", "manual-reject")); !strings.Contains(response, "180 Ringing") {
		t.Fatalf("expected ringing before rejection, got %s", response)
	}
	if err := s.RejectIncomingCall("manual-reject"); err != nil {
		t.Fatal(err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err = peer.Read(buf)
	if err != nil || !strings.Contains(string(buf[:n]), "603 Decline") {
		t.Fatalf("expected final rejection, response=%q err=%v", buf[:n], err)
	}

	peer, exchangePeer = sipPeer(t, s)
	if response := exchangePeer(request("INVITE", "manual-cancel")); !strings.Contains(response, "180 Ringing") {
		t.Fatalf("expected ringing before CANCEL, got %s", response)
	}
	if _, err = peer.Write([]byte(request("CANCEL", "manual-cancel"))); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"487 Request Terminated", "200 OK"} {
		if err = peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		n, err = peer.Read(buf)
		if err != nil || !strings.Contains(string(buf[:n]), want) {
			t.Fatalf("expected %q after CANCEL, response=%q err=%v", want, buf[:n], err)
		}
	}
	if dialog := s.dialogManager.Get("manual-cancel"); dialog != nil {
		t.Fatal("cancelled pending call was not removed")
	}
}
