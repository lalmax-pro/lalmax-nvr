package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi"
	"github.com/stretchr/testify/require"
)

func TestXiaomiLogoutClearsPersistedAndRuntimeCredentials(t *testing.T) {
	cfg := &config.Config{Xiaomi: config.XiaomiConfig{UserID: "user", Token: "secret-token", Region: "sg"}}
	cloud := NewLocalXiaomiAuth(cfg)
	t.Cleanup(func() { xiaomi.SetCloudConfig(config.XiaomiConfig{}) })
	h, _, _, _ := api2ioNewHandler(t, cfg, cloud)
	h.configPath = filepath.Join(t.TempDir(), "nvr.yaml")
	require.NoError(t, config.Save(h.configPath, cfg))

	status := api2ioDo(t, h, http.MethodGet, "/api/xiaomi/status", "")
	require.JSONEq(t, `{"authenticated":true,"region":"sg"}`, status.Body.String())
	require.NotContains(t, status.Body.String(), "secret-token")
	for i := 0; i < 2; i++ {
		rr := api2ioDo(t, h, http.MethodPost, "/api/xiaomi/logout", "")
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	}
	require.Empty(t, cfg.Xiaomi)
	require.Empty(t, cloud.cloudConfig())
	onDisk, err := config.Load(h.configPath)
	require.NoError(t, err)
	require.Empty(t, onDisk.Xiaomi.UserID)
	require.Empty(t, onDisk.Xiaomi.Token)
	status = api2ioDo(t, h, http.MethodGet, "/api/xiaomi/status", "")
	require.JSONEq(t, `{"authenticated":false,"region":""}`, status.Body.String())
	devices := api2ioDo(t, h, http.MethodGet, "/api/xiaomi/devices", "")
	require.Equal(t, http.StatusOK, devices.Code)
	require.Contains(t, devices.Body.String(), `"authenticated":false`)
}

func TestXiaomiLogoutPersistenceFailurePreservesLogin(t *testing.T) {
	previous := config.XiaomiConfig{UserID: "user", Token: "token", Region: "cn"}
	cfg := &config.Config{Xiaomi: previous}
	cloud := &api2ioCloud{}
	h, _, _, _ := api2ioNewHandler(t, cfg, cloud)
	h.configPath = t.TempDir()
	rr := api2ioDo(t, h, http.MethodPost, "/api/xiaomi/logout", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	require.Equal(t, previous, cfg.Xiaomi)
	require.Zero(t, cloud.setCalls)
}

type failingXiaomiLogout struct {
	api2ioCloud
	live config.XiaomiConfig
}

func (c *failingXiaomiLogout) SetCloudConfig(_ context.Context, user, token, region string) error {
	if token == "" {
		return errors.New("runtime logout failed")
	}
	c.live = config.XiaomiConfig{UserID: user, Token: token, Region: region}
	return nil
}

func TestXiaomiLogoutRuntimeFailureRollsBackCredentials(t *testing.T) {
	previous := config.XiaomiConfig{UserID: "user", Token: "token", Region: "sg"}
	cfg := &config.Config{Xiaomi: previous}
	cloud := &failingXiaomiLogout{live: previous}
	h, _, _, _ := api2ioNewHandler(t, cfg, cloud)
	h.configPath = filepath.Join(t.TempDir(), "nvr.yaml")
	require.NoError(t, config.Save(h.configPath, cfg))
	rr := api2ioDo(t, h, http.MethodPost, "/api/xiaomi/logout", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	require.Equal(t, previous, h.xiaomiConfig())
	require.Equal(t, previous, cloud.live)
	onDisk, err := config.Load(h.configPath)
	require.NoError(t, err)
	require.Equal(t, previous, onDisk.Xiaomi)
}

func TestXiaomiEmptyDevicesRetainAuthenticatedStatus(t *testing.T) {
	cfg := &config.Config{Xiaomi: config.XiaomiConfig{UserID: "user", Token: "token", Region: "de"}}
	h, _, _, _ := api2ioNewHandler(t, cfg, &api2ioCloud{})
	rr := api2ioDo(t, h, http.MethodGet, "/api/xiaomi/devices", "")
	require.JSONEq(t, `{"authenticated":true,"devices":[]}`, rr.Body.String())
}

func TestXiaomiLogoutRequiresOperatePermission(t *testing.T) {
	cfg := &config.Config{Xiaomi: config.XiaomiConfig{UserID: "user", Token: "token"}}
	h, _, _, _ := api2ioNewHandler(t, cfg, &api2ioCloud{})
	h.SetMultiUserAuthMW(middleware.InjectUser(&model.User{Role: model.RoleUser}))
	rr := api2ioDo(t, h, http.MethodPost, "/api/xiaomi/logout", "")
	require.Equal(t, http.StatusForbidden, rr.Code)
	require.Equal(t, "token", cfg.Xiaomi.Token)
}

type blockingXiaomiLogin struct {
	api2ioCloud
	started chan struct{}
	release chan struct{}
}

func (c *blockingXiaomiLogin) SignIn(ctx context.Context, _, _, _ string) (*CloudAuthResult, *CloudVerificationRequired, error) {
	close(c.started)
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-c.release:
		return &CloudAuthResult{UserID: "user", PassToken: "token", Region: "cn"}, nil, nil
	}
}

func TestXiaomiLogoutWaitsForInFlightLogin(t *testing.T) {
	cfg := &config.Config{}
	cloud := &blockingXiaomiLogin{started: make(chan struct{}), release: make(chan struct{})}
	h, _, _, _ := api2ioNewHandler(t, cfg, cloud)
	h.configPath = filepath.Join(t.TempDir(), "nvr.yaml")
	router := h.Routes()
	var once sync.Once
	release := func() { once.Do(func() { close(cloud.release) }) }
	t.Cleanup(release)
	loginDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/xiaomi/auth", strings.NewReader(`{"username":"user","password":"pass"}`)))
		loginDone <- rr
	}()
	select {
	case <-cloud.started:
	case <-time.After(3 * time.Second):
		t.Fatal("login did not start")
	}
	logoutDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/xiaomi/logout", nil))
		logoutDone <- rr
	}()
	select {
	case <-logoutDone:
		t.Fatal("logout completed before the in-flight login")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case rr := <-loginDone:
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	case <-time.After(3 * time.Second):
		t.Fatal("login did not finish")
	}
	select {
	case rr := <-logoutDone:
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	case <-time.After(3 * time.Second):
		t.Fatal("logout did not finish")
	}
	require.Empty(t, h.xiaomiConfig())
}
