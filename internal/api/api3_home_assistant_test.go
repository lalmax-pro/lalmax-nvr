package api

// Tests for the Home Assistant integration handlers: settings, publication,
// catalog, snapshot, PTZ (ONVIF + GB28181) and transcoded variants.
//
// All helpers and types carry the api3 prefix so they cannot collide with the
// other test files in this package.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
	"github.com/stretchr/testify/require"
)

// api3HAEngine is a media engine stub whose play URL and stream listing are
// controlled per test.
type api3HAEngine struct {
	*stubMediaEngine
	playFn    func(media.PlayURLRequest) (*media.PlayURL, error)
	buildErr  error
	streamsIn []media.StreamInfo
	listFn    func() ([]media.StreamInfo, error)
}

func (e *api3HAEngine) BuildPlayURL(_ context.Context, req media.PlayURLRequest) (*media.PlayURL, error) {
	if e.playFn != nil {
		return e.playFn(req)
	}
	if e.buildErr != nil {
		return nil, e.buildErr
	}
	u := url.URL{Scheme: "rtsp", Host: "127.0.0.1:15544", Path: "/" + req.AppName + "/" + req.StreamID}
	return &media.PlayURL{URL: u.String(), Protocol: req.Protocol}, nil
}

func (e *api3HAEngine) ListStreams(context.Context) ([]media.StreamInfo, error) {
	if e.listFn != nil {
		return e.listFn()
	}
	return e.streamsIn, nil
}

func api3HAEngineFor(streams ...media.StreamInfo) *api3HAEngine {
	byID := make(map[string]*media.StreamInfo, len(streams))
	for i := range streams {
		byID[streams[i].StreamID] = &streams[i]
	}
	return &api3HAEngine{stubMediaEngine: &stubMediaEngine{streamsByID: byID, streams: streams}, streamsIn: streams}
}

// api3HARepo injects storage failures for Home Assistant related methods.
type api3HARepo struct {
	storage.APIRepository
	fail map[string]error
}

func api3HAFailing(base storage.APIRepository, methods ...string) *api3HARepo {
	fail := make(map[string]error, len(methods))
	for _, m := range methods {
		fail[m] = fmt.Errorf("api3 injected %s failure", m)
	}
	return &api3HARepo{APIRepository: base, fail: fail}
}

func (r *api3HARepo) boom(name string) error { return r.fail[name] }

func (r *api3HARepo) GetHomeAssistantSettings(ctx context.Context) (storage.HomeAssistantSettings, error) {
	if err := r.boom("GetHomeAssistantSettings"); err != nil {
		return storage.HomeAssistantSettings{}, err
	}
	return r.APIRepository.GetHomeAssistantSettings(ctx)
}

func (r *api3HARepo) SaveHomeAssistantSettings(ctx context.Context, cfg storage.HomeAssistantSettings) error {
	if err := r.boom("SaveHomeAssistantSettings"); err != nil {
		return err
	}
	return r.APIRepository.SaveHomeAssistantSettings(ctx, cfg)
}

func (r *api3HARepo) ListCameras(ctx context.Context) ([]storage.CameraRow, error) {
	if err := r.boom("ListCameras"); err != nil {
		return nil, err
	}
	return r.APIRepository.ListCameras(ctx)
}

func (r *api3HARepo) ListStreamBindings(ctx context.Context) ([]storage.StreamBinding, error) {
	if err := r.boom("ListStreamBindings"); err != nil {
		return nil, err
	}
	return r.APIRepository.ListStreamBindings(ctx)
}

func (r *api3HARepo) ListCreatedStreams(ctx context.Context) ([]storage.CreatedStream, error) {
	if err := r.boom("ListCreatedStreams"); err != nil {
		return nil, err
	}
	return r.APIRepository.ListCreatedStreams(ctx)
}

