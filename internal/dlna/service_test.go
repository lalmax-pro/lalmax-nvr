package dlna

import (
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
)

func TestActivePushesIncludeUnregisteredStream(t *testing.T) {
	got := activePushItems("http://192.168.31.35:8200", map[string]struct{}{"cam-1": {}}, []media.StreamInfo{
		{StreamID: "cam-1", AppName: "live", Active: true},
		{StreamID: "test110", AppName: "live", Active: true},
		{StreamID: "test110_sub", AppName: "live", Active: true},
		{StreamID: "idle", AppName: "live", Active: false},
	}, nil)
	if len(got) != 1 || got[0].Title != "test110" || !strings.Contains(got[0].URL, "/dlna/live/test110.ts") {
		t.Fatalf("pushes = %+v", got)
	}
}

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
	cfg := &config.Config{DLNA: config.DLNAConfig{Enabled: &enabled, FriendlyName: "Test NVR", Port: 8200, AdvertiseURL: "http://192.168.1.2:9090"}}
	s := NewService(cfg, nil, nil, nil)
	rr := httptest.NewRecorder()
	s.device(rr, httptest.NewRequest("GET", "/dlna/device.xml", nil))
	body := rr.Body.String()
	for _, want := range []string{"MediaServer:1", "ContentDirectory:1", "ConnectionManager:1", "Test NVR", "serviceId", "DMS-1.50", "<SCPDURL>/dlna/content-directory.xml</SCPDURL>", ":8200"} {
		if !strings.Contains(body, want) {
			t.Fatalf("device description missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "http://192.168.1.2:9090/dlna/content-directory.xml") {
		t.Fatalf("control URL must stay relative: %s", body)
	}
}

func TestNotifyUsesAliveOnce(t *testing.T) {
	msg := ssdpNotify("http://192.168.31.35:8200/dlna/device.xml", deviceType, "ssdp:alive", "uuid:x::"+deviceType)
	if !strings.Contains(msg, "\r\nNTS: ssdp:alive\r\n") || strings.Contains(msg, "ssdp:ssdp:") {
		t.Fatalf("notify NTS = %q", msg)
	}
	if !strings.Contains(msg, ":8200/dlna/device.xml") {
		t.Fatalf("notify location = %q", msg)
	}
}

func TestSearchAllExpands(t *testing.T) {
	enabled := true
	s := NewService(&config.Config{DLNA: config.DLNAConfig{Enabled: &enabled}}, nil, nil, nil)
	got := s.matchSearch("ssdp:all")
	if len(got) < 4 {
		t.Fatalf("ssdp:all targets = %v", got)
	}
}

func TestDLNAPortSeparateFromHTTP(t *testing.T) {
	enabled := true
	s := NewService(&config.Config{
		Server: config.ServerConfig{Listen: ":9090"},
		DLNA:   config.DLNAConfig{Enabled: &enabled, Port: 9090},
	}, nil, nil, nil)
	if err := s.validatePort(); err == nil {
		t.Fatal("expected DLNA port to be rejected when it matches HTTP")
	}
	s.cfg.DLNA.Port = 8200
	if err := s.validatePort(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(s.baseURL(), ":8200") {
		t.Fatalf("base URL = %s", s.baseURL())
	}
}

func TestWriteLiveHeaderIsRawMPEGTS(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	errCh := make(chan error, 1)
	go func() {
		errCh <- writeLiveHeader(server, "video/mpeg", liveContentFeature)
		server.Close()
	}()
	body, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if werr := <-errCh; werr != nil {
		t.Fatal(werr)
	}
	text := string(body)
	if strings.Contains(strings.ToLower(text), "transfer-encoding") {
		t.Fatalf("live header is chunked:\n%s", text)
	}
	for _, want := range []string{
		"HTTP/1.1 200 OK\r\n",
		"Content-Type: video/mpeg\r\n",
		"Connection: close\r\n",
		"DLNA.ORG_CI=1",
		"\r\n\r\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("header missing %q:\n%s", want, text)
		}
	}
}
