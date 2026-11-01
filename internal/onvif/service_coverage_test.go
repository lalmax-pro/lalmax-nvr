package onvif

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	onviflib "github.com/lalmax-pro/lalmax-nvr/onvif"
	"github.com/stretchr/testify/require"
)

func TestONVIFMocks(t *testing.T) {
	ctx := context.Background()
	device := DiscoveredDevice{UUID: "uuid-1", Name: "cam"}

	discoverer := &MockDiscoverer{Devices: []DiscoveredDevice{device}, Error: errors.New("discover failed")}
	devices, err := discoverer.Discover(ctx, time.Second)
	require.Equal(t, []DiscoveredDevice{device}, devices)
	require.EqualError(t, err, "discover failed")
	probed, err := discoverer.ProbeDevice(ctx, "203.0.113.10", 80, time.Second)
	require.Equal(t, &device, probed)
	require.EqualError(t, err, "discover failed")
	require.Equal(t, 1, discoverer.DiscoverCalls)
	require.Equal(t, 1, discoverer.ProbeDeviceCalls)

	empty := &MockDiscoverer{}
	got, err := empty.ProbeDevice(ctx, "203.0.113.10", 80, 0)
	require.Nil(t, got)
	require.NoError(t, err)

	info := &DeviceInfo{Manufacturer: "Acme", Model: "Cam"}
	profiles := []DeviceProfile{{Token: "p1", Name: "Main"}}
	stream := &StreamInfo{URI: "rtsp://cam/main", Protocol: "RTSP"}
	caps := &DeviceCapabilities{PTZ: true, Streaming: true}
	client := &MockDeviceClient{
		DeviceInfo: info, Profiles: profiles, StreamURI: stream, Capabilities: caps,
		ConnectError: errors.New("connect failed"),
	}
	require.EqualError(t, client.Connect(ctx), "connect failed")
	gotInfo, err := client.GetDeviceInformation(ctx)
	require.NoError(t, err)
	require.Equal(t, info, gotInfo)
	gotProfiles, err := client.GetProfiles(ctx)
	require.NoError(t, err)
	require.Equal(t, profiles, gotProfiles)
	gotStream, err := client.GetStreamURI(ctx, "p1")
	require.NoError(t, err)
	require.Equal(t, stream, gotStream)
	gotCaps, err := client.GetCapabilities(ctx)
	require.NoError(t, err)
	require.Equal(t, caps, gotCaps)
	require.Equal(t, 1, client.ConnectCalls)
	require.Equal(t, 1, client.GetDeviceInformationCalls)
	require.Equal(t, 1, client.GetProfilesCalls)
	require.Equal(t, 1, client.GetStreamURICalls)
	require.Equal(t, 1, client.GetCapabilitiesCalls)

	velocity := PTZVector{Pan: 0.2, Tilt: -0.1, Zoom: 0.3}
	ptzMock := &MockPTZController{
		Position: velocity, Moving: true, Error: errors.New("ptz failed"),
		Presets: []PTZPreset{{Token: "pre", Name: "Door", Position: velocity}}, SetPresetToken: "pre",
	}
	require.EqualError(t, ptzMock.ContinuousMove(ctx, velocity), "ptz failed")
	require.EqualError(t, ptzMock.AbsoluteMove(ctx, velocity), "ptz failed")
	require.EqualError(t, ptzMock.RelativeMove(ctx, velocity), "ptz failed")
	require.EqualError(t, ptzMock.Stop(ctx, true, false), "ptz failed")
	pos, moving, err := ptzMock.GetStatus(ctx)
	require.Equal(t, velocity, pos)
	require.True(t, moving)
	require.EqualError(t, err, "ptz failed")
	presets, err := ptzMock.GetPresets(ctx)
	require.EqualError(t, err, "ptz failed")
	require.Len(t, presets, 1)
	token, err := ptzMock.SetPreset(ctx, "Door")
	require.Equal(t, "pre", token)
	require.EqualError(t, err, "ptz failed")
	require.EqualError(t, ptzMock.GoToPreset(ctx, "pre"), "ptz failed")
	require.EqualError(t, ptzMock.RemovePreset(ctx, "pre"), "ptz failed")
	require.Len(t, ptzMock.MoveHistory, 3)
	require.Equal(t, 1, ptzMock.ContinuousMoveCalls)
	require.Equal(t, 1, ptzMock.StopCalls)

	settings := &ImagingSettings{Brightness: 1, Contrast: 2}
	options := &ImagingOptions{Brightness: &Range{Min: 0, Max: 10}}
	imaging := &MockImagingController{Settings: settings, Options: options, Error: errors.New("imaging failed")}
	gotSettings, err := imaging.GetImagingSettings(ctx)
	require.Equal(t, settings, gotSettings)
	require.EqualError(t, err, "imaging failed")
	require.EqualError(t, imaging.SetImagingSettings(ctx, *settings), "imaging failed")
	gotOptions, err := imaging.GetImagingOptions(ctx)
	require.Equal(t, options, gotOptions)
	require.EqualError(t, err, "imaging failed")

	presetsMgr := &MockPresetManager{Presets: presets, Error: errors.New("preset failed")}
	_, err = presetsMgr.GetPresets(ctx)
	require.EqualError(t, err, "preset failed")
	token, err = presetsMgr.SetPreset(ctx, "Door")
	require.Equal(t, "preset-token-Door", token)
	require.EqualError(t, err, "preset failed")
	require.EqualError(t, presetsMgr.GoToPreset(ctx, "pre"), "preset failed")
	require.EqualError(t, presetsMgr.RemovePreset(ctx, "pre"), "preset failed")

	events := &MockEventSubscriber{Events: []ONVIFEvent{{Topic: "motion"}}, Error: errors.New("event failed")}
	require.EqualError(t, events.Subscribe(ctx, "cam"), "event failed")
	require.EqualError(t, events.Unsubscribe(ctx, "cam"), "event failed")
	msgs, err := events.GetEventMessages(ctx)
	require.EqualError(t, err, "event failed")
	require.Equal(t, "motion", msgs[0].Topic)

	ifaces := []NetworkInterface{{Name: "eth0", Enabled: true}}
	users := []ONVIFUser{{Username: "admin", Level: "Administrator"}}
	mgr := &MockDeviceManager{NetworkInterfaces: ifaces, Users: users, Error: errors.New("device failed")}
	require.EqualError(t, mgr.SystemReboot(ctx), "device failed")
	gotIfaces, err := mgr.GetNetworkInterfaces(ctx)
	require.Equal(t, ifaces, gotIfaces)
	require.EqualError(t, err, "device failed")
	require.EqualError(t, mgr.SetNetworkInterfaces(ctx, ifaces), "device failed")
	gotUsers, err := mgr.GetUsers(ctx)
	require.Equal(t, users, gotUsers)
	require.EqualError(t, err, "device failed")
	require.EqualError(t, mgr.CreateUsers(ctx, users), "device failed")
	require.EqualError(t, mgr.DeleteUsers(ctx, []string{"admin"}), "device failed")
	require.EqualError(t, mgr.SetUser(ctx, "admin", "secret"), "device failed")

	snap := &MockSnapshotProvider{URI: "http://cam/snap.jpg", Error: errors.New("snap failed")}
	uri, err := snap.GetSnapshotUri(ctx)
	require.Equal(t, "http://cam/snap.jpg", uri)
	require.EqualError(t, err, "snap failed")
	require.Equal(t, 1, snap.GetSnapshotUriCalls)
}

