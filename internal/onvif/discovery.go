package onvif

import (
	"context"
	"strings"
	"time"

	onviflib "github.com/lalmax-pro/lalmax-nvr/onvif"
)

const defaultDiscoveryTimeout = 5 * time.Second

// Discover performs WS-Discovery to find ONVIF devices on the local network.
func Discover(ctx context.Context, timeout time.Duration) *DiscoveryResult {
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}

	devices, err := onviflib.DiscoverDevices(ctx, timeout)
	if err != nil {
		return &DiscoveryResult{
			Devices: []DiscoveredDevice{},
			Error:   categorizeDiscoveryError(ctx, err),
		}
	}

	if len(devices) == 0 {
		return &DiscoveryResult{
			Devices: []DiscoveredDevice{},
			Error: &DiscoveryError{
				Category: "NO_DEVICES",
				Message:  "no ONVIF devices found on the network",
			},
		}
	}

	result := make([]DiscoveredDevice, 0, len(devices))
	for _, d := range devices {
		if d != nil {
			result = append(result, MapDiscoveredDevice(d))
		}
	}

	return &DiscoveryResult{
		Devices: result,
		Error:   nil,
	}
}

// ProbeDevice sends a direct WS-Discovery Probe via HTTP POST to a specific host:port.
func ProbeDevice(ctx context.Context, host string, port int, timeout time.Duration) (*DiscoveredDevice, error) {
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}

	device, err := onviflib.ProbeDevice(ctx, host, port, timeout)
	if err != nil {
		return nil, err
	}
	if device == nil {
		return nil, nil
	}

	return &DiscoveredDevice{
		UUID:     device.UUID,
		Name:     device.Name,
		XAddrs:   device.XAddrs,
		Scopes:   device.Scopes,
		Hardware: device.Hardware,
		Endpoint: device.Endpoint,
	}, nil
}

// MapDiscoveredDevice converts a standalone library DiscoveredDevice to the project's DiscoveredDevice.
func MapDiscoveredDevice(d *onviflib.DiscoveredDevice) DiscoveredDevice {
	name := d.Name
	endpoint := d.Endpoint

	var hardware string
	for _, scope := range d.Scopes {
		if strings.Contains(scope, "hardware") {
			parts := strings.Split(scope, "/")
			if len(parts) > 0 {
				hardware = parts[len(parts)-1]
			}
		}
	}

	return DiscoveredDevice{
		UUID:     d.UUID,
		Name:     name,
		XAddrs:   d.XAddrs,
		Scopes:   d.Scopes,
		Hardware: hardware,
		Endpoint: endpoint,
	}
}

// ListenHello starts a WS-Discovery Hello listener. Bind failures are returned to the caller.
func ListenHello(ctx context.Context, networkInterface string, handler func(DiscoveredDevice)) error {
	return onviflib.ListenHello(ctx, networkInterface, func(d *onviflib.DiscoveredDevice) {
		if d == nil {
			return
		}
		handler(MapDiscoveredDevice(d))
	})
}

// MapDiscoveredDevices converts a slice of standalone library DiscoveredDevice.
func MapDiscoveredDevices(devices []*onviflib.DiscoveredDevice) []DiscoveredDevice {
	result := make([]DiscoveredDevice, 0, len(devices))
	for _, d := range devices {
		result = append(result, MapDiscoveredDevice(d))
	}
	return result
}

// categorizeDiscoveryError maps a discovery error to a DiscoveryError category.
func categorizeDiscoveryError(ctx context.Context, err error) *DiscoveryError {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if ctx.Err() == context.DeadlineExceeded {
		return &DiscoveryError{Category: "TIMEOUT", Message: "discovery timed out: " + msg}
	}
	if ctx.Err() == context.Canceled {
		return &DiscoveryError{Category: "TIMEOUT", Message: "discovery was cancelled"}
	}
	if strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "timeout") {
		return &DiscoveryError{Category: "TIMEOUT", Message: "discovery timed out: " + msg}
	}
	if strings.Contains(msg, "network is unreachable") ||
		strings.Contains(msg, "no route to host") ||
		strings.Contains(msg, "connection refused") {
		return &DiscoveryError{Category: "NETWORK", Message: "network error: " + msg}
	}
	return &DiscoveryError{Category: "PARSE_ERROR", Message: msg}
}
