package gb28181

import (
	"net"
	"strings"
	"testing"
)

func TestConfigValidateAutoDetectsWildcardMediaIP(t *testing.T) {
	cfg := &Config{
		Enabled:  true,
		ID:       "34020000002000000001",
		Port:     5060,
		MediaIP:  "0.0.0.0",
		Password: "test-password-123",
	}

	err := cfg.Validate()
	if err != nil {
		if !strings.Contains(err.Error(), "reachable media server IP") {
			t.Fatalf("unexpected validation error: %v", err)
		}
		return
	}
	ip := net.ParseIP(cfg.MediaIP)
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() {
		t.Fatalf("media ip was not auto-detected to a reachable address: %q", cfg.MediaIP)
	}
}