func TestONVIFDisconnectedClient(t *testing.T) {
	ctx := context.Background()
	client := NewClient("http://127.0.0.1:9/onvif/device_service", "user", "secret")
	require.Equal(t, "http://127.0.0.1:9/onvif/device_service", client.GetEndpoint())
	require.Nil(t, client.GetRawClient())
	require.Nil(t, client.NewPTZController("p1"))
	require.Nil(t, client.NewImagingController("p1"))
	require.Nil(t, client.NewSnapshotProvider("p1"))
	require.Nil(t, client.NewEventSubscriber())
	require.Nil(t, client.NewDeviceManager())
	require.Nil(t, client.GetRecordingService())
	require.Nil(t, client.GetReplayService())
	require.Nil(t, client.GetMediaService())

	_, err := client.GetDeviceInformation(ctx)
	require.ErrorContains(t, err, "not connected")
	_, err = client.GetProfiles(ctx)
	require.ErrorContains(t, err, "not connected")
	_, err = client.GetStreamURI(ctx, "p1")
	require.ErrorContains(t, err, "not connected")
	_, err = client.GetCapabilities(ctx)
	require.ErrorContains(t, err, "not connected")
	_, err = client.GetCapabilitiesDetailed(ctx)
	require.ErrorContains(t, err, "not connected")
	_, err = client.GetRecordings(ctx)
	require.ErrorContains(t, err, "not connected")
	_, err = client.SearchRecordings(ctx, SearchRequest{})
	require.ErrorContains(t, err, "not connected")
	_, err = client.GetReplayURI(ctx, "rec")
	require.ErrorContains(t, err, "not connected")

	err = NewClient("http://[", "user", "secret").Connect(ctx)
	require.ErrorContains(t, err, "create ONVIF client")

	err = client.Connect(ctx)
	require.ErrorContains(t, err, "initialize ONVIF client")

	raw, err := onviflib.NewClient("http://127.0.0.1:9/onvif/device_service", "user", "secret")
	require.NoError(t, err)
	imaging := NewImagingControllerImpl(raw, "p1")
	imaging.SetImagingEndpoint("unused")
	_, err = imaging.GetImagingSettings(ctx)
	require.ErrorContains(t, err, "imaging service not available")
	require.ErrorContains(t, imaging.SetImagingSettings(ctx, ImagingSettings{Brightness: 1}), "imaging service not available")
	_, err = imaging.GetImagingOptions(ctx)
	require.ErrorContains(t, err, "imaging service not available")
}