// api3HAPublish enables Home Assistant and publishes the given stream IDs.
func api3HAPublish(t *testing.T, db *storage.DB, baseURL string, pubs ...storage.HomeAssistantPublication) {
	t.Helper()
	cfg, err := db.GetHomeAssistantSettings(context.Background())
	require.NoError(t, err)
	cfg.Enabled = true
	cfg.RTSPBaseURL = baseURL
	cfg.Streams = pubs
	require.NoError(t, db.SaveHomeAssistantSettings(context.Background(), cfg))
}

func api3HAHandler(t *testing.T, base storage.APIRepository, engine media.Engine) *Handler {
	t.Helper()
	_, store := setupTestDB(t)
	h := NewHandler(base, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)
	return h
}

func TestApi3HomeAssistantSettingsAndPublication(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "cam-1", "Front Door", "onvif", "h264", "rtsp://192.0.2.10/live", "", "", true, "http://192.0.2.10/onvif", "", ""))
	require.NoError(t, db.BindStreamToCamera(ctx, "push-1", "cam-1"))
	engine := api3HAEngineFor(
		media.StreamInfo{StreamID: "push-1", AppName: "custom", Active: true, VideoCodec: "H264"},
		media.StreamInfo{StreamID: "push-2", AppName: "live", Active: true},
	)
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)
	router := h.Routes()

	get := func() *httptest.ResponseRecorder {
		t.Helper()
		return doRequest(t, router, http.MethodGet, "/api/settings/home-assistant", nil, "", "")
	}
	put := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		return doRequest(t, router, http.MethodPut, "/api/settings/home-assistant", strings.NewReader(body), "", "")
	}
	publish := func(streamID, body string) *httptest.ResponseRecorder {
		t.Helper()
		return doRequest(t, router, http.MethodPut, "/api/integrations/home-assistant/streams/"+streamID, strings.NewReader(body), "", "")
	}

	api3Status(t, get(), http.StatusOK)

	// Malformed bodies: not an object, unknown fields, trailing content.
	for _, body := range []string{
		`null`,
		`[]`,
		`{"unknown_field":true}`,
		`{"enabled":true}{"enabled":false}`,
	} {
		api3Status(t, put(body), http.StatusBadRequest)
	}
	// Base URL validation.
	for _, body := range []string{
		`{"rtsp_base_url":"http://nvr.lan"}`,
		`{"rtsp_base_url":"rtsp://user:pass@nvr.lan:554"}`,
		`{"rtsp_base_url":"rtsp://nvr.lan:554/live"}`,
		`{"rtsp_base_url":"rtsp://nvr.lan:554?x=1"}`,
	} {
		api3Status(t, put(body), http.StatusBadRequest)
	}
	// Publication validation inside the settings update.
	for _, body := range []string{
		`{"streams":[{"stream_id":""}]}`,
		`{"streams":[{"stream_id":"a/b"}]}`,
		`{"streams":[{"stream_id":"a?b"}]}`,
		`{"streams":[{"stream_id":"dup"},{"stream_id":"dup"}]}`,
		`{"streams":[{"stream_id":"big","name":"` + strings.Repeat("n", 129) + `"}]}`,
		`{"streams":[{"stream_id":"tc","transcode_job_id":"job-1"}]}`,
	} {
		api3Status(t, put(body), http.StatusBadRequest)
	}

	// A brand new publication requires a media engine and a known stream.
	noEngine := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	api3Status(t, doRequest(t, noEngine.Routes(), http.MethodPut, "/api/settings/home-assistant",
		strings.NewReader(`{"streams":[{"stream_id":"push-1"}]}`), "", ""), http.StatusServiceUnavailable)

	// Stream lookup failure and unknown stream.
	lookupFail := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	lookupFail.SetMediaEngine(&api3HAEngine{stubMediaEngine: &stubMediaEngine{getErr: fmt.Errorf("engine down")}})
	api3Status(t, doRequest(t, lookupFail.Routes(), http.MethodPut, "/api/settings/home-assistant",
		strings.NewReader(`{"streams":[{"stream_id":"push-1"}]}`), "", ""), http.StatusServiceUnavailable)
	api3Status(t, doRequest(t, lookupFail.Routes(), http.MethodPut, "/api/integrations/home-assistant/streams/push-1",
		strings.NewReader(`{"enabled":true}`), "", ""), http.StatusServiceUnavailable)

	unknown := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	unknown.SetMediaEngine(api3HAEngineFor())
	api3Status(t, doRequest(t, unknown.Routes(), http.MethodPut, "/api/settings/home-assistant",
		strings.NewReader(`{"streams":[{"stream_id":"ghost"}]}`), "", ""), http.StatusBadRequest)
	api3Status(t, doRequest(t, unknown.Routes(), http.MethodPut, "/api/integrations/home-assistant/streams/ghost",
		strings.NewReader(`{"enabled":true}`), "", ""), http.StatusNotFound)
	api3Status(t, doRequest(t, noEngine.Routes(), http.MethodPut, "/api/integrations/home-assistant/streams/push-1",
		strings.NewReader(`{"enabled":true}`), "", ""), http.StatusServiceUnavailable)

	// Successful settings update adopts the engine-provided application name.
	res := put(`{"enabled":true,"rtsp_base_url":"rtsp://nvr.lan:25444","streams":[{"stream_id":"push-1","app_name":"untrusted"}]}`)
	api3Status(t, res, http.StatusOK)
	var settings storage.HomeAssistantSettings
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &settings))
	require.Equal(t, "custom", settings.Streams[0].AppName)
	require.True(t, settings.Enabled)

	// Publication endpoint: malformed / missing enabled flag.
	api3Status(t, publish("push-1", `{}`), http.StatusBadRequest)
	api3Status(t, publish("push-1", `{"enabled":"yes"}`), http.StatusBadRequest)

	// Existing publication keeps its captured application name.
	api3Status(t, publish("push-2", `{"enabled":true}`), http.StatusOK)
	api3Status(t, publish("push-2", `{"enabled":false}`), http.StatusOK)
	// Disabling a publication that does not exist is a no-op.
	api3Status(t, publish("push-9", `{"enabled":false}`), http.StatusOK)

	// Storage failures surface as server errors.
	failingGet := api3HAFailing(db, "GetHomeAssistantSettings")
	hErr := api3HAHandler(t, failingGet, engine)
	api3Status(t, doRequest(t, hErr.Routes(), http.MethodGet, "/api/settings/home-assistant", nil, "", ""), http.StatusInternalServerError)
	api3Status(t, doRequest(t, hErr.Routes(), http.MethodPut, "/api/settings/home-assistant",
		strings.NewReader(`{"enabled":true}`), "", ""), http.StatusInternalServerError)
	api3Status(t, doRequest(t, hErr.Routes(), http.MethodPut, "/api/integrations/home-assistant/streams/push-1",
		strings.NewReader(`{"enabled":true}`), "", ""), http.StatusInternalServerError)

	failingSave := api3HAFailing(db, "SaveHomeAssistantSettings")
	hSaveErr := api3HAHandler(t, failingSave, engine)
	api3Status(t, doRequest(t, hSaveErr.Routes(), http.MethodPut, "/api/settings/home-assistant",
		strings.NewReader(`{"enabled":true}`), "", ""), http.StatusInternalServerError)
	api3Status(t, doRequest(t, hSaveErr.Routes(), http.MethodPut, "/api/integrations/home-assistant/streams/push-1",
		strings.NewReader(`{"enabled":true}`), "", ""), http.StatusInternalServerError)
}

