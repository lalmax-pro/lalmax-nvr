package voip

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestLostInviteAnswerRetransmitsUntilValidACK(t *testing.T) {
	s, added, _, _ := newTestServer(t, 5000, 15000)
	peer, err := net.DialUDP("udp", nil, s.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	read := func() string {
		t.Helper()
		peer.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 8192)
		n, err := peer.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		return string(buf[:n])
	}
	peer.Write([]byte(request("INVITE", "lost-answer")))
	first := read() // Simulate an answer discarded before reaching the SIP UA.
	<-added
	if !strings.Contains(first, "200 OK") || read() != first {
		t.Fatal("200 OK was not retransmitted unchanged")
	}
	wrong, _ := ParseMessage(inDialogRequest(s, "ACK", "lost-answer"))
	wrong.SetHeader("To", "<sip:door@127.0.0.1>;tag=wrong")
	peer.Write([]byte(serializePeerRequest(wrong)))
	if read() != first {
		t.Fatal("invalid ACK stopped retransmissions")
	}
	if s.dialogManager.Get("lost-answer").State != DialogStateConfirmed {
		t.Fatal("invalid ACK established call")
	}
	peer.Write([]byte(inDialogRequest(s, "ACK", "lost-answer")))
	peer.Write([]byte(request("OPTIONS", "barrier")))
	if !strings.Contains(read(), "CSeq: 1 OPTIONS") {
		t.Fatal("missing ACK barrier")
	}
	peer.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
	_, err = peer.Read(make([]byte, 8192))
	if e, ok := err.(net.Error); !ok || !e.Timeout() {
		t.Fatalf("response retransmissions did not stop: %v", err)
	}
	if len(added) != 0 {
		t.Fatal("retransmission created extra publisher")
	}
}

func TestDialogIdentityAndCSeqRejectWrongRequests(t *testing.T) {
	s, added, _, removed := newTestServer(t, 5000, 15000)
	exchange(t, s, request("INVITE", "protected"))
	<-added
	for _, change := range []string{"from", "to", "sequence", "method"} {
		m, _ := ParseMessage(inDialogRequest(s, "BYE", "protected"))
		m.SetHeader("Via", m.GetHeader("Via")+"-"+change)
		want := "481"
		switch change {
		case "from":
			m.SetHeader("From", "<sip:other@127.0.0.1>;tag=other")
		case "to":
			m.SetHeader("To", "<sip:door@127.0.0.1>;tag=other")
		case "sequence":
			m.SetHeader("CSeq", "1 BYE")
			want = "500"
		case "method":
			m.SetHeader("CSeq", "2 INVITE")
			want = "400"
		}
		if answer := exchange(t, s, serializePeerRequest(m)); !strings.Contains(answer, "SIP/2.0 "+want) {
			t.Fatal(answer)
		}
		if s.dialogManager.Get("protected") == nil || len(removed) != 0 {
			t.Fatal("invalid BYE ended call")
		}
	}
	bye := inDialogRequest(s, "BYE", "protected")
	first := exchange(t, s, bye)
	waitRemoved(t, removed)
	if again := exchange(t, s, bye); first != again || !strings.Contains(again, "200 OK") {
		t.Fatal("lost BYE response not replayed")
	}
	if len(removed) != 0 {
		t.Fatal("BYE retransmission repeated cleanup")
	}
	m, _ := ParseMessage(bye)
	m.SetHeader("Via", m.GetHeader("Via")+"-new")
	if answer := exchange(t, s, serializePeerRequest(m)); !strings.Contains(answer, "481") {
		t.Fatal("new transaction matched removed dialog")
	}
}

func TestTransactionExpiryAndReliableInviteRetransmission(t *testing.T) {
	s, _, _, _ := newTestServer(t, 5000, 15000)
	m, _ := ParseMessage(request("OPTIONS", "expiry"))
	s.requestMu.Lock()
	s.rememberResponse(m, BuildResponse(m, 200, "OK", ""), TransportAddr{Type: TransportTCP})
	s.runTransactions(time.Now().Add(transactionLifetime + time.Second))
	if len(s.transactions) != 0 {
		t.Fatal("completed transaction leaked")
	}
	m, _ = ParseMessage(request("INVITE", "reliable"))
	s.rememberResponse(m, BuildResponse(m, 200, "OK", ""), TransportAddr{Type: TransportTCP})
	tx := s.transactions[transactionKey(m, MethodInvite)]
	if tx.next.IsZero() {
		t.Fatal("reliable hop disabled end-to-end 2xx retransmission")
	}
	s.requestMu.Unlock()
}
