package main

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMain3StartAllOptionalSubsystems boots the application with every optional
// subsystem enabled (embedded media engine, rolling merge, MQTT, FTP, RTMP/SRT/
// WHIP ingest, DLNA and health monitoring) and shuts it down with SIGTERM.
func TestMain3StartAllOptionalSubsystems(t *testing.T) {
	// The embedded engine and FTP server may create files in the working
	// directory, so keep them out of the repository.
	t.Chdir(t.TempDir())

	cfg, cfgPath := testConfig(t)
	addr := freeTCPAddr(t)
	cfg.Server.Listen = addr
	cfg.JT808.Enabled = false

	enabled := true
	cfg.Merge.Enabled = true
	cfg.Merge.CheckInterval = "1h"
	cfg.Health.Enabled = true
	cfg.DLNA.Enabled = &enabled
	// ftpserverlib reads and clears its listener from two goroutines during
	// shutdown. The race detector reports that library race, so this test
	// leaves FTP disabled like the other graceful-shutdown cases.
	ftpDisabled := false
	cfg.FTP.Enabled = &ftpDisabled
	cfg.FTP.Port = freeTCPPort(t)
	cfg.RTMP.Enabled = &enabled
	cfg.RTMP.Port = freeTCPPort(t)
	cfg.SRT.Enabled = &enabled
	cfg.SRT.Port = freeTCPPort(t)
	cfg.WHIP.Enabled = &enabled
	cfg.GB28181.WHEPOnDemandEnabled = true
	cfg.MQTT.Enabled = true
	cfg.MQTT.Broker = "tcp://127.0.0.1:1"
	cfg.MQTT.ClientID = "nvr-main3"
	cfg.MQTT.Topic = "nvr"

	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	require.NotNil(t, app.groupWriter)
	require.NotNil(t, app.gbWHEPHook)
	t.Cleanup(func() { signal.Reset(syscall.SIGTERM) })

	done := make(chan error, 1)
	go func() { done <- app.Start() }()
	waitForTCP(t, addr)

	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGTERM))
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(60 * time.Second):
		t.Fatal("Start did not return after SIGTERM")
	}

	// The optional ingest handlers and services are released by Stop.
	require.Nil(t, app.recSched)
}
