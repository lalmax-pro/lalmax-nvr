package livekitsfu

import (
	"errors"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
)

func TestJoinTokenScopesPermissions(t *testing.T) {
	issuer, err := NewTokenIssuer("test-key", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.IssueJoin(JoinGrant{Room: "front-door", Identity: "viewer-1", ValidFor: time.Minute, CanSubscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.ParseAPIToken(token)
	if err != nil {
		t.Fatal(err)
	}
	claims, grants, err := verifier.Verify("test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if verifier.APIKey() != "test-key" || verifier.Identity() != "viewer-1" || claims.ExpiresAt == nil || time.Until(claims.ExpiresAt.Time) > time.Minute {
		t.Fatal("unexpected identity or expiry")
	}
	g := grants.Video
	if g == nil || !g.RoomJoin || g.Room != "front-door" || g.RoomAdmin || g.RoomCreate || g.RoomList || g.RoomRecord ||
		g.CanPublish == nil || *g.CanPublish || g.CanSubscribe == nil || !*g.CanSubscribe || g.CanPublishData == nil || *g.CanPublishData {
		t.Fatalf("unexpected permissions: %+v", g)
	}
	if _, _, err := verifier.Verify("wrong-secret"); err == nil {
		t.Fatal("accepted wrong signing secret")
	}
}

func TestJoinTokenRejectsInvalidScope(t *testing.T) {
	issuer, err := NewTokenIssuer("key", "secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range []JoinGrant{
		{Room: "", Identity: "id", ValidFor: time.Minute},
		{Room: "room", Identity: " ", ValidFor: time.Minute},
		{Room: " room", Identity: "id", ValidFor: time.Minute},
		{Room: "room", Identity: "id", ValidFor: 0},
		{Room: "room", Identity: "id", ValidFor: 25 * time.Hour},
	} {
		if _, err := issuer.IssueJoin(grant); !errors.Is(err, ErrInvalidJoinGrant) {
			t.Fatalf("accepted invalid grant %+v: %v", grant, err)
		}
	}
}
