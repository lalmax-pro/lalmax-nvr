package signaling

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
)

type roomIsolationPolicy struct{}

func (roomIsolationPolicy) Authorize(_ context.Context, req access.Request) error {
	if !strings.HasPrefix(req.Principal, req.Room.ID+"-") || (strings.HasSuffix(req.Principal, "-viewer") && req.Operation == access.Publish) {
		return errors.New("denied")
	}
	return nil
}

func TestWebSocketRoomsIsolateDiscoverySubscriptionsAndAuthorization(t *testing.T) {
	manager := sfu.NewManager()
	keyA := sfu.Key{Namespace: "tenant", ID: "a"}
	keyB := sfu.Key{Namespace: "tenant", ID: "b"}
	roomA, err := manager.Create(keyA, 8)
	if err != nil {
		t.Fatal(err)
	}
	roomB, err := manager.Create(keyB, 8)
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{Rooms: manager, Policy: roomIsolationPolicy{}, CheckOrigin: func(*http.Request) bool { return true },
		Authenticate: func(r *http.Request) (Identity, error) {
			principal := r.Header.Get("X-Test-Identity")
			return Identity{Principal: principal, Participant: sfu.Participant{ID: principal}, Room: sfu.Key{Namespace: "tenant", ID: r.Header.Get("X-Test-Room")}}, nil
		},
		Filter: func(_ context.Context, _ Identity, snapshot sfu.Snapshot) (sfu.Snapshot, error) { return snapshot, nil },
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	connect := func(principal, room string) (*websocket.Conn, *http.Response, error) {
		return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), http.Header{"X-Test-Identity": {principal}, "X-Test-Room": {room}})
	}
	read := func(c *websocket.Conn, key sfu.Key, predicate func(response) bool) response {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			var msg response
			if err := c.ReadJSON(&msg); err != nil {
				t.Fatal(err)
			}
			if msg.Snapshot != nil {
				if msg.Snapshot.Key != key {
					t.Fatalf("wrong room in snapshot: %+v", msg.Snapshot.Key)
				}
				for _, p := range msg.Snapshot.Participants {
					if !strings.HasPrefix(p.ID, key.ID+"-") {
						t.Fatalf("cross-room participant %q", p.ID)
					}
				}
				for _, p := range msg.Snapshot.Publications {
					if !strings.HasPrefix(p.ParticipantID, key.ID+"-") {
						t.Fatalf("cross-room publication %q", p.ID)
					}
				}
			}
			if predicate(msg) {
				return msg
			}
		}
	}
	result := func(id string) func(response) bool {
		return func(r response) bool { return r.Type == "result" && r.ID == id }
	}
	initial := func(r response) bool { return r.Type == "snapshot" }
	viewerA, _, err := connect("a-viewer", "a")
	if err != nil {
		t.Fatal(err)
	}
	defer viewerA.Close()
	read(viewerA, keyA, initial)
	viewerB, _, err := connect("b-viewer", "b")
	if err != nil {
		t.Fatal(err)
	}
	defer viewerB.Close()
	read(viewerB, keyB, initial)
	if c, response, err := connect("b-intruder", "a"); err == nil {
		c.Close()
		t.Fatal("cross-room join accepted")
	} else if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("join rejection: %v", response)
	}
	publisherA, _, err := connect("a-publisher", "a")
	if err != nil {
		t.Fatal(err)
	}
	defer publisherA.Close()
	read(publisherA, keyA, initial)
	if err = publisherA.WriteJSON(command{ID: "publish-a", Type: "publish", Kind: "video", Source: "camera", Codec: "video/H264"}); err != nil {
		t.Fatal(err)
	}
	publishedA := read(publisherA, keyA, result("publish-a"))
	if publishedA.Publication == nil || publishedA.Error != "" {
		t.Fatalf("publish: %+v", publishedA)
	}
	read(viewerA, keyA, func(r response) bool { return r.Snapshot != nil && len(r.Snapshot.Publications) == 1 })
	if err = viewerB.WriteJSON(command{ID: "cross-subscribe", Type: "subscribe", PublicationID: publishedA.Publication.ID}); err != nil {
		t.Fatal(err)
	}
	if r := read(viewerB, keyB, result("cross-subscribe")); r.Error == "" {
		t.Fatal("cross-room subscription accepted")
	}
	if err = viewerB.WriteJSON(command{ID: "viewer-publish", Type: "publish", Kind: "audio", Source: "microphone", Codec: "audio/opus"}); err != nil {
		t.Fatal(err)
	}
	if r := read(viewerB, keyB, result("viewer-publish")); r.Error != "forbidden" {
		t.Fatalf("viewer publish: %+v", r)
	}
	if err = viewerA.WriteJSON(command{ID: "subscribe-a", Type: "subscribe", PublicationID: publishedA.Publication.ID}); err != nil {
		t.Fatal(err)
	}
	if r := read(viewerA, keyA, result("subscribe-a")); r.Error != "" {
		t.Fatalf("viewer subscription: %+v", r)
	}
	publisherB, _, err := connect("b-publisher", "b")
	if err != nil {
		t.Fatal(err)
	}
	defer publisherB.Close()
	read(publisherB, keyB, initial)
	if err = publisherB.WriteJSON(command{ID: "publish-b", Type: "publish", Kind: "audio", Source: "microphone", Codec: "audio/opus"}); err != nil {
		t.Fatal(err)
	}
	if r := read(publisherB, keyB, result("publish-b")); r.Error != "" || r.Publication == nil {
		t.Fatalf("publish b: %+v", r)
	}
	read(viewerB, keyB, func(r response) bool { return r.Snapshot != nil && len(r.Snapshot.Publications) == 1 })
	if len(roomA.Snapshot().Subscriptions) != 1 || len(roomB.Snapshot().Subscriptions) != 0 {
		t.Fatal("subscriptions escaped their room")
	}
	if err = publisherA.WriteJSON(command{ID: "unpublish-a", Type: "unpublish", PublicationID: publishedA.Publication.ID}); err != nil {
		t.Fatal(err)
	}
	if r := read(publisherA, keyA, result("unpublish-a")); r.Error != "" {
		t.Fatalf("unpublish: %+v", r)
	}
	read(viewerA, keyA, func(r response) bool { return r.Snapshot != nil && len(r.Snapshot.Publications) == 0 })
}