func TestONVIFDiscoveryHelpers(t *testing.T) {
	mapped := MapDiscoveredDevice(&onviflib.DiscoveredDevice{
		UUID:     "uuid-1",
		Name:     "Lobby",
		XAddrs:   []string{"http://203.0.113.10/onvif/device_service"},
		Scopes:   []string{"onvif://www.onvif.org/name/Lobby", "onvif://www.onvif.org/hardware/IPC-100"},
		Endpoint: "http://203.0.113.10/onvif/device_service",
	})
	require.Equal(t, "IPC-100", mapped.Hardware)
	require.Equal(t, "Lobby", mapped.Name)

	noHardware := MapDiscoveredDevice(&onviflib.DiscoveredDevice{UUID: "uuid-2", Scopes: []string{"onvif://www.onvif.org/name/Plain"}})
	require.Empty(t, noHardware.Hardware)
	slashOnly := MapDiscoveredDevice(&onviflib.DiscoveredDevice{Scopes: []string{"hardware"}})
	require.Equal(t, "hardware", slashOnly.Hardware)

	many := MapDiscoveredDevices([]*onviflib.DiscoveredDevice{
		{UUID: "a", Scopes: []string{"onvif://www.onvif.org/hardware/A/"}},
		{UUID: "b"},
	})
	require.Len(t, many, 2)
	require.Empty(t, many[0].Hardware)

	require.Nil(t, categorizeDiscoveryError(context.Background(), nil))
	require.Equal(t, "PARSE_ERROR", categorizeDiscoveryError(context.Background(), errors.New("xml broken")).Category)
	require.Equal(t, "TIMEOUT", categorizeDiscoveryError(context.Background(), errors.New("deadline exceeded")).Category)
	require.Equal(t, "TIMEOUT", categorizeDiscoveryError(context.Background(), errors.New("i/o timeout")).Category)
	require.Equal(t, "NETWORK", categorizeDiscoveryError(context.Background(), errors.New("network is unreachable")).Category)
	require.Equal(t, "NETWORK", categorizeDiscoveryError(context.Background(), errors.New("no route to host")).Category)
	require.Equal(t, "NETWORK", categorizeDiscoveryError(context.Background(), errors.New("connection refused")).Category)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.Equal(t, "discovery was cancelled", categorizeDiscoveryError(cancelled, errors.New("boom")).Message)
	timed, stop := context.WithTimeout(context.Background(), time.Millisecond)
	defer stop()
	time.Sleep(5 * time.Millisecond)
	require.Equal(t, "TIMEOUT", categorizeDiscoveryError(timed, errors.New("boom")).Category)

	ctx, cancelDiscover := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancelDiscover()
	result := Discover(ctx, 20*time.Millisecond)
	require.NotNil(t, result)
	require.NotNil(t, result.Devices)

	require.Error(t, ListenHello(context.Background(), "not-a-real-iface", func(DiscoveredDevice) {}))

	require.Empty(t, formatPrefixMask(0))
	require.Empty(t, formatPrefixMask(33))
	require.Equal(t, net.IPv4Mask(255, 255, 255, 0).String(), formatPrefixMask(24))
	require.Equal(t, net.IPv4Mask(255, 240, 0, 0).String(), formatPrefixMask(12))
	require.Equal(t, net.IPv4Mask(255, 255, 255, 255).String(), formatPrefixMask(32))

	require.False(t, isPTZTransportError(nil))
	require.False(t, isPTZTransportError(errors.New("soap fault")))
	require.True(t, isPTZTransportError(errors.New("connection broken")))
	require.True(t, isPTZTransportError(errors.New("malformed HTTP status")))
	require.True(t, isPTZTransportError(errors.New("unexpected EOF")))
	require.True(t, isPTZTransportError(errors.New("connection reset by peer")))
}

