package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSFULiveKitConfiguration(t *testing.T) {
	base := SFUConfig{Enabled: true, Backend: "livekit", LiveKit: SFULiveKitConfig{ConfigFile: "livekit.yaml", APIKey: "private-key-name", PublicURL: "wss://sfu.example.test"}}
	if err := base.ValidateBackend(); err != nil {
		t.Fatal(err)
	}
	defaultBackend := base
	defaultBackend.Backend = ""
	if err := defaultBackend.ValidateBackend(); err != nil {
		t.Fatalf("default LiveKit backend rejected: %v", err)
	}
	disabledLegacy := SFUConfig{Backend: "legacy"}
	if err := disabledLegacy.ValidateBackend(); err != nil {
		t.Fatalf("disabled legacy configuration prevented startup: %v", err)
	}
	for _, change := range []func(*SFUConfig){
		func(c *SFUConfig) { c.Backend = "other" },
		func(c *SFUConfig) { c.Backend = "legacy" },
		func(c *SFUConfig) { c.LiveKit.ConfigFile = "" },
		func(c *SFUConfig) { c.LiveKit.APIKey = "" },
		func(c *SFUConfig) { c.LiveKit.PublicURL = "wss://user:pass@sfu.example.test" },
		func(c *SFUConfig) { c.LiveKit.PublicURL = "javascript:alert(1)" },
		func(c *SFUConfig) { c.ICEServers = []SFUICEServer{{URLs: []string{"stun:example.test"}}} },
		func(c *SFUConfig) { c.IdleTimeout = "1m" },
		func(c *SFUConfig) { c.ObserveBandwidth = true },
		func(c *SFUConfig) { c.Pacing = true },
	} {
		c := base
		change(&c)
		if err := c.ValidateBackend(); err == nil {
			t.Fatalf("invalid config accepted: %+v", c)
		}
	}
	data, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-key-name") || strings.Contains(string(data), "livekit.yaml") {
		t.Fatal("LiveKit configuration leaked to settings response")
	}
}
