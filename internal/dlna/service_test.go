package dlna

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
)

func TestDIDLescapesMetadataAndURL(t *testing.T) {
	got := didl([]didlObject{{ID: "live:a&b", Parent: "live", Title: `Front <Door>`, Class: "object.item.videoItem", URL: "http://nvr/dlna/live/a%26b.ts", Protocol: "http-get:*:video/mp2t:*"}})
	for _, want := range []string{"live:a&amp;b", "Front &lt;Door&gt;", "a%26b.ts", "video/mp2t"} {
		if !strings.Contains(got, want) {
			t.Fatalf("DIDL missing %q: %s", want, got)
		}
	}
}

func TestParseST(t *testing.T) {
	if got := parseST("M-SEARCH * HTTP/1.1\r\nST: urn:schemas-upnp-org:service:ContentDirectory:1\r\n"); got != contentDirectoryType {
		t.Fatalf("parseST = %q", got)
	}
}

func TestCIDRAccessControl(t *testing.T) {
	enabled := true
	cfg := &config.Config{DLNA: config.DLNAConfig{Enabled: &enabled, AllowedCIDRs: []string{"192.168.1.0/24"}}}
	s := NewService(cfg, nil, nil, nil)
	if !s.isAllowed("192.168.1.20:1234") {
		t.Fatal("expected configured LAN address to be allowed")
	}
	if s.isAllowed("10.0.0.2:1234") {
		t.Fatal("expected foreign address to be denied")
	}
}

func TestDeviceDescription(t *testing.T) {
	enabled := true
	cfg := &config.Config{DLNA: config.DLNAConfig{Enabled: &enabled, FriendlyName: "Test NVR", AdvertiseURL: "http://192.168.1.2:9090"}}
	s := NewService(cfg, nil, nil, nil)
	rr := httptest.NewRecorder()
	s.device(rr, httptest.NewRequest("GET", "/dlna/device.xml", nil))
	body := rr.Body.String()
	for _, want := range []string{"MediaServer:1", "ContentDirectory:1", "ConnectionManager:1", "Test NVR"} {
		if !strings.Contains(body, want) {
			t.Fatalf("device description missing %q: %s", want, body)
		}
	}
}
