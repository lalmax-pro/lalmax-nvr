package api

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
)

type xiaomiFullConfigProxy interface {
	SetXiaomiConfig(context.Context, config.XiaomiConfig) error
}
type xiaomiChallenge struct {
	account string
	expires time.Time
}

func normalizeXiaomiAccount(id string) string {
	if id == "" {
		return "default"
	}
	return id
}
func validXiaomiAccount(id string) error {
	if id == "default" {
		return nil
	}
	if id == "" || len(id) > 64 || strings.ContainsAny(id, "/?&# ") || strings.IndexFunc(id, unicode.IsControl) >= 0 {
		return errors.New("invalid Xiaomi account ID")
	}
	return nil
}
func (h *Handler) rememberXiaomiChallenge(v *CloudVerificationRequired, account string) {
	if v == nil || v.CaptchaSessionID == "" {
		return
	}
	if h.xiaomiChallenges == nil {
		h.xiaomiChallenges = make(map[string]xiaomiChallenge)
	}
	now := time.Now()
	for id, entry := range h.xiaomiChallenges {
		if now.After(entry.expires) {
			delete(h.xiaomiChallenges, id)
		}
	}
	h.xiaomiChallenges[v.CaptchaSessionID] = xiaomiChallenge{account: account, expires: now.Add(11 * time.Minute)}
}
func (h *Handler) challengeAccount(id, requested string) (string, error) {
	account := normalizeXiaomiAccount(requested)
	if err := validXiaomiAccount(account); err != nil {
		return "", err
	}
	if entry, ok := h.xiaomiChallenges[id]; ok {
		if entry.account != account || time.Now().After(entry.expires) {
			return "", errors.New("invalid Xiaomi verification session")
		}
		return account, nil
	}
	if account != "default" {
		return "", errors.New("invalid Xiaomi verification session")
	}
	return account, nil
}
func (h *Handler) saveXiaomiAccountToken(ctx context.Context, account string, result *CloudAuthResult) error {
	if account == "default" {
		return h.saveXiaomiToken(ctx, result)
	}
	if result == nil || result.UserID == "" || result.PassToken == "" {
		return errors.New("missing Xiaomi auth result")
	}
	return h.updateXiaomiNamedAccount(ctx, account, &config.XiaomiAccount{UserID: result.UserID, Token: result.PassToken, Region: result.Region})
}
func (h *Handler) removeXiaomiAccount(ctx context.Context, account string) error {
	return h.updateXiaomiNamedAccount(ctx, account, nil)
}
func (h *Handler) updateXiaomiNamedAccount(ctx context.Context, id string, account *config.XiaomiAccount) error {
	h.xiaomiMu.Lock()
	defer h.xiaomiMu.Unlock()
	if h.config == nil {
		return errors.New("missing Xiaomi configuration")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	proxy, ok := h.cloudProxy.(xiaomiFullConfigProxy)
	if !ok {
		return errors.New("named account management unavailable")
	}
	previous := h.config.Xiaomi
	candidate := *h.config
	candidate.Xiaomi.Accounts = maps.Clone(previous.Accounts)
	if candidate.Xiaomi.Accounts == nil {
		candidate.Xiaomi.Accounts = make(map[string]config.XiaomiAccount)
	}
	if account == nil {
		if _, ok := candidate.Xiaomi.Accounts[id]; !ok {
			return errors.New("Xiaomi account not found")
		}
		var cameras []config.CameraConfig
		if h.camMgr != nil {
			cameras = h.camMgr.ListCameraConfigs()
		} else {
			cameras = h.config.Cameras
		}
		for _, camera := range cameras {
			u, err := url.Parse(camera.URL)
			if camera.Protocol == "xiaomi" && err == nil && u.Query().Get("account") == id {
				return fmt.Errorf("Xiaomi account is used by camera %q", camera.ID)
			}
		}
		delete(candidate.Xiaomi.Accounts, id)
	} else {
		candidate.Xiaomi.Accounts[id] = *account
	}
	if err := config.Save(h.configPath, &candidate); err != nil {
		return err
	}
	if err := proxy.SetXiaomiConfig(ctx, candidate.Xiaomi); err != nil {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return errors.Join(err, proxy.SetXiaomiConfig(rollbackCtx, previous), config.Save(h.configPath, h.config))
	}
	h.config.Xiaomi = candidate.Xiaomi
	return nil
}

func (h *Handler) cancelXiaomiChallenges(account string) {
	for id, challenge := range h.xiaomiChallenges {
		if challenge.account != account {
			continue
		}
		if proxy, ok := h.cloudProxy.(interface{ CancelPendingLogin(string) }); ok {
			proxy.CancelPendingLogin(id)
		}
		delete(h.xiaomiChallenges, id)
	}
}
