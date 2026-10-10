package voip

import (
	"testing"
	"time"
)

func TestInboundCallTerminationEmitsHistory(t *testing.T) {
	var records []CallRecord
	s := &Server{dialogManager: NewDialogManager(), transactions: map[string]*serverTransaction{}, onCallEnded: func(record CallRecord) { records = append(records, record) }}
	d := s.dialogManager.Create("inbound-call", "from-tag", "to-tag", "door-1", "nvr", "127.0.0.1:5060")
	d.Peer = TransportAddr{Type: TransportTLS, Addr: "127.0.0.1:5060"}
	s.dialogManager.UpdateState(d.CallID, DialogStateEstablished)
	s.terminate(d.CallID, "completed", "ended by terminal")
	if len(records) != 1 {
		t.Fatalf("expected one call record, got %d", len(records))
	}
	got := records[0]
	if got.CallID != d.CallID || got.Direction != "inbound" || got.Outcome != "completed" || got.AnsweredAt.IsZero() || got.EndedAt.Before(got.AnsweredAt) {
		t.Fatalf("unexpected completed call record: %+v", got)
	}
}

func TestOutboundCallFinishEmitsHistory(t *testing.T) {
	var got CallRecord
	s := &Server{onCallEnded: func(record CallRecord) { got = record }}
	started := time.Now().Add(-time.Minute)
	c := &outboundCall{id: "outbound-call", user: "6002", authUsername: "6001", peer: TransportAddr{Type: TransportTCP, Addr: "127.0.0.1:5060"}, state: "ringing", created: started}
	s.finishOutbound(c, "failed", "terminal did not answer")
	if got.CallID != c.id || got.Direction != "outbound" || got.FromUser != "6001" || got.ToUser != "6002" || got.Outcome != "missed" || got.DurationSecond != 0 {
		t.Fatalf("unexpected outbound missed call record: %+v", got)
	}
}
