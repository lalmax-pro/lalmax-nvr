package main

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/voip"
	"github.com/stretchr/testify/require"
)

func TestVoIPRuntimeRestoresAfterListenFailure(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	addr := listener.LocalAddr().String()
	listener.Close()
	cfg := voip.Config{Enable: true, SipListenAddr: addr}
	cfg.Normalize()
	app := &App{mediaEngine: &media.EmbeddedLalmax{}}
	defer app.stopVoIP()
	require.NoError(t, app.RestartVoIP(context.Background(), &cfg))
	occupied, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer occupied.Close()
	next := cfg
	next.SipListenAddr = occupied.LocalAddr().String()
	require.Error(t, app.RestartVoIP(context.Background(), &next))
	require.NotNil(t, app.voipSvr)
	require.Equal(t, addr, app.voipApplied.SipListenAddr)
	// Previous listener was restored instead of leaving the service disabled.
	restored, err := net.ListenPacket("udp", addr)
	if restored != nil {
		restored.Close()
	}
	require.Error(t, err)
	cfg.Enable = false
	require.NoError(t, app.RestartVoIP(context.Background(), &cfg))
	require.Nil(t, app.voipSvr)
}
func TestVoIPRestartCannotStartAfterShutdown(t *testing.T) {
	app := &App{mediaEngine: &media.EmbeddedLalmax{}}
	cfg := voip.Config{Enable: true, SipListenAddr: "127.0.0.1:0"}
	cfg.Normalize()
	require.NoError(t, app.RestartVoIP(context.Background(), &cfg))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = app.RestartVoIP(context.Background(), &cfg) }()
	}
	app.stopVoIP()
	wg.Wait()
	require.Error(t, app.RestartVoIP(context.Background(), &cfg))
	require.Nil(t, app.voipSvr)
}
