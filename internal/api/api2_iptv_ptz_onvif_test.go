package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/camera"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/gb28181"
	"github.com/lalmax-pro/lalmax-nvr/internal/iptv"
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// This file is self-contained: every helper, type and constant below carries the
// unique "api2io" prefix so it cannot collide with helpers added by other test
// files in the same package.

// --- shared request/handler helpers ---

func api2ioDo(t *testing.T, h *Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)
	return rr
}

// api2ioDirect invokes a handler method directly (no chi route context), which
// is how the empty-URL-parameter validation branches are exercised.
func api2ioDirect(t *testing.T, fn func(http.ResponseWriter, *http.Request), method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/api/cameras//onvif", reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	fn(rr, req)
	return rr
}

// api2ioChiRequest builds a request carrying chi URL parameters, letting tests
// drive handlers that read route params directly.
func api2ioChiRequest(method, path string, params map[string]string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func api2ioNewHandler(t *testing.T, cfg *config.Config, cloud CloudAuthProxy) (*Handler, *camera.CameraManager, *storage.DB, *storage.Manager) {
	t.Helper()
	db, store := setupTestDB(t)
	var camMgr *camera.CameraManager
	if cfg != nil {
		camMgr = camera.NewCameraManager(cfg, store, db, "")
	}
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, "", nil, cloud)
	h.SetMultiUserAuthMW(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.UserContextKey, &model.User{
				ID:       1,
				Username: "admin",
				Role:     model.RoleSuperAdmin,
				Enabled:  true,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	return h, camMgr, db, store
}

func api2ioCam(id, protocol, url, onvifEndpoint string) config.CameraConfig {
	return config.CameraConfig{
		ID:            id,
		Name:          id,
		Protocol:      protocol,
		URL:           url,
		ONVIFEndpoint: onvifEndpoint,
		Username:      "user",
		Password:      "secret",
		Enabled:       true,
	}
}

func api2ioCfg(cams ...config.CameraConfig) *config.Config {
	return &config.Config{Cameras: cams}
}

func api2ioSeedCamera(t *testing.T, db *storage.DB, id, protocol, url, onvifEndpoint string) {
	t.Helper()
	require.NoError(t, db.UpsertCamera(context.Background(), id, id, protocol, "", url, "user", "secret", true, onvifEndpoint, "", ""))
}

// --- fake ONVIF device (SOAP over httptest, no real hardware) ---

type api2ioFakeONVIFOptions struct {
	EmptyProfiles bool
	EmptyPresets  bool
	FailOps       []string
	// FailAfter fails an operation only after the given number of successful
	// calls, which models endpoints that probe a URI once before using it.
	FailAfter map[string]int
	// FaultOps returns a SOAP fault (HTTP 200) for the given operation.
	FaultOps  map[string]string
	ReplayURI string
}

type api2ioFakeONVIF struct {
	emptyProfiles bool
	emptyPresets  bool
	failOps       map[string]struct{}
	failAfter     map[string]int
	faultOps      map[string]string
	replayURI     string
	mu            sync.Mutex
	calls         map[string]int
}

func api2ioNewFakeONVIF(t *testing.T, opts api2ioFakeONVIFOptions) (*api2ioFakeONVIF, *httptest.Server) {
	t.Helper()
	fake := &api2ioFakeONVIF{
		emptyProfiles: opts.EmptyProfiles,
		emptyPresets:  opts.EmptyPresets,
		failOps:       make(map[string]struct{}, len(opts.FailOps)),
		failAfter:     opts.FailAfter,
		faultOps:      opts.FaultOps,
		replayURI:     opts.ReplayURI,
		calls:         map[string]int{},
	}
	if fake.replayURI == "" {
		fake.replayURI = "rtsp://203.0.113.10/replay/rec1"
	}
	for _, op := range opts.FailOps {
		fake.failOps[op] = struct{}{}
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return fake, srv
}

func (f *api2ioFakeONVIF) callCount(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

func (f *api2ioFakeONVIF) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	op := api2ioSOAPOperation(string(body))
	f.mu.Lock()
	f.calls[op]++
	calls := f.calls[op]
	f.mu.Unlock()
	if _, fail := f.failOps[op]; fail {
		http.Error(w, "soap failed", http.StatusInternalServerError)
		return
	}
	if after, ok := f.failAfter[op]; ok && calls > after {
		http.Error(w, "soap failed", http.StatusInternalServerError)
		return
	}
	if reason, ok := f.faultOps[op]; ok {
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		_, _ = io.WriteString(w, api2ioSOAPFaultEnvelope("ter:ActionNotSupported", reason))
		return
	}
	base := "http://" + r.Host
	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	_, _ = io.WriteString(w, api2ioSOAPEnvelope(f.response(op, base, string(body))))
}

func api2ioSOAPFaultEnvelope(code, reason string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><s:Fault><s:Code><s:Value>` + code + `</s:Value></s:Code><s:Reason><s:Text xml:lang="en">` + reason + `</s:Text></s:Reason></s:Fault></s:Body></s:Envelope>`
}

func api2ioSOAPEnvelope(inner string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>` + inner + `</s:Body></s:Envelope>`
}

func api2ioSOAPOperation(body string) string {
	ops := []string{
		"GetSystemDateAndTime", "GetServices", "GetCapabilities", "GetDeviceInformation",
		"GetProfiles", "GetStreamUri", "GetSnapshotUri",
		"FindRecordings", "GetRecordingSearchResults", "EndSearch", "GetRecordings", "GetReplayUri",
		"GetImagingSettings", "SetImagingSettings", "GetOptions",
		"GetNodes", "GetConfigurations", "GetStatus", "GetPresets", "SetPreset", "GotoPreset",
		"RemovePreset", "ContinuousMove", "AbsoluteMove", "RelativeMove", "Stop",
		"SystemReboot", "GetNetworkInterfaces", "SetNetworkInterfaces",
		"GetUsers", "CreateUsers", "DeleteUsers", "SetUser",
	}
	for _, op := range ops {
		if strings.Contains(body, "<"+op+" ") || strings.Contains(body, "<"+op+">") || strings.Contains(body, "<"+op+"/") {
			return op
		}
	}
	return ""
}

func (f *api2ioFakeONVIF) response(op, base, body string) string {
	switch op {
	case "GetSystemDateAndTime":
		return `<GetSystemDateAndTimeResponse><SystemDateAndTime><UTCDateTime><Time><Hour>1</Hour><Minute>2</Minute><Second>3</Second></Time><Date><Year>2026</Year><Month>1</Month><Day>2</Day></Date></UTCDateTime></SystemDateAndTime></GetSystemDateAndTimeResponse>`
	case "GetServices":
		var b strings.Builder
		b.WriteString(`<GetServicesResponse>`)
		for _, ns := range []string{
			"http://www.onvif.org/ver10/media/wsdl",
			"http://www.onvif.org/ver20/media/wsdl",
			"http://www.onvif.org/ver10/recording/wsdl",
			"http://www.onvif.org/ver10/search/wsdl",
			"http://www.onvif.org/ver10/replay/wsdl",
			"http://www.onvif.org/ver20/ptz/wsdl",
			"http://www.onvif.org/ver20/imaging/wsdl",
			"http://www.onvif.org/ver10/events/wsdl",
		} {
			fmt.Fprintf(&b, `<Service><Namespace>%s</Namespace><XAddr>%s/onvif/service</XAddr><Version><Major>2</Major><Minor>0</Minor></Version></Service>`, ns, base)
		}
		b.WriteString(`</GetServicesResponse>`)
		return b.String()
	case "GetCapabilities":
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
	case "GetSnapshotUri":
		if strings.Contains(body, "ver20/media") {
			return `<GetSnapshotUriResponse><Uri>http://192.0.2.10/snap.jpg</Uri></GetSnapshotUriResponse>`
		}
		return `<GetSnapshotUriResponse><MediaUri><Uri>http://192.0.2.10/snap.jpg</Uri></MediaUri></GetSnapshotUriResponse>`
	case "GetImagingSettings":
		return `<GetImagingSettingsResponse><ImagingSettings><Brightness>40</Brightness><Contrast>10</Contrast><ColorSaturation>20</ColorSaturation><Sharpness>30</Sharpness><WhiteBalance><Mode>manual</Mode></WhiteBalance><Exposure><Mode>auto</Mode></Exposure></ImagingSettings></GetImagingSettingsResponse>`
	case "GetOptions":
		return `<GetOptionsResponse><ImagingOptions><Brightness><Min>0</Min><Max>100</Max></Brightness><Contrast><Min>0</Min><Max>100</Max></Contrast><ColorSaturation><Min>0</Min><Max>100</Max></ColorSaturation><Sharpness><Min>0</Min><Max>100</Max></Sharpness></ImagingOptions></GetOptionsResponse>`
	case "GetNodes":
		return `<GetNodesResponse><PTZNode token="node1"><Name>Node</Name><MaximumNumberOfPresets>8</MaximumNumberOfPresets><HomeSupported>true</HomeSupported></PTZNode></GetNodesResponse>`
	case "GetConfigurations":
		return `<GetConfigurationsResponse><PTZConfiguration token="cfg1"><Name>Cfg</Name><DefaultAbsolutePantTiltPositionSpace>abs</DefaultAbsolutePantTiltPositionSpace><DefaultAbsoluteZoomPositionSpace>zoom</DefaultAbsoluteZoomPositionSpace><DefaultContinuousPanTiltVelocitySpace>cont</DefaultContinuousPanTiltVelocitySpace><DefaultContinuousZoomVelocitySpace>czoom</DefaultContinuousZoomVelocitySpace></PTZConfiguration></GetConfigurationsResponse>`
	case "GetStatus":
		return `<GetStatusResponse><Status><Position><PanTilt x="0.1" y="0.2"/><Zoom x="0.3"/></Position><MoveStatus><PanTilt>MOVING</PanTilt><Zoom>IDLE</Zoom></MoveStatus></Status></GetStatusResponse>`
	case "GetPresets":
		if f.emptyPresets {
			return `<GetPresetsResponse></GetPresetsResponse>`
		}
		return `<GetPresetsResponse><Preset token="pre1"><Name>Door</Name><PTZPosition><PanTilt x="0.4" y="0.5"/><Zoom x="0.6"/></PTZPosition></Preset></GetPresetsResponse>`
	case "SetPreset":
		return `<SetPresetResponse><PresetToken>pre-new</PresetToken></SetPresetResponse>`
	case "GetRecordings":
		return `<GetRecordingsResponse><RecordingItem><RecordingToken>rec1</RecordingToken><Configuration><Source><SourceId>src</SourceId><Name>cam</Name><Location>lobby</Location><Description>front</Description></Source><Content><Name>clip</Name></Content></Configuration></RecordingItem></GetRecordingsResponse>`
	case "FindRecordings":
		return `<FindRecordingsResponse><SearchToken>search-1</SearchToken></FindRecordingsResponse>`
	case "GetRecordingSearchResults":
		return `<GetRecordingSearchResultsResponse><ResultList><SearchState>Completed</SearchState><Recording><RecordingToken>rec1</RecordingToken><Track><TrackToken>tr1</TrackToken><Segment><SegmentToken>seg1</SegmentToken><Start>2026-01-02T03:04:05Z</Start><End>2026-01-02T03:05:05Z</End><FileURL>/recordings/seg1.mp4</FileURL><Size>1024</Size></Segment></Track></Recording></ResultList></GetRecordingSearchResultsResponse>`
	case "GetReplayUri":
		return fmt.Sprintf(`<GetReplayUriResponse><Uri>%s</Uri></GetReplayUriResponse>`, f.replayURI)
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

// --- ONVIF profile / capability endpoints ---

func TestApi2IOONVIFProfilesAndCapabilitiesSuccess(t *testing.T) {
	fake, srv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{})
	cfg := api2ioCfg(api2ioCam("onvif-cam", "onvif", srv.URL+"/onvif/device_service", srv.URL+"/onvif/device_service"))
	h, _, db, _ := api2ioNewHandler(t, cfg, nil)
	api2ioSeedCamera(t, db, "onvif-cam", "onvif", srv.URL+"/onvif/device_service", srv.URL+"/onvif/device_service")

	rr := api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/profiles", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var profiles struct {
		Profiles []struct {
			Token    string `json:"token"`
			Encoding string `json:"encoding"`
		} `json:"profiles"`
		Capabilities struct {
			PTZ       bool `json:"ptz"`
			Streaming bool `json:"streaming"`
		} `json:"capabilities"`
	}
	parseJSON(t, rr, &profiles)
	require.Len(t, profiles.Profiles, 1)
	require.Equal(t, "Profile_1", profiles.Profiles[0].Token)
	require.Equal(t, "H264", profiles.Profiles[0].Encoding)
	require.True(t, profiles.Capabilities.PTZ)
	require.True(t, profiles.Capabilities.Streaming)
	require.Positive(t, fake.callCount("GetProfiles"))

	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/capabilities", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var caps map[string]any
	parseJSON(t, rr, &caps)
	require.Equal(t, true, caps["ptz"])
	require.Equal(t, true, caps["imaging"])
}

func TestApi2IOONVIFProfileErrors(t *testing.T) {
	_, srv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{})
	endpoint := srv.URL + "/onvif/device_service"

	// no camera manager configured
	nilMgr, _, db, _ := api2ioNewHandler(t, nil, nil)
	api2ioSeedCamera(t, db, "onvif-cam", "onvif", endpoint, endpoint)
	rr := api2ioDo(t, nilMgr, http.MethodGet, "/api/cameras/onvif-cam/onvif/profiles", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
	rr = api2ioDo(t, nilMgr, http.MethodGet, "/api/cameras/onvif-cam/onvif/capabilities", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())

	// camera exists in DB but not in the camera-manager config
	h, _, db2, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("other-cam", "onvif", endpoint, endpoint)), nil)
	api2ioSeedCamera(t, db2, "onvif-cam", "onvif", endpoint, endpoint)
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/profiles", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// DB says ONVIF but the manager config says RTSP -> not an ONVIF camera
	h2, _, db3, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "rtsp_h264", "rtsp://192.0.2.10/live", "")), nil)
	api2ioSeedCamera(t, db3, "onvif-cam", "onvif", endpoint, endpoint)
	rr = api2ioDo(t, h2, http.MethodGet, "/api/cameras/onvif-cam/onvif/profiles", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	// unreachable endpoint -> connection error
	dead := "http://127.0.0.1:1/onvif/device_service"
	h3, _, db4, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", dead, dead)), nil)
	api2ioSeedCamera(t, db4, "onvif-cam", "onvif", dead, dead)
	rr = api2ioDo(t, h3, http.MethodGet, "/api/cameras/onvif-cam/onvif/profiles", "")
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())

	// upstream profile failure
	_, failSrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{FailOps: []string{"GetProfiles"}})
	failEndpoint := failSrv.URL + "/onvif/device_service"
	h4, _, db5, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", failEndpoint, failEndpoint)), nil)
	api2ioSeedCamera(t, db5, "onvif-cam", "onvif", failEndpoint, failEndpoint)
	rr = api2ioDo(t, h4, http.MethodGet, "/api/cameras/onvif-cam/onvif/profiles", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())

	// upstream capability failure
	_, capSrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{FailOps: []string{"GetCapabilities"}})
	capEndpoint := capSrv.URL + "/onvif/device_service"
	h5, _, db6, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", capEndpoint, capEndpoint)), nil)
	api2ioSeedCamera(t, db6, "onvif-cam", "onvif", capEndpoint, capEndpoint)
	rr = api2ioDo(t, h5, http.MethodGet, "/api/cameras/onvif-cam/onvif/capabilities", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
}

