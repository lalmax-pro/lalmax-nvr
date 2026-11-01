package onvif

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newONVIFDeviceServer(t *testing.T, handler func(body string) string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		reply := handler(string(raw))
		if reply == "" {
			http.Error(w, "unhandled", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/soap+xml")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(server.Close)
	return server
}

func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClient(server.URL+"/onvif/device_service", "admin", "secret",
		WithHTTPClient(server.Client()), WithTimeout(2*time.Second))
	require.NoError(t, err)
	return client
}

func deviceDateTimeXML() string {
	return soapOK(`<GetSystemDateAndTimeResponse>
		<SystemDateAndTime>
			<UTCDateTime>
				<Time><Hour>12</Hour><Minute>30</Minute><Second>45</Second></Time>
				<Date><Year>2024</Year><Month>6</Month><Day>15</Day></Date>
			</UTCDateTime>
		</SystemDateAndTime>
	</GetSystemDateAndTimeResponse>`)
}

func deviceServicesXML() string {
	return soapOK(`<GetServicesResponse>
		<Service>
			<Namespace>http://www.onvif.org/ver10/media/wsdl</Namespace>
			<XAddr>http://device.local/onvif/media</XAddr>
			<Version><Major>2</Major><Minor>0</Minor></Version>
		</Service>
		<Service>
			<Namespace>http://www.onvif.org/ver20/media/wsdl</Namespace>
			<XAddr>http://device.local/onvif/media2</XAddr>
			<Version><Major>2</Major><Minor>10</Minor></Version>
		</Service>
		<Service>
			<Namespace>http://www.onvif.org/ver10/recording/wsdl</Namespace>
			<XAddr>http://device.local/onvif/recording</XAddr>
			<Version><Major>2</Major><Minor>5</Minor></Version>
		</Service>
		<Service>
			<Namespace>http://www.onvif.org/ver10/search/wsdl</Namespace>
			<XAddr>http://device.local/onvif/search</XAddr>
			<Version><Major>2</Major><Minor>0</Minor></Version>
		</Service>
		<Service>
			<Namespace>http://www.onvif.org/ver10/replay/wsdl</Namespace>
			<XAddr>http://device.local/onvif/replay</XAddr>
			<Version><Major>2</Major><Minor>0</Minor></Version>
		</Service>
		<Service>
			<Namespace>http://www.onvif.org/ver20/ptz/wsdl</Namespace>
			<XAddr>http://device.local/onvif/ptz</XAddr>
			<Version><Major>2</Major><Minor>0</Minor></Version>
		</Service>
		<Service>
			<Namespace>http://www.onvif.org/ver20/imaging/wsdl</Namespace>
			<XAddr>http://device.local/onvif/imaging</XAddr>
			<Version><Major>2</Major><Minor>0</Minor></Version>
		</Service>
		<Service>
			<Namespace>http://www.onvif.org/ver10/events/wsdl</Namespace>
			<XAddr>http://device.local/onvif/events</XAddr>
			<Version><Major>2</Major><Minor>0</Minor></Version>
		</Service>
	</GetServicesResponse>`)
}

func deviceCapabilitiesXML() string {
	return soapOK(`<GetCapabilitiesResponse>
		<Capabilities>
			<Device><XAddr>http://device.local/onvif/device</XAddr></Device>
			<Media><XAddr>http://device.local/onvif/media</XAddr></Media>
			<Recording><XAddr>http://device.local/onvif/recording</XAddr></Recording>
			<Search><XAddr>http://device.local/onvif/search</XAddr></Search>
			<Replay><XAddr>http://device.local/onvif/replay</XAddr></Replay>
			<PTZ><XAddr>http://device.local/onvif/ptz</XAddr></PTZ>
			<Imaging><XAddr>http://device.local/onvif/imaging</XAddr></Imaging>
			<Events><XAddr>http://device.local/onvif/events</XAddr></Events>
			<Extension>
				<Media2><XAddr>http://device.local/onvif/media2</XAddr></Media2>
			</Extension>
		</Capabilities>
	</GetCapabilitiesResponse>`)
}

func deviceInfoXML() string {
	return soapOK(`<GetDeviceInformationResponse>
		<Manufacturer>Hikvision</Manufacturer>
		<Model>DS-2CD</Model>
		<FirmwareVersion>5.6.0</FirmwareVersion>
		<SerialNumber>SN123</SerialNumber>
		<HardwareId>HW1</HardwareId>
	</GetDeviceInformationResponse>`)
}

func TestNewClientHTTPDefaultsAndOptions(t *testing.T) {
	client, err := NewClient("http://camera.example/onvif/device_service", "u", "p")
	require.NoError(t, err)
	require.Equal(t, 80, client.port)
	require.False(t, client.useTLS)
	require.Equal(t, "camera.example", client.hostname)
	require.Equal(t, "http://camera.example/onvif/device_service", client.Endpoint())
	require.Equal(t, "http", client.baseURL().Scheme)
	require.Equal(t, "camera.example:80", client.baseURL().Host)
	require.Equal(t, "/onvif/device_service", client.baseURL().Path)
	require.Equal(t, "/onvif/search_service", client.deriveEndpoint("search_service").Path)

	hc := &http.Client{Timeout: 5 * time.Second}
	client, err = NewClient("http://cam:8080/onvif", "u", "p", WithHTTPClient(hc))
	require.NoError(t, err)
	require.Same(t, hc, client.httpClient)
	require.Equal(t, 8080, client.port)
}

func TestConnectViaGetServices(t *testing.T) {
	var hits atomic.Int32
	server := newONVIFDeviceServer(t, func(body string) string {
		hits.Add(1)
		switch {
		case strings.Contains(body, "GetSystemDateAndTime"):
			return deviceDateTimeXML()
		case strings.Contains(body, "GetServices"):
			return deviceServicesXML()
		default:
			return ""
		}
	})
	client := newTestClient(t, server)

	ctx := context.Background()
	require.NoError(t, client.Connect(ctx))
	require.True(t, client.IsReady())
	require.NotNil(t, client.Endpoints())
	require.Equal(t, "http://device.local/onvif/media", client.Endpoints().Media.String())
	require.Equal(t, "http://device.local/onvif/media2", client.Endpoints().Media2.String())
	require.Equal(t, "http://device.local/onvif/recording", client.Endpoints().Recording.String())
	require.Equal(t, "http://device.local/onvif/ptz", client.Endpoints().PTZ.String())
	require.NotNil(t, client.RecordingService())
	require.NotNil(t, client.ReplayService())
	require.NotNil(t, client.MediaService())
	require.NotNil(t, client.PTZService())
	require.NotNil(t, client.ImagingService())
	require.NotNil(t, client.EventsService())
	require.NotNil(t, client.DeviceManager())
	require.NotZero(t, client.timeShift)

	before := hits.Load()
	require.NoError(t, client.Connect(ctx))
	require.Equal(t, before, hits.Load())
}

func TestConnectFallsBackToGetCapabilities(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetSystemDateAndTime"):
			return soapFaultXML("s:Receiver", "not supported")
		case strings.Contains(body, "GetServices"):
			return soapOK(`<GetServicesResponse></GetServicesResponse>`)
		case strings.Contains(body, "GetCapabilities"):
			return deviceCapabilitiesXML()
		default:
			return ""
		}
	})
	client := newTestClient(t, server)
	require.NoError(t, client.Connect(context.Background()))
	require.True(t, client.IsReady())
	eps := client.Endpoints()
	require.Equal(t, "http://device.local/onvif/media", eps.Media.String())
	require.Equal(t, "http://device.local/onvif/media2", eps.Media2.String())
	require.Equal(t, "http://device.local/onvif/recording", eps.Recording.String())
	require.Equal(t, "http://device.local/onvif/search", eps.Search.String())
	require.Equal(t, "http://device.local/onvif/replay", eps.Replay.String())
	require.Equal(t, "http://device.local/onvif/events", eps.Events.String())
}

