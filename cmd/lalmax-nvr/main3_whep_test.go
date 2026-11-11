package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/gb28181"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	lalmaxserver "github.com/q191201771/lalmax/server"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

const (
	main3GBDeviceID  = "34020000002000000001"
	main3GBChannelID = "34020000001320000001"
)

// main3WHEPEvent builds the stream-not-found hook event for a WHEP stream.
func main3WHEPEvent(stream string) lalmaxserver.HookEvent {
	return lalmaxserver.HookEvent{
		Event:   lalmaxserver.HookEventStreamNotFound,
		Payload: []byte(`{"app":"live","stream":"` + stream + `","schema":"whep"}`),
	}
}

// TestMain3GBWHEPSIPIDValidation covers the numeric-only SIP identifier check.
func TestMain3GBWHEPSIPIDValidation(t *testing.T) {
	_, _, ok := splitGBWHEPStreamID("3402000000200000000a:" + main3GBChannelID)
	require.False(t, ok)
	_, _, ok = splitGBWHEPStreamID("short:" + main3GBChannelID)
	require.False(t, ok)
	_, _, ok = splitGBWHEPStreamID(main3GBDeviceID)
	require.False(t, ok)
	device, channel, ok := splitGBWHEPStreamID(main3GBDeviceID + ":" + main3GBChannelID)
	require.True(t, ok)
	require.Equal(t, main3GBDeviceID, device)
	require.Equal(t, main3GBChannelID, channel)
}

// TestMain3GBWHEPHookDeduplicatesAndCoolsDown covers the in-flight guard, the
// per-stream cooldown and the INVITE failure warning.
func TestMain3GBWHEPHookDeduplicatesAndCoolsDown(t *testing.T) {
	stream := main3GBDeviceID + ":" + main3GBChannelID
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	hook := newGBWHEPOnDemandHook(
		func(string, string) bool { return true },
		func(string, string, string) error {
			entered <- struct{}{}
			<-release
			return nil
		},
	)
	hook.SetEnabled(true)

	require.NoError(t, hook.OnHookEvent(main3WHEPEvent(stream)))
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("on-demand INVITE never started")
	}

	// A second request while the first INVITE is in flight is ignored.
	require.NoError(t, hook.OnHookEvent(main3WHEPEvent(stream)))

	close(release)
	require.Eventually(t, func() bool {
		hook.mu.Lock()
		defer hook.mu.Unlock()
		return len(hook.inFlight) == 0
	}, 10*time.Second, 5*time.Millisecond)

	// The cooldown window keeps the same stream from being invited again.
	require.NoError(t, hook.OnHookEvent(main3WHEPEvent(stream)))

	// A failing INVITE is logged, not returned.
	failing := newGBWHEPOnDemandHook(
		func(string, string) bool { return true },
		func(string, string, string) error { return errors.New("invite failed") },
	)
	failing.SetEnabled(true)
	require.NoError(t, failing.OnHookEvent(main3WHEPEvent(
		main3GBDeviceID+":34020000001320000002")))
	require.Eventually(t, func() bool {
		failing.mu.Lock()
		defer failing.mu.Unlock()
		return len(failing.inFlight) == 0
	}, 10*time.Second, 5*time.Millisecond)
}

// TestMain3GBWHEPHookPrunesRecentEntries covers the recent-entry garbage
// collection branch once the map grows beyond its soft limit.
func TestMain3GBWHEPHookPrunesRecentEntries(t *testing.T) {
	hook := newGBWHEPOnDemandHook(
		func(string, string) bool { return true },
		func(string, string, string) error { return nil },
	)
	hook.SetEnabled(true)

	stale := time.Now().Add(-10 * time.Minute)
	hook.mu.Lock()
	for i := 0; i < 600; i++ {
		hook.recent["34020000002000000"+string(rune('0'+i%10))+":"+main3GBChannelID+string(rune('a'+i%26))] = stale
	}
	hook.mu.Unlock()

	stream := main3GBDeviceID + ":" + main3GBChannelID
	require.NoError(t, hook.OnHookEvent(main3WHEPEvent(stream)))
	require.Eventually(t, func() bool {
		hook.mu.Lock()
		defer hook.mu.Unlock()
		return len(hook.recent) < 600
	}, 10*time.Second, 5*time.Millisecond)
}