func TestONVIFProbeDevice(t *testing.T) {
	match := `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <ProbeMatches>
      <ProbeMatch>
        <EndpointReference><Address>uuid:device-1</Address></EndpointReference>
        <Scopes>onvif://www.onvif.org/name/Lobby onvif://www.onvif.org/hardware/IPC-100</Scopes>
        <XAddrs>http://203.0.113.10/onvif/device_service</XAddrs>
      </ProbeMatch>
    </ProbeMatches>
  </s:Body>
</s:Envelope>`
	hello := `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <Hello>
      <EndpointReference><Address>uuid:hello-1</Address></EndpointReference>
      <Scopes>onvif://www.onvif.org/hardware/HelloCam</Scopes>
      <XAddrs>http://203.0.113.11/onvif/device_service</XAddrs>
    </Hello>
  </s:Body>
</s:Envelope>`

	var mode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "match":
			_, _ = io.WriteString(w, match)
		case "hello":
			_, _ = io.WriteString(w, hello)
		case "badxml":
			_, _ = io.WriteString(w, "<not-xml")
		case "empty":
			_, _ = io.WriteString(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><ProbeMatches/></s:Body></s:Envelope>`)
		default:
			http.Error(w, "nope", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	host, port := splitHostPort(t, srv.URL)

	mode = "match"
	device, err := ProbeDevice(context.Background(), host, port, time.Second)
	require.NoError(t, err)
	require.Equal(t, "uuid:device-1", device.UUID)
	require.Equal(t, "Lobby", device.Name)
	require.Equal(t, "IPC-100", device.Hardware)
	require.Equal(t, "http://203.0.113.10/onvif/device_service", device.Endpoint)

	mode = "hello"
	device, err = ProbeDevice(context.Background(), host, port, time.Second)
	require.NoError(t, err)
	require.Equal(t, "HelloCam", device.Hardware)

	mode = "badxml"
	device, err = ProbeDevice(context.Background(), host, port, time.Second)
	require.NoError(t, err)
	require.Nil(t, device)

	mode = "empty"
	device, err = ProbeDevice(context.Background(), host, port, time.Second)
	require.NoError(t, err)
	require.Nil(t, device)

	mode = "missing"
	device, err = ProbeDevice(context.Background(), host, port, time.Second)
	require.NoError(t, err)
	require.Nil(t, device)

	_, err = ProbeDevice(context.Background(), "127.0.0.1", 1, 200*time.Millisecond)
	require.Error(t, err)
}

func TestONVIFLocalDevice(t *testing.T) {
	fake := newFakeONVIF()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	client := connectFake(t, srv.URL)

	info, err := client.GetDeviceInformation(ctx)
	require.NoError(t, err)
	require.Equal(t, "Acme", info.Manufacturer)
	require.Equal(t, "Cam-1", info.Model)
	require.NotNil(t, client.GetRawClient())
	require.NotNil(t, client.GetRecordingService())
	require.NotNil(t, client.GetReplayService())
	require.NotNil(t, client.GetMediaService())

	caps, err := client.GetCapabilities(ctx)
	require.NoError(t, err)
	require.True(t, caps.PTZ)
	require.True(t, caps.Streaming)

	detailed, err := client.GetCapabilitiesDetailed(ctx)
	require.NoError(t, err)
	require.True(t, detailed.PTZ)
	require.True(t, detailed.PTZDetail.PanTilt)
	require.True(t, detailed.PTZDetail.Zoom)
	require.True(t, detailed.PTZDetail.Home)
	require.True(t, detailed.PTZDetail.Presets)
	require.True(t, detailed.Imaging)
	require.True(t, detailed.Events)
	require.True(t, detailed.Snapshot)
	require.True(t, detailed.Streaming)
	require.True(t, detailed.Device)
	require.Equal(t, "Acme", detailed.DeviceInfo.Manufacturer)

	profiles, err := client.GetProfiles(ctx)
	require.NoError(t, err)
	require.Equal(t, "Profile_1", profiles[0].Token)
	require.Equal(t, "H264", profiles[0].Encoding)
	require.Equal(t, 1920, profiles[0].Width)

	stream, err := client.GetStreamURI(ctx, "Profile_1")
	require.NoError(t, err)
	require.Equal(t, "rtsp://203.0.113.10/main", stream.URI)
	require.Equal(t, "RTSP", stream.Protocol)
	require.Equal(t, "Profile_1", stream.ProfileToken)

	snap := client.NewSnapshotProvider("Profile_1")
	uri, err := snap.GetSnapshotUri(ctx)
	require.NoError(t, err)
	require.Equal(t, "http://203.0.113.10/snap.jpg", uri)

	imaging := client.NewImagingController("Profile_1")
	settings, err := imaging.GetImagingSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, 40.0, settings.Brightness)
	require.Equal(t, "auto", settings.Exposure.Mode)
	require.Equal(t, "manual", settings.WhiteBalance.Mode)
	require.NoError(t, imaging.SetImagingSettings(ctx, ImagingSettings{
		Brightness: 41, Contrast: 11, Saturation: 12, Sharpness: 13,
		Exposure: ExposureSettings{Mode: "manual"}, WhiteBalance: WhiteBalanceSettings{Mode: "auto"},
	}))
	options, err := imaging.GetImagingOptions(ctx)
	require.NoError(t, err)
	require.Equal(t, 100.0, options.Brightness.Max)
	require.Equal(t, 100.0, options.Sharpness.Max)

	ptz := client.NewPTZController("Profile_1").(*serializedPTZController)
	ptz.inner.SetProfileToken("Profile_1")
	fake.hijack["ContinuousMove"] = 1
	require.NoError(t, ptz.ContinuousMove(ctx, PTZVector{Pan: 0.1, Tilt: 0.2, Zoom: 0.3}))
	require.NoError(t, ptz.AbsoluteMove(ctx, PTZVector{Pan: 0.4}))
	require.NoError(t, ptz.RelativeMove(ctx, PTZVector{Tilt: 0.5}))
	require.NoError(t, ptz.Stop(ctx, true, true))
	pos, moving, err := ptz.GetStatus(ctx)
	require.NoError(t, err)
	require.True(t, moving)
	require.InDelta(t, 0.1, pos.Pan, 0.001)
	require.InDelta(t, 0.2, pos.Tilt, 0.001)
	require.InDelta(t, 0.3, pos.Zoom, 0.001)
	presets, err := ptz.GetPresets(ctx)
	require.NoError(t, err)
	require.Equal(t, "pre1", presets[0].Token)
	require.Equal(t, "Door", presets[0].Name)
	token, err := ptz.SetPreset(ctx, "Door")
	require.NoError(t, err)
	require.Equal(t, "pre-new", token)
	require.NoError(t, ptz.GoToPreset(ctx, "pre1"))
	require.NoError(t, ptz.RemovePreset(ctx, "pre1"))

	fake.fail["GotoPreset"] = 1
	require.NoError(t, ptz.GoToPreset(ctx, "pre1"))
	fake.fail["GotoPreset"] = 1
	fake.presetToken = "other"
	require.ErrorContains(t, ptz.GoToPreset(ctx, "pre1"), "not found")
	fake.presetToken = "pre1"
	fake.fail["GotoPreset"] = 1
	fake.fail["GetPresets"] = 1
	require.ErrorContains(t, ptz.GoToPreset(ctx, "pre1"), "go to PTZ preset failed")

	fake.hijack["ContinuousMove"] = 2
	require.Error(t, ptz.ContinuousMove(ctx, PTZVector{Pan: 1}))
	fake.fail["GetStatus"] = 1
	_, _, err = ptz.GetStatus(ctx)
	require.ErrorContains(t, err, "get PTZ status failed")
	fake.fail["SetPreset"] = 1
	_, err = ptz.SetPreset(ctx, "Door")
	require.ErrorContains(t, err, "set PTZ preset failed")
	fake.fail["Stop"] = 1
	require.Error(t, ptz.Stop(ctx, true, false))

	recordings, err := client.GetRecordings(ctx)
	require.NoError(t, err)
	require.Equal(t, "rec1", recordings[0].Token)
	require.Equal(t, "clip", recordings[0].Name)
	require.Equal(t, "lobby", recordings[0].Source.Location)

	segments, err := client.SearchRecordings(ctx, SearchRequest{
		RecordingToken: "rec1",
		StartTime:      time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		EndTime:        time.Date(2026, 1, 2, 4, 4, 5, 0, time.UTC),
		MaxResults:     5,
	})
	require.NoError(t, err)
	require.Equal(t, "seg1", segments[0].Token)
	require.Equal(t, "rec1", segments[0].RecordingToken)
	require.Equal(t, int64(1024), segments[0].Size)

	fake.searchState = "MoreResults"
	_, err = client.SearchRecordings(ctx, SearchRequest{MaxResults: 1})
	require.NoError(t, err)
	fake.searchState = "Completed"
	fake.fail["EndSearch"] = 1
	_, err = client.SearchRecordings(ctx, SearchRequest{MaxResults: 1})
	require.NoError(t, err)
	fake.fail["FindRecordings"] = 1
	_, err = client.SearchRecordings(ctx, SearchRequest{})
	require.ErrorContains(t, err, "search recordings")
	fake.fail["GetRecordings"] = 1
	_, err = client.GetRecordings(ctx)
	require.ErrorContains(t, err, "get recordings")

	replay, err := client.GetReplayURI(ctx, "rec1")
	require.NoError(t, err)
	require.Equal(t, "rtsp://203.0.113.10/replay", replay)
	fake.fail["GetReplayUri"] = 1
	_, err = client.GetReplayURI(ctx, "rec1")
	require.ErrorContains(t, err, "get replay URI")

	mgr := client.NewDeviceManager()
	require.NoError(t, mgr.SystemReboot(ctx))
	ifaces, err := mgr.GetNetworkInterfaces(ctx)
	require.NoError(t, err)
	require.Equal(t, "eth0", ifaces[0].Name)
	require.Equal(t, "203.0.113.10", ifaces[0].IPv4.Address)
	require.True(t, ifaces[0].IPv4.DHCP)
	require.True(t, ifaces[0].IPv6.Enabled)
	require.ErrorIs(t, mgr.SetNetworkInterfaces(ctx, ifaces), ErrUnsupported)
	users, err := mgr.GetUsers(ctx)
	require.NoError(t, err)
	require.Equal(t, "admin", users[0].Username)
	require.Equal(t, "Administrator", users[0].Level)
	require.NoError(t, mgr.CreateUsers(ctx, []ONVIFUser{{Username: "op", Password: "secret", Level: "Operator"}}))
	require.NoError(t, mgr.DeleteUsers(ctx, []string{"op"}))
	fake.redirect["SetUser"] = 1
	require.Error(t, mgr.SetUser(ctx, "admin", "next"))
	require.NoError(t, mgr.SetUser(ctx, "admin", "next"))

	fake.fail["SystemReboot"] = 1
	require.ErrorContains(t, mgr.SystemReboot(ctx), "system reboot failed")
	fake.fail["GetNetworkInterfaces"] = 1
	_, err = mgr.GetNetworkInterfaces(ctx)
	require.ErrorContains(t, err, "get network interfaces failed")
	fake.fail["GetUsers"] = 1
	_, err = mgr.GetUsers(ctx)
	require.ErrorContains(t, err, "get users failed")
	fake.fail["CreateUsers"] = 1
	require.ErrorContains(t, mgr.CreateUsers(ctx, nil), "create users failed")
	fake.fail["DeleteUsers"] = 1
	require.ErrorContains(t, mgr.DeleteUsers(ctx, nil), "delete users failed")
	fake.fail["SetUser"] = 1
	require.ErrorContains(t, mgr.SetUser(ctx, "admin", "x"), "set user failed")

	fake.fail["GetSnapshotUri"] = 2
	_, err = snap.GetSnapshotUri(ctx)
	require.ErrorContains(t, err, "get snapshot URI failed")
	fake.fail["GetImagingSettings"] = 1
	_, err = imaging.GetImagingSettings(ctx)
	require.ErrorContains(t, err, "get imaging settings failed")
	fake.fail["SetImagingSettings"] = 1
	require.Error(t, imaging.SetImagingSettings(ctx, ImagingSettings{}))
	fake.fail["GetOptions"] = 1
	_, err = imaging.GetImagingOptions(ctx)
	require.ErrorContains(t, err, "get imaging options failed")
	fake.fail["GetProfiles"] = 2
	_, err = client.GetProfiles(ctx)
	require.ErrorContains(t, err, "get profiles")
	fake.fail["GetStreamUri"] = 2
	_, err = client.GetStreamURI(ctx, "Profile_1")
	require.ErrorContains(t, err, "get stream URI")
	fake.fail["GetCapabilities"] = 1
	_, err = client.GetCapabilities(ctx)
	require.ErrorContains(t, err, "get capabilities")
}

func TestONVIFCapabilityBranches(t *testing.T) {
	ctx := context.Background()

	bare := newFakeONVIF()
	bare.omitImaging = true
	bare.omitPTZ = true
	bare.emptyCapabilities = true
	srv := httptest.NewServer(bare)
	defer srv.Close()
	client := connectFake(t, srv.URL)
	caps, err := client.GetCapabilities(ctx)
	require.NoError(t, err)
	require.False(t, caps.PTZ)
	require.False(t, caps.Streaming)
	detailed, err := client.GetCapabilitiesDetailed(ctx)
	require.NoError(t, err)
	require.False(t, detailed.PTZ)
	require.False(t, detailed.Imaging)
	require.False(t, detailed.Device)
	require.Equal(t, "Acme", detailed.DeviceInfo.Manufacturer)

	rich := newFakeONVIF()
	richSrv := httptest.NewServer(rich)
	defer richSrv.Close()
	richClient := connectFake(t, richSrv.URL)
	rich.fail["GetDeviceInformation"] = 1
	detailed, err = richClient.GetCapabilitiesDetailed(ctx)
	require.NoError(t, err)
	require.Nil(t, detailed.DeviceInfo)

	rich.fail["GetImagingSettings"] = 1
	detailed, err = richClient.GetCapabilitiesDetailed(ctx)
	require.NoError(t, err)
	require.False(t, detailed.Imaging)

	rich.emptyProfiles = true
	detailed, err = richClient.GetCapabilitiesDetailed(ctx)
	require.NoError(t, err)
	require.True(t, detailed.Imaging)
	rich.emptyProfiles = false

	rich.fail["GetNodes"] = 1
	detailed, err = richClient.GetCapabilitiesDetailed(ctx)
	require.NoError(t, err)
	require.False(t, detailed.PTZDetail.PanTilt)

	rich.emptyNodes = true
	detailed, err = richClient.GetCapabilitiesDetailed(ctx)
	require.NoError(t, err)
	require.True(t, detailed.PTZDetail.PanTilt)
	require.True(t, detailed.PTZDetail.Zoom)
	require.False(t, detailed.PTZDetail.Home)
	rich.emptyNodes = false

	rich.fail["GetConfigurations"] = 1
	detailed, err = richClient.GetCapabilitiesDetailed(ctx)
	require.NoError(t, err)
	require.True(t, detailed.PTZDetail.Presets)
}

func TestONVIFEvents(t *testing.T) {
	fake := newFakeONVIF()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	client := connectFake(t, srv.URL)

	events := make(chan ONVIFEvent, 1)
	sub := client.NewEventSubscriber(
		WithPollInterval(20*time.Millisecond),
		WithPullTimeout(time.Second),
		WithSubscriptionDuration(time.Minute),
		WithEventCallback(func(event ONVIFEvent) {
			select {
			case events <- event:
			default:
			}
		}),
	)
	t.Cleanup(func() { sub.(*EventSubscriberImpl).StopAll(ctx) })

	require.NoError(t, sub.Subscribe(ctx, "cam-1"))
	require.NoError(t, sub.Subscribe(ctx, "cam-1"))
	require.True(t, sub.(*EventSubscriberImpl).IsSubscribed("cam-1"))
	msgs, err := sub.GetEventMessages(ctx)
	require.NoError(t, err)
	require.Nil(t, msgs)

	select {
	case event := <-events:
		require.Equal(t, "tns1:VideoSource/MotionAlarm", event.Topic)
		require.Equal(t, "cam-1", event.CameraID)
		require.Equal(t, "true", event.Data["State"])
	case <-time.After(2 * time.Second):
		t.Fatal("event was not published")
	}
	fake.setCount(fake.fail, "Renew", 1)
	time.Sleep(300 * time.Millisecond)
	require.False(t, sub.(*EventSubscriberImpl).IsSubscribed("cam-1"))
	sub.(*EventSubscriberImpl).SetEventCallback(nil)
	require.NoError(t, sub.Unsubscribe(ctx, "missing"))
	require.NoError(t, sub.Unsubscribe(ctx, "cam-1"))

	quiet := client.NewEventSubscriber(WithPollInterval(time.Hour))
	require.NoError(t, quiet.Subscribe(ctx, "cam-2"))
	require.NoError(t, quiet.Subscribe(ctx, "cam-2"))
	require.True(t, quiet.(*EventSubscriberImpl).IsSubscribed("cam-2"))
	msgs, err = quiet.GetEventMessages(ctx)
	require.NoError(t, err)
	require.Nil(t, msgs)
	fake.setCount(fake.fail, "Unsubscribe", 1)
	require.NoError(t, quiet.Unsubscribe(ctx, "cam-2"))
	require.False(t, quiet.(*EventSubscriberImpl).IsSubscribed("cam-2"))

	pullFails := client.NewEventSubscriber(WithPollInterval(20*time.Millisecond), WithSubscriptionDuration(time.Minute))
	fake.setCount(fake.fail, "PullMessages", 100)
	require.NoError(t, pullFails.Subscribe(ctx, "cam-3"))
	time.Sleep(80 * time.Millisecond)
	fake.setCount(fake.fail, "Renew", 1)
	time.Sleep(300 * time.Millisecond)
	require.False(t, pullFails.(*EventSubscriberImpl).IsSubscribed("cam-3"))
	require.NoError(t, pullFails.Unsubscribe(ctx, "cam-3"))

	fake.setCount(fake.fail, "CreatePullPointSubscription", 1)
	require.ErrorContains(t, quiet.Subscribe(ctx, "cam-4"), "create PullPoint subscription")

	stopped := client.NewEventSubscriber(WithPollInterval(time.Hour))
	require.NoError(t, stopped.Subscribe(ctx, "cam-5"))
	require.NoError(t, stopped.Subscribe(ctx, "cam-6"))
	stopped.(*EventSubscriberImpl).StopAll(ctx)
	require.False(t, stopped.(*EventSubscriberImpl).IsSubscribed("cam-5"))
	require.False(t, stopped.(*EventSubscriberImpl).IsSubscribed("cam-6"))
}

type fakeONVIF struct {
	mu                sync.Mutex
	fail              map[string]int
	hijack            map[string]int
	redirect          map[string]int
	omitImaging       bool
	omitPTZ           bool
	emptyCapabilities bool
	emptyProfiles     bool
	emptyNodes        bool
	presetToken       string
	searchState       string
}

func newFakeONVIF() *fakeONVIF {
	return &fakeONVIF{
		fail:        map[string]int{},
		hijack:      map[string]int{},
		redirect:    map[string]int{},
		presetToken: "pre1",
		searchState: "Completed",
	}
}

func (f *fakeONVIF) take(bucket map[string]int, op string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if bucket[op] <= 0 {
		return false
	}
	bucket[op]--
	return true
}

func (f *fakeONVIF) setCount(bucket map[string]int, op string, n int) {
	f.mu.Lock()
	bucket[op] = n
	f.mu.Unlock()
}

func (f *fakeONVIF) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	op := soapOperation(string(body))
	if f.take(f.hijack, op) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	if f.take(f.redirect, op) {
		http.Redirect(w, r, "http://203.0.113.10/redirected", http.StatusFound)
		return
	}
	if f.take(f.fail, op) {
		http.Error(w, "soap failed", http.StatusInternalServerError)
		return
	}
	base := "http://" + r.Host
	_, _ = io.WriteString(w, soapEnvelope(f.response(op, base, string(body))))
}

func (f *fakeONVIF) response(op, base, body string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch op {
	case "GetSystemDateAndTime":
		return `<GetSystemDateAndTimeResponse><SystemDateAndTime><UTCDateTime><Time><Hour>1</Hour><Minute>2</Minute><Second>3</Second></Time><Date><Year>2026</Year><Month>1</Month><Day>2</Day></Date></UTCDateTime></SystemDateAndTime></GetSystemDateAndTimeResponse>`
	case "GetServices":
		return servicesResponse(base, !f.omitImaging, !f.omitPTZ)
	case "GetCapabilities":
		if f.emptyCapabilities {
			return `<GetCapabilitiesResponse><Capabilities></Capabilities></GetCapabilitiesResponse>`
		}
		return fmt.Sprintf(`<GetCapabilitiesResponse><Capabilities>
			<Device><XAddr>%s</XAddr></Device>
			<Media><XAddr>%s</XAddr></Media>
			<PTZ><XAddr>%s</XAddr></PTZ>
			<Imaging><XAddr>%s</XAddr></Imaging>
			<Events><XAddr>%s</XAddr></Events>
			<Extension><Media2><XAddr>%s</XAddr></Media2></Extension>
		</Capabilities></GetCapabilitiesResponse>`, base, base, base, base, base, base)
	case "GetDeviceInformation":
		return `<GetDeviceInformationResponse><Manufacturer>Acme</Manufacturer><Model>Cam-1</Model><FirmwareVersion>1.2.3</FirmwareVersion><SerialNumber>SN1</SerialNumber><HardwareId>HW1</HardwareId></GetDeviceInformationResponse>`
	case "GetProfiles":
		if f.emptyProfiles {
			return `<GetProfilesResponse></GetProfilesResponse>`
		}
		if strings.Contains(body, "ver20/media") {
			return `<GetProfilesResponse><Profiles token="Profile_1"><Name>Main</Name><Configurations>
				<VideoSource token="vs1"/>
				<VideoEncoder token="ve1"><Encoding>H264</Encoding><Resolution><Width>1920</Width><Height>1080</Height></Resolution><RateControl><FrameRateLimit>25</FrameRateLimit><BitrateLimit>4096</BitrateLimit></RateControl></VideoEncoder>
			</Configurations></Profiles></GetProfilesResponse>`
		}
		return `<GetProfilesResponse><Profiles token="Profile_1"><Name>Main</Name>
			<VideoSourceConfiguration token="vs1"/>
			<VideoEncoderConfiguration token="ve1"><Encoding>H264</Encoding><Resolution><Width>1280</Width><Height>720</Height></Resolution></VideoEncoderConfiguration>
		</Profiles></GetProfilesResponse>`
	case "GetStreamUri":
		if strings.Contains(body, "ver20/media") {
			return `<GetStreamUriResponse><Uri>rtsp://203.0.113.10/main</Uri></GetStreamUriResponse>`
		}
		return `<GetStreamUriResponse><MediaUri><Uri>rtsp://203.0.113.10/main</Uri></MediaUri></GetStreamUriResponse>`
	case "GetSnapshotUri":
		if strings.Contains(body, "ver20/media") {
			return `<GetSnapshotUriResponse><Uri>http://203.0.113.10/snap.jpg</Uri></GetSnapshotUriResponse>`
		}
		return `<GetSnapshotUriResponse><MediaUri><Uri>http://203.0.113.10/snap.jpg</Uri></MediaUri></GetSnapshotUriResponse>`
	case "GetImagingSettings":
		return `<GetImagingSettingsResponse><ImagingSettings><Brightness>40</Brightness><Contrast>10</Contrast><ColorSaturation>20</ColorSaturation><Sharpness>30</Sharpness><WhiteBalance><Mode>manual</Mode></WhiteBalance><Exposure><Mode>auto</Mode></Exposure></ImagingSettings></GetImagingSettingsResponse>`
	case "GetOptions":
		return `<GetOptionsResponse><ImagingOptions><Brightness><Min>0</Min><Max>100</Max></Brightness><Contrast><Min>0</Min><Max>100</Max></Contrast><ColorSaturation><Min>0</Min><Max>100</Max></ColorSaturation><Sharpness><Min>0</Min><Max>100</Max></Sharpness></ImagingOptions></GetOptionsResponse>`
	case "GetNodes":
		if f.emptyNodes {
			return `<GetNodesResponse></GetNodesResponse>`
		}
		return `<GetNodesResponse><PTZNode token="node1"><Name>Node</Name><FixedHomePosition>true</FixedHomePosition><MaximumNumberOfPresets>8</MaximumNumberOfPresets><HomeSupported>true</HomeSupported></PTZNode></GetNodesResponse>`
	case "GetConfigurations":
		return `<GetConfigurationsResponse><PTZConfiguration token="cfg1"><Name>Cfg</Name><DefaultAbsolutePantTiltPositionSpace>abs</DefaultAbsolutePantTiltPositionSpace><DefaultAbsoluteZoomPositionSpace>zoom</DefaultAbsoluteZoomPositionSpace><DefaultContinuousPanTiltVelocitySpace>cont</DefaultContinuousPanTiltVelocitySpace><DefaultContinuousZoomVelocitySpace>czoom</DefaultContinuousZoomVelocitySpace></PTZConfiguration></GetConfigurationsResponse>`
	case "GetStatus":
		return `<GetStatusResponse><Status><Position><PanTilt x="0.1" y="0.2"/><Zoom x="0.3"/></Position><MoveStatus><PanTilt>MOVING</PanTilt><Zoom>IDLE</Zoom></MoveStatus></Status></GetStatusResponse>`
	case "GetPresets":
		return fmt.Sprintf(`<GetPresetsResponse><Preset token="%s"><Name>Door</Name><PTZPosition><PanTilt x="0.4" y="0.5"/><Zoom x="0.6"/></PTZPosition></Preset></GetPresetsResponse>`, f.presetToken)
	case "SetPreset":
		return `<SetPresetResponse><PresetToken>pre-new</PresetToken></SetPresetResponse>`
	case "CreatePullPointSubscription":
		return fmt.Sprintf(`<CreatePullPointSubscriptionResponse><SubscriptionReference><Address>%s</Address></SubscriptionReference></CreatePullPointSubscriptionResponse>`, base)
	case "PullMessages":
		return `<PullMessagesResponse>
			<NotificationMessage><Topic></Topic></NotificationMessage>
			<NotificationMessage><Topic>tns1:VideoSource/MotionAlarm</Topic><Message><Message UtcTime="2026-01-02T03:04:05Z"><Data><SimpleItem Name="State" Value="true"/></Data></Message></Message></NotificationMessage>
		</PullMessagesResponse>`
	case "GetRecordings":
		return `<GetRecordingsResponse><RecordingItem><RecordingToken>rec1</RecordingToken><Configuration><Source><SourceId>src</SourceId><Name>cam</Name><Location>lobby</Location><Description>front</Description></Source><Content><Name>clip</Name></Content></Configuration></RecordingItem></GetRecordingsResponse>`
	case "FindRecordings":
		return `<FindRecordingsResponse><SearchToken>search-1</SearchToken></FindRecordingsResponse>`
	case "GetRecordingSearchResults":
		return fmt.Sprintf(`<GetRecordingSearchResultsResponse><ResultList><SearchState>%s</SearchState><Recording><RecordingToken>rec1</RecordingToken><Track><TrackToken>tr1</TrackToken><Segment><SegmentToken>seg1</SegmentToken><Start>2026-01-02T03:04:05Z</Start><End>2026-01-02T03:05:05Z</End><FileURL>/recordings/seg1.mp4</FileURL><Size>1024</Size></Segment></Track></Recording></ResultList></GetRecordingSearchResultsResponse>`, f.searchState)
	case "GetReplayUri":
		return `<GetReplayUriResponse><Uri>rtsp://203.0.113.10/replay</Uri></GetReplayUriResponse>`
	case "SystemReboot":
		return `<SystemRebootResponse><Message>rebooting</Message></SystemRebootResponse>`
	case "GetNetworkInterfaces":
		return `<GetNetworkInterfacesResponse><NetworkInterfaces token="if1"><Enabled>true</Enabled><Info><Name>eth0</Name></Info><IPv4><Enabled>true</Enabled><Config><Manual><Address>203.0.113.10</Address><PrefixLength>24</PrefixLength></Manual><DHCP>true</DHCP></Config></IPv4><IPv6><Enabled>true</Enabled><Config><DHCP>false</DHCP></Config></IPv6></NetworkInterfaces></GetNetworkInterfacesResponse>`
	case "GetUsers":
		return `<GetUsersResponse><User><Username>admin</Username><UserLevel>Administrator</UserLevel></User></GetUsersResponse>`
	default:
		return `<ok/>`
	}
}