func TestApi3HomeAssistantCatalog(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "cam-1", "Front Door", "onvif", "h264", "rtsp://192.0.2.10/live", "", "", true, "http://192.0.2.10/onvif", "", ""))
	require.NoError(t, db.BindStreamToCamera(ctx, "push-1", "cam-1"))
	require.NoError(t, db.InsertCreatedStream(ctx, storage.CreatedStream{StreamID: "created-1", Name: "Created Stream"}))

	engine := api3HAEngineFor(media.StreamInfo{StreamID: "push-1", AppName: "custom", Active: true, VideoCodec: "H264"})
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)
	router := h.Routes()

	catalog := func() homeAssistantCatalog {
		t.Helper()
		rr := doRequest(t, router, http.MethodGet, "/api/integrations/home-assistant", nil, "", "")
		api3Status(t, rr, http.StatusOK)
		var out homeAssistantCatalog
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
		return out
	}

	// Disabled integration returns an empty catalog.
	require.Empty(t, catalog().Streams)

	// Enabled, but without a media engine the catalog fails closed.
	api3HAPublish(t, db, "", storage.HomeAssistantPublication{StreamID: "push-1", AppName: "live"})
	noEngine := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	api3Status(t, doRequest(t, noEngine.Routes(), http.MethodGet, "/api/integrations/home-assistant", nil, "", ""), http.StatusServiceUnavailable)

	// Stream listing failure.
	listFail := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	listFail.SetMediaEngine(&api3HAEngine{stubMediaEngine: &stubMediaEngine{}, listFn: func() ([]media.StreamInfo, error) {
		return nil, fmt.Errorf("listing down")
	}})
	api3Status(t, doRequest(t, listFail.Routes(), http.MethodGet, "/api/integrations/home-assistant", nil, "", ""), http.StatusServiceUnavailable)

	// Catalog with names resolved in several ways.
	api3HAPublish(t, db, "",
		storage.HomeAssistantPublication{StreamID: "push-1", AppName: "live"},
		storage.HomeAssistantPublication{StreamID: "created-1", AppName: "live"},
		storage.HomeAssistantPublication{StreamID: "orphan", AppName: "live"},
		storage.HomeAssistantPublication{StreamID: "tc-1", AppName: "live", TranscodeJobID: "job-1"},
		storage.HomeAssistantPublication{StreamID: "push-1", AppName: "live", Name: "Custom Label"},
	)
	got := catalog()
	require.Len(t, got.Streams, 5)
	names := map[string]string{}
	reasons := map[string]string{}
	for _, stream := range got.Streams {
		names[stream.StreamID] = stream.Name
		reasons[stream.StreamID] = stream.UnavailableReason
	}
	require.Equal(t, "Custom Label", names["push-1"])
	require.Equal(t, "Created Stream", names["created-1"])
	require.Equal(t, "orphan", names["orphan"])
	// A transcode-selected publication without a runtime is reported offline.
	require.Equal(t, "rtsp_unavailable", reasons["tc-1"])

	// Camera/binding/created-stream lookups failing must not leak partial data.
	for _, method := range []string{"ListCameras", "ListStreamBindings", "ListCreatedStreams"} {
		repo := api3HAFailing(db, method)
		hErr := NewHandler(repo, store, noopAuthMW(), nil, nil, "", nil, nil)
		hErr.SetMediaEngine(engine)
		api3Status(t, doRequest(t, hErr.Routes(), http.MethodGet, "/api/integrations/home-assistant", nil, "", ""), http.StatusInternalServerError)
	}

	// RTSP settings from config: a configured playback token must exist per stream.
	cfgWithToken := &config.Config{Auth: config.AuthConfig{PlaybackTokens: map[string]string{"other": "secret"}}}
	tokenHandler := NewHandler(db, store, noopAuthMW(), cfgWithToken, nil, "", nil, nil)
	tokenHandler.SetMediaEngine(engine)
	rr := doRequest(t, tokenHandler.Routes(), http.MethodGet, "/api/integrations/home-assistant", nil, "", "")
	api3Status(t, rr, http.StatusOK)
	var tokenCatalog homeAssistantCatalog
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &tokenCatalog))
	for _, stream := range tokenCatalog.Streams {
		require.Equal(t, "rtsp_unavailable", stream.UnavailableReason, stream.StreamID)
	}

	// Token attached to the playback URL when configured for the stream.
	cfgToken := &config.Config{Auth: config.AuthConfig{PlaybackTokens: map[string]string{"push-1": "secret"}}}
	tokenOK := NewHandler(db, store, noopAuthMW(), cfgToken, nil, "", nil, nil)
	tokenOK.SetMediaEngine(engine)
	rr = doRequest(t, tokenOK.Routes(), http.MethodGet, "/api/integrations/home-assistant", nil, "", "")
	api3Status(t, rr, http.StatusOK)
	require.Contains(t, rr.Body.String(), "token=secret")

	// Engine failures and unusable URLs are surfaced as unavailable RTSP.
	for name, playFn := range map[string]func(media.PlayURLRequest) (*media.PlayURL, error){
		"error": func(media.PlayURLRequest) (*media.PlayURL, error) { return nil, fmt.Errorf("engine down") },
		"nil":   func(media.PlayURLRequest) (*media.PlayURL, error) { return nil, nil },
		"bad-scheme": func(media.PlayURLRequest) (*media.PlayURL, error) {
			return &media.PlayURL{URL: "http://127.0.0.1/live"}, nil
		},
		"empty-url":    func(media.PlayURLRequest) (*media.PlayURL, error) { return &media.PlayURL{URL: ""}, nil },
		"unparsable":   func(media.PlayURLRequest) (*media.PlayURL, error) { return &media.PlayURL{URL: "://bad"}, nil },
		"missing-host": func(media.PlayURLRequest) (*media.PlayURL, error) { return &media.PlayURL{URL: "rtsp:///live/x"}, nil },
	} {
		brokenEngine := &api3HAEngine{stubMediaEngine: &stubMediaEngine{}, playFn: playFn}
		brokenHandler := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
		brokenHandler.SetMediaEngine(brokenEngine)
		rr := doRequest(t, brokenHandler.Routes(), http.MethodGet, "/api/integrations/home-assistant", nil, "", "")
		api3Status(t, rr, http.StatusOK)
		require.Contains(t, rr.Body.String(), "rtsp_unavailable", name)
	}

	// Snapshot endpoint: unpublished streams are rejected.
	api3Status(t, doRequest(t, router, http.MethodGet, "/api/integrations/home-assistant/streams/ghost/snapshot", nil, "", ""), http.StatusNotFound)

	// A published stream without a usable FFmpeg binary reports 503.
	badFFmpegCfg := &config.Config{Transcoding: transcode.Config{FFmpegPath: "/nonexistent/api3-ffmpeg"}}
	noFFmpeg := NewHandler(db, store, noopAuthMW(), badFFmpegCfg, nil, "", nil, nil)
	noFFmpeg.SetMediaEngine(engine)
	api3Status(t, doRequest(t, noFFmpeg.Routes(), http.MethodGet, "/api/integrations/home-assistant/streams/push-1/snapshot", nil, "", ""), http.StatusServiceUnavailable)

	// Cached snapshot bytes are served without invoking FFmpeg.
	cacheID := "home-assistant:push-1:"
	noFFmpeg.setCachedSnapshot(cacheID, []byte("jpeg-bytes"))
	rr = doRequest(t, noFFmpeg.Routes(), http.MethodGet, "/api/integrations/home-assistant/streams/push-1/snapshot", nil, "", "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, "jpeg-bytes", rr.Body.String())
}

