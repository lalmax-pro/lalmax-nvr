// Package livekitsfu contains the NVR integration with LiveKit. Callers must
// authorize the NVR principal before issuing a room token.
package livekitsfu

import (
	"errors"
	"strings"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

var ErrInvalidJoinGrant = errors.New("livekitsfu: invalid room join grant")

// TokenIssuer signs scoped room join tokens using the embedded server's API key.
// It does not authenticate NVR users or decide their permissions.
type TokenIssuer struct {
	key, secret string
}

func NewTokenIssuer(key, secret string) (*TokenIssuer, error) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(secret) == "" {
		return nil, ErrInvalidJoinGrant
	}
	return &TokenIssuer{key: key, secret: secret}, nil
}

// JoinGrant describes an already-authorized participant's room permissions.
type JoinGrant struct {
	Room, Identity           string
	ValidFor                 time.Duration
	CanPublish, CanSubscribe bool
	Hidden, Dependent        bool
}

func (i *TokenIssuer) IssueJoin(g JoinGrant) (string, error) {
	if i == nil || g.Room == "" || strings.TrimSpace(g.Room) != g.Room ||
		g.Identity == "" || strings.TrimSpace(g.Identity) != g.Identity ||
		g.ValidFor <= 0 || g.ValidFor > 24*time.Hour ||
		(g.Dependent && (!g.Hidden || g.CanPublish || !g.CanSubscribe)) {
		return "", ErrInvalidJoinGrant
	}
	grant := &auth.VideoGrant{RoomJoin: true, Room: g.Room, Hidden: g.Hidden}
	grant.SetCanPublish(g.CanPublish)
	grant.SetCanSubscribe(g.CanSubscribe)
	grant.SetCanPublishData(false)
	token := auth.NewAccessToken(i.key, i.secret).
		SetIdentity(g.Identity).
		SetValidFor(g.ValidFor).
		SetVideoGrant(grant)
	if g.Dependent {
		// LiveKit excludes agent-kind participants from ordinary room capacity
		// and idle retention. The token grants no agent API or publish access.
		token.SetKind(livekit.ParticipantInfo_AGENT)
	}
	return token.ToJWT()
}