// --- unified PTZ endpoints ---

func TestApi2IOPTZMoveModesAndStop(t *testing.T) {
	_, srv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{})
	endpoint := srv.URL + "/onvif/device_service"
	cfg := api2ioCfg(api2ioCam("onvif-cam", "onvif", endpoint, endpoint))
	h, _, db, _ := api2ioNewHandler(t, cfg, nil)
	api2ioSeedCamera(t, db, "onvif-cam", "onvif", endpoint, endpoint)

	for _, mode := range []string{"continuous", "absolute", "relative"} {
		body := fmt.Sprintf(`{"mode":%q,"pan":0.5,"tilt":0.25,"zoom":0.1}`, mode)
		rr := api2ioDo(t, h, http.MethodPost, "/api/cameras/onvif-cam/ptz/move", body)
		require.Equal(t, http.StatusOK, rr.Code, "%s: %s", mode, rr.Body.String())
	}

	rr := api2ioDo(t, h, http.MethodPost, "/api/cameras/onvif-cam/ptz/stop", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/ptz/status", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var status struct {
		Position map[string]any `json:"position"`
		Moving   bool           `json:"moving"`
	}
	parseJSON(t, rr, &status)
	require.True(t, status.Moving)
	require.NotEmpty(t, status.Position)
}

func TestApi2IOPTZUpstreamFailures(t *testing.T) {
	_, srv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{FailOps: []string{
		"ContinuousMove", "AbsoluteMove", "RelativeMove", "Stop", "GetStatus", "GetPresets",
		"SetPreset", "GotoPreset", "RemovePreset",
	}})
	endpoint := srv.URL + "/onvif/device_service"
	h, _, db, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", endpoint, endpoint)), nil)
	api2ioSeedCamera(t, db, "onvif-cam", "onvif", endpoint, endpoint)

	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/cameras/onvif-cam/ptz/move", `{"mode":"continuous","pan":0.5}`},
		{http.MethodPost, "/api/cameras/onvif-cam/ptz/move", `{"mode":"absolute","pan":0.5}`},
		{http.MethodPost, "/api/cameras/onvif-cam/ptz/move", `{"mode":"relative","pan":0.5}`},
		{http.MethodPost, "/api/cameras/onvif-cam/ptz/stop", ""},
		{http.MethodGet, "/api/cameras/onvif-cam/ptz/status", ""},
		{http.MethodGet, "/api/cameras/onvif-cam/ptz/presets", ""},
		{http.MethodPost, "/api/cameras/onvif-cam/ptz/presets", `{"name":"Door"}`},
		{http.MethodPost, "/api/cameras/onvif-cam/ptz/presets/pre1/goto", ""},
		{http.MethodDelete, "/api/cameras/onvif-cam/ptz/presets/pre1", ""},
	}
	for _, tc := range cases {
		rr := api2ioDo(t, h, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusInternalServerError, rr.Code, "%s %s: %s", tc.method, tc.path, rr.Body.String())
	}
}

