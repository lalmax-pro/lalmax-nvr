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

type testPolicy struct{}

func (testPolicy) Authorize(_ context.Context, r access.Request) error {
	if r.Principal == "viewer" && r.Operation == access.Publish {
		return errors.New("deny")
	}
	return nil
}
func newHandler(t *testing.T) (*Handler, *sfu.Room) {
	t.Helper()
	m := sfu.NewManager()
	room, err := m.Create(sfu.Key{Namespace: "test", ID: "room"}, 8)
	if err != nil {
		t.Fatal(err)
	}
	return &Handler{Rooms: m, Policy: testPolicy{}, Authenticate: func(r *http.Request) (Identity, error) {
		id := r.Header.Get("X-Test-Identity")
		if id == "" {
			return Identity{}, errors.New("missing")
		}
		return Identity{Principal: id, Participant: sfu.Participant{ID: id}, Room: sfu.Key{Namespace: "test", ID: "room"}}, nil
	}, Filter: func(_ context.Context, _ Identity, s sfu.Snapshot) (sfu.Snapshot, error) {
		s.Participants = nil
		return s, nil
	}, CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "https://trusted.example" }}, room
}
func dial(t *testing.T, server, id, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	headers := http.Header{"X-Test-Identity": []string{id}, "Origin": []string{origin}}
	return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server, "http"), headers)
}
func readType(t *testing.T, c *websocket.Conn, want string) response {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(4 * time.Second))
	for {
		var r response
		if err := c.ReadJSON(&r); err != nil {
			t.Fatal(err)
		}
		if r.Type == want {
			return r
		}
	}
}
func TestWebSocketControlAndFilteredSnapshots(t *testing.T) {
	h, room := newHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()
	pub, _, err := dial(t, srv.URL, "publisher", "https://trusted.example")
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	initial := readType(t, pub, "snapshot")
	if initial.Snapshot == nil || len(initial.Snapshot.Participants) != 0 {
		t.Fatalf("unfiltered: %+v", initial)
	}
	viewer, _, err := dial(t, srv.URL, "viewer", "https://trusted.example")
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	_ = readType(t, viewer, "snapshot")
	if err = viewer.WriteJSON(command{ID: "deny", Type: "publish", Kind: "audio", Source: "microphone", Codec: "audio/opus"}); err != nil {
		t.Fatal(err)
	}
	denied := readType(t, viewer, "result")
	if denied.Error != "forbidden" || len(room.Snapshot().Publications) != 0 {
		t.Fatalf("denied: %+v", denied)
	}
	if err = pub.WriteJSON(command{ID: "p", Type: "publish", Kind: "audio", Source: "microphone", Codec: "audio/opus"}); err != nil {
		t.Fatal(err)
	}
	published := readType(t, pub, "result")
	if published.Publication == nil || published.Error != "" {
		t.Fatalf("publish: %+v", published)
	}
	for {
		s := readType(t, viewer, "snapshot")
		if len(s.Snapshot.Publications) > 0 {
			if len(s.Snapshot.Participants) != 0 {
				t.Fatal("filter bypassed")
			}
			break
		}
	}
	if err = viewer.WriteJSON(command{ID: "s", Type: "subscribe", PublicationID: published.Publication.ID}); err != nil {
		t.Fatal(err)
	}
	result := readType(t, viewer, "result")
	if result.ID != "s" || result.Error != "" {
		t.Fatalf("subscribe: %+v", result)
	}
	if len(room.Snapshot().Subscriptions) != 1 {
		t.Fatal("subscription missing")
	}
}
func TestRejectsMissingDependenciesOriginAndAuthentication(t *testing.T) {
	h, room := newHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()
	if c, resp, err := dial(t, srv.URL, "user", "https://evil.example"); err == nil {
		c.Close()
		t.Fatal("origin accepted")
	} else if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("origin response: %v", resp)
	}
	if c, resp, err := dial(t, srv.URL, "", "https://trusted.example"); err == nil {
		c.Close()
		t.Fatal("unauthenticated accepted")
	} else if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("auth response: %v", resp)
	}
	if len(room.Snapshot().Participants) != 0 {
		t.Fatal("rejected connection allocated participant")
	}
	h.Filter = nil
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal(w.Code)
	}
}

func TestDisconnectRemovesParticipant(t *testing.T) {
	h, room := newHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()
	conn, _, err := dial(t, srv.URL, "publisher", "https://trusted.example")
	if err != nil {
		t.Fatal(err)
	}
	_ = readType(t, conn, "snapshot")
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(room.Snapshot().Participants) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("participant not cleaned up")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