func servicesResponse(base string, imaging, ptz bool) string {
	var b strings.Builder
	b.WriteString(`<GetServicesResponse>`)
	writeService(&b, "http://www.onvif.org/ver10/media/wsdl", base)
	writeService(&b, "http://www.onvif.org/ver20/media/wsdl", base)
	writeService(&b, "http://www.onvif.org/ver10/events/wsdl", base)
	writeService(&b, "http://www.onvif.org/ver10/recording/wsdl", base)
	writeService(&b, "http://www.onvif.org/ver10/search/wsdl", base)
	writeService(&b, "http://www.onvif.org/ver10/replay/wsdl", base)
	if imaging {
		writeService(&b, "http://www.onvif.org/ver20/imaging/wsdl", base)
	}
	if ptz {
		writeService(&b, "http://www.onvif.org/ver20/ptz/wsdl", base)
	}
	b.WriteString(`</GetServicesResponse>`)
	return b.String()
}

func writeService(b *strings.Builder, namespace, addr string) {
	fmt.Fprintf(b, `<Service><Namespace>%s</Namespace><XAddr>%s</XAddr><Version><Major>2</Major><Minor>0</Minor></Version></Service>`, namespace, addr)
}

func soapEnvelope(inner string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>` + inner + `</s:Body></s:Envelope>`
}

func soapOperation(body string) string {
	ops := []string{
		"GetSystemDateAndTime", "GetServices", "GetCapabilities", "GetDeviceInformation",
		"GetRecordingSearchResults", "FindRecordings", "EndSearch", "GetRecordings", "GetReplayUri",
		"GetSnapshotUri", "GetStreamUri", "GetProfiles", "GetImagingSettings", "SetImagingSettings",
		"GetOptions", "CreatePullPointSubscription", "PullMessages", "Renew", "Unsubscribe",
		"GetNetworkInterfaces", "SystemReboot", "CreateUsers", "DeleteUsers", "SetUser", "GetUsers",
		"GetNodes", "GetConfigurations", "GetStatus", "GetPresets", "SetPreset", "GotoPreset",
		"RemovePreset", "ContinuousMove", "AbsoluteMove", "RelativeMove", "Stop",
	}
	for _, op := range ops {
		if strings.Contains(body, "<"+op+" ") || strings.Contains(body, "<"+op+">") || strings.Contains(body, "<"+op+"/") {
			return op
		}
	}
	return ""
}

func connectFake(t *testing.T, rawURL string) *Client {
	t.Helper()
	client := NewClient(rawURL+"/onvif/device_service", "user", "secret")
	require.NoError(t, client.Connect(context.Background()))
	return client
}

func splitHostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	host, portText, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	return host, port
}