func TestApi2IOPTZPresetLifecycle(t *testing.T) {
	_, srv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{})
	endpoint := srv.URL + "/onvif/device_service"
	h, _, db, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", endpoint, endpoint)), nil)
	api2ioSeedCamera(t, db, "onvif-cam", "onvif", endpoint, endpoint)

	rr := api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/ptz/presets", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var presets struct {
		Presets []struct {
			Token string `json:"token"`
			Name  string `json:"name"`
		} `json:"presets"`
	}
	parseJSON(t, rr, &presets)
	require.Len(t, presets.Presets, 1)
	require.Equal(t, "pre1", presets.Presets[0].Token)

	rr = api2ioDo(t, h, http.MethodPost, "/api/cameras/onvif-cam/ptz/presets", `{"name":""}`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPost, "/api/cameras/onvif-cam/ptz/presets", `{`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	rr = api2ioDo(t, h, http.MethodPost, "/api/cameras/onvif-cam/ptz/presets", `{"name":"Door"}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var created struct {
		Token string `json:"token"`
		Name  string `json:"name"`
	}
	parseJSON(t, rr, &created)
	require.Equal(t, "pre-new", created.Token)
	require.Equal(t, "Door", created.Name)

	rr = api2ioDo(t, h, http.MethodPost, "/api/cameras/onvif-cam/ptz/presets/pre1/goto", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodDelete, "/api/cameras/onvif-cam/ptz/presets/pre1", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
}

func TestApi2IOPTZValidationAndDependencyErrors(t *testing.T) {
	_, srv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{})
	endpoint := srv.URL + "/onvif/device_service"

	// RTSP camera: presets rejected, status/move rejected, presets list empty.
	h, _, db, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("rtsp-cam", "rtsp_h264", "rtsp://192.0.2.10/live", "")), nil)
	api2ioSeedCamera(t, db, "rtsp-cam", "rtsp_h264", "rtsp://192.0.2.10/live", "")
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/cameras/rtsp-cam/ptz/presets"},
		{http.MethodPost, "/api/cameras/rtsp-cam/ptz/presets/pre1/goto"},
		{http.MethodDelete, "/api/cameras/rtsp-cam/ptz/presets/pre1"},
	} {
		rr := api2ioDo(t, h, tc.method, tc.path, `{"name":"Door"}`)
		require.Equal(t, http.StatusBadRequest, rr.Code, "%s %s: %s", tc.method, tc.path, rr.Body.String())
	}
	rr := api2ioDo(t, h, http.MethodGet, "/api/cameras/rtsp-cam/ptz/presets", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var emptyPresets struct {
		Presets []any `json:"presets"`
	}
	parseJSON(t, rr, &emptyPresets)
	require.Empty(t, emptyPresets.Presets)

	// No camera manager: preset endpoints report the missing dependency.
	nilMgr, _, db2, _ := api2ioNewHandler(t, nil, nil)
	api2ioSeedCamera(t, db2, "onvif-cam", "onvif", endpoint, endpoint)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/cameras/onvif-cam/ptz/presets"},
		{http.MethodPost, "/api/cameras/onvif-cam/ptz/presets"},
		{http.MethodPost, "/api/cameras/onvif-cam/ptz/presets/pre1/goto"},
		{http.MethodDelete, "/api/cameras/onvif-cam/ptz/presets/pre1"},
	} {
		rr := api2ioDo(t, nilMgr, tc.method, tc.path, `{"name":"Door"}`)
		require.Equal(t, http.StatusInternalServerError, rr.Code, "%s %s: %s", tc.method, tc.path, rr.Body.String())
	}

	// Camera absent from the manager config -> not found.
	h3, _, db3, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("other", "onvif", endpoint, endpoint)), nil)
	api2ioSeedCamera(t, db3, "onvif-cam", "onvif", endpoint, endpoint)
	rr = api2ioDo(t, h3, http.MethodGet, "/api/cameras/onvif-cam/ptz/presets", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// Empty profile list -> no-profile error.
	_, noProfSrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{EmptyProfiles: true})
	noProfEndpoint := noProfSrv.URL + "/onvif/device_service"
	h4, _, db4, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", noProfEndpoint, noProfEndpoint)), nil)
	api2ioSeedCamera(t, db4, "onvif-cam", "onvif", noProfEndpoint, noProfEndpoint)
	rr = api2ioDo(t, h4, http.MethodGet, "/api/cameras/onvif-cam/ptz/presets", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h4, http.MethodGet, "/api/cameras/onvif-cam/ptz/status", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// Empty camera ID validation (handler-level, no route parameters).
	h5, _, _, _ := api2ioNewHandler(t, api2ioCfg(), nil)
	for _, fn := range []func(http.ResponseWriter, *http.Request){
		h5.handlePTZMove, h5.handlePTZStop, h5.handlePTZStatus,
		h5.handlePTZGetPresets, h5.handlePTZCreatePreset,
		h5.handlePTZGoToPreset, h5.handlePTZDeletePreset,
	} {
		rr := api2ioDirect(t, fn, http.MethodPost, `{"mode":"continuous"}`)
		require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestApi2IOGB28181PTZPaths(t *testing.T) {
	// GB28181 module disabled: the dedicated endpoint reports 503.
	h, _, db, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("gb-cam", "gb28181", "gb28181://dev:ch", "")), nil)
	api2ioSeedCamera(t, db, "gb-cam", "gb28181", "gb28181://dev:ch", "")
	rr := api2ioDo(t, h, http.MethodPost, "/api/cameras/gb-cam/ptz/move", `{"mode":"continuous","pan":0.5}`)
	require.Equal(t, http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPost, "/api/cameras/gb-cam/ptz/stop", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	// Presets are unsupported for GB28181 but the endpoint answers with an empty list.
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/gb-cam/ptz/presets", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	// Status is a static response for GB28181.
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/gb-cam/ptz/status", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var gbStatus struct {
		Moving bool `json:"moving"`
	}
	parseJSON(t, rr, &gbStatus)
	require.False(t, gbStatus.Moving)

	// With a real (but device-less) GB28181 server, PTZ commands fail upstream.
	srv := api2ioNewGBServer(t, db)
	h2, _, db2, _ := api2ioNewHandler(t, api2ioCfg(
		api2ioCam("dev:ch", "gb28181", "gb28181://dev:ch", ""),
		api2ioCam("bad-id", "gb28181", "gb28181://bad", ""),
	), nil)
	h2.SetGB28181ServerInstance(srv)
	api2ioSeedCamera(t, db2, "dev:ch", "gb28181", "gb28181://dev:ch", "")
	api2ioSeedCamera(t, db2, "bad-id", "gb28181", "gb28181://bad", "")

	for _, body := range []string{
		`{"mode":"continuous","pan":0.5}`,
		`{"mode":"continuous","pan":-0.5}`,
		`{"mode":"continuous","tilt":0.5}`,
		`{"mode":"continuous","tilt":-0.5}`,
		`{"mode":"continuous","zoom":0.5}`,
		`{"mode":"continuous","zoom":-0.5}`,
		`{"mode":"absolute","pan":0.5}`,
	} {
		rr := api2ioDo(t, h2, http.MethodPost, "/api/cameras/dev:ch/ptz/move", body)
		require.Equal(t, http.StatusInternalServerError, rr.Code, "%s: %s", body, rr.Body.String())
	}
	rr = api2ioDo(t, h2, http.MethodPost, "/api/cameras/bad-id/ptz/move", `{"mode":"continuous"}`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h2, http.MethodPost, "/api/cameras/dev:ch/ptz/stop", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h2, http.MethodPost, "/api/cameras/bad-id/ptz/stop", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
}

func api2ioNewGBServer(t *testing.T, db *storage.DB) *gb28181.Server {
	t.Helper()
	cfg := &gb28181.Config{
		Enabled:         true,
		Host:            "127.0.0.1",
		Port:            0,
		ID:              "34020000002000000001",
		Password:        "api2io-secret",
		MediaIP:         "127.0.0.1",
		StandardVersion: "2016",
	}
	srv, err := gb28181.NewServerChecked(cfg, nil, db)
	require.NoError(t, err)
	t.Cleanup(srv.Stop)
	return srv
}

// --- snapshot, imaging and device-management endpoints ---

func TestApi2IOONVIFSnapshotAndImaging(t *testing.T) {
	_, srv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{})
	endpoint := srv.URL + "/onvif/device_service"
	h, _, db, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", endpoint, endpoint)), nil)
	api2ioSeedCamera(t, db, "onvif-cam", "onvif", endpoint, endpoint)

	rr := api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/snapshot/uri", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var snap struct {
		URI string `json:"uri"`
	}
	parseJSON(t, rr, &snap)
	require.Equal(t, "http://192.0.2.10/snap.jpg", snap.URI)

	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/imaging/settings", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var settings struct {
		Brightness float64 `json:"brightness"`
		Contrast   float64 `json:"contrast"`
	}
	parseJSON(t, rr, &settings)
	require.InDelta(t, 40, settings.Brightness, 0.001)
	require.InDelta(t, 10, settings.Contrast, 0.001)

	rr = api2ioDo(t, h, http.MethodPut, "/api/cameras/onvif-cam/imaging/settings", `{"brightness":55}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPut, "/api/cameras/onvif-cam/imaging/settings", `{`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/imaging/options", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// Missing camera manager.
	nilMgr, _, db2, _ := api2ioNewHandler(t, nil, nil)
	api2ioSeedCamera(t, db2, "onvif-cam", "onvif", endpoint, endpoint)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/cameras/onvif-cam/snapshot/uri"},
		{http.MethodGet, "/api/cameras/onvif-cam/imaging/settings"},
		{http.MethodGet, "/api/cameras/onvif-cam/imaging/options"},
		{http.MethodPut, "/api/cameras/onvif-cam/imaging/settings"},
	} {
		rr := api2ioDo(t, nilMgr, tc.method, tc.path, `{}`)
		require.Equal(t, http.StatusInternalServerError, rr.Code, "%s %s: %s", tc.method, tc.path, rr.Body.String())
	}

	// Upstream imaging/snapshot failures.
	_, failSrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{FailOps: []string{
		"GetSnapshotUri", "GetImagingSettings", "SetImagingSettings", "GetOptions",
	}})
	failEndpoint := failSrv.URL + "/onvif/device_service"
	h3, _, db3, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", failEndpoint, failEndpoint)), nil)
	api2ioSeedCamera(t, db3, "onvif-cam", "onvif", failEndpoint, failEndpoint)
	rr = api2ioDo(t, h3, http.MethodGet, "/api/cameras/onvif-cam/snapshot/uri", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h3, http.MethodGet, "/api/cameras/onvif-cam/imaging/settings", "")
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h3, http.MethodPut, "/api/cameras/onvif-cam/imaging/settings", `{}`)
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h3, http.MethodGet, "/api/cameras/onvif-cam/imaging/options", "")
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
}

