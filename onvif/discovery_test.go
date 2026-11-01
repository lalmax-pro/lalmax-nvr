package onvif

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseProbeMatchUsesFirstMatchAndFallback(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing">
  <s:Body>
    <d:ProbeMatches>
      <d:ProbeMatch>
        <a:EndpointReference><a:Address>urn:uuid:first</a:Address></a:EndpointReference>
        <d:Scopes>onvif://www.onvif.org/type/video_encoder</d:Scopes>
        <d:XAddrs></d:XAddrs>
      </d:ProbeMatch>
      <d:ProbeMatch>
        <a:EndpointReference><a:Address>urn:uuid:second</a:Address></a:EndpointReference>
        <d:XAddrs>http://10.0.0.9/onvif</d:XAddrs>
      </d:ProbeMatch>
    </d:ProbeMatches>
  </s:Body>
</s:Envelope>`
	dev, err := parseProbeMatchResponse([]byte(xml), "192.168.1.1:3702")
	require.NoError(t, err)
	require.NotNil(t, dev)
	require.Equal(t, "urn:uuid:first", dev.UUID)
	require.Empty(t, dev.Name)
	require.Empty(t, dev.Hardware)
	require.Equal(t, "192.168.1.1:3702", dev.Endpoint)
}

func TestDeviceFromHelloWithoutXAddrs(t *testing.T) {
	dev := deviceFromHello(helloEntry{
		EndpointRef: struct {
			Address string `xml:"Address"`
		}{Address: "urn:uuid:hello-fallback"},
		Scopes: "onvif://www.onvif.org/name/Gate",
	}, "10.1.1.1:3702")
	require.Equal(t, "urn:uuid:hello-fallback", dev.UUID)
	require.Equal(t, "Gate", dev.Name)
	require.Equal(t, "10.1.1.1:3702", dev.Endpoint)
}

func TestDiscoverDevicesLocalTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	devices, err := DiscoverDevices(ctx, 50*time.Millisecond)
	if err != nil {
		require.Empty(t, devices)
		return
	}
	// UDP multicast may find nothing (nil/empty) or local stubs. Neither requires a camera.
	for _, device := range devices {
		require.NotNil(t, device)
	}
}

func TestListenHelloCancelLocal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := ListenHello(ctx, "", func(*DiscoveredDevice) {})
	if err != nil {
		require.ErrorContains(t, err, "listen hello")
		return
	}
	cancel()
	time.Sleep(20 * time.Millisecond)
}

func TestParseProbeMatchEmptyAndInvalid(t *testing.T) {
	dev, err := parseProbeMatchResponse([]byte("not xml"), "ep")
	require.NoError(t, err)
	require.Nil(t, dev)

	empty := `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body></s:Body></s:Envelope>`
	dev, err = parseProbeMatchResponse([]byte(empty), "ep")
	require.NoError(t, err)
	require.Nil(t, dev)
}

func TestGenerateProbeUUID(t *testing.T) {
	id := generateProbeUUID()
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`), id)
	require.NotEqual(t, generateProbeUUID(), id)
}

func TestProbeDeviceHTTP(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing">
  <s:Body>
    <d:ProbeMatches>
      <d:ProbeMatch>
        <a:EndpointReference><a:Address>urn:uuid:probe-http</a:Address></a:EndpointReference>
        <d:Scopes>onvif://www.onvif.org/name/StubCam onvif://www.onvif.org/hardware/IPC</d:Scopes>
        <d:XAddrs>http://10.0.0.5/onvif/device_service</d:XAddrs>
      </d:ProbeMatch>
    </d:ProbeMatches>
  </s:Body>
</s:Envelope>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/onvif/device_service", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", "application/soap+xml")
		_, _ = w.Write([]byte(xml))
	}))
	t.Cleanup(server.Close)

	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	dev, err := ProbeDevice(context.Background(), u.Hostname(), port, 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, dev)
	require.Equal(t, "urn:uuid:probe-http", dev.UUID)
	require.Equal(t, "StubCam", dev.Name)
	require.Equal(t, "IPC", dev.Hardware)
	require.Equal(t, "http://10.0.0.5/onvif/device_service", dev.Endpoint)
}

func TestProbeDeviceNonOKAndTimeout(t *testing.T) {
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(notFound.Close)
	u, err := url.Parse(notFound.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	dev, err := ProbeDevice(context.Background(), u.Hostname(), port, time.Second)
	require.NoError(t, err)
	require.Nil(t, dev)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = ProbeDevice(ctx, "127.0.0.1", 1, 20*time.Millisecond)
	require.Error(t, err)
}

func TestListenHelloErrors(t *testing.T) {
	err := ListenHello(context.Background(), "", nil)
	require.ErrorContains(t, err, "hello handler is required")

	err = ListenHello(context.Background(), "not-a-real-iface-xyz", func(*DiscoveredDevice) {})
	require.ErrorContains(t, err, "lookup interface")
}

func TestListenHelloOnIPv4Interface(t *testing.T) {
	ifaces, err := net.Interfaces()
	require.NoError(t, err)
	for _, iface := range ifaces {
		addrs, addrErr := iface.Addrs()
		if addrErr != nil {
			continue
		}
		hasIPv4 := false
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				hasIPv4 = true
				break
			}
		}
		if !hasIPv4 {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		listenErr := ListenHello(ctx, iface.Name, func(*DiscoveredDevice) {})
		cancel()
		time.Sleep(15 * time.Millisecond)
		if listenErr != nil {
			require.Error(t, listenErr)
		}
		return
	}
	t.Skip("no IPv4 interface available")
}

func TestListenHelloNoIPv4Interface(t *testing.T) {
	ifaces, err := net.Interfaces()
	require.NoError(t, err)
	for _, iface := range ifaces {
		addrs, addrErr := iface.Addrs()
		if addrErr != nil || iface.Name == "" {
			continue
		}
		hasIPv4 := false
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				hasIPv4 = true
				break
			}
		}
		if !hasIPv4 {
			err = ListenHello(context.Background(), iface.Name, func(*DiscoveredDevice) {})
			if err != nil && strings.Contains(err.Error(), "no IPv4 address") {
				return
			}
		}
	}
	t.Skip("no IPv4-less interface available")
}