func TestConnectDiscoveryFailure(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetSystemDateAndTime"):
			return deviceDateTimeXML()
		default:
			return soapFaultXML("s:Receiver", "nope")
		}
	})
	client := newTestClient(t, server)
	err := client.Connect(context.Background())
	require.ErrorContains(t, err, "service discovery failed")
	require.False(t, client.IsReady())
}

func TestGetSystemDateAndTimeAndServices(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetSystemDateAndTime"):
			return deviceDateTimeXML()
		case strings.Contains(body, "GetServices"):
			return deviceServicesXML()
		case strings.Contains(body, "GetCapabilities"):
			return deviceCapabilitiesXML()
		default:
			return ""
		}
	})
	client := newTestClient(t, server)
	ctx := context.Background()

	got, err := client.GetSystemDateAndTime(ctx)
	require.NoError(t, err)
	require.True(t, got.Equal(time.Date(2024, 6, 15, 12, 30, 45, 0, time.UTC)))

	svcs, err := client.GetServices(ctx)
	require.NoError(t, err)
	require.Len(t, svcs, 8)
	require.Equal(t, "2.10", svcs[1].Version)
	require.Equal(t, "http://www.onvif.org/ver20/media/wsdl", svcs[1].Namespace)

	caps, err := client.GetCapabilities(ctx)
	require.NoError(t, err)
	require.Equal(t, "http://device.local/onvif/device", caps.Device.XAddr)
	require.Equal(t, "http://device.local/onvif/media2", caps.Media2.XAddr)
	require.Equal(t, "http://device.local/onvif/imaging", caps.Imaging.XAddr)
}

func TestGetSystemDateAndTimeErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(string(body), "GetSystemDateAndTime"):
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", "application/soap+xml")
			_, _ = w.Write([]byte("not xml"))
		}
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	_, err := client.GetSystemDateAndTime(context.Background())
	require.ErrorContains(t, err, "GetSystemDateAndTime failed")
	require.NoError(t, client.syncTime(context.Background()))

	_, err = client.GetServices(context.Background())
	require.Error(t, err)
	_, err = client.GetCapabilities(context.Background())
	require.Error(t, err)
}

func TestGetDeviceInformationAndCache(t *testing.T) {
	var hits atomic.Int32
	server := newONVIFDeviceServer(t, func(body string) string {
		if strings.Contains(body, "GetDeviceInformation") {
			hits.Add(1)
			return deviceInfoXML()
		}
		return ""
	})
	client := newTestClient(t, server)

	info, err := client.GetDeviceInformation(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Hikvision", info.Manufacturer)
	require.Equal(t, "DS-2CD", info.Model)
	require.Equal(t, "5.6.0", info.FirmwareVersion)
	require.Equal(t, "SN123", info.SerialNumber)
	require.Equal(t, "HW1", info.HardwareId)

	again, err := client.GetDeviceInformation(context.Background())
	require.NoError(t, err)
	require.Same(t, info, again)
	require.Equal(t, int32(1), hits.Load())
}

func TestGetDeviceInformationError(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Sender", "denied")
	})
	client := newTestClient(t, server)
	_, err := client.GetDeviceInformation(context.Background())
	require.ErrorContains(t, err, "GetDeviceInformation failed")
}

func TestGetDeviceInformationParseError(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetDeviceInformationResponse><`)
	})
	client := newTestClient(t, server)
	_, err := client.GetDeviceInformation(context.Background())
	require.Error(t, err)
}

func TestGetSystemDateAndTimeParseError(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetSystemDateAndTimeResponse><`)
	})
	client := newTestClient(t, server)
	_, err := client.GetSystemDateAndTime(context.Background())
	require.Error(t, err)
}

func TestParseServiceEndpoints(t *testing.T) {
	client, err := NewClient("https://cam.example/onvif/device_service", "u", "p")
	require.NoError(t, err)

	eps := client.parseServiceEndpoints([]Service{
		{Namespace: "http://www.onvif.org/ver10/media/wsdl", XAddr: "http://cam.example/onvif/media"},
		{Namespace: "http://www.onvif.org/ver20/media/wsdl", XAddr: "http://cam.example/onvif/media2"},
		{Namespace: "http://www.onvif.org/ver10/recording/wsdl", XAddr: "://bad"},
		{Namespace: "http://www.onvif.org/ver10/search/wsdl", XAddr: "http://cam.example/onvif/search"},
		{Namespace: "http://www.onvif.org/ver10/replay/wsdl", XAddr: "http://cam.example/onvif/replay"},
		{Namespace: "http://www.onvif.org/ver20/ptz/wsdl", XAddr: "http://cam.example/onvif/ptz"},
		{Namespace: "http://www.onvif.org/ver20/imaging/wsdl", XAddr: "http://cam.example/onvif/imaging"},
		{Namespace: "http://www.onvif.org/ver10/events/wsdl", XAddr: "http://cam.example/onvif/events"},
		{Namespace: "http://example/unknown", XAddr: "http://cam.example/onvif/other"},
	})
	require.Equal(t, "https", eps.Device.Scheme)
	require.Equal(t, "cam.example:443", eps.Device.Host)
	require.Equal(t, "http://cam.example/onvif/media", eps.Media.String())
	require.Equal(t, "http://cam.example/onvif/media2", eps.Media2.String())
	require.Equal(t, "/onvif/recording_service", eps.Recording.Path)
	require.Equal(t, "http://cam.example/onvif/search", eps.Search.String())
	require.Equal(t, "http://cam.example/onvif/replay", eps.Replay.String())
	require.Equal(t, "http://cam.example/onvif/ptz", eps.PTZ.String())
	require.Equal(t, "http://cam.example/onvif/imaging", eps.Imaging.String())
	require.Equal(t, "http://cam.example/onvif/events", eps.Events.String())
}

