package voip

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestACKTimeoutNotifiesBYEAndRetriesUntilResponse(t *testing.T) {
	s, _, _, removed := newTestServer(t, 100, 15000)
	peer, err := net.DialUDP("udp", nil, s.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	read := func() string {
		t.Helper()
		peer.SetReadDeadline(time.Now().Add(2 * time.Second))
		b := make([]byte, 8192)
		n, err := peer.Read(b)
		if err != nil {
			t.Fatal(err)
		}
		return string(b[:n])
	}
	peer.Write([]byte(request("INVITE", "timeout-notify")))
	answer, _ := ParseMessage(read())
	first := read()
	bye, err := ParseMessage(first)
	if err != nil {
		t.Fatal(err)
	}
	if !bye.IsRequest || bye.Method != MethodBye || ExtractTag(bye.From()) != ExtractTag(answer.To()) || ExtractTag(bye.To()) != "caller" {
		t.Fatal("timeout BYE has wrong dialog identity")
	}
	waitRemoved(t, removed)
	if second := read(); second != first {
		t.Fatal("lost BYE was not retransmitted unchanged")
	}
	peer.Write([]byte(BuildResponse(bye, 200, "OK", "")))
	peer.Write([]byte(request("OPTIONS", "bye-barrier")))
	if !strings.Contains(read(), "CSeq: 1 OPTIONS") {
		t.Fatal("missing BYE response barrier")
	}
	peer.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
	_, err = peer.Read(make([]byte, 8192))
	if e, ok := err.(net.Error); !ok || !e.Timeout() {
		t.Fatalf("BYE retries did not stop: %v", err)
	}
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	if len(s.clientTransactions) != 0 {
		t.Fatal("BYE client transaction leaked")
	}
}

func TestLowercaseHeadersAndChangedTransactionBody(t *testing.T) {
	s, added, _, _ := newTestServer(t, 5000, 15000)
	m, _ := ParseMessage(request("INVITE", "case-insensitive"))
	raw := serializePeerRequest(m)
	parts := strings.SplitN(raw, "\r\n\r\n", 2)
	lines := strings.Split(parts[0], "\r\n")
	for i := 1; i < len(lines); i++ {
		at := strings.IndexByte(lines[i], ':')
		if at >= 0 {
			lines[i] = strings.ToLower(lines[i][:at]) + lines[i][at:]
		}
	}
	raw = strings.Join(lines, "\r\n") + "\r\n\r\n" + parts[1]
	first := exchange(t, s, raw)
	if !strings.Contains(first, "200 OK") {
		t.Fatal(first)
	}
	<-added
	changed := strings.Replace(raw, "PCMA/8000", "PCMU/8000", 1)
	if got := exchange(t, s, changed); !strings.Contains(got, "400 Changed Transaction Request") {
		t.Fatal("changed SDP reused accepted transaction")
	}
	if got := exchange(t, s, raw); got != first {
		t.Fatal("changed request replaced cached original answer")
	}
}