func TestApi3HomeAssistantPTZONVIF(t *testing.T) {
	ctx := context.Background()
	_, srv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{})
	endpoint := srv.URL + "/onvif/device_service"
	h, _, db, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", endpoint, endpoint)), nil)
	api2ioSeedCamera(t, db, "onvif-cam", "onvif", endpoint, endpoint)
	require.NoError(t, db.BindStreamToCamera(ctx, "published", "onvif-cam"))
	api3HAPublish(t, db, "", storage.HomeAssistantPublication{StreamID: "published", AppName: "live"})
	router := h.Routes()

	rr := doRequest(t, router, http.MethodGet, "/api/integrations/home-assistant/streams/published/ptz/presets", nil, "", "")
	api3Status(t, rr, http.StatusOK)
	require.Contains(t, rr.Body.String(), "pre1")

	for _, body := range []string{
		`{"mode":"continuous","pan":0.5,"duration":0.1}`,
		`{"mode":"absolute","pan":0.4,"tilt":0.2,"zoom":0.5}`,
		`{"mode":"relative","pan":-0.2,"duration":0.1}`,
		`{"mode":"goto_preset","preset_token":"pre1"}`,
		`{"mode":"stop"}`,
	} {
		rr := doRequest(t, router, http.MethodPost, "/api/integrations/home-assistant/streams/published/ptz", strings.NewReader(body), "", "")
		api3Status(t, rr, http.StatusOK)
	}

	// Validation failures.
	for _, body := range []string{
		`{"mode":"invalid"}`,
		`{"mode":"continuous","pan":1.5}`,
		`{"mode":"continuous","tilt":-2}`,
		`{"mode":"continuous","zoom":1.2}`,
		`{"mode":"absolute","zoom":-0.1}`,
		`{"mode":"continuous","duration":0.05}`,
		`{"mode":"continuous","duration":9}`,
		`{"mode":"goto_preset"}`,
		`{"mode":"goto_preset","preset_token":"` + strings.Repeat("t", 129) + `"}`,
		`{"mode":"goto_preset","preset_token":"<xml>"}`,
		`{"mode":"stop","extra_field":1}`,
		`null`,
	} {
		rr := doRequest(t, router, http.MethodPost, "/api/integrations/home-assistant/streams/published/ptz", strings.NewReader(body), "", "")
		api3Status(t, rr, http.StatusBadRequest)
	}

	// Unpublished stream and unsupported camera protocol.
	api3Status(t, doRequest(t, router, http.MethodPost, "/api/integrations/home-assistant/streams/ghost/ptz", strings.NewReader(`{"mode":"stop"}`), "", ""), http.StatusNotFound)

	_, rtspSrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{})
	rtspEndpoint := rtspSrv.URL + "/onvif/device_service"
	rtspHandler, _, rtspDB, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("rtsp-cam", "rtsp_h264", "rtsp://192.0.2.10/live", "")), nil)
	api2ioSeedCamera(t, rtspDB, "rtsp-cam", "rtsp_h264", "rtsp://192.0.2.10/live", "")
	require.NoError(t, rtspDB.BindStreamToCamera(ctx, "published-rtsp", "rtsp-cam"))
	api3HAPublish(t, rtspDB, "", storage.HomeAssistantPublication{StreamID: "published-rtsp", AppName: "live"})
	api3Status(t, doRequest(t, rtspHandler.Routes(), http.MethodPost, "/api/integrations/home-assistant/streams/published-rtsp/ptz", strings.NewReader(`{"mode":"stop"}`), "", ""), http.StatusBadRequest)
	// A published stream with no camera binding reports the same rejection.
	_ = rtspEndpoint

	// Missing camera manager.
	noMgr, _, noMgrDB, _ := api2ioNewHandler(t, nil, nil)
	api2ioSeedCamera(t, noMgrDB, "onvif-cam", "onvif", endpoint, endpoint)
	require.NoError(t, noMgrDB.BindStreamToCamera(ctx, "published", "onvif-cam"))
	api3HAPublish(t, noMgrDB, "", storage.HomeAssistantPublication{StreamID: "published", AppName: "live"})
	api3Status(t, doRequest(t, noMgr.Routes(), http.MethodGet, "/api/integrations/home-assistant/streams/published/ptz/presets", nil, "", ""), http.StatusServiceUnavailable)
	api3Status(t, doRequest(t, noMgr.Routes(), http.MethodPost, "/api/integrations/home-assistant/streams/published/ptz", strings.NewReader(`{"mode":"stop"}`), "", ""), http.StatusServiceUnavailable)

	// Camera present in the database but absent from the camera-manager config.
	other, _, otherDB, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("other-cam", "onvif", endpoint, endpoint)), nil)
	api2ioSeedCamera(t, otherDB, "onvif-cam", "onvif", endpoint, endpoint)
	require.NoError(t, otherDB.BindStreamToCamera(ctx, "published", "onvif-cam"))
	api3HAPublish(t, otherDB, "", storage.HomeAssistantPublication{StreamID: "published", AppName: "live"})
	api3Status(t, doRequest(t, other.Routes(), http.MethodGet, "/api/integrations/home-assistant/streams/published/ptz/presets", nil, "", ""), http.StatusBadGateway)
	api3Status(t, doRequest(t, other.Routes(), http.MethodPost, "/api/integrations/home-assistant/streams/published/ptz", strings.NewReader(`{"mode":"stop"}`), "", ""), http.StatusBadGateway)

	// Upstream PTZ failures.
	_, failSrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{FailOps: []string{"GetPresets", "ContinuousMove", "AbsoluteMove", "RelativeMove", "GotoPreset", "Stop"}})
	failEndpoint := failSrv.URL + "/onvif/device_service"
	failHandler, _, failDB, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", failEndpoint, failEndpoint)), nil)
	api2ioSeedCamera(t, failDB, "onvif-cam", "onvif", failEndpoint, failEndpoint)
	require.NoError(t, failDB.BindStreamToCamera(ctx, "published", "onvif-cam"))
	api3HAPublish(t, failDB, "", storage.HomeAssistantPublication{StreamID: "published", AppName: "live"})
	api3Status(t, doRequest(t, failHandler.Routes(), http.MethodGet, "/api/integrations/home-assistant/streams/published/ptz/presets", nil, "", ""), http.StatusBadGateway)
	for _, body := range []string{
		`{"mode":"continuous","pan":0.5,"duration":0.1}`,
		`{"mode":"absolute","pan":0.5}`,
		`{"mode":"relative","pan":0.5}`,
		`{"mode":"goto_preset","preset_token":"pre1"}`,
		`{"mode":"stop"}`,
	} {
		rr := doRequest(t, failHandler.Routes(), http.MethodPost, "/api/integrations/home-assistant/streams/published/ptz", strings.NewReader(body), "", "")
		api3Status(t, rr, http.StatusBadGateway)
	}

	// Empty preset list from the device.
	_, emptySrv := api2ioNewFakeONVIF(t, api2ioFakeONVIFOptions{EmptyPresets: true})
	emptyEndpoint := emptySrv.URL + "/onvif/device_service"
	emptyHandler, _, emptyDB, _ := api2ioNewHandler(t, api2ioCfg(api2ioCam("onvif-cam", "onvif", emptyEndpoint, emptyEndpoint)), nil)
	api2ioSeedCamera(t, emptyDB, "onvif-cam", "onvif", emptyEndpoint, emptyEndpoint)
	require.NoError(t, emptyDB.BindStreamToCamera(ctx, "published", "onvif-cam"))
	api3HAPublish(t, emptyDB, "", storage.HomeAssistantPublication{StreamID: "published", AppName: "live"})
	rr = doRequest(t, emptyHandler.Routes(), http.MethodGet, "/api/integrations/home-assistant/streams/published/ptz/presets", nil, "", "")
	api3Status(t, rr, http.StatusOK)
	require.Contains(t, rr.Body.String(), `"presets":[]`)
}

