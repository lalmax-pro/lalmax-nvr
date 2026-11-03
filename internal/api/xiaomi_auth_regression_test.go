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

func TestXiaomiAuthPersistenceFailureDoesNotPublishCredentials(t *testing.T) {
	t.Helper()
	for _, tc := range []struct{ path, body string }{
		{"/api/xiaomi/auth", `{"username":"user","password":"password"}`},
		{"/api/xiaomi/captcha", `{"session_id":"session","captcha_code":"1234"}`},
		{"/api/xiaomi/verify", `{"session_id":"session","ticket":"1234"}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Helper()
			previous := config.XiaomiConfig{UserID: "old-user", Token: "old-token", Region: "cn"}
			result := &CloudAuthResult{UserID: "new-user", PassToken: "new-token", Region: "sg"}
			cloud := &api2ioCloud{signInResult: result, captchaResult: result, verifyResult: result}
			cfg := &config.Config{Xiaomi: previous}
			h, _, _, _ := api2ioNewHandler(t, cfg, cloud)
			h.configPath = t.TempDir() // Replacing a directory with a config file must fail.
			rr := api2ioDo(t, h, http.MethodPost, tc.path, tc.body)
			require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
			require.Equal(t, previous, cfg.Xiaomi)
			require.Zero(t, cloud.setCalls, "running cameras must not receive unpersisted credentials")
		})
	}
}

type xiaomiRuntimeFailure struct {
	api2ioCloud
	live config.XiaomiConfig
}

func (c *xiaomiRuntimeFailure) SetCloudConfig(_ context.Context, user, token, region string) error {
	if token == "new-token" {
		return errors.New("runtime update failed")
	}
	c.live = config.XiaomiConfig{UserID: user, Token: token, Region: region}
	return nil
}

func TestXiaomiRuntimeUpdateFailureRollsBackDiskAndMemory(t *testing.T) {
	t.Helper()
	previous := config.XiaomiConfig{UserID: "old-user", Token: "old-token", Region: "cn"}
	cloud := &xiaomiRuntimeFailure{live: previous}
	cloud.signInResult = &CloudAuthResult{UserID: "new-user", PassToken: "new-token", Region: "sg"}
	cfg := &config.Config{Xiaomi: previous}
	h, _, _, _ := api2ioNewHandler(t, cfg, cloud)
	h.configPath = filepath.Join(t.TempDir(), "nvr.yaml")
	require.NoError(t, config.Save(h.configPath, cfg))
	rr := api2ioDo(t, h, http.MethodPost, "/api/xiaomi/auth", `{"username":"user","password":"password"}`)
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
	require.Equal(t, previous, cfg.Xiaomi)
	require.Equal(t, previous, cloud.live)
	onDisk, err := config.Load(h.configPath)
	require.NoError(t, err)
	require.Equal(t, previous, onDisk.Xiaomi)
}

func TestXiaomiSuccessfulLoginPersistsAndPublishesSameCredentials(t *testing.T) {
	t.Helper()
	result := &CloudAuthResult{UserID: "new-user", PassToken: "new-token", Region: "sg"}
	cloud := &api2ioCloud{signInResult: result}
	cfg := &config.Config{}
	h, _, _, _ := api2ioNewHandler(t, cfg, cloud)
	h.configPath = filepath.Join(t.TempDir(), "nvr.yaml")
	rr := api2ioDo(t, h, http.MethodPost, "/api/xiaomi/auth", `{"username":"user","password":"password"}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	onDisk, err := config.Load(h.configPath)
	require.NoError(t, err)
	require.Equal(t, cfg.Xiaomi, onDisk.Xiaomi)
	require.Equal(t, "new-token", cfg.Xiaomi.Token)
	require.Equal(t, "new-user", cloud.lastUserID)
	require.Equal(t, 1, cloud.setCalls)
}

func TestLocalXiaomiCloudOperationsHonorCanceledContext(t *testing.T) {
	t.Helper()
	adapter := NewLocalXiaomiAuth(&config.Config{Xiaomi: config.XiaomiConfig{UserID: "user", Token: "token", Region: "cn"}})
	t.Cleanup(func() { xiaomi.SetCloudConfig(config.XiaomiConfig{}) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := adapter.SignIn(ctx, "user", "password", "cn")
	require.ErrorIs(t, err, context.Canceled)
	_, err = adapter.ListDevices(ctx)
	require.ErrorIs(t, err, context.Canceled)
	_, err = adapter.CheckVendor(ctx, "1")
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, adapter.SetCloudConfig(ctx, "other", "other", "sg"), context.Canceled)
	require.Equal(t, "user", adapter.cloudConfig().UserID)
}
