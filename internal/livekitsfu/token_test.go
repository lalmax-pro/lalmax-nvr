package livekitsfu

import (
	"errors"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
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
	if verifier.APIKey() != "test-key" || verifier.Identity() != "viewer-1" || claims.ExpiresAt == nil || time.Until(claims.ExpiresAt.Time) > time.Minute || grants.GetParticipantKind() != livekit.ParticipantInfo_STANDARD {
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

func TestDependentJoinTokenIsHiddenSubscribeOnly(t *testing.T) {
	issuer, err := NewTokenIssuer("key", "secret")
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.IssueJoin(JoinGrant{Room: "room", Identity: "bridge", ValidFor: time.Minute, CanSubscribe: true, Hidden: true, Dependent: true})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.ParseAPIToken(token)
	if err != nil {
		t.Fatal(err)
	}
	_, grants, err := verifier.Verify("secret")
	if err != nil {
		t.Fatal(err)
	}
	if grants.GetParticipantKind() != livekit.ParticipantInfo_AGENT || grants.Video == nil || !grants.Video.Hidden || grants.Video.Agent || grants.Video.Recorder ||
		grants.Video.RoomAdmin || grants.Video.CanPublish == nil || *grants.Video.CanPublish || grants.Video.CanSubscribe == nil || !*grants.Video.CanSubscribe || grants.Video.CanPublishData == nil || *grants.Video.CanPublishData {
		t.Fatalf("unexpected dependent grant: %+v", grants)
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
		{Room: "room", Identity: "id", ValidFor: time.Minute, Dependent: true, CanSubscribe: true},
		{Room: "room", Identity: "id", ValidFor: time.Minute, Dependent: true, Hidden: true, CanPublish: true, CanSubscribe: true},
		{Room: "room", Identity: "id", ValidFor: time.Minute, Dependent: true, Hidden: true},
	} {
		if _, err := issuer.IssueJoin(grant); !errors.Is(err, ErrInvalidJoinGrant) {
			t.Fatalf("accepted invalid grant %+v: %v", grant, err)
		}
	}
}
