package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/q191201771/lal/pkg/logic"
	lalmaxserver "github.com/q191201771/lalmax/server"
)

type bridgeRequestKey struct{ principal, key string }

const bridgeRequestRetention = 24 * time.Hour
const maxBridgeRequests = 4096

type nvrSFUPolicy struct{ app *App }

// Only registered SFU outputs use the shared RTP sender-report clock.
func (a *App) sfuRecordingSourceTime(ctx context.Context, streamID string) (bool, error) {
	if len(streamID) != 52 || !strings.HasPrefix(streamID, "sfu_") {
		return false, nil
	}
	if a.db == nil {
		return false, errors.New("SFU output registry unavailable")
	}
	return a.db.IsSFUBridgeOutput(ctx, streamID)
}

// A publisher belongs to one embedded media-server instance.
type nvrSFUPublisher struct {
	engine *media.EmbeddedLalmax
	server *lalmaxserver.LalMaxServer
}

func (p *nvrSFUPublisher) AddCustomizePubSession(stream string) (logic.ICustomizePubSessionContext, error) {
	if p.engine.Server() != p.server || !p.server.Ready() {
		return nil, errors.New("SFU media server unavailable")
	}
	return p.server.AddCustomizePubSession(stream)
}
func (p *nvrSFUPublisher) DelCustomizePubSession(session logic.ICustomizePubSessionContext) {
	p.server.DelCustomizePubSession(session)
}

func (p *nvrSFUPolicy) check(ctx context.Context, principal string) error {
	switch {
	case strings.HasPrefix(principal, "user:"):
		id, err := strconv.ParseInt(strings.TrimPrefix(principal, "user:"), 10, 64)
		if err != nil || id <= 0 {
			return errors.New("unauthorized")
		}
		user, err := p.app.db.GetUserByID(ctx, id)
		if err != nil || user == nil || !user.Enabled || user.Role != model.RoleSuperAdmin {
			return errors.New("unauthorized")
		}
		return nil
	case strings.HasPrefix(principal, "legacy:"):
		count, err := p.app.db.CountUsers(ctx)
		if err != nil || count != 0 || strings.TrimPrefix(principal, "legacy:") != p.app.cfg.Auth.Username {
			return errors.New("unauthorized")
		}
		return nil
	default:
		return errors.New("unauthorized")
	}
}

func validNVRSFURoomID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