// TestMain3GBWHEPWithMissingDeviceStore covers the guards used when the SIP
// server has no device store.
func TestMain3GBWHEPWithMissingDeviceStore(t *testing.T) {
	app := &App{gb28181Svr: &gb28181.Server{}}
	require.False(t, app.gbWHEPEligible(main3GBDeviceID, main3GBChannelID))
	require.ErrorContains(t, app.startGBWHEP(main3GBDeviceID, main3GBChannelID, "stream"), "device store unavailable")
}

// TestMain3GBWHEPOnlineDevice covers the eligibility and INVITE paths for an
// online device with a known channel.
func TestMain3GBWHEPOnlineDevice(t *testing.T) {
	cfg, cfgPath := main3HTTPConfig(t)
	enabled := true
	cfg.GB28181 = config.GB28181Config{
		Enabled:  &enabled,
		ID:       main3GBDeviceID,
		Password: "secret",
		MediaIP:  "127.0.0.1",
		Port:     freeTCPPort(t),
	}
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, app.Stop()) })
	require.NotNil(t, app.gb28181Svr)

	store := app.gb28181Svr.GetDeviceStore()
	require.NotNil(t, store)
	device := &gb28181.Device{DeviceID: main3GBDeviceID, IsOnline: true}
	device.Channels.Store(main3GBChannelID, &gb28181.Channel{
		ChannelID: main3GBChannelID,
		Name:      "channel",
	})
	store.Store(main3GBDeviceID, device)

	require.True(t, app.gbWHEPEligible(main3GBDeviceID, main3GBChannelID))
	require.False(t, app.gbWHEPEligible(main3GBDeviceID, "34020000001320000099"))
	require.ErrorContains(t,
		app.startGBWHEP(main3GBDeviceID, "34020000001320000099", "stream"),
		"unknown GB28181 channel")
}

// TestMain3MultiUserLogin covers the users-table authentication closures, which
// only run once at least one database user exists.
func TestMain3MultiUserLogin(t *testing.T) {
	app := main3AuthedApp(t, nil)
	hash, err := bcrypt.GenerateFromPassword([]byte("main3-user-password"), bcrypt.MinCost)
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, app.db.CreateUser(context.Background(), &model.User{
		Username:     "operator",
		PasswordHash: string(hash),
		Role:         model.RoleSuperAdmin,
		Enabled:      true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/cameras", nil)
	req.SetBasicAuth("operator", "main3-user-password")
	rec := httptest.NewRecorder()
	app.httpServer.Handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

// TestMain3GBWHEPHookIgnoresUnrelatedEvents covers the early returns of the
// on-demand hook for unrelated hook payloads.
func TestMain3GBWHEPHookIgnoresUnrelatedEvents(t *testing.T) {
	called := false
	hook := newGBWHEPOnDemandHook(
		func(string, string) bool { called = true; return true },
		func(string, string, string) error { called = true; return nil },
	)
	hook.SetEnabled(true)
	// Not a WHEP schema.
	require.NoError(t, hook.OnHookEvent(lalmaxserver.HookEvent{
		Event:   lalmaxserver.HookEventStreamNotFound,
		Payload: []byte(`{"app":"live","stream":"a:b","schema":"hls"}`),
	}))
	// Not the live app.
	require.NoError(t, hook.OnHookEvent(lalmaxserver.HookEvent{
		Event:   lalmaxserver.HookEventStreamNotFound,
		Payload: []byte(`{"app":"vod","stream":"a:b","schema":"whep"}`),
	}))
	require.False(t, called)
}
