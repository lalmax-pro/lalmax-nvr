package api

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi"
	"github.com/stretchr/testify/require"
)

func TestXiaomiAccountsDoNotExposeCredentials(t *testing.T) {
	cfg := &config.Config{Xiaomi: config.XiaomiConfig{UserID: "default-user", Token: "default-secret", Accounts: map[string]config.XiaomiAccount{
		"home": {UserID: "home-user", Token: "home-secret", Region: "sg"},
	}}}
	h, _, _, _ := api2ioNewHandler(t, cfg, &api2ioCloud{})
	rr := api2ioDo(t, h, http.MethodGet, "/api/xiaomi/accounts", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"id":"home"`)
	require.Contains(t, rr.Body.String(), `"region":"sg"`)
	require.NotContains(t, rr.Body.String(), "home-secret")
	require.NotContains(t, rr.Body.String(), "default-secret")
	require.NotContains(t, rr.Body.String(), "home-user")
}

func TestXiaomiDefaultLogoutPreservesNamedAccounts(t *testing.T) {
	account := config.XiaomiAccount{UserID: "home-user", Token: "home-token", Region: "sg"}
	cfg := &config.Config{Xiaomi: config.XiaomiConfig{UserID: "default-user", Token: "default-token", Accounts: map[string]config.XiaomiAccount{"home": account}}}
	cloud := NewLocalXiaomiAuth(cfg)
	t.Cleanup(func() { xiaomi.SetCloudConfig(config.XiaomiConfig{}) })
	h, _, _, _ := api2ioNewHandler(t, cfg, cloud)
	h.configPath = filepath.Join(t.TempDir(), "nvr.yaml")
	require.NoError(t, config.Save(h.configPath, cfg))
	rr := api2ioDo(t, h, http.MethodPost, "/api/xiaomi/logout", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Empty(t, cfg.Xiaomi.Token)
	require.Equal(t, account, cfg.Xiaomi.Accounts["home"])
	require.Equal(t, account, cloud.cloudConfig().Accounts["home"])
	onDisk, err := config.Load(h.configPath)
	require.NoError(t, err)
	require.Equal(t, account, onDisk.Xiaomi.Accounts["home"])
}

type namedAccountCloud struct {
	api2ioCloud
	selected string
}

func (c *namedAccountCloud) ListDevicesForAccount(_ context.Context, id string) ([]CloudDeviceInfo, error) {
	c.selected = id
	return []CloudDeviceInfo{{DID: "1", Name: "named"}}, nil
}

func TestXiaomiNamedAccountDeviceDiscovery(t *testing.T) {
	cfg := &config.Config{Xiaomi: config.XiaomiConfig{Accounts: map[string]config.XiaomiAccount{
		"home": {UserID: "home-user", Token: "home-secret", Region: "sg"},
	}}}
	cloud := &namedAccountCloud{}
	h, _, _, _ := api2ioNewHandler(t, cfg, cloud)
	rr := api2ioDo(t, h, http.MethodGet, "/api/xiaomi/devices?account=home", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "home", cloud.selected)
	require.Contains(t, rr.Body.String(), `"name":"named"`)
	rr = api2ioDo(t, h, http.MethodGet, "/api/xiaomi/devices?account=missing", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "not authenticated")
}

type manageableXiaomiCloud struct {
	api2ioCloud
	live     config.XiaomiConfig
	fail     bool
	canceled []string
}

func (c *manageableXiaomiCloud) CancelPendingLogin(id string) { c.canceled = append(c.canceled, id) }

func (c *manageableXiaomiCloud) SetXiaomiConfig(_ context.Context, cfg config.XiaomiConfig) error {
	if c.fail && len(cfg.Accounts) > 0 {
		return errors.New("runtime update failed")
	}
	c.live = cfg
	return nil
}

func TestXiaomiNamedAccountLoginVerificationAndRemoval(t *testing.T) {
	cfg := &config.Config{Xiaomi: config.XiaomiConfig{UserID: "default-user", Token: "default-token", Region: "cn"}}
	cloud := &manageableXiaomiCloud{}
	cloud.signInVerify = &CloudVerificationRequired{Captcha: []byte("image"), CaptchaSessionID: "session-home"}
	cloud.captchaResult = &CloudAuthResult{UserID: "home-user", PassToken: "home-token", Region: "sg"}
	h, _, _, _ := api2ioNewHandler(t, cfg, cloud)
	h.configPath = filepath.Join(t.TempDir(), "nvr.yaml")
	rr := api2ioDo(t, h, http.MethodPost, "/api/xiaomi/auth", `{"username":"user","password":"pass","account":"home"}`)
	require.Equal(t, http.StatusAccepted, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPost, "/api/xiaomi/captcha", `{"session_id":"session-home","captcha_code":"123","account":"default"}`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPost, "/api/xiaomi/captcha", `{"session_id":"session-home","captcha_code":"123","account":"home"}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, "default-token", cfg.Xiaomi.Token)
	require.Equal(t, "home-token", cfg.Xiaomi.Accounts["home"].Token)
	require.Equal(t, cfg.Xiaomi, cloud.live)
	saved, err := config.Load(h.configPath)
	require.NoError(t, err)
	require.Equal(t, cfg.Xiaomi, saved.Xiaomi)
	rr = api2ioDo(t, h, http.MethodPost, "/api/xiaomi/logout?account=home", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.NotContains(t, cfg.Xiaomi.Accounts, "home")
	require.Equal(t, "default-token", cfg.Xiaomi.Token)
}

func TestXiaomiNamedAccountRollbackAndCameraProtection(t *testing.T) {
	cfg := &config.Config{Xiaomi: config.XiaomiConfig{UserID: "default", Token: "default-token"}}
	cloud := &manageableXiaomiCloud{fail: true}
	h, _, _, _ := api2ioNewHandler(t, cfg, cloud)
	h.configPath = filepath.Join(t.TempDir(), "nvr.yaml")
	require.NoError(t, config.Save(h.configPath, cfg))
	err := h.saveXiaomiAccountToken(context.Background(), "home", &CloudAuthResult{UserID: "u", PassToken: "p"})
	require.Error(t, err)
	require.Empty(t, cfg.Xiaomi.Accounts)
	saved, err := config.Load(h.configPath)
	require.NoError(t, err)
	require.Empty(t, saved.Xiaomi.Accounts)
	cloud.fail = false
	require.NoError(t, h.saveXiaomiAccountToken(context.Background(), "home", &CloudAuthResult{UserID: "u", PassToken: "p"}))
	cfg.Cameras = []config.CameraConfig{{ID: "cam", Protocol: "xiaomi", URL: "xiaomi://1?account=home"}}
	require.ErrorContains(t, h.removeXiaomiAccount(context.Background(), "home"), "used by camera")
	require.Contains(t, cfg.Xiaomi.Accounts, "home")
}

func TestXiaomiLogoutCancelsOnlyItsOwnVerification(t *testing.T) {
	cfg := &config.Config{Xiaomi: config.XiaomiConfig{UserID: "u", Token: "p", Accounts: map[string]config.XiaomiAccount{"home": {UserID: "home", Token: "secret"}}}}
	cloud := &manageableXiaomiCloud{}
	h, _, _, _ := api2ioNewHandler(t, cfg, cloud)
	h.configPath = filepath.Join(t.TempDir(), "nvr.yaml")
	h.rememberXiaomiChallenge(&CloudVerificationRequired{CaptchaSessionID: "default-session"}, "default")
	h.rememberXiaomiChallenge(&CloudVerificationRequired{CaptchaSessionID: "home-session"}, "home")
	rr := api2ioDo(t, h, http.MethodPost, "/api/xiaomi/logout", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, []string{"default-session"}, cloud.canceled)
	require.Contains(t, h.xiaomiChallenges, "home-session")
	require.NotContains(t, h.xiaomiChallenges, "default-session")
}
