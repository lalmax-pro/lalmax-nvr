package sfubridge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
)

type testPolicy struct {
	mu   sync.Mutex
	deny access.Operation
}

func (p *testPolicy) Authorize(_ context.Context, r access.Request) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Principal != "operator" || r.Operation == p.deny {
		return errors.New("denied")
	}
	return nil
}

type testPublisher struct {
	opened chan string
	closed chan struct{}
}

func (p *testPublisher) AddCustomizePubSession(name string) (logic.ICustomizePubSessionContext, error) {
	p.opened <- name
	return &testSession{}, nil
}
func (p *testPublisher) DelCustomizePubSession(logic.ICustomizePubSessionContext) {
	p.closed <- struct{}{}
}

type testSession struct {
	logic.ICustomizePubSessionContext
}

func (*testSession) WithOption(func(*base.AvPacketStreamOption)) {}
func (*testSession) FeedAvPacket(base.AvPacket) error            { return nil }

func setup(t *testing.T) (*Service, *testPolicy, *testPublisher, *auth.MediaAuthorizer, string) {
	t.Helper()
	rooms := sfu.NewManager()
	room, err := rooms.Create(sfu.Key{Namespace: "nvr", ID: "room"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	gen, _ := room.JoinSession(sfu.Participant{ID: "source"})
	pub, _ := room.PublishSession("source", gen, "video", "camera", "video/H264")
	if err = room.EnablePublicationRTP("source", gen, pub.ID, 4, 8); err != nil {
		t.Fatal(err)
	}
	policy := &testPolicy{}
	publisher := &testPublisher{opened: make(chan string, 4), closed: make(chan struct{}, 4)}
	grants := auth.NewMediaAuthorizer()
	service, err := New(room, policy, publisher, grants, func(context.Context, sfu.Publication) error { return nil }, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := service.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return service, policy, publisher, grants, pub.ID
}
func TestProtectedLifecycle(t *testing.T) {
	s, _, publisher, grants, id := setup(t)
	req, cancel := context.WithCancel(context.Background())
	h, err := s.Start(req, "operator", id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-publisher.opened:
		if got != h.StreamID {
			t.Fatal(got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not opened")
	}
	cancel()
	if err = grants.AuthorizePlayback("rtsp", h.StreamID, h.PlaybackToken); err != nil {
		t.Fatal(err)
	}
	if err = grants.AuthorizePlayback("rtsp", h.StreamID, "wrong"); err == nil {
		t.Fatal("wrong token accepted")
	}
	if _, err = s.Start(context.Background(), "operator", id); !errors.Is(err, ErrLimit) {
		t.Fatalf("limit: %v", err)
	}
	h.Stop()
	select {
	case err = <-h.Done():
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not stop")
	}
	select {
	case <-publisher.closed:
	default:
		t.Fatal("publisher not closed")
	}
	if err = grants.AuthorizePlayback("rtsp", h.StreamID, h.PlaybackToken); err == nil {
		t.Fatal("stopped grant remains valid")
	}
}
func TestDeniedBridgeReleasesCapacity(t *testing.T) {
	s, policy, publisher, _, id := setup(t)
	policy.mu.Lock()
	policy.deny = access.ManageBridge
	policy.mu.Unlock()
	if _, err := s.Start(context.Background(), "operator", id); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("authorization: %v", err)
	}
	select {
	case <-publisher.opened:
		t.Fatal("denied publisher started")
	default:
	}
	policy.mu.Lock()
	policy.deny = ""
	policy.mu.Unlock()
	h, err := s.Start(context.Background(), "operator", id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-publisher.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not opened")
	}
	h.Stop()
	select {
	case <-h.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not stop")
	}
}

func TestServiceCloseCancelsAndRejectsStarts(t *testing.T) {
	s, _, publisher, grants, id := setup(t)
	h, err := s.Start(context.Background(), "operator", id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-publisher.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not opened")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not stop")
	}
	if err = grants.AuthorizePlayback("rtsp", h.StreamID, h.PlaybackToken); err == nil {
		t.Fatal("playback still authorized")
	}
	if _, err = s.Start(context.Background(), "operator", id); !errors.Is(err, ErrClosed) {
		t.Fatalf("start after close: %v", err)
	}
}
func TestValidationFailureReleasesSessionAndCapacity(t *testing.T) {
	s, _, publisher, _, id := setup(t)
	want := errors.New("unsupported profile")
	s.validate = func(context.Context, sfu.Publication) error { return want }
	if _, err := s.Start(context.Background(), "operator", id); !errors.Is(err, want) {
		t.Fatalf("validation: %v", err)
	}
	if got := len(s.room.Snapshot().Participants); got != 1 {
		t.Fatalf("system participant leaked: %d", got)
	}
	select {
	case <-publisher.opened:
		t.Fatal("invalid source published")
	default:
	}
	s.validate = func(context.Context, sfu.Publication) error { return nil }
	h, err := s.Start(context.Background(), "operator", id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-publisher.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not opened")
	}
	h.Stop()
	select {
	case <-h.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not stop")
	}
}
