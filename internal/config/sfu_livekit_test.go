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
	for _, change := range []func(*SFUConfig){
		func(c *SFUConfig) { c.Backend = "other" },
		func(c *SFUConfig) { c.LiveKit.ConfigFile = "" },
		func(c *SFUConfig) { c.LiveKit.APIKey = "" },
		func(c *SFUConfig) { c.LiveKit.PublicURL = "wss://user:pass@sfu.example.test" },
		func(c *SFUConfig) { c.LiveKit.PublicURL = "javascript:alert(1)" },
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
