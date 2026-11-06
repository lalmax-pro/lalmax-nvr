package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/lalmax/sfu"
)

type bridgeTestPublisher struct {
	opened chan string
	closed chan struct{}
}

func (p *bridgeTestPublisher) AddCustomizePubSession(name string) (logic.ICustomizePubSessionContext, error) {
	p.opened <- name
	return logic.NewCustomizePubSessionContext(name), nil
}
func (p *bridgeTestPublisher) DelCustomizePubSession(s logic.ICustomizePubSessionContext) {
	s.(*logic.CustomizePubSessionContext).Dispose()
	close(p.closed)
}

func TestBridgeChecksBothPoliciesAndCleansUp(t *testing.T) {
	ctx := context.Background()
	manager := sfu.NewManager()
	room, _ := manager.Create(sfu.Key{Namespace: "tenant", ID: "room"}, 0)
	gen, _ := room.JoinSession(sfu.Participant{ID: "source"})
	pub, _ := room.PublishSession("source", gen, "video", "camera", "video/H264")
	if err := room.EnablePublicationRTP("source", gen, pub.ID, 2, 8); err != nil {
		t.Fatal(err)
	}
	auth := &policy{deny: map[Operation]bool{}}
	session, err := JoinRoom(ctx, room, auth, "alice", sfu.Participant{ID: "bridge"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err = session.Subscribe(ctx, pub.ID); err != nil {
		t.Fatal(err)
	}
	publisher := &bridgeTestPublisher{opened: make(chan string, 1), closed: make(chan struct{})}
	auth.set(Subscribe, true)
	if _, err = session.StartH264Bridge(ctx, pub.ID, "target", publisher); !errors.Is(err, ErrDenied) {
		t.Fatalf("subscribe denial: %v", err)
	}
	auth.set(Subscribe, false)
	auth.set(ManageBridge, true)
	if _, err = session.StartH264Bridge(ctx, pub.ID, "target", publisher); !errors.Is(err, ErrDenied) {
		t.Fatalf("bridge denial: %v", err)
	}
	auth.set(ManageBridge, false)
	done, err := session.StartH264Bridge(ctx, pub.ID, "target", publisher)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case name := <-publisher.opened:
		if name != "target" {
			t.Fatal(name)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not opened")
	}
	auth.mu.Lock()
	req := auth.requests[len(auth.requests)-1]
	auth.mu.Unlock()
	if req.Operation != ManageBridge || req.TargetGroup != "target" || req.PublicationID != pub.ID || req.Principal != "alice" || req.RoomGeneration != room.Snapshot().Generation {
		t.Fatalf("authorization scope: %+v", req)
	}
	if err = session.Unsubscribe(ctx, pub.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not stop")
	}
	select {
	case <-publisher.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not closed")
	}
}
