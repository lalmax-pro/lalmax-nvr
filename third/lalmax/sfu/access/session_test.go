package access

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/q191201771/lalmax/sfu"
)

type policy struct {
	mu       sync.Mutex
	deny     map[Operation]bool
	requests []Request
}

func (p *policy) Authorize(ctx context.Context, req Request) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	if p.deny[req.Operation] {
		return errors.New("details should not leak")
	}
	return nil
}
func (p *policy) set(op Operation, deny bool) { p.mu.Lock(); defer p.mu.Unlock(); p.deny[op] = deny }
func TestSessionPolicyAndScope(t *testing.T) {
	ctx := context.Background()
	m := sfu.NewManager()
	r, _ := m.Create(sfu.Key{Namespace: "tenant", ID: "room"}, 0)
	p := &policy{deny: map[Operation]bool{Publish: true}}
	session, err := JoinRoom(ctx, r, p, "user-1", sfu.Participant{ID: "member"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Publish(ctx, "video", "camera", "video/H264"); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if len(r.Snapshot().Publications) != 0 {
		t.Fatal("denied publication created")
	}
	p.set(Publish, false)
	track, err := session.Publish(ctx, "video", "camera", "video/H264")
	if err != nil {
		t.Fatal(err)
	}
	req := p.requests[len(p.requests)-1]
	snap := r.Snapshot()
	if req.Principal != "user-1" || req.ParticipantID != "member" || req.Room != snap.Key || req.RoomGeneration != snap.Generation || req.Source != "camera" {
		t.Fatalf("scope: %+v", req)
	}
	p.set(Unpublish, true)
	if err = session.Unpublish(ctx, track.ID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	if len(r.Snapshot().Publications) != 0 || len(r.Snapshot().Participants) != 0 {
		t.Fatal("cleanup after denial failed")
	}
	if err = session.Unpublish(ctx, track.ID); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
func TestJoinDeniedNoAllocationAndContext(t *testing.T) {
	m := sfu.NewManager()
	r, _ := m.Create(sfu.Key{Namespace: "n", ID: "r"}, 0)
	p := &policy{deny: map[Operation]bool{Join: true}}
	if _, err := JoinRoom(context.Background(), r, p, "u", sfu.Participant{ID: "p"}); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if len(r.Snapshot().Participants) != 0 {
		t.Fatal("denied join allocated participant")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := JoinRoom(ctx, r, p, "u", sfu.Participant{ID: "p"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := JoinRoom(context.Background(), r, nil, "u", sfu.Participant{ID: "p"}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
func TestSubscriberPermissionsAndReplacedSession(t *testing.T) {
	ctx := context.Background()
	m := sfu.NewManager()
	r, _ := m.Create(sfu.Key{Namespace: "n", ID: "r"}, 0)
	p := &policy{deny: map[Operation]bool{}}
	pub, _ := JoinRoom(ctx, r, p, "a", sfu.Participant{ID: "p"})
	viewer, _ := JoinRoom(ctx, r, p, "b", sfu.Participant{ID: "v"})
	track, _ := pub.Publish(ctx, "audio", "microphone", "audio/opus")
	p.set(Subscribe, true)
	if err := viewer.Subscribe(ctx, track.ID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	p.set(Subscribe, false)
	if err := viewer.Subscribe(ctx, track.ID); err != nil {
		t.Fatal(err)
	}
	p.set(Unsubscribe, true)
	if err := viewer.Unsubscribe(ctx, track.ID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if err := r.Leave("v"); err != nil {
		t.Fatal(err)
	}
	_, _ = r.JoinSession(sfu.Participant{ID: "v"})
	if err := viewer.Close(); err != nil {
		t.Fatal(err)
	}
	if len(r.Snapshot().Participants) != 2 {
		t.Fatal("stale session removed replacement")
	}
}
