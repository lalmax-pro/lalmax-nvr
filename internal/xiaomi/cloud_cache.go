package xiaomi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

const cloudSessionTTL = 10 * time.Minute
const cloudDeviceTTL = 30 * time.Second

type recorderCloudCache struct {
	gate         chan struct{}
	configHash   [32]byte
	session      *CloudSession
	sessionUntil time.Time
	devices      []CloudDevice
	devicesUntil time.Time
	login        func(context.Context, string, string, string) (*CloudSession, error)
	list         func(context.Context, *CloudSession) ([]CloudDevice, error)
}

func newRecorderCloudCache() *recorderCloudCache {
	return &recorderCloudCache{gate: make(chan struct{}, 1), login: SignInWithTokenContext, list: GetDeviceListContext}
}

func (c *recorderCloudCache) resolve(ctx context.Context, cfg XiaomiCloudConfig, did, model string) (string, error) {
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-c.gate }()
	now := time.Now()
	identity, _ := json.Marshal([3]string{cfg.UserID, cfg.Token, cfg.Region})
	configHash := sha256.Sum256(identity)
	if c.configHash != configHash || c.session == nil || !now.Before(c.sessionUntil) {
		c.session = nil
		c.devices = nil
		c.devicesUntil = time.Time{}
		session, err := c.login(ctx, cfg.UserID, cfg.Token, cfg.Region)
		if err != nil {
			return "", fmt.Errorf("xiaomi cloud auth: %w", err)
		}
		if session == nil {
			return "", fmt.Errorf("xiaomi cloud auth returned no session")
		}
		snapshot := *session
		snapshot.PassToken = ""
		c.configHash, c.session, c.sessionUntil = configHash, &snapshot, time.Now().Add(cloudSessionTTL)
	}
	if c.devices == nil || !now.Before(c.devicesUntil) {
		devices, err := c.list(ctx, c.session)
		if err != nil {
			c.session = nil
			return "", fmt.Errorf("xiaomi cloud device list: %w", err)
		}
		c.devices = append([]CloudDevice{}, devices...)
		c.devicesUntil = time.Now().Add(cloudDeviceTTL)
	}
	result, err := resolveMISSWithSession(ctx, c.session, did, model, c.devices)
	if err != nil && ctx.Err() == nil {
		c.session = nil
		c.devices = nil
	}
	return result, err
}

func wakeCamera(ctx context.Context, session *CloudSession, did string) error {
	body, err := json.Marshal(map[string]any{"id": 1, "method": "wakeup", "params": map[string]string{"video": "1"}})
	if err != nil {
		return err
	}
	c := &Cloud{ctx: ctx, client: session.client, sid: "xiaomiio", region: session.Region, ssecurity: session.ssecurity, cookies: session.cookies, userID: session.UserID}
	_, err = c.Request(getAPIBaseURL(session.Region), "/home/rpc/"+url.PathEscape(did), string(body), nil)
	return err
}
