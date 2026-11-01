package rediscovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/stretchr/testify/require"
)

// s2Engine builds an engine whose probe is replaced by fn.
func s2Engine(cfg config.RediscoveryConfig, fn func(ctx context.Context, host string, port int, timeout time.Duration) (*onvif.DiscoveredDevice, error)) *Engine {
	e := New(cfg)
	e.probeFn = fn
	return e
}

func TestS2NewEngine(t *testing.T) {
	e := New(config.RediscoveryConfig{})
	require.NotNil(t, e)
	require.NotNil(t, e.probeFn, "the real ONVIF probe is installed by default")
}

func TestS2DiscoverByStableIDRequiresStableID(t *testing.T) {
	e := s2Engine(config.RediscoveryConfig{}, func(context.Context, string, int, time.Duration) (*onvif.DiscoveredDevice, error) {
		t.Fatal("probe must not run without a stable id")
		return nil, nil
	})
	_, err := e.DiscoverByStableID(context.Background(), "  ", "http://10.0.0.5/onvif/device_service", nil, nil)
	require.ErrorContains(t, err, "stable_id is required")
}

func TestS2DiscoverByStableIDFindsDevice(t *testing.T) {
	cfg := config.RediscoveryConfig{MaxParallel: 2}
	e := s2Engine(cfg, func(_ context.Context, host string, port int, _ time.Duration) (*onvif.DiscoveredDevice, error) {
		require.Equal(t, 80, port)
		if host != "10.0.0.7" {
			return nil, errors.New("no device")
		}
		return &onvif.DiscoveredDevice{Endpoint: "http://10.0.0.7/onvif/device_service"}, nil
	})

	var confirmCalls int
	res, err := e.DiscoverByStableID(context.Background(), "SN-123", "http://10.0.0.7/onvif/device_service", []string{"10.0.0.0/30"}, func(_ context.Context, endpoint string) (string, error) {
		confirmCalls++
		require.Equal(t, "http://10.0.0.7/onvif/device_service", endpoint)
		return "  sn-123 ", nil
	})
	require.NoError(t, err)
	require.True(t, res.Found)
	require.Equal(t, "10.0.0.7", res.Host)
	require.Equal(t, "http://10.0.0.7/onvif/device_service", res.Endpoint)
	require.GreaterOrEqual(t, confirmCalls, 1)
}

func TestS2DiscoverByStableIDFallsBackToDefaultEndpoint(t *testing.T) {
	e := s2Engine(config.RediscoveryConfig{MaxParallel: 1}, func(_ context.Context, host string, _ int, _ time.Duration) (*onvif.DiscoveredDevice, error) {
		return &onvif.DiscoveredDevice{}, nil
	})
	res, err := e.DiscoverByStableID(context.Background(), "SN-1", "10.0.0.9", []string{"10.0.0.8/32"}, nil)
	require.NoError(t, err)
	require.True(t, res.Found)
	require.Equal(t, "http://10.0.0.9/onvif/device_service", res.Endpoint)
}

func TestS2DiscoverByStableIDRejectsMismatches(t *testing.T) {
	devices := map[string]*onvif.DiscoveredDevice{
		"10.0.0.20": {Endpoint: "http://10.0.0.20/onvif/device_service"},
		"10.0.0.21": {Endpoint: "http://10.0.0.21/onvif/device_service"},
		"10.0.0.22": {Endpoint: "http://10.0.0.22/onvif/device_service"},
	}
	e := s2Engine(config.RediscoveryConfig{MaxParallel: 1}, func(_ context.Context, host string, _ int, _ time.Duration) (*onvif.DiscoveredDevice, error) {
		return devices[host], nil
	})

	// Confirm errors, serial mismatches and probe failures all reject a host.
	res, err := e.DiscoverByStableID(context.Background(), "SN-TARGET", "", []string{"10.0.0.20/32", "10.0.0.21/32", "10.0.0.22/32"}, func(_ context.Context, endpoint string) (string, error) {
		switch endpoint {
		case "http://10.0.0.20/onvif/device_service":
			return "", errors.New("confirm exploded")
		default:
			return "OTHER-SERIAL", nil
		}
	})
	require.NoError(t, err)
	require.False(t, res.Found)
	require.Empty(t, res.Endpoint)
}

func TestS2DiscoverByStableIDProbeFailures(t *testing.T) {
	e := s2Engine(config.RediscoveryConfig{}, func(_ context.Context, host string, _ int, _ time.Duration) (*onvif.DiscoveredDevice, error) {
		if host == "10.0.0.31" {
			return nil, errors.New("unreachable")
		}
		return nil, nil //nolint:nilnil // exercises the nil-device guard
	})
	res, err := e.DiscoverByStableID(context.Background(), "SN-1", "", []string{"10.0.0.31/32", "10.0.0.32/32"}, nil)
	require.NoError(t, err)
	require.False(t, res.Found)
}

func TestS2DiscoverByStableIDCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	probed := false
	e := s2Engine(config.RediscoveryConfig{}, func(context.Context, string, int, time.Duration) (*onvif.DiscoveredDevice, error) {
		probed = true
		return nil, nil
	})
	res, err := e.DiscoverByStableID(ctx, "SN-1", "10.0.0.40", []string{"10.0.0.0/30"}, nil)
	require.NoError(t, err)
	require.False(t, res.Found)
	require.False(t, probed, "cancelled scans must not probe")
}
