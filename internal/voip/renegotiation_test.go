package voip

import (
	"encoding/binary"
	"github.com/q191201771/lal/pkg/base"
	maxvoip "github.com/q191201771/lalmax/voip"
	"github.com/q191201771/lalmax/voip/media"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func renegotiationServer(t *testing.T) (*Server, chan *maxvoip.PubSession, chan base.AvPacket) {
	t.Helper()
	s, added, packets, _ := newTestServer(t, 5000, 200)
	s.requestMu.Lock()
	s.portAllocator = media.NewPortAllocator(43000, 43200)
	s.onReplaceSession = func(old, next *maxvoip.PubSession) error {
		next.SetOnAvPacket(func(p base.AvPacket) { packets <- p })
		added <- next
		return nil
	}
	s.requestMu.Unlock()
	return s, added, packets
}
func sendAudio(t *testing.T, port int) {
	t.Helper()
	c, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for seq := uint16(1); seq < 5; seq++ {
		p := make([]byte, 172)
		p[0] = 0x80
		p[1] = 8
		binary.BigEndian.PutUint16(p[2:], seq)
		binary.BigEndian.PutUint32(p[4:], uint32(seq)*160)
		binary.BigEndian.PutUint32(p[8:], 42)
		_, _ = c.Write(p)
	}
}
func TestRenegotiationHoldResumeAndVideo(t *testing.T) {
	s, added, packets := renegotiationServer(t)
	id := "hold-resume"
	answer := exchange(t, s, request("INVITE", id))
	if !strings.Contains(answer, "200 OK") {
		t.Fatal(answer)
	}
	old := <-added
	s.handleMessage(inDialogRequest(s, "ACK", id), TransportAddr{Type: TransportUDP})
	epoch := s.dialogManager.Get(id).MediaCreatedAt
	hold := strings.Replace(inDialogRequest(s, "INVITE", id), "a=rtcp-mux\r\n", "a=rtcp-mux\r\na=inactive\r\n", 1)
	m, _ := ParseMessage(hold)
	hold = serializePeerRequest(m)
	heldAnswer := exchange(t, s, hold)
	if !strings.Contains(heldAnswer, "200 OK") || !strings.Contains(heldAnswer, "a=inactive") {
		t.Fatal(heldAnswer)
	}
	next := <-added
	if next.AudioPort() == old.AudioPort() {
		t.Fatal("replacement must prepare its own media before disposal")
	}
	if again := exchange(t, s, hold); again != heldAnswer {
		t.Fatal("retransmission changed held answer")
	}
	s.handleMessage(inDialogRequest(s, "ACK", id), TransportAddr{Type: TransportUDP})
	time.Sleep(1200 * time.Millisecond)
	if d := s.dialogManager.Get(id); d == nil || !d.Held {
		t.Fatal("held call expired as media-idle")
	}
	s.handleRtpTimeout(id, epoch)
	if s.dialogManager.Get(id) == nil {
		t.Fatal("old receiver timeout terminated replacement")
	}
	resume := inDialogRequest(s, "INVITE", id)
	m, _ = ParseMessage(resume)
	m.Body += "m=video 40002 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=rtcp-mux\r\n"
	resume = serializePeerRequest(m)
	if r := exchange(t, s, resume); !strings.Contains(r, "200 OK") || !strings.Contains(r, "m=video") {
		t.Fatal(r)
	}
	video := <-added
	s.handleMessage(inDialogRequest(s, "ACK", id), TransportAddr{Type: TransportUDP})
	if video.VideoPort() == 0 || s.Status().Calls[0].Held {
		t.Fatal("video upgrade/hold state incorrect")
	}
	sendAudio(t, video.AudioPort())
	select {
	case p := <-packets:
		if p.PayloadType != base.AvPacketPtG711A {
			t.Fatal(p.PayloadType)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement received no audio")
	}
	if !s.Hangup(id) || s.Hangup(id) {
		t.Fatal("hangup must be idempotent at API boundary")
	}
}
func TestRejectedRenegotiationPreservesCallAndUpdateRefresh(t *testing.T) {
	s, added, _ := renegotiationServer(t)
	id := "reject-update"
	_ = exchange(t, s, request("INVITE", id))
	old := <-added
	s.handleMessage(inDialogRequest(s, "ACK", id), TransportAddr{Type: TransportUDP})
	raw := inDialogRequest(s, "INVITE", id)
	m, _ := ParseMessage(raw)
	m.Body = "invalid SDP"
	raw = serializePeerRequest(m)
	if r := exchange(t, s, raw); !strings.Contains(r, "488") {
		t.Fatal(r)
	}
	if s.dialogManager.Get(id).PubSession != old {
		t.Fatal("rejected offer replaced original media")
	}
	if r := exchange(t, s, inDialogRequest(s, "UPDATE", id)); !strings.Contains(r, "200 OK") {
		t.Fatal(r)
	}
	if s.dialogManager.Get(id).State != DialogStateEstablished {
		t.Fatal("UPDATE incorrectly waits for ACK")
	}
}
func TestStatusAndHangupConcurrentClose(t *testing.T) {
	s, added, _ := renegotiationServer(t)
	_ = exchange(t, s, request("REGISTER", "register-status"))
	_ = exchange(t, s, request("INVITE", "status-call"))
	<-added
	status := s.Status()
	if len(status.Endpoints) != 1 || len(status.Calls) != 1 || status.Calls[0].AudioCodec != "PCMA" {
		t.Fatalf("%+v", status)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 20; n++ {
				s.Status()
				s.Hangup("status-call")
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); s.Close() }()
	wg.Wait()
	if s.Status().Enabled || len(s.Status().Calls) > 0 {
		t.Fatal("closed status still active")
	}
}
