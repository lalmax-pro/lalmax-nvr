package rediscovery

import (
	"strings"
	"testing"
)

func TestCandidateHostsLastAndHints(t *testing.T) {
	hosts := CandidateHosts("http://192.168.1.50:80/onvif/device_service", []string{"10.0.0.8/30"})
	if len(hosts) == 0 {
		t.Fatal("expected candidates")
	}
	if hosts[0] != "192.168.1.50" {
		t.Fatalf("last host should be first, got %q", hosts[0])
	}
	foundHint := false
	for _, h := range hosts {
		if strings.HasPrefix(h, "10.0.0.") {
			foundHint = true
			break
		}
	}
	if !foundHint {
		t.Fatalf("expected hint subnet hosts, got %v", hosts[:min(8, len(hosts))])
	}
}

func TestParseHintRejectsLargePrefix(t *testing.T) {
	if _, err := parseHint("10.0.0.0/16"); err == nil {
		t.Fatal("expected /16 to be rejected")
	}
	if _, err := parseHint("10.0.0.0/24"); err != nil {
		t.Fatal(err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
