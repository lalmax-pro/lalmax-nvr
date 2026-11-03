package api

import (
	"context"
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
