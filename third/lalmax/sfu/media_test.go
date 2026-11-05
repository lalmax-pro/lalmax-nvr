package sfu

import (
	"errors"
	"github.com/pion/rtp"
	"github.com/q191201771/lalmax/sfu/rtpforward"
	"sync"
	"testing"
)

func TestRoomRTPDeliveryAndLifecycle(t *testing.T) {
	m := NewManager()
	key := Key{"n", "r"}
	r, _ := m.Create(key, 0)
	pub, _ := r.JoinSession(Participant{ID: "p"})
	view, _ := r.JoinSession(Participant{ID: "v"})
	p, err := r.PublishSession("p", pub, "video", "camera", "video/H264")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.EnablePublicationRTP("p", pub, p.ID, 2, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = r.OpenSubscriptionRTP("v", view, p.ID); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatal(err)
	}
	if err = r.SubscribeSession("v", view, p.ID); err != nil {
		t.Fatal(err)
	}
	sub, err := r.OpenSubscriptionRTP("v", view, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.WritePublicationRTP("p", pub, p.ID, &rtp.Packet{Payload: []byte{9}}); err != nil {
		t.Fatal(err)
	}
	if (<-sub.Packets()).Payload[0] != 9 {
		t.Fatal("wrong packet")
	}
	if err = r.UnsubscribeSession("v", view, p.ID); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(sub.Err(), rtpforward.ErrSubscriberClosed) {
		t.Fatal(sub.Err())
	}
	_ = r.SubscribeSession("v", view, p.ID)
	sub, _ = r.OpenSubscriptionRTP("v", view, p.ID)
	if err = r.UnpublishSession("p", pub, p.ID); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(sub.Err(), rtpforward.ErrClosed) {
		t.Fatal(sub.Err())
	}
}
func TestRoomRTPRejectsStaleAndCleansUp(t *testing.T) {
	m := NewManager()
	key := Key{"n", "r"}
	r, _ := m.Create(key, 0)
	old, _ := r.JoinSession(Participant{ID: "p"})
	_ = r.LeaveSession("p", old)
	pub, _ := r.JoinSession(Participant{ID: "p"})
	view, _ := r.JoinSession(Participant{ID: "v"})
	p, _ := r.PublishSession("p", pub, "audio", "microphone", "audio/opus")
	if err := r.EnablePublicationRTP("p", old, p.ID, 1, 1); !errors.Is(err, ErrStaleSession) {
		t.Fatal(err)
	}
	_ = r.EnablePublicationRTP("p", pub, p.ID, 1, 1)
	_ = r.SubscribeSession("v", view, p.ID)
	sub, _ := r.OpenSubscriptionRTP("v", view, p.ID)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			err := r.WritePublicationRTP("p", pub, p.ID, &rtp.Packet{Payload: []byte{1}})
			if err != nil && !errors.Is(err, ErrRoomClosed) && !errors.Is(err, rtpforward.ErrClosed) {
				t.Error(err)
			}
		}
	}()
	_ = m.Close(key, r.Snapshot().Generation)
	wg.Wait()
	if sub.Err() == nil {
		t.Fatal("subscriber not closed")
	}
}

func TestRoomRTPParticipantLeaveClosesOnlyAssociatedMedia(t *testing.T) {
	m := NewManager()
	r, _ := m.Create(Key{"n", "r"}, 0)
	pub, _ := r.JoinSession(Participant{ID: "p"})
	v1, _ := r.JoinSession(Participant{ID: "v1"})
	v2, _ := r.JoinSession(Participant{ID: "v2"})
	p, _ := r.PublishSession("p", pub, "video", "camera", "video/H264")
	_ = r.EnablePublicationRTP("p", pub, p.ID, 2, 2)
	_ = r.SubscribeSession("v1", v1, p.ID)
	_ = r.SubscribeSession("v2", v2, p.ID)
	a, _ := r.OpenSubscriptionRTP("v1", v1, p.ID)
	b, _ := r.OpenSubscriptionRTP("v2", v2, p.ID)
	if err := r.LeaveSession("v1", v1); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(a.Err(), rtpforward.ErrSubscriberClosed) {
		t.Fatal(a.Err())
	}
	if b.Err() != nil {
		t.Fatal(b.Err())
	}
	if err := r.WritePublicationRTP("p", pub, p.ID, &rtp.Packet{Payload: []byte{3}}); err != nil {
		t.Fatal(err)
	}
	if (<-b.Packets()).Payload[0] != 3 {
		t.Fatal("remaining viewer lost packet")
	}
	if err := r.LeaveSession("p", pub); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(b.Err(), rtpforward.ErrClosed) {
		t.Fatal(b.Err())
	}
}

func TestPublicationRTPReadinessEvent(t *testing.T) {
	m := NewManager()
	r, _ := m.Create(Key{Namespace: "n", ID: "r"}, 0)
	session, _ := r.JoinSession(Participant{ID: "p"})
	pub, _ := r.PublishSession("p", session, "video", "camera", "video/H264")
	snap, events, cancel, err := r.Watch(2)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if snap.Publications[0].RTPReady {
		t.Fatal("publication ready before media enabled")
	}
	if err = r.EnablePublicationRTP("p", session, pub.ID, 1, 2); err != nil {
		t.Fatal(err)
	}
	event := <-events
	if event.Type != "track_ready" || event.Snapshot.Sequence != snap.Sequence+1 || !event.Snapshot.Publications[0].RTPReady {
		t.Fatalf("ready event: %+v", event)
	}
	if err = r.UnpublishSession("p", session, pub.ID); err != nil {
		t.Fatal(err)
	}
	event = <-events
	if len(event.Snapshot.Publications) != 0 {
		t.Fatal("unpublished track retained")
	}
}
