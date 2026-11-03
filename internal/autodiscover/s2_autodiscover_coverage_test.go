package autodiscover

import (
	"context"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/stretchr/testify/require"
)

func s2Bool(v bool) *bool { return &v }

func s2EnabledConfig() config.AutoDiscoverConfig {
	return config.AutoDiscoverConfig{
		Enabled:        s2Bool(true),
		ListenForHello: s2Bool(false),
		ScanInterval:   "30s",
	}
}

func TestS2ServiceStartDisabled(t *testing.T) {
	// Nil receiver and disabled configuration are both no-ops.
	var nilService *Service
	require.NoError(t, nilService.Start(context.Background()))

	service := New(config.AutoDiscoverConfig{}, &fakeEnroller{}, nil, nil, nil)
	require.NoError(t, service.Start(context.Background()))
	service.Stop()

	// A second Start while running returns immediately.
	cfg := s2EnabledConfig()
	service = New(cfg, &fakeEnroller{}, nil, nil, nil)
	require.NoError(t, service.Start(context.Background()))
	require.True(t, service.started)
	require.NoError(t, service.Start(context.Background()))
	service.Stop()
	require.False(t, service.started)
	// Stopping an already stopped service is safe.
	service.Stop()
}

func TestS2ServiceHelloListenerBranches(t *testing.T) {
	// An unknown interface makes the Hello listener fail and fall back to scans.
	cfg := s2EnabledConfig()
	cfg.ListenForHello = s2Bool(true)
	cfg.NetworkInterface = "s2-not-a-real-interface"
	service := New(cfg, &fakeEnroller{}, nil, nil, nil)
	require.NoError(t, service.Start(context.Background()))
	service.Stop()

	// The default interface listener either binds or degrades gracefully; both
	// outcomes are acceptable and exercised here.
	cfg = s2EnabledConfig()
	cfg.ListenForHello = s2Bool(true)
	service = New(cfg, &fakeEnroller{}, nil, nil, nil)
	require.NoError(t, service.Start(context.Background()))
	service.Stop()
}

func TestS2ServiceApply(t *testing.T) {
	// Nil receiver is a no-op.
	var nilService *Service
	nilService.Apply(context.Background(), config.AutoDiscoverConfig{})

	service := New(config.AutoDiscoverConfig{}, &fakeEnroller{}, nil, nil, nil)
	require.NoError(t, service.Start(context.Background()))

	// Enabling through Apply starts the service and forwards config to the adder.
	cfg := s2EnabledConfig()
	service.Apply(context.Background(), cfg)
	require.True(t, service.started)
	require.Equal(t, cfg, service.cfg)
	require.Equal(t, cfg, service.adder.cfg)

	// Disabling through Apply stops the service.
	service.Apply(context.Background(), config.AutoDiscoverConfig{})
	require.False(t, service.started)
	service.Stop()
}

func TestS2NewServiceWiresAdder(t *testing.T) {
	cfg := s2EnabledConfig()
	enroller := &fakeEnroller{}
	service := New(cfg, enroller, nil, nil, nil)
	require.NotNil(t, service.adder)
	require.Equal(t, cfg, service.adder.cfg)
	require.NotNil(t, service.adder.enroller)
}