func TestApi2IOONVIFDeviceManagement(t *testing.T) {
	_, srv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{})
	endpoint := srv.URL + "/onvif/device_service"
	h, _, db, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", endpoint, endpoint)), nil)
	api2ioSeedCamera(t, db, "onvif-cam", "onvif", endpoint, endpoint)

	rr := api2ioDo(t, h, http.MethodPost, "/api/cameras/onvif-cam/onvif/reboot", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/network", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var network struct {
		Interfaces []struct {
			Name string `json:"name"`
		} `json:"interfaces"`
	}
	parseJSON(t, rr, &network)
	require.Len(t, network.Interfaces, 1)
	require.Equal(t, "eth0", network.Interfaces[0].Name)

	// Updating network interfaces is not implemented by the ONVIF bridge.
	rr = api2ioDo(t, h, http.MethodPut, "/api/cameras/onvif-cam/onvif/network", `{"interfaces":[]}`)
	require.Equal(t, http.StatusNotImplemented, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPut, "/api/cameras/onvif-cam/onvif/network", `{`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/users", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var users struct {
		Users []struct {
			Username string `json:"username"`
		} `json:"users"`
	}
	parseJSON(t, rr, &users)
	require.Len(t, users.Users, 1)
	require.Equal(t, "admin", users.Users[0].Username)

	rr = api2ioDo(t, h, http.MethodPost, "/api/cameras/onvif-cam/onvif/users", `{"users":[{"username":"bob","level":"Operator"}]}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPost, "/api/cameras/onvif-cam/onvif/users", `{`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	rr = api2ioDo(t, h, http.MethodDelete, "/api/cameras/onvif-cam/onvif/users", `{"usernames":["bob"]}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodDelete, "/api/cameras/onvif-cam/onvif/users", `{`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	rr = api2ioDo(t, h, http.MethodPut, "/api/cameras/onvif-cam/onvif/users/bob", `{"password":"pw"}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPut, "/api/cameras/onvif-cam/onvif/users/bob", `{`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	// Missing camera manager.
	nilMgr, _, db2, _ := api2ioNewHandler(t, nil, nil)
	api2ioSeedCamera(t, db2, "onvif-cam", "onvif", endpoint, endpoint)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/cameras/onvif-cam/onvif/reboot", ""},
		{http.MethodGet, "/api/cameras/onvif-cam/onvif/network", ""},
		{http.MethodPut, "/api/cameras/onvif-cam/onvif/network", `{}`},
		{http.MethodGet, "/api/cameras/onvif-cam/onvif/users", ""},
		{http.MethodPost, "/api/cameras/onvif-cam/onvif/users", `{}`},
		{http.MethodDelete, "/api/cameras/onvif-cam/onvif/users", `{}`},
		{http.MethodPut, "/api/cameras/onvif-cam/onvif/users/bob", `{}`},
	} {
		rr := api2ioDo(t, nilMgr, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusInternalServerError, rr.Code, "%s %s: %s", tc.method, tc.path, rr.Body.String())
	}

	// Upstream device-management failures.
	_, failSrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{FailOps: []string{
		"SystemReboot", "GetNetworkInterfaces", "GetUsers", "CreateUsers", "DeleteUsers", "SetUser",
	}})
	failEndpoint := failSrv.URL + "/onvif/device_service"
	h3, _, db3, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", failEndpoint, failEndpoint)), nil)
	api2ioSeedCamera(t, db3, "onvif-cam", "onvif", failEndpoint, failEndpoint)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/cameras/onvif-cam/onvif/reboot", ""},
		{http.MethodGet, "/api/cameras/onvif-cam/onvif/network", ""},
		{http.MethodGet, "/api/cameras/onvif-cam/onvif/users", ""},
		{http.MethodPost, "/api/cameras/onvif-cam/onvif/users", `{"users":[{"username":"bob"}]}`},
		{http.MethodDelete, "/api/cameras/onvif-cam/onvif/users", `{"usernames":["bob"]}`},
		{http.MethodPut, "/api/cameras/onvif-cam/onvif/users/bob", `{"password":"pw"}`},
	} {
		rr := api2ioDo(t, h3, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusBadGateway, rr.Code, "%s %s: %s", tc.method, tc.path, rr.Body.String())
	}
}

// --- ONVIF recording endpoints ---

func TestApi2IOONVIFRecordingQueries(t *testing.T) {
	_, srv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{})
	endpoint := srv.URL + "/onvif/device_service"
	cfg := api2ioCfg(
		api2ioCam("onvif-cam", "onvif", endpoint, endpoint),
		api2ioCam("rtsp-cam", "rtsp_h264", "rtsp://192.0.2.10/live", ""),
	)
	h, _, db, _ := api2ioNewHandler(t, cfg, nil)
	api2ioSeedCamera(t, db, "onvif-cam", "onvif", endpoint, endpoint)
	api2ioSeedCamera(t, db, "rtsp-cam", "rtsp_h264", "rtsp://192.0.2.10/live", "")

	rr := api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var recordings struct {
		Recordings []struct {
			Token string `json:"token"`
		} `json:"recordings"`
	}
	parseJSON(t, rr, &recordings)
	require.Len(t, recordings.Recordings, 1)
	require.Equal(t, "rec1", recordings.Recordings[0].Token)

	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings?start_time=2020-01-01T00:00:00Z&end_time=2035-01-01T00:00:00Z", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings?start_time=nope", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings?end_time=nope", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	// Single recording information.
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings/rec1", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings/unknown", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// Search
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings/search", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings/search?start_time=nope", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings/search?start_time=2020-01-01T00:00:00Z&end_time=nope", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings/search?start_time=2020-01-01T00:00:00Z&max_results=abc", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var segments struct {
		Segments []struct {
			Token string `json:"token"`
		} `json:"segments"`
		Fallback bool `json:"fallback"`
	}
	parseJSON(t, rr, &segments)
	require.Len(t, segments.Segments, 1)
	require.Equal(t, "seg1", segments.Segments[0].Token)
	require.False(t, segments.Fallback)

	// Replay URI
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/replay/rec1", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var replay struct {
		URI      string `json:"uri"`
		Protocol string `json:"protocol"`
	}
	parseJSON(t, rr, &replay)
	require.Equal(t, "rtsp://203.0.113.10/replay/rec1", replay.URI)
	require.Equal(t, "rtsp", replay.Protocol)

	// Camera lookup / protocol / connection errors.
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/missing/onvif/recordings", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/missing/onvif/recordings/search?start_time=2020-01-01T00:00:00Z", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/rtsp-cam/onvif/recordings", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/rtsp-cam/onvif/recordings/rec1", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/rtsp-cam/onvif/recordings/search?start_time=2020-01-01T00:00:00Z", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/rtsp-cam/onvif/replay/rec1", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
}

func TestApi2IOONVIFRecordingFailures(t *testing.T) {
	dead := "http://127.0.0.1:1/onvif/device_service"
	cfg := api2ioCfg(api2ioCam("dead-cam", "onvif", dead, dead))
	h, _, db, _ := api2ioNewHandler(t, cfg, nil)
	api2ioSeedCamera(t, db, "dead-cam", "onvif", dead, dead)
	for _, path := range []string{
		"/api/cameras/dead-cam/onvif/recordings",
		"/api/cameras/dead-cam/onvif/recordings/rec1",
		"/api/cameras/dead-cam/onvif/recordings/search?start_time=2020-01-01T00:00:00Z",
		"/api/cameras/dead-cam/onvif/replay/rec1",
	} {
		rr := api2ioDo(t, h, http.MethodGet, path, "")
		require.Equal(t, http.StatusInternalServerError, rr.Code, "%s: %s", path, rr.Body.String())
	}

	// Upstream GetRecordings failure.
	_, failSrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{FailOps: []string{"GetRecordings"}})
	failEndpoint := failSrv.URL + "/onvif/device_service"
	h2, _, db2, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", failEndpoint, failEndpoint)), nil)
	api2ioSeedCamera(t, db2, "onvif-cam", "onvif", failEndpoint, failEndpoint)
	rr := api2ioDo(t, h2, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h2, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings/rec1", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
	// The search service is independent from GetRecordings, so it still succeeds.
	rr = api2ioDo(t, h2, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings/search?start_time=2020-01-01T00:00:00Z", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// Search and its GetRecordings fallback both fail.
	_, bothFailSrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{FailOps: []string{"FindRecordings", "GetRecordings"}})
	bothFailEndpoint := bothFailSrv.URL + "/onvif/device_service"
	h2b, _, db2b, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", bothFailEndpoint, bothFailEndpoint)), nil)
	api2ioSeedCamera(t, db2b, "onvif-cam", "onvif", bothFailEndpoint, bothFailEndpoint)
	rr = api2ioDo(t, h2b, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings/search?start_time=2020-01-01T00:00:00Z", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())

	// Search unsupported: fall back to GetRecordings.
	_, fallbackSrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{FailOps: []string{"FindRecordings"}})
	fallbackEndpoint := fallbackSrv.URL + "/onvif/device_service"
	h3, _, db3, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", fallbackEndpoint, fallbackEndpoint)), nil)
	api2ioSeedCamera(t, db3, "onvif-cam", "onvif", fallbackEndpoint, fallbackEndpoint)
	rr = api2ioDo(t, h3, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings/search?start_time=2020-01-01T00:00:00Z&end_time=2035-01-01T00:00:00Z", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var fallback struct {
		Segments []struct {
			Token          string `json:"token"`
			RecordingToken string `json:"recording_token"`
		} `json:"segments"`
		Fallback bool `json:"fallback"`
	}
	parseJSON(t, rr, &fallback)
	require.True(t, fallback.Fallback)
	require.Len(t, fallback.Segments, 1)
	require.Equal(t, "rec1", fallback.Segments[0].RecordingToken)

	// HTTP replay URI maps to the http protocol.
	_, httpSrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{ReplayURI: "http://192.0.2.10/replay/stream"})
	httpEndpoint := httpSrv.URL + "/onvif/device_service"
	h4, _, db4, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", httpEndpoint, httpEndpoint)), nil)
	api2ioSeedCamera(t, db4, "onvif-cam", "onvif", httpEndpoint, httpEndpoint)
	rr = api2ioDo(t, h4, http.MethodGet, "/api/cameras/onvif-cam/onvif/replay/rec1", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var httpReplay struct {
		Protocol string `json:"protocol"`
	}
	parseJSON(t, rr, &httpReplay)
	require.Equal(t, "http", httpReplay.Protocol)

	// Replay upstream failure.
	_, replayFailSrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{FailOps: []string{"GetReplayUri"}})
	replayFailEndpoint := replayFailSrv.URL + "/onvif/device_service"
	h5, _, db5, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", replayFailEndpoint, replayFailEndpoint)), nil)
	api2ioSeedCamera(t, db5, "onvif-cam", "onvif", replayFailEndpoint, replayFailEndpoint)
	rr = api2ioDo(t, h5, http.MethodGet, "/api/cameras/onvif-cam/onvif/replay/rec1", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
}

// --- additional ONVIF / PTZ branch coverage ---

func TestApi2IOONVIFRequireAndManagerErrors(t *testing.T) {
	_, srv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{})
	endpoint := srv.URL + "/onvif/device_service"
	cfg := api2ioCfg(
		api2ioCam("onvif-cam", "onvif", endpoint, endpoint),
		api2ioCam("rtsp-cam", "rtsp_h264", "rtsp://192.0.2.10/live", ""),
	)
	h, _, db, _ := api2ioNewHandler(t, cfg, nil)
	api2ioSeedCamera(t, db, "onvif-cam", "onvif", endpoint, endpoint)
	api2ioSeedCamera(t, db, "rtsp-cam", "rtsp_h264", "rtsp://192.0.2.10/live", "")

	// Unknown camera: requireONVIF rejects before touching the manager.
	for _, path := range []string{
		"/api/cameras/missing/onvif/profiles",
		"/api/cameras/missing/onvif/capabilities",
		"/api/cameras/missing/imaging/settings",
		"/api/cameras/missing/imaging/options",
		"/api/cameras/missing/snapshot/uri",
		"/api/cameras/missing/onvif/network",
		"/api/cameras/missing/onvif/users",
		"/api/cameras/missing/onvif/recordings",
	} {
		rr := api2ioDo(t, h, http.MethodGet, path, "")
		require.Equal(t, http.StatusNotFound, rr.Code, "%s: %s", path, rr.Body.String())
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/cameras/missing/onvif/reboot", ""},
		{http.MethodPut, "/api/cameras/missing/onvif/network", `{"interfaces":[]}`},
		{http.MethodPost, "/api/cameras/missing/onvif/users", `{"users":[]}`},
		{http.MethodDelete, "/api/cameras/missing/onvif/users", `{"usernames":[]}`},
		{http.MethodPut, "/api/cameras/missing/onvif/users/bob", `{"password":"x"}`},
		{http.MethodPut, "/api/cameras/missing/imaging/settings", `{}`},
	} {
		rr := api2ioDo(t, h, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusNotFound, rr.Code, "%s %s: %s", tc.method, tc.path, rr.Body.String())
	}

	// Non-ONVIF camera: requireONVIF returns 400.
	for _, path := range []string{
		"/api/cameras/rtsp-cam/onvif/profiles",
		"/api/cameras/rtsp-cam/onvif/capabilities",
	} {
		rr := api2ioDo(t, h, http.MethodGet, path, "")
		require.Equal(t, http.StatusBadRequest, rr.Code, "%s: %s", path, rr.Body.String())
	}

	// No database: the handler cannot resolve cameras.
	nilDB := NewHandler(nil, nil, noopAuthMW(), nil, nil, "", nil, nil)
	rr := api2ioDo(t, nilDB, http.MethodGet, "/api/cameras/onvif-cam/onvif/profiles", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
	rr = api2ioDo(t, nilDB, http.MethodGet, "/api/cameras/onvif-cam/ptz/status", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// Camera present in the DB but unknown to the camera manager: every ONVIF
	// endpoint reports the typed "camera not found" error.
	h2, _, db2, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("other-cam", "onvif", endpoint, endpoint)), nil)
	api2ioSeedCamera(t, db2, "onvif-cam", "onvif", endpoint, endpoint)
	for _, path := range []string{
		"/api/cameras/onvif-cam/onvif/profiles",
		"/api/cameras/onvif-cam/onvif/capabilities",
		"/api/cameras/onvif-cam/imaging/settings",
		"/api/cameras/onvif-cam/imaging/options",
		"/api/cameras/onvif-cam/snapshot/uri",
		"/api/cameras/onvif-cam/onvif/network",
		"/api/cameras/onvif-cam/onvif/users",
	} {
		rr := api2ioDo(t, h2, http.MethodGet, path, "")
		require.Equal(t, http.StatusNotFound, rr.Code, "%s: %s", path, rr.Body.String())
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/cameras/onvif-cam/onvif/reboot", ""},
		{http.MethodPut, "/api/cameras/onvif-cam/onvif/network", `{"interfaces":[]}`},
		{http.MethodPost, "/api/cameras/onvif-cam/onvif/users", `{"users":[]}`},
		{http.MethodDelete, "/api/cameras/onvif-cam/onvif/users", `{"usernames":[]}`},
		{http.MethodPut, "/api/cameras/onvif-cam/onvif/users/bob", `{"password":"x"}`},
		{http.MethodPut, "/api/cameras/onvif-cam/imaging/settings", `{}`},
	} {
		rr := api2ioDo(t, h2, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusNotFound, rr.Code, "%s %s: %s", tc.method, tc.path, rr.Body.String())
	}

	// Manager config says RTSP although the DB row says ONVIF.
	h3, _, db3, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "rtsp_h264", "rtsp://192.0.2.10/live", "")), nil)
	api2ioSeedCamera(t, db3, "onvif-cam", "onvif", endpoint, endpoint)
	rr = api2ioDo(t, h3, http.MethodGet, "/api/cameras/onvif-cam/imaging/settings", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h3, http.MethodPost, "/api/cameras/onvif-cam/onvif/reboot", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	// Unreachable endpoint: connection errors map to 502.
	dead := "http://127.0.0.1:1/onvif/device_service"
	h4, _, db4, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", dead, dead)), nil)
	api2ioSeedCamera(t, db4, "onvif-cam", "onvif", dead, dead)
	rr = api2ioDo(t, h4, http.MethodGet, "/api/cameras/onvif-cam/imaging/settings", "")
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h4, http.MethodPost, "/api/cameras/onvif-cam/onvif/reboot", "")
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h4, http.MethodGet, "/api/cameras/onvif-cam/onvif/network", "")
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h4, http.MethodGet, "/api/cameras/onvif-cam/onvif/users", "")
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())

	// A profile failure surfaces as a generic imaging error.
	_, profFailSrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{FailOps: []string{"GetProfiles"}})
	profFailEndpoint := profFailSrv.URL + "/onvif/device_service"
	h6, _, db6, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", profFailEndpoint, profFailEndpoint)), nil)
	api2ioSeedCamera(t, db6, "onvif-cam", "onvif", profFailEndpoint, profFailEndpoint)
	rr = api2ioDo(t, h6, http.MethodGet, "/api/cameras/onvif-cam/imaging/settings", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h6, http.MethodGet, "/api/cameras/onvif-cam/imaging/options", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h6, http.MethodPut, "/api/cameras/onvif-cam/imaging/settings", `{}`)
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())

	// Device exposes no media profile.
	_, noProfSrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{EmptyProfiles: true})
	noProfEndpoint := noProfSrv.URL + "/onvif/device_service"
	h5, _, db5, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", noProfEndpoint, noProfEndpoint)), nil)
	api2ioSeedCamera(t, db5, "onvif-cam", "onvif", noProfEndpoint, noProfEndpoint)
	rr = api2ioDo(t, h5, http.MethodGet, "/api/cameras/onvif-cam/imaging/settings", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h5, http.MethodGet, "/api/cameras/onvif-cam/imaging/options", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h5, http.MethodPut, "/api/cameras/onvif-cam/imaging/settings", `{}`)
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h5, http.MethodGet, "/api/cameras/onvif-cam/snapshot/uri", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
}

func TestApi2IOONVIFProbeDiscoverDetail(t *testing.T) {
	h := TestHandler(nil, nil)

	// Probe validation: undecodable body falls back to defaults, then host is required.
	rr := api2ioDo(t, h, http.MethodPost, "/api/onvif/probe", `{`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	// Probe success through an injected probe result.
	h.onvifProbeDevice = func(_ context.Context, host string, port int, _ time.Duration) (*onvif.DiscoveredDevice, error) {
		return &onvif.DiscoveredDevice{UUID: "uuid-1", Name: host, Endpoint: fmt.Sprintf("http://%s/onvif/device_service", host), XAddrs: []string{fmt.Sprintf("http://%s:%d/onvif/device_service", host, port)}}, nil
	}
	rr = api2ioDo(t, h, http.MethodPost, "/api/onvif/probe", `{"host":"192.0.2.10","port":80}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var probed struct {
		Device struct {
			UUID string `json:"uuid"`
		} `json:"device"`
	}
	parseJSON(t, rr, &probed)
	require.Equal(t, "uuid-1", probed.Device.UUID)

	// Discovery with no devices normalizes the list to an empty slice.
	h.onvifDiscover = func(_ context.Context, _ time.Duration) *onvif.DiscoveryResult {
		return &onvif.DiscoveryResult{}
	}
	rr = api2ioDo(t, h, http.MethodPost, "/api/onvif/discover", `{}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var discovered map[string]any
	parseJSON(t, rr, &discovered)
	require.NotNil(t, discovered["devices"])

	// Device detail success.
	h.onvifNewClient = func(endpoint, _, _ string) onvifDeviceClient {
		return &stubONVIFDeviceClient{
			info:     &onvif.DeviceInfo{Manufacturer: "Acme", Model: "Cam-1"},
			profiles: []onvif.DeviceProfile{{Token: "Profile_1", Name: "Main"}},
		}
	}
	rr = api2ioDo(t, h, http.MethodGet, "/api/onvif/discover/192.0.2.10", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var detail struct {
		DeviceInfo map[string]any `json:"device_info"`
		Profiles   []any          `json:"profiles"`
	}
	parseJSON(t, rr, &detail)
	require.Equal(t, "Acme", detail.DeviceInfo["manufacturer"])
	require.Len(t, detail.Profiles, 1)

	// Missing IP parameter.
	rr = api2ioDirect(t, h.handleONVIFDeviceDetail, http.MethodGet, "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	// Connect failure.
	h.onvifNewClient = func(endpoint, _, _ string) onvifDeviceClient {
		return &stubONVIFDeviceClient{connectErr: errors.New("connect failed")}
	}
	rr = api2ioDo(t, h, http.MethodGet, "/api/onvif/discover/192.0.2.10", "")
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())

	// Device-info failure.
	h.onvifNewClient = func(endpoint, _, _ string) onvifDeviceClient {
		return &stubONVIFDeviceClient{infoErr: errors.New("info failed")}
	}
	rr = api2ioDo(t, h, http.MethodGet, "/api/onvif/discover/192.0.2.10", "")
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())

	// Profile failure is tolerated: the endpoint returns device info only.
	h.onvifNewClient = func(endpoint, _, _ string) onvifDeviceClient {
		return &stubONVIFDeviceClient{
			info:        &onvif.DeviceInfo{Manufacturer: "Acme"},
			profilesErr: errors.New("profiles failed"),
		}
	}
	rr = api2ioDo(t, h, http.MethodGet, "/api/onvif/discover/192.0.2.10", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
}

func TestApi2IOONVIFSnapshotSecondCallAndCapabilityFallback(t *testing.T) {
	// GetSnapshotProvider probes the URI once; the handler call then fails.
	fake, srv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{FailAfter: map[string]int{"GetSnapshotUri": 1}})
	endpoint := srv.URL + "/onvif/device_service"
	h, _, db, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", endpoint, endpoint)), nil)
	api2ioSeedCamera(t, db, "onvif-cam", "onvif", endpoint, endpoint)
	rr := api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/snapshot/uri", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
	require.GreaterOrEqual(t, fake.callCount("GetSnapshotUri"), 2)

	// Capability faults are tolerated by the profile endpoint.
	_, faultSrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{FaultOps: map[string]string{"GetCapabilities": "Action Not Supported"}})
	faultEndpoint := faultSrv.URL + "/onvif/device_service"
	h2, _, db2, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", faultEndpoint, faultEndpoint)), nil)
	api2ioSeedCamera(t, db2, "onvif-cam", "onvif", faultEndpoint, faultEndpoint)
	rr = api2ioDo(t, h2, http.MethodGet, "/api/cameras/onvif-cam/onvif/profiles", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var profiles struct {
		Profiles     []any          `json:"profiles"`
		Capabilities map[string]any `json:"capabilities"`
	}
	parseJSON(t, rr, &profiles)
	require.Len(t, profiles.Profiles, 1)
	require.Equal(t, false, profiles.Capabilities["ptz"])
}

func TestApi2IOPTZMissingCameraAndPresetFallbacks(t *testing.T) {
	_, srv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{})
	endpoint := srv.URL + "/onvif/device_service"
	h, _, db, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("rtsp-cam", "rtsp_h264", "rtsp://192.0.2.10/live", "")), nil)
	api2ioSeedCamera(t, db, "rtsp-cam", "rtsp_h264", "rtsp://192.0.2.10/live", "")

	// Non-PTZ protocols are rejected by the unified endpoints.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/cameras/rtsp-cam/ptz/move", `{"mode":"continuous"}`},
		{http.MethodPost, "/api/cameras/rtsp-cam/ptz/stop", ""},
		{http.MethodGet, "/api/cameras/rtsp-cam/ptz/status", ""},
	} {
		rr := api2ioDo(t, h, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusBadRequest, rr.Code, "%s %s: %s", tc.method, tc.path, rr.Body.String())
	}

	// Unknown camera on every PTZ endpoint.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/cameras/missing/ptz/move", `{"mode":"continuous"}`},
		{http.MethodPost, "/api/cameras/missing/ptz/stop", ""},
		{http.MethodGet, "/api/cameras/missing/ptz/status", ""},
		{http.MethodGet, "/api/cameras/missing/ptz/presets", ""},
		{http.MethodPost, "/api/cameras/missing/ptz/presets", `{"name":"Door"}`},
		{http.MethodPost, "/api/cameras/missing/ptz/presets/pre1/goto", ""},
		{http.MethodDelete, "/api/cameras/missing/ptz/presets/pre1", ""},
	} {
		rr := api2ioDo(t, h, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusNotFound, rr.Code, "%s %s: %s", tc.method, tc.path, rr.Body.String())
	}

	// Empty preset token validation in the ONVIF preset handlers.
	nilMgr, _, db2, _ := api2ioNewHandler(t, nil, nil)
	api2ioSeedCamera(t, db2, "onvif-cam", "onvif", endpoint, endpoint)
	rr := api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/ptz/status", "")
	require.NotEqual(t, http.StatusMethodNotAllowed, rr.Code)
	gotoRec := httptest.NewRecorder()
	nilMgr.handleONVIFPTZGoToPreset(gotoRec, httptest.NewRequest(http.MethodPost, "/api/cameras/onvif-cam/ptz/presets//goto", nil), "onvif-cam")
	require.Equal(t, http.StatusBadRequest, gotoRec.Code, gotoRec.Body.String())
	deleteRec := httptest.NewRecorder()
	nilMgr.handleONVIFPTZDeletePreset(deleteRec, httptest.NewRequest(http.MethodDelete, "/api/cameras/onvif-cam/ptz/presets/", nil), "onvif-cam")
	require.Equal(t, http.StatusBadRequest, deleteRec.Code, deleteRec.Body.String())

	// The camera exists but the manager does not know it: ONVIF sub-handlers
	// surface the typed camera-not-found error.
	mgrless, _, dbMgr, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("other-cam", "onvif", endpoint, endpoint)), nil)
	api2ioSeedCamera(t, dbMgr, "onvif-cam", "onvif", endpoint, endpoint)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/cameras/onvif-cam/ptz/move", `{"mode":"continuous","pan":0.1}`},
		{http.MethodPost, "/api/cameras/onvif-cam/ptz/stop", ""},
		{http.MethodGet, "/api/cameras/onvif-cam/ptz/presets", ""},
		{http.MethodPost, "/api/cameras/onvif-cam/ptz/presets", `{"name":"Door"}`},
		{http.MethodPost, "/api/cameras/onvif-cam/ptz/presets/pre1/goto", ""},
		{http.MethodDelete, "/api/cameras/onvif-cam/ptz/presets/pre1", ""},
	} {
		rr := api2ioDo(t, mgrless, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusNotFound, rr.Code, "%s %s: %s", tc.method, tc.path, rr.Body.String())
	}

	// A database failure while resolving the camera is reported as not found.
	closed, _, dbClosed, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("rtsp-cam", "rtsp_h264", "rtsp://192.0.2.10/live", "")), nil)
	api2ioSeedCamera(t, dbClosed, "rtsp-cam", "rtsp_h264", "rtsp://192.0.2.10/live", "")
	require.NoError(t, dbClosed.Close())
	rr = api2ioDo(t, closed, http.MethodGet, "/api/cameras/rtsp-cam/ptz/status", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// Preset listing tolerates devices without preset support.
	_, faultSrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{FaultOps: map[string]string{"GetPresets": "Action Not Supported"}})
	faultEndpoint := faultSrv.URL + "/onvif/device_service"
	h2, _, db3, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", faultEndpoint, faultEndpoint)), nil)
	api2ioSeedCamera(t, db3, "onvif-cam", "onvif", faultEndpoint, faultEndpoint)
	rr = api2ioDo(t, h2, http.MethodGet, "/api/cameras/onvif-cam/ptz/presets", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var noPresets struct {
		Presets []any `json:"presets"`
	}
	parseJSON(t, rr, &noPresets)
	require.Empty(t, noPresets.Presets)

	// Empty preset payload also yields an empty list.
	_, emptySrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{EmptyPresets: true})
	emptyEndpoint := emptySrv.URL + "/onvif/device_service"
	h3, _, db4, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", emptyEndpoint, emptyEndpoint)), nil)
	api2ioSeedCamera(t, db4, "onvif-cam", "onvif", emptyEndpoint, emptyEndpoint)
	rr = api2ioDo(t, h3, http.MethodGet, "/api/cameras/onvif-cam/ptz/presets", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	parseJSON(t, rr, &noPresets)
	require.Empty(t, noPresets.Presets)
}

func TestApi2IOGB28181PTZSuccess(t *testing.T) {
	f := gb74NewFixture(t, nil)
	const deviceID = "34020000001320000001"
	const channelID = "34020000001320000002"
	f.seed(deviceID)
	h := f.handler()
	cameraID := deviceID + ":" + channelID
	api2ioSeedCamera(t, f.db, cameraID, "gb28181", "gb28181://"+cameraID, "")

	rr := api2ioDo(t, h, http.MethodPost, "/api/cameras/"+cameraID+"/ptz/move", `{"mode":"continuous","pan":0.5,"tilt":0.2}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPost, "/api/cameras/"+cameraID+"/ptz/stop", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.GreaterOrEqual(t, f.peer.seen(), 1)
}

func TestApi2IOONVIFRecordingParameterBranches(t *testing.T) {
	_, srv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{})
	endpoint := srv.URL + "/onvif/device_service"
	cfg := api2ioCfg(
		api2ioCam("onvif-cam", "onvif", endpoint, endpoint),
		// URL fallback: ONVIFEndpoint is empty so the camera URL is used.
		api2ioCam("url-cam", "onvif", endpoint, ""),
	)
	h, _, db, _ := api2ioNewHandler(t, cfg, nil)
	api2ioSeedCamera(t, db, "onvif-cam", "onvif", endpoint, endpoint)
	api2ioSeedCamera(t, db, "url-cam", "onvif", endpoint, "")

	// Missing route parameters are rejected up front.
	require.Equal(t, http.StatusBadRequest, api2ioDirect(t, h.handleGetONVIFRecordings, http.MethodGet, "").Code)
	require.Equal(t, http.StatusBadRequest, api2ioDirect(t, h.handleGetONVIFRecordingInformation, http.MethodGet, "").Code)
	require.Equal(t, http.StatusBadRequest, api2ioDirect(t, h.handleSearchONVIFRecordings, http.MethodGet, "").Code)
	require.Equal(t, http.StatusBadRequest, api2ioDirect(t, h.handleGetONVIFReplayURI, http.MethodGet, "").Code)

	// A camera ID without the recording token is rejected as well.
	infoRec := httptest.NewRecorder()
	h.handleGetONVIFRecordingInformation(infoRec, api2ioChiRequest(http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings/", map[string]string{"id": "onvif-cam"}))
	require.Equal(t, http.StatusBadRequest, infoRec.Code, infoRec.Body.String())
	replayRec := httptest.NewRecorder()
	h.handleGetONVIFReplayURI(replayRec, api2ioChiRequest(http.MethodGet, "/api/cameras/onvif-cam/onvif/replay/", map[string]string{"id": "onvif-cam"}))
	require.Equal(t, http.StatusBadRequest, replayRec.Code, replayRec.Body.String())

	// Unknown camera for the recording-information and replay endpoints.
	rr := api2ioDo(t, h, http.MethodGet, "/api/cameras/missing/onvif/recordings/rec1", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/missing/onvif/replay/rec1", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// Time strings without a timezone use the secondary parser.
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings?start_time=2020-01-01T00:00:00&end_time=2035-01-01T00:00:00", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/cameras/onvif-cam/onvif/recordings?start_time=2020-01-01&end_time=2035-01-01", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// The URL fallback camera works for every recording endpoint.
	for _, path := range []string{
		"/api/cameras/url-cam/onvif/recordings",
		"/api/cameras/url-cam/onvif/recordings/rec1",
		"/api/cameras/url-cam/onvif/recordings/search?start_time=2020-01-01T00:00:00Z",
		"/api/cameras/url-cam/onvif/replay/rec1",
	} {
		rr := api2ioDo(t, h, http.MethodGet, path, "")
		require.Equal(t, http.StatusOK, rr.Code, "%s: %s", path, rr.Body.String())
	}
}

// --- Xiaomi cloud endpoints ---

type api2ioCloud struct {
	signInResult  *CloudAuthResult
	signInVerify  *CloudVerificationRequired
	signInErr     error
	captchaResult *CloudAuthResult
	captchaVerify *CloudVerificationRequired
	captchaErr    error
	verifyResult  *CloudAuthResult
	verifyVerify  *CloudVerificationRequired
	verifyErr     error
	devices       []CloudDeviceInfo
	devicesErr    error
	vendor        string
	vendorErr     error
	setCalls      int
	lastUserID    string
}

func (c *api2ioCloud) SetCloudConfig(_ context.Context, userID, _, _ string) error {
	c.setCalls++
	c.lastUserID = userID
	return nil
}

func (c *api2ioCloud) SignIn(_ context.Context, _, _, _ string) (*CloudAuthResult, *CloudVerificationRequired, error) {
	return c.signInResult, c.signInVerify, c.signInErr
}

func (c *api2ioCloud) SubmitCaptcha(_ context.Context, _, _ string) (*CloudAuthResult, *CloudVerificationRequired, error) {
	return c.captchaResult, c.captchaVerify, c.captchaErr
}

func (c *api2ioCloud) SubmitVerify(_ context.Context, _, _ string) (*CloudAuthResult, *CloudVerificationRequired, error) {
	return c.verifyResult, c.verifyVerify, c.verifyErr
}

func (c *api2ioCloud) ListDevices(_ context.Context) ([]CloudDeviceInfo, error) {
	return c.devices, c.devicesErr
}

func (c *api2ioCloud) CheckVendor(_ context.Context, _ string) (string, error) {
	return c.vendor, c.vendorErr
}

func TestApi2IOXiaomiUnavailable(t *testing.T) {
	h, _, _, _ := api2ioNewHandler(t, nil, nil)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/xiaomi/auth", `{}`},
		{http.MethodPost, "/api/xiaomi/captcha", `{}`},
		{http.MethodPost, "/api/xiaomi/verify", `{}`},
		{http.MethodGet, "/api/xiaomi/devices", ""},
		{http.MethodPost, "/api/xiaomi/sync", ""},
		{http.MethodGet, "/api/xiaomi/talk/ws?camera_id=x", ""},
	} {
		rr := api2ioDo(t, h, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusServiceUnavailable, rr.Code, "%s %s: %s", tc.method, tc.path, rr.Body.String())
	}

	// Vendor probing degrades gracefully without the cloud bridge.
	rr := api2ioDo(t, h, http.MethodGet, "/api/xiaomi/check-vendor?did=did-1", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var vendor map[string]any
	parseJSON(t, rr, &vendor)
	require.Equal(t, "unknown", vendor["vendor"])
	require.Equal(t, true, vendor["compatible"])
}

func TestApi2IOXiaomiAuthValidation(t *testing.T) {
	cloud := &api2ioCloud{}
	cfg := &config.Config{}
	h, _, _, _ := api2ioNewHandler(t, cfg, cloud)

	for _, tc := range []struct{ path, body string }{
		{"/api/xiaomi/auth", `{`},
		{"/api/xiaomi/captcha", `{`},
		{"/api/xiaomi/verify", `{`},
		{"/api/xiaomi/auth", `{"username":"u"}`},
		{"/api/xiaomi/captcha", `{"session_id":"s"}`},
		{"/api/xiaomi/verify", `{"session_id":"s"}`},
	} {
		rr := api2ioDo(t, h, http.MethodPost, tc.path, tc.body)
		require.Equal(t, http.StatusBadRequest, rr.Code, "%s %s: %s", tc.path, tc.body, rr.Body.String())
	}

	// Upstream authentication errors map to 401.
	cloud.signInErr = errors.New("bad credentials")
	rr := api2ioDo(t, h, http.MethodPost, "/api/xiaomi/auth", `{"username":"u","password":"p"}`)
	require.Equal(t, http.StatusUnauthorized, rr.Code, rr.Body.String())
	cloud.signInErr = nil

	cloud.captchaErr = errors.New("bad captcha")
	rr = api2ioDo(t, h, http.MethodPost, "/api/xiaomi/captcha", `{"session_id":"s","captcha_code":"1234"}`)
	require.Equal(t, http.StatusUnauthorized, rr.Code, rr.Body.String())
	cloud.captchaErr = nil

	cloud.verifyErr = errors.New("bad ticket")
	rr = api2ioDo(t, h, http.MethodPost, "/api/xiaomi/verify", `{"session_id":"s","ticket":"t"}`)
	require.Equal(t, http.StatusUnauthorized, rr.Code, rr.Body.String())
	cloud.verifyErr = nil

	// Interactive verification (captcha / 2FA) returns 202 with the challenge.
	cloud.signInVerify = &CloudVerificationRequired{
		Captcha:          []byte("captcha-bytes"),
		VerifyPhone:      "13800000000",
		VerifyEmail:      "user@example.test",
		CaptchaSessionID: "session-1",
	}
	rr = api2ioDo(t, h, http.MethodPost, "/api/xiaomi/auth", `{"username":"u","password":"p","region":"sg"}`)
	require.Equal(t, http.StatusAccepted, rr.Code, rr.Body.String())
	var verification map[string]any
	parseJSON(t, rr, &verification)
	require.Equal(t, "verification_required", verification["status"])
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("captcha-bytes")), verification["captcha"])
	require.Equal(t, "13800000000", verification["verify_phone"])
	require.Equal(t, "user@example.test", verification["verify_email"])
	require.Equal(t, "session-1", verification["session_id"])
	cloud.signInVerify = nil

	cloud.captchaVerify = &CloudVerificationRequired{CaptchaSessionID: "session-2"}
	rr = api2ioDo(t, h, http.MethodPost, "/api/xiaomi/captcha", `{"session_id":"s","captcha_code":"1234"}`)
	require.Equal(t, http.StatusAccepted, rr.Code, rr.Body.String())
	cloud.captchaVerify = nil

	cloud.verifyVerify = &CloudVerificationRequired{VerifyEmail: "user@example.test"}
	rr = api2ioDo(t, h, http.MethodPost, "/api/xiaomi/verify", `{"session_id":"s","ticket":"t"}`)
	require.Equal(t, http.StatusAccepted, rr.Code, rr.Body.String())
	cloud.verifyVerify = nil

	// Successful authentication persists the token and pushes it to the bridge.
	cloud.signInResult = &CloudAuthResult{UserID: "user-1", PassToken: "pass-token", Region: "sg"}
	rr = api2ioDo(t, h, http.MethodPost, "/api/xiaomi/auth", `{"username":"u","password":"p","region":"sg"}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, "pass-token", cfg.Xiaomi.Token)
	require.Equal(t, "user-1", cfg.Xiaomi.UserID)
	require.Equal(t, "sg", cfg.Xiaomi.Region)
	require.Equal(t, 1, cloud.setCalls)
	require.Equal(t, "user-1", cloud.lastUserID)

	cloud.captchaResult = &CloudAuthResult{UserID: "user-2", PassToken: "pass-token-2", Region: "cn"}
	rr = api2ioDo(t, h, http.MethodPost, "/api/xiaomi/captcha", `{"session_id":"s","captcha_code":"1234"}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, "pass-token-2", cfg.Xiaomi.Token)

	cloud.verifyResult = &CloudAuthResult{UserID: "user-3", PassToken: "pass-token-3", Region: "de"}
	rr = api2ioDo(t, h, http.MethodPost, "/api/xiaomi/verify", `{"session_id":"s","ticket":"t"}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, "pass-token-3", cfg.Xiaomi.Token)
	require.Equal(t, 3, cloud.setCalls)
}

func TestApi2IOXiaomiDevicesAndVendor(t *testing.T) {
	cloud := &api2ioCloud{
		devices: []CloudDeviceInfo{{DID: "did-1", Name: "Lobby", Model: "CS2", MAC: "AA:BB:CC:DD:EE:FF", IsOnline: true}},
	}

	// No stored token: the device list is empty but the endpoint still succeeds.
	noToken, _, _, _ := api2ioNewHandler(t, &config.Config{}, cloud)
	rr := api2ioDo(t, noToken, http.MethodGet, "/api/xiaomi/devices", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var notAuthed struct {
		Devices []CloudDeviceInfo `json:"devices"`
		Message string            `json:"message"`
	}
	parseJSON(t, rr, &notAuthed)
	require.Empty(t, notAuthed.Devices)
	require.Equal(t, "not authenticated", notAuthed.Message)
	rr = api2ioDo(t, noToken, http.MethodGet, "/api/xiaomi/check-vendor?did=did-1", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// Authenticated device listing.
	cfg := &config.Config{Xiaomi: config.XiaomiConfig{UserID: "u", Token: "token", Region: "cn"}}
	authed, _, _, _ := api2ioNewHandler(t, cfg, cloud)
	rr = api2ioDo(t, authed, http.MethodGet, "/api/xiaomi/devices", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var listed struct {
		Devices []CloudDeviceInfo `json:"devices"`
	}
	parseJSON(t, rr, &listed)
	require.Len(t, listed.Devices, 1)
	require.Equal(t, "did-1", listed.Devices[0].DID)

	// Upstream failure.
	cloud.devicesErr = errors.New("cloud down")
	rr = api2ioDo(t, authed, http.MethodGet, "/api/xiaomi/devices", "")
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
	cloud.devicesErr = nil

	// Vendor probing.
	rr = api2ioDo(t, authed, http.MethodGet, "/api/xiaomi/check-vendor", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	cloud.vendorErr = errors.New("vendor lookup failed")
	rr = api2ioDo(t, authed, http.MethodGet, "/api/xiaomi/check-vendor?did=did-1", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var unknownVendor map[string]any
	parseJSON(t, rr, &unknownVendor)
	require.Equal(t, "unknown", unknownVendor["vendor"])
	require.Equal(t, true, unknownVendor["compatible"])
	cloud.vendorErr = nil

	cloud.vendor = "tutk"
	rr = api2ioDo(t, authed, http.MethodGet, "/api/xiaomi/check-vendor?did=did-1", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var tutk map[string]any
	parseJSON(t, rr, &tutk)
	require.Equal(t, "tutk", tutk["vendor"])
	require.Equal(t, false, tutk["compatible"])
	require.NotEmpty(t, tutk["message"])

	cloud.vendor = "cs2"
	rr = api2ioDo(t, authed, http.MethodGet, "/api/xiaomi/check-vendor?did=did-1", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var cs2 map[string]any
	parseJSON(t, rr, &cs2)
	require.Equal(t, "cs2", cs2["vendor"])
	require.Equal(t, true, cs2["compatible"])
}

func TestApi2IOXiaomiSync(t *testing.T) {
	cloud := &api2ioCloud{
		devices: []CloudDeviceInfo{
			{DID: "did-1", Name: "Lobby", Model: "CS2", MAC: "AA:BB:CC:DD:EE:01"},
			{DID: "did-2", Name: "Garden", Model: "C300", MAC: "AA:BB:CC:DD:EE:02"},
			{DID: "did-3", Name: "Unused", Model: "C400", MAC: "AA:BB:CC:DD:EE:03"},
		},
	}
	cfg := &config.Config{
		Xiaomi: config.XiaomiConfig{UserID: "u", Token: "token", Region: "cn"},
		Cameras: []config.CameraConfig{
			{ID: "xiaomi-1", Name: "Old 1", Protocol: "xiaomi", DID: "did-1", Enabled: true},
			{ID: "xiaomi-2", Name: "Old 2", Protocol: "xiaomi", URL: "xiaomi://did-2", Enabled: true},
			{ID: "rtsp-1", Name: "Wired", Protocol: "rtsp_h264", URL: "rtsp://192.0.2.10/live", Enabled: true},
		},
	}
	h, _, db, _ := api2ioNewHandler(t, cfg, cloud)
	api2ioSeedCamera(t, db, "xiaomi-1", "xiaomi", "", "")
	api2ioSeedCamera(t, db, "xiaomi-2", "xiaomi", "xiaomi://did-2", "")
	api2ioSeedCamera(t, db, "rtsp-1", "rtsp_h264", "rtsp://192.0.2.10/live", "")

	rr := api2ioDo(t, h, http.MethodPost, "/api/xiaomi/sync", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var synced struct {
		Synced int `json:"synced"`
		Total  int `json:"total"`
	}
	parseJSON(t, rr, &synced)
	require.Equal(t, 2, synced.Synced)
	require.Equal(t, 3, synced.Total)

	// Missing token.
	noTokenCfg := &config.Config{Cameras: []config.CameraConfig{{ID: "xiaomi-1", Protocol: "xiaomi", DID: "did-1"}}}
	noToken, _, _, _ := api2ioNewHandler(t, noTokenCfg, cloud)
	rr = api2ioDo(t, noToken, http.MethodPost, "/api/xiaomi/sync", "")
	require.Equal(t, http.StatusUnauthorized, rr.Code, rr.Body.String())

	// Missing camera manager.
	db2, store2 := setupTestDB(t)
	noMgr := NewHandler(db2, store2, noopAuthMW(), cfg, nil, "", nil, cloud)
	rr = api2ioDo(t, noMgr, http.MethodPost, "/api/xiaomi/sync", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())

	// Upstream device listing failure.
	cloud.devicesErr = errors.New("cloud down")
	rr = api2ioDo(t, h, http.MethodPost, "/api/xiaomi/sync", "")
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
}

func TestApi2IOXiaomiTalkWSValidation(t *testing.T) {
	cfg := &config.Config{Cameras: []config.CameraConfig{
		{ID: "rtsp-1", Name: "Wired", Protocol: "rtsp_h264", URL: "rtsp://192.0.2.10/live"},
		{ID: "xiaomi-nodid", Name: "No DID", Protocol: "xiaomi"},
	}}
	cloud := &api2ioCloud{}
	h, _, _, _ := api2ioNewHandler(t, cfg, cloud)

	rr := api2ioDo(t, h, http.MethodGet, "/api/xiaomi/talk/ws", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/xiaomi/talk/ws?camera_id=unknown", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/xiaomi/talk/ws?camera_id=rtsp-1", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/xiaomi/talk/ws?camera_id=xiaomi-nodid", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
}

func TestApi2IOXiaomiSyncEdgeCases(t *testing.T) {
	cloud := &api2ioCloud{devices: []CloudDeviceInfo{{DID: "did-1", Name: "Lobby", Model: "CS2"}}}
	cfg := &config.Config{
		Xiaomi: config.XiaomiConfig{UserID: "u", Token: "token", Region: "cn"},
		Cameras: []config.CameraConfig{
			// No DID and no xiaomi:// URL: nothing to sync.
			{ID: "xiaomi-no-did", Name: "No DID", Protocol: "xiaomi"},
			// DID unknown to the cloud account.
			{ID: "xiaomi-unknown", Name: "Unknown", Protocol: "xiaomi", DID: "did-unknown"},
			// Known DID: gets updated.
			{ID: "xiaomi-ok", Name: "Old", Protocol: "xiaomi", DID: "did-1", Enabled: true},
		},
	}
	h, _, db, _ := api2ioNewHandler(t, cfg, cloud)
	api2ioSeedCamera(t, db, "xiaomi-no-did", "xiaomi", "", "")
	api2ioSeedCamera(t, db, "xiaomi-unknown", "xiaomi", "", "")
	api2ioSeedCamera(t, db, "xiaomi-ok", "xiaomi", "xiaomi://did-1", "")

	rr := api2ioDo(t, h, http.MethodPost, "/api/xiaomi/sync", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var result struct {
		Synced int `json:"synced"`
		Total  int `json:"total"`
	}
	parseJSON(t, rr, &result)
	require.Equal(t, 1, result.Synced)
	require.Equal(t, 1, result.Total)

	// A camera manager built from a different config cannot update the camera.
	db2, store2 := setupTestDB(t)
	mismatched := camera.NewCameraManager(api2ioCfg(), store2, db2, "")
	syncHandler := NewHandler(db2, store2, noopAuthMW(), cfg, mismatched, "", nil, cloud)
	rr = api2ioDo(t, syncHandler, http.MethodPost, "/api/xiaomi/sync", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	parseJSON(t, rr, &result)
	require.Equal(t, 0, result.Synced)
}

func TestApi2IOXiaomiAuthWithoutConfig(t *testing.T) {
	cloud := &api2ioCloud{signInResult: &CloudAuthResult{UserID: "user-1", PassToken: "token-1"}}
	h, _, _, _ := api2ioNewHandler(t, nil, cloud)
	rr := api2ioDo(t, h, http.MethodPost, "/api/xiaomi/auth", `{"username":"u","password":"p"}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
}

// --- IPTV endpoints ---

func api2ioIPTVHandler(t *testing.T, withService bool) (*Handler, *storage.DB, *iptv.Service) {
	t.Helper()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	var svc *iptv.Service
	if withService {
		svc = iptv.NewService(db, nil)
		h.SetIPTVService(svc)
	}
	return h, db, svc
}

func api2ioSeedIPTVSource(t *testing.T, db *storage.DB, id, name, playlistURL string, enabled bool) {
	t.Helper()
	require.NoError(t, db.InsertIPTVSource(context.Background(), storage.IPTVSource{
		ID:                 id,
		Name:               name,
		PlaylistURL:        playlistURL,
		Enabled:            enabled,
		RefreshIntervalSec: 21600,
	}))
}

func api2ioSeedIPTVChannel(t *testing.T, db *storage.DB, ch storage.IPTVChannel) {
	t.Helper()
	require.NoError(t, db.UpsertIPTVChannel(context.Background(), ch))
}

func TestApi2IOIPTVModuleUnavailable(t *testing.T) {
	h, _, _ := api2ioIPTVHandler(t, false)
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/api/iptv/imports", `{}`},
		{http.MethodGet, "/api/iptv/imports/job-1", ""},
		{http.MethodGet, "/api/iptv/imports/job-1/items", ""},
		{http.MethodPost, "/api/iptv/imports/job-1/items/item-1/probe", ""},
		{http.MethodPost, "/api/iptv/imports/job-1/commit", `{}`},
		{http.MethodGet, "/api/iptv/sources", ""},
		{http.MethodPost, "/api/iptv/sources/src-1/refresh", ""},
		{http.MethodPut, "/api/iptv/sources/src-1", `{}`},
		{http.MethodDelete, "/api/iptv/sources/src-1", ""},
		{http.MethodGet, "/api/iptv/groups", ""},
		{http.MethodGet, "/api/iptv/channels", ""},
		{http.MethodGet, "/api/iptv/channels/ch-1", ""},
		{http.MethodPost, "/api/iptv/channels/ch-1/check", ""},
		{http.MethodGet, "/api/iptv/channels/ch-1/playback", ""},
		{http.MethodGet, "/api/iptv/channels/ch-1/hls", ""},
		{http.MethodPut, "/api/iptv/channels/ch-1", `{}`},
		{http.MethodDelete, "/api/iptv/channels/ch-1", ""},
	}
	for _, tc := range cases {
		rr := api2ioDo(t, h, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusServiceUnavailable, rr.Code, "%s %s: %s", tc.method, tc.path, rr.Body.String())
	}
}

func TestApi2IOIPTVImportValidationAndFlow(t *testing.T) {
	h, db, _ := api2ioIPTVHandler(t, true)
	ctx := context.Background()

	// Body validation.
	rr := api2ioDo(t, h, http.MethodPost, "/api/iptv/imports", `{`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPost, "/api/iptv/imports", `{"playlist_url":"http://127.0.0.1/list.m3u"}`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPost, "/api/iptv/imports", `{"playlist_url":""}`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	// Inline playlist text without any channel is rejected without network access.
	rr = api2ioDo(t, h, http.MethodPost, "/api/iptv/imports", `{"playlist_text":"#EXTM3U\n"}`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	// Seed an import job plus one playable item.
	require.NoError(t, db.InsertIPTVImportJob(ctx, storage.IPTVImportJob{
		ID: "job-1", Name: "Demo", Status: "ready", TotalItems: 1,
	}))
	require.NoError(t, db.InsertIPTVImportItems(ctx, []storage.IPTVImportItem{{
		ID: "item-1", JobID: "job-1", RowNo: 1, ExternalID: "ext-1", Name: "News",
		GroupName: "News", SourceURL: "http://127.0.0.1/live/index.m3u8", Status: "playable",
		Playable: true, Recordable: true,
	}}))

	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/imports/job-1", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var job struct {
		ID         string `json:"id"`
		TotalItems int    `json:"total_items"`
	}
	parseJSON(t, rr, &job)
	require.Equal(t, "job-1", job.ID)
	require.Equal(t, 1, job.TotalItems)

	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/imports/missing", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/imports/job-1/items?status=playable&q=news", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var items struct {
		Total int `json:"total"`
	}
	parseJSON(t, rr, &items)
	require.Equal(t, 1, items.Total)

	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/imports/missing/items", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// Probing a blocked (loopback) URL fails fast without touching the network.
	// A dedicated job is used so the commit below still sees a playable item.
	require.NoError(t, db.InsertIPTVImportJob(ctx, storage.IPTVImportJob{ID: "job-3", Name: "Probe", Status: "ready", TotalItems: 1}))
	require.NoError(t, db.InsertIPTVImportItems(ctx, []storage.IPTVImportItem{{
		ID: "item-3", JobID: "job-3", RowNo: 1, ExternalID: "ext-3", Name: "Probe Me",
		SourceURL: "http://127.0.0.1/live/probe.m3u8", Status: "playable", Playable: true,
	}}))
	rr = api2ioDo(t, h, http.MethodPost, "/api/iptv/imports/job-3/items/item-3/probe", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var probed struct {
		Status string `json:"status"`
	}
	parseJSON(t, rr, &probed)
	require.NotEmpty(t, probed.Status)

	rr = api2ioDo(t, h, http.MethodPost, "/api/iptv/imports/job-3/items/missing/probe", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// Commit validation.
	rr = api2ioDo(t, h, http.MethodPost, "/api/iptv/imports/job-1/commit", `{`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPost, "/api/iptv/imports/job-1/commit", `{"item_ids":[]}`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPost, "/api/iptv/imports/missing/commit", `{"item_ids":[]}`)
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// A playable item commits to a source and one channel.
	rr = api2ioDo(t, h, http.MethodPost, "/api/iptv/imports/job-1/commit", `{"item_ids":["item-1"]}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var committed struct {
		Source struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"source"`
		ChannelCount int `json:"channel_count"`
	}
	parseJSON(t, rr, &committed)
	require.NotEmpty(t, committed.Source.ID)
	require.Equal(t, 1, committed.ChannelCount)

	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/channels", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var channels struct {
		Total int `json:"total"`
	}
	parseJSON(t, rr, &channels)
	require.Equal(t, 1, channels.Total)

	// A job whose only selected item was not accepted cannot be committed.
	require.NoError(t, db.InsertIPTVImportJob(ctx, storage.IPTVImportJob{ID: "job-2", Name: "Empty", Status: "ready"}))
	require.NoError(t, db.InsertIPTVImportItems(ctx, []storage.IPTVImportItem{{
		ID: "item-2", JobID: "job-2", RowNo: 1, ExternalID: "ext-2", Name: "Broken",
		SourceURL: "http://127.0.0.1/broken.m3u8", Status: "failed",
	}}))
	rr = api2ioDo(t, h, http.MethodPost, "/api/iptv/imports/job-2/commit", `{"item_ids":["item-2"]}`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
}

func TestApi2IOIPTVSourceLifecycle(t *testing.T) {
	h, db, _ := api2ioIPTVHandler(t, true)
	api2ioSeedIPTVSource(t, db, "src-1", "Demo", "", true)
	api2ioSeedIPTVSource(t, db, "src-2", "Backup", "https://cdn.example.test/list.m3u", false)

	rr := api2ioDo(t, h, http.MethodGet, "/api/iptv/sources", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var sources struct {
		Sources []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"sources"`
	}
	parseJSON(t, rr, &sources)
	require.Len(t, sources.Sources, 2)

	// Invalid body.
	rr = api2ioDo(t, h, http.MethodPut, "/api/iptv/sources/src-1", `{`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	// Invalid refresh interval.
	rr = api2ioDo(t, h, http.MethodPut, "/api/iptv/sources/src-1", `{"refresh_interval_sec":60}`)
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())

	// Missing source.
	rr = api2ioDo(t, h, http.MethodPut, "/api/iptv/sources/missing", `{}`)
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// Successful update.
	rr = api2ioDo(t, h, http.MethodPut, "/api/iptv/sources/src-2", `{"name":"Renamed","enabled":true,"refresh_interval_sec":7200}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var updated struct {
		Name               string `json:"name"`
		RefreshIntervalSec int    `json:"refresh_interval_sec"`
		PlaylistURL        string `json:"playlist_url"`
	}
	parseJSON(t, rr, &updated)
	require.Equal(t, "Renamed", updated.Name)
	require.Equal(t, 7200, updated.RefreshIntervalSec)
	require.NotContains(t, updated.PlaylistURL, "cdn.example.test")

	// Refresh without a playlist URL.
	rr = api2ioDo(t, h, http.MethodPost, "/api/iptv/sources/src-1/refresh", "")
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPost, "/api/iptv/sources/missing/refresh", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// Delete.
	rr = api2ioDo(t, h, http.MethodDelete, "/api/iptv/sources/src-1", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodDelete, "/api/iptv/sources/src-1", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
}

func TestApi2IOIPTVChannelLifecycle(t *testing.T) {
	h, db, _ := api2ioIPTVHandler(t, true)
	api2ioSeedIPTVSource(t, db, "src-1", "Demo", "", true)
	api2ioSeedIPTVSource(t, db, "src-2", "Disabled", "", false)
	api2ioSeedIPTVChannel(t, db, storage.IPTVChannel{
		ID: "ch-1", SourceID: "src-1", ExternalID: "ext-1", StreamID: "iptv_ext_1", Name: "News",
		GroupName: "News", SourceURL: "http://127.0.0.1/live/index.m3u8", Enabled: true, Favorite: true,
	})
	api2ioSeedIPTVChannel(t, db, storage.IPTVChannel{
		ID: "ch-2", SourceID: "src-1", ExternalID: "ext-2", StreamID: "iptv_ext_2", Name: "Sports",
		GroupName: "Sports", SourceURL: "http://127.0.0.1/live/sports.m3u8", Enabled: false,
	})
	api2ioSeedIPTVChannel(t, db, storage.IPTVChannel{
		ID: "ch-3", SourceID: "src-2", ExternalID: "ext-3", StreamID: "iptv_ext_3", Name: "Offline",
		GroupName: "News", SourceURL: "http://127.0.0.1/live/offline.m3u8", Enabled: true,
		Recordable: true,
	})

	rr := api2ioDo(t, h, http.MethodGet, "/api/iptv/channels", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var all struct {
		Total int `json:"total"`
	}
	parseJSON(t, rr, &all)
	require.Equal(t, 3, all.Total)

	for _, query := range []string{"?favorite=true", "?favorite=1", "?favorite=false", "?favorite=0"} {
		rr := api2ioDo(t, h, http.MethodGet, "/api/iptv/channels"+query, "")
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	}
	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/channels?source_id=src-1&group=News&q=New", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/groups", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var groups struct {
		Groups []string `json:"groups"`
	}
	parseJSON(t, rr, &groups)
	require.Contains(t, groups.Groups, "News")

	// Single channel.
	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/channels/ch-1", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var channel struct {
		ID        string `json:"id"`
		SourceURL string `json:"source_url"`
	}
	parseJSON(t, rr, &channel)
	require.Equal(t, "ch-1", channel.ID)
	require.NotContains(t, rr.Body.String(), "127.0.0.1")
	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/channels/missing", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// A channel referencing a missing source is reported as not found.
	api2ioSeedIPTVChannel(t, db, storage.IPTVChannel{
		ID: "ch-orphan", SourceID: "missing-src", ExternalID: "ext-orphan", StreamID: "iptv_ext_orphan",
		Name: "Orphan", SourceURL: "http://127.0.0.1/live/orphan.m3u8", Enabled: true,
	})
	rr = api2ioDo(t, h, http.MethodPost, "/api/iptv/channels/ch-orphan/check", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/channels/ch-orphan/playback", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())

	// Health check (loopback URL fails fast, still a successful API call).
	rr = api2ioDo(t, h, http.MethodPost, "/api/iptv/channels/ch-1/check", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPost, "/api/iptv/channels/missing/check", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// Playback details.
	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/channels/ch-1/playback", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/channels/ch-2/playback", "")
	require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/channels/ch-3/playback", "")
	require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/channels/missing/playback", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// Update channel: body validation, missing channel, publishing constraints, success.
	rr = api2ioDo(t, h, http.MethodPut, "/api/iptv/channels/ch-1", `{`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPut, "/api/iptv/channels/missing", `{}`)
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodPut, "/api/iptv/channels/ch-2", `{"publish_enabled":true}`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	// Publishable channels need the embedded HLS puller, which is absent here.
	rr = api2ioDo(t, h, http.MethodPut, "/api/iptv/channels/ch-3", `{"publish_enabled":true,"enabled":true}`)
	require.Equal(t, http.StatusServiceUnavailable, rr.Code, rr.Body.String())

	rr = api2ioDo(t, h, http.MethodPut, "/api/iptv/channels/ch-1", `{"name":"Live News","enabled":true,"favorite":true}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var updated struct {
		Name     string `json:"name"`
		Favorite bool   `json:"favorite"`
	}
	parseJSON(t, rr, &updated)
	require.Equal(t, "Live News", updated.Name)
	require.True(t, updated.Favorite)

	// Delete channels.
	rr = api2ioDo(t, h, http.MethodDelete, "/api/iptv/channels/ch-2", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodDelete, "/api/iptv/channels/ch-1", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
}

func TestApi2IOIPTVHLSProxyBranches(t *testing.T) {
	h, db, _ := api2ioIPTVHandler(t, true)
	api2ioSeedIPTVSource(t, db, "src-1", "Demo", "", true)
	api2ioSeedIPTVSource(t, db, "src-off", "Disabled", "", false)
	api2ioSeedIPTVChannel(t, db, storage.IPTVChannel{
		ID: "ch-1", SourceID: "src-1", ExternalID: "ext-1", StreamID: "iptv_ext_1", Name: "News",
		SourceURL: "http://127.0.0.1/live/index.m3u8", Enabled: true,
	})
	api2ioSeedIPTVChannel(t, db, storage.IPTVChannel{
		ID: "ch-off", SourceID: "src-1", ExternalID: "ext-off", StreamID: "iptv_ext_off", Name: "Off",
		SourceURL: "http://127.0.0.1/live/off.m3u8", Enabled: false,
	})
	api2ioSeedIPTVChannel(t, db, storage.IPTVChannel{
		ID: "ch-src-off", SourceID: "src-off", ExternalID: "ext-src-off", StreamID: "iptv_ext_src_off",
		Name: "SrcOff", SourceURL: "http://127.0.0.1/live/src_off.m3u8", Enabled: true,
	})

	// Unknown channel -> not found.
	rr := api2ioDo(t, h, http.MethodGet, "/api/iptv/channels/missing/hls", "")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// Disabled channel / disabled source -> forbidden.
	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/channels/ch-off/hls", "")
	require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/channels/ch-src-off/hls", "")
	require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())

	// Blocked target (loopback) is rejected without any outbound request.
	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/channels/ch-1/hls", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/channels/ch-1/hls?url=http://127.0.0.1/evil.m3u8", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	rr = api2ioDo(t, h, http.MethodGet, "/api/iptv/channels/ch-1/hls?url=file:///etc/passwd", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
}

func TestApi2IOIPTVStorageFailures(t *testing.T) {
	h, db, _ := api2ioIPTVHandler(t, true)
	require.NoError(t, db.Close())

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/iptv/imports/job-1", ""},
		{http.MethodGet, "/api/iptv/imports/job-1/items", ""},
		{http.MethodGet, "/api/iptv/sources", ""},
		{http.MethodGet, "/api/iptv/groups", ""},
		{http.MethodGet, "/api/iptv/channels", ""},
		{http.MethodGet, "/api/iptv/channels/ch-1", ""},
		{http.MethodGet, "/api/iptv/channels/ch-1/playback", ""},
		{http.MethodDelete, "/api/iptv/channels/ch-1", ""},
		{http.MethodDelete, "/api/iptv/sources/src-1", ""},
	} {
		rr := api2ioDo(t, h, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusInternalServerError, rr.Code, "%s %s: %s", tc.method, tc.path, rr.Body.String())
	}

	// Error paths routed through writeIPTVError.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/iptv/imports/job-1/items/item-1/probe", ""},
		{http.MethodPost, "/api/iptv/imports/job-1/commit", `{"item_ids":["item-1"]}`},
		{http.MethodPost, "/api/iptv/sources/src-1/refresh", ""},
	} {
		rr := api2ioDo(t, h, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusBadGateway, rr.Code, "%s %s: %s", tc.method, tc.path, rr.Body.String())
	}
	rr := api2ioDo(t, h, http.MethodPut, "/api/iptv/channels/ch-1", `{}`)
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

	// Source updates surface storage failures as 500.
	rr = api2ioDo(t, h, http.MethodPut, "/api/iptv/sources/src-1", `{}`)
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
}
