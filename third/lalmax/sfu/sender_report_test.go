package sfu

import (
	"errors"
	"testing"
	"time"
)

func TestSenderReportFollowsPublicationAndSession(t *testing.T) {
	r, _ := NewManager().Create(Key{Namespace: "n", ID: "r"}, 0)
	publisher, _ := r.JoinSession(Participant{ID: "p"})
	viewer, _ := r.JoinSession(Participant{ID: "v"})
	pub, _ := r.PublishSession("p", publisher, "audio", "microphone", "audio/opus")
	_ = r.EnablePublicationRTPWithCodec("p", publisher, pub.ID, 4, 4, NegotiatedCodec{MimeType: "audio/opus", ClockRate: 48000, Channels: 2})
	_ = r.SubscribeSession("v", viewer, pub.ID)
	report := SenderReport{SSRC: 1, NTP: 2, RTP: 3, ReceivedAt: time.Now()}
	if err := r.RecordSenderReport("p", publisher, pub.ID, report); err != nil {
		t.Fatal(err)
	}
	old := report
	old.NTP = 4
	old.ReceivedAt = old.ReceivedAt.Add(-time.Second)
	if err := r.RecordSenderReport("p", publisher, pub.ID, old); err != nil {
		t.Fatal(err)
	}
	got, ok, err := r.SenderReportSession("v", viewer, pub.ID)
	if err != nil || !ok || got.NTP != 2 {
		t.Fatalf("report: %+v %v %v", got, ok, err)
	}
	if _, _, err = r.SenderReportSession("p", publisher, pub.ID); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatal(err)
	}
	_ = r.LeaveSession("p", publisher)
	replacement, _ := r.JoinSession(Participant{ID: "p"})
	if err = r.RecordSenderReport("p", publisher, pub.ID, report); !errors.Is(err, ErrStaleSession) {
		t.Fatal(err)
	}
	if _, _, err = r.SenderReportSession("v", viewer, pub.ID); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatal(err)
	}
	newPub, _ := r.PublishSession("p", replacement, "audio", "microphone", "audio/opus")
	_ = r.SubscribeSession("v", viewer, newPub.ID)
	if _, ok, err = r.SenderReportSession("v", viewer, newPub.ID); err != nil || ok {
		t.Fatalf("stale report reused: %v %v", ok, err)
	}
}
