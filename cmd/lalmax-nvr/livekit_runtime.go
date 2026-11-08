package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/api"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/livekitsfu"
	lkconfig "github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/livekit-server/pkg/service"
)

const liveKitTokenValidity = 5 * time.Minute

type nvrLiveKitRuntime struct {
	server    *service.LivekitServer
	config    *lkconfig.Config
	issuer    *livekitsfu.TokenIssuer
	policy    *nvrSFUPolicy
	publicURL string
	bridges   *nvrLiveKitBridgeRegistry
	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	err       error
	closed    bool
}

func newNVRLiveKitRuntime(a *App, cfg config.SFULiveKitConfig) (*nvrLiveKitRuntime, error) {
	data, err := os.ReadFile(cfg.ConfigFile)
	if err != nil {
		return nil, fmt.Errorf("read LiveKit configuration: %w", err)
	}
	conf, err := lkconfig.NewConfig(string(data), true, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("parse LiveKit configuration: %w", err)
	}
	// LiveKit development mode registers debug handlers on the process-global
	// HTTP mux. NVR embeds the server and must not expose or collide with them.
	if conf.Development {
		return nil, errors.New("LiveKit development mode is not supported in NVR")
	}
	if err := conf.ValidateKeys(); err != nil {
		return nil, fmt.Errorf("validate LiveKit keys: %w", err)
	}
	// Freeze the validated key-file snapshot so a later file rotation cannot
	// make the token issuer and the embedded server use different secrets.
	conf.KeyFile = ""
	secret := conf.Keys[cfg.APIKey]
	issuer, err := livekitsfu.NewTokenIssuer(cfg.APIKey, secret)
	if err != nil {
		return nil, errors.New("configured LiveKit API key was not found")
	}
	return &nvrLiveKitRuntime{config: conf, issuer: issuer, policy: &nvrSFUPolicy{app: a}, publicURL: cfg.PublicURL}, nil
}

func (r *nvrLiveKitRuntime) Start(parent context.Context) error {
	if err := parent.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("LiveKit is closed")
	}
	if r.done != nil {
		r.mu.Unlock()
		return errors.New("LiveKit already started")
	}
	server, err := livekitsfu.NewConfiguredServer(r.config)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	r.server = server
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	r.done = make(chan struct{})
	done := r.done
	r.mu.Unlock()
	go func() {
		err := livekitsfu.RunPrepared(ctx, server)
		r.mu.Lock()
		r.err = err
		r.mu.Unlock()
		close(done)
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()
	for {
		if server.IsRunning() {
			r.mu.Lock()
			closed := r.closed
			r.mu.Unlock()
			if closed {
				return errors.New("LiveKit is closed")
			}
			if err := parent.Err(); err != nil {
				return err
			}
			if r.bridges != nil {
				go r.bridges.monitor(ctx)
			}
			return nil
		}
		select {
		case <-done:
			r.mu.Lock()
			err := r.err
			r.mu.Unlock()
			if err == nil {
				err = errors.New("LiveKit stopped during startup")
			}
			return err
		case <-parent.Done():
			cancel()
			return parent.Err()
		case <-timeout.C:
			cancel()
			return errors.New("LiveKit startup timed out")
		case <-ticker.C:
		}
	}
}

func (r *nvrLiveKitRuntime) Close(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	cancel, done, bridges := r.cancel, r.done, r.bridges
	r.mu.Unlock()
	var bridgeErr error
	if bridges != nil {
		bridgeErr = bridges.Close(ctx)
	}
	if cancel == nil {
		return bridgeErr
	}
	cancel()
	select {
	case <-done:
		r.mu.Lock()
		err := r.err
		r.mu.Unlock()
		if errors.Is(err, context.Canceled) {
			return bridgeErr
		}
		return errors.Join(bridgeErr, err)
	case <-ctx.Done():
		return errors.Join(bridgeErr, ctx.Err())
	}
}

// IssueLiveKitJoin is only called after HTTP authentication; it rechecks the
// NVR account so revoked administrators cannot obtain new room credentials.
func (r *nvrLiveKitRuntime) IssueLiveKitJoin(ctx context.Context, principal, room string) (api.LiveKitJoin, error) {
	r.mu.Lock()
	server := r.server
	closed := r.closed
	r.mu.Unlock()
	if !validNVRSFURoomID(room) || closed || server == nil || !server.IsRunning() {
		return api.LiveKitJoin{}, errors.New("room unavailable")
	}
	if err := r.policy.check(ctx, principal); err != nil {
		return api.LiveKitJoin{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.server != server || !server.IsRunning() {
		return api.LiveKitJoin{}, errors.New("room unavailable")
	}
	if err := ctx.Err(); err != nil {
		return api.LiveKitJoin{}, err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return api.LiveKitJoin{}, err
	}
	identity := "nvr_" + hex.EncodeToString(id[:])
	token, err := r.issuer.IssueJoin(livekitsfu.JoinGrant{Room: room, Identity: identity, ValidFor: liveKitTokenValidity, CanPublish: true, CanSubscribe: true})
	if err != nil {
		return api.LiveKitJoin{}, err
	}
	return api.LiveKitJoin{URL: r.publicURL, Token: token, Room: room, Identity: identity, ExpiresIn: int(liveKitTokenValidity.Seconds())}, nil
}
