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

func TestGCCSelectionIsPerSubscriberAndDowngradesImmediately(t *testing.T) {
	m := NewManager()
	r, _ := m.Create(Key{Namespace: "n", ID: "simulcast"}, 0)
	publisher, _ := r.JoinSession(Participant{ID: "publisher"})
	viewerA, _ := r.JoinSession(Participant{ID: "viewer-a"})
	viewerB, _ := r.JoinSession(Participant{ID: "viewer-b"})
	pub, _ := r.PublishSession("publisher", publisher, "video", "camera", "video/H264")
	codec := NegotiatedCodec{MimeType: "video/H264", ClockRate: 90_000, FMTP: "packetization-mode=1;profile-level-id=42e01f"}
	if err := r.EnablePublicationLayerRTPWithCodec("publisher", publisher, pub.ID, 4, 8, codec, "l", 150_000); err != nil {
		t.Fatal(err)
	}
	for _, layer := range []rtpforward.LayerInfo{{RID: "m", MaxBitrate: 500_000}, {RID: "h", MaxBitrate: 1_500_000}} {
		if err := r.EnablePublicationLayerRTPWithCodec("publisher", publisher, pub.ID, 4, 8, codec, layer.RID, layer.MaxBitrate); err != nil {
			t.Fatal(err)
		}
	}
	for participantID, session := range map[string]string{"viewer-a": viewerA, "viewer-b": viewerB} {
		if err := r.SubscribeSession(participantID, session, pub.ID); err != nil {
			t.Fatal(err)
		}
	}
	subA, err := r.OpenSubscriptionRTP("viewer-a", viewerA, pub.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer subA.Close()
	subB, err := r.OpenSubscriptionRTP("viewer-b", viewerB, pub.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer subB.Close()
	if err = subA.SelectLayer("h"); err != nil {
		t.Fatal(err)
	}
	if err = subB.SelectLayer("h"); err != nil {
		t.Fatal(err)
	}
	if err = r.UpdateSubscriberBandwidth("viewer-a", 400_000); err != nil {
		t.Fatal(err)
	}
	if got := subA.SelectedLayer(); got != "l" {
		t.Fatalf("viewer-a RID %q after low estimate, want immediate l", got)
	}
	if got := subB.SelectedLayer(); got != "h" {
		t.Fatalf("viewer-b RID changed with another subscriber's estimate: %q", got)
	}
}

func TestNegotiatedCodecFollowsPublicationGeneration(t *testing.T) {
	m := NewManager()
	r, err := m.Create(Key{"n", "codec"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	old, err := r.JoinSession(Participant{ID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.PublishSession("source", old, "video", "camera", "video/H264")
	if err != nil {
		t.Fatal(err)
	}
	codec := NegotiatedCodec{MimeType: "video/H264", ClockRate: 90000, FMTP: "packetization-mode=1;profile-level-id=42e01f"}
	if err = r.EnablePublicationRTPWithCodec("source", old, p.ID, 2, 4, NegotiatedCodec{MimeType: "audio/opus", ClockRate: 48000}); !errors.Is(err, ErrInvalidNegotiatedCodec) {
		t.Fatalf("mismatch: %v", err)
	}
	if err = r.EnablePublicationRTPWithCodec("source", old, p.ID, 2, 4, codec); err != nil {
		t.Fatal(err)
	}
	snap := r.Snapshot()
	if got := snap.Publications[0]; !got.RTPReady || got.Negotiated != codec {
		t.Fatalf("snapshot: %+v", got)
	}
	snap.Publications[0].Negotiated.FMTP = "changed"
	if got := r.Snapshot().Publications[0].Negotiated; got != codec {
		t.Fatalf("snapshot aliases room: %+v", got)
	}
	if err = r.EnablePublicationRTPWithCodec("source", old, p.ID, 2, 4, codec); !errors.Is(err, ErrMediaAlreadyEnabled) {
		t.Fatal(err)
	}
	if err = r.LeaveSession("source", old); err != nil {
		t.Fatal(err)
	}
	current, err := r.JoinSession(Participant{ID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	next, err := r.PublishSession("source", current, "video", "camera", "video/H264")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.EnablePublicationRTPWithCodec("source", old, next.ID, 2, 4, codec); !errors.Is(err, ErrStaleSession) {
		t.Fatalf("stale session: %v", err)
	}
	if got := r.Snapshot().Publications[0]; got.RTPReady || got.Negotiated != (NegotiatedCodec{}) {
		t.Fatalf("metadata leaked to new publication: %+v", got)
	}
}