func TestApi3HomeAssistantPTZGB28181(t *testing.T) {
	ctx := context.Background()
	db, store := setupTestDB(t)
	for _, id := range []string{"dev:ch", "bad-id"} {
		require.NoError(t, db.UpsertCamera(ctx, id, id, "gb28181", "h264", "gb28181://"+id, "", "", true, "", "", ""))
	}
	require.NoError(t, db.BindStreamToCamera(ctx, "published", "dev:ch"))
	api3HAPublish(t, db, "", storage.HomeAssistantPublication{StreamID: "published", AppName: "live"})
	cfg := &config.Config{Cameras: []config.CameraConfig{{ID: "dev:ch", Protocol: "gb28181"}}}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, "", nil, nil)
	router := h.Routes()

	// GB28181 server unavailable.
	api3Status(t, doRequest(t, router, http.MethodPost, "/api/integrations/home-assistant/streams/published/ptz",
		strings.NewReader(`{"mode":"continuous","pan":0.5,"duration":0.1}`), "", ""), http.StatusServiceUnavailable)

	// A real (device-less) GB28181 server rejects the command upstream.
	srv := api2ioNewGBServer(t, db)
	h.SetGB28181ServerInstance(srv)
	for _, body := range []string{
		`{"mode":"continuous","pan":0.5,"duration":0.1}`,
		`{"mode":"continuous","pan":-0.5,"duration":0.1}`,
		`{"mode":"continuous","tilt":0.5,"duration":0.1}`,
		`{"mode":"continuous","zoom":0.5,"duration":0.1}`,
		`{"mode":"continuous","zoom":-0.4,"duration":0.1}`,
		`{"mode":"stop"}`,
	} {
		rr := doRequest(t, router, http.MethodPost, "/api/integrations/home-assistant/streams/published/ptz", strings.NewReader(body), "", "")
		api3Status(t, rr, http.StatusBadGateway)
	}

	// GB28181 only supports continuous movement and stop.
	for _, body := range []string{
		`{"mode":"absolute","pan":0.5}`,
		`{"mode":"relative","pan":0.5}`,
		`{"mode":"goto_preset","preset_token":"pre1"}`,
		`{"mode":"continuous","pan":0.5,"tilt":0.5}`,
	} {
		rr := doRequest(t, router, http.MethodPost, "/api/integrations/home-assistant/streams/published/ptz", strings.NewReader(body), "", "")
		api3Status(t, rr, http.StatusBadRequest)
	}

	// Presets are not supported for GB28181, but the endpoint answers empty.
	rr := doRequest(t, router, http.MethodGet, "/api/integrations/home-assistant/streams/published/ptz/presets", nil, "", "")
	api3Status(t, rr, http.StatusOK)
	require.Contains(t, rr.Body.String(), `"presets":[]`)

	// Channel IDs without a "device:channel" split are rejected.
	require.NoError(t, db.UpsertCamera(ctx, "bad-id", "bad", "gb28181", "h264", "gb28181://bad", "", "", true, "", "", ""))
	require.NoError(t, db.BindStreamToCamera(ctx, "published-bad", "bad-id"))
	api3HAPublish(t, db, "", storage.HomeAssistantPublication{StreamID: "published-bad", AppName: "live"})
	api3Status(t, doRequest(t, router, http.MethodPost, "/api/integrations/home-assistant/streams/published-bad/ptz",
		strings.NewReader(`{"mode":"continuous","pan":0.5,"duration":0.1}`), "", ""), http.StatusBadRequest)
}

func TestApi3HomeAssistantVariantResolution(t *testing.T) {
	// Without a resolvable derived playback the publication keeps its source
	// identity and reports the RTSP output as unavailable.
	db, store := setupTestDB(t)
	ctx := context.Background()
	api3HAPublish(t, db, "rtsp://public.example:554", storage.HomeAssistantPublication{
		StreamID: "published", AppName: "live", TranscodeJobID: "job-1",
	})
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(api3HAEngineFor())
	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/integrations/home-assistant", nil, "", "")
	api3Status(t, rr, http.StatusOK)
	var catalog homeAssistantCatalog
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &catalog))
	require.Len(t, catalog.Streams, 1)
	require.Equal(t, "rtsp_unavailable", catalog.Streams[0].UnavailableReason)

	cfg, err := db.GetHomeAssistantSettings(ctx)
	require.NoError(t, err)
	_, _, err = h.homeAssistantVariant(ctx, cfg, cfg.Streams[0])
	require.Error(t, err)
	require.Contains(t, err.Error(), "derived RTSP unavailable")
}