func TestCapabilitiesToEndpointsFallbacks(t *testing.T) {
	client, err := NewClient("http://cam.example:8080/onvif/device_service", "u", "p")
	require.NoError(t, err)

	eps := client.capabilitiesToEndpoints(&Capabilities{
		Media:     &MediaCapabilities{XAddr: "http://cam.example/onvif/media"},
		Media2:    &MediaCapabilities{XAddr: "http://cam.example/onvif/media2"},
		Recording: &RecordingCapabilities{XAddr: "http://cam.example/onvif/recording"},
		Search:    &SearchCapabilities{XAddr: "http://cam.example/onvif/search"},
		Replay:    &ReplayCapabilities{XAddr: ""},
		PTZ:       &PTZCapabilities{XAddr: "http://cam.example/onvif/ptz"},
		Imaging:   &ImagingCapabilities{XAddr: "http://cam.example/onvif/imaging"},
		Events:    &EventsCapabilities{XAddr: "http://cam.example/onvif/events"},
	})
	require.Equal(t, 8080, client.port)
	require.Equal(t, "http://cam.example/onvif/media", eps.Media.String())
	require.Equal(t, "http://cam.example/onvif/media2", eps.Media2.String())
	require.Equal(t, "http://cam.example/onvif/recording", eps.Recording.String())
	require.Equal(t, "http://cam.example/onvif/search", eps.Search.String())
	require.Equal(t, "/onvif/replay_service", eps.Replay.Path)
	require.Equal(t, "http://cam.example/onvif/ptz", eps.PTZ.String())
}

func TestMediaEndpointFallback(t *testing.T) {
	client, err := NewClient("http://cam.example/onvif/device_service", "u", "p")
	require.NoError(t, err)
	svc := NewMediaService(client)
	client.endpoints = &ServiceEndpoints{}
	require.Equal(t, client.endpoint, svc.mediaEndpoint())
	u, err := url.Parse("http://cam.example/onvif/media")
	require.NoError(t, err)
	client.endpoints.Media = u
	require.Equal(t, u.String(), svc.mediaEndpoint())
}

func TestContainsHelpers(t *testing.T) {
	require.True(t, contains("http://www.onvif.org/ver10/recording/wsdl", "/recording/wsdl"))
	require.True(t, contains("/recording/wsdl", "/recording/wsdl"))
	require.False(t, contains("short", "longer-than-s"))
	require.False(t, contains("abc", "x"))
	require.True(t, containsSubstring("abcdef", "cde"))
	require.False(t, containsSubstring("abc", "z"))
}

func TestClientPoolConnectsAndConnectFailed(t *testing.T) {
	okServer := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetSystemDateAndTime"):
			return deviceDateTimeXML()
		case strings.Contains(body, "GetServices"):
			return deviceServicesXML()
		default:
			return ""
		}
	})
	pool := NewClientPool()
	pool.factory = func(endpoint, username, password string, opts ...ClientOption) (*Client, error) {
		return NewClient(endpoint, username, password, WithHTTPClient(okServer.Client()))
	}
	client, err := pool.GetClient(okServer.URL+"/onvif/device_service", "admin", "secret")
	require.NoError(t, err)
	require.True(t, client.IsReady())
	again, err := pool.GetClient(okServer.URL+"/onvif/device_service", "admin", "secret")
	require.NoError(t, err)
	require.Same(t, client, again)

	failServer := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "down")
	})
	failPool := NewClientPool()
	failPool.factory = func(endpoint, username, password string, opts ...ClientOption) (*Client, error) {
		return NewClient(endpoint, username, password, WithHTTPClient(failServer.Client()))
	}
	_, err = failPool.GetClient(failServer.URL+"/onvif/device_service", "admin", "secret")
	require.ErrorContains(t, err, "connect failed")
}

func TestParseServiceEndpointsInvalidURL(t *testing.T) {
	client, err := NewClient("http://127.0.0.1/onvif/device_service", "u", "p")
	require.NoError(t, err)
	u, parseErr := url.Parse("%")
	if parseErr == nil && u != nil {
		t.Skip("url.Parse accepts % on this Go version")
	}
	eps := client.parseServiceEndpoints([]Service{
		{Namespace: "http://www.onvif.org/ver10/media/wsdl", XAddr: "%"},
	})
	require.Nil(t, eps.Media)
	require.Equal(t, "/onvif/recording_service", eps.Recording.Path)
}
