package dlna

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func boolPtr(v bool) *bool { return &v }

type dlnaRepo struct {
	mu         sync.Mutex
	cameras    []storage.CameraRow
	cameraErr  error
	recordings []model.Recording
	recordErr  error
	filter     model.RecordingFilter
	created    []storage.CreatedStream
	createdErr error
	recording  *model.Recording
	getErr     error
}

func (r *dlnaRepo) ListCameras(context.Context) ([]storage.CameraRow, error) {
	if r.cameraErr != nil {
		return nil, r.cameraErr
	}
	return r.cameras, nil
}

func (r *dlnaRepo) ListRecordings(_ context.Context, filter model.RecordingFilter) ([]model.Recording, error) {
	r.mu.Lock()
	r.filter = filter
	r.mu.Unlock()
	if r.recordErr != nil {
		return nil, r.recordErr
	}
	return r.recordings, nil
}

func (r *dlnaRepo) ListCreatedStreams(context.Context) ([]storage.CreatedStream, error) {
	if r.createdErr != nil {
		return nil, r.createdErr
	}
	return r.created, nil
}

func (r *dlnaRepo) GetRecording(_ context.Context, id string) (*model.Recording, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.recording == nil || r.recording.ID != id {
		return nil, nil
	}
	return r.recording, nil
}

type dlnaEngine struct {
	media.Engine
	mu      sync.Mutex
	streams []media.StreamInfo
	listErr error
	info    *media.StreamInfo
	getErr  error
	play    string
	playErr error
}

func (e *dlnaEngine) ListStreams(context.Context) ([]media.StreamInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.listErr != nil {
		return nil, e.listErr
	}
	return append([]media.StreamInfo(nil), e.streams...), nil
}

func (e *dlnaEngine) GetStream(context.Context, string) (*media.StreamInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.getErr != nil {
		return nil, e.getErr
	}
	if e.info == nil {
		return nil, nil
	}
	copied := *e.info
	return &copied, nil
}

func (e *dlnaEngine) BuildPlayURL(context.Context, media.PlayURLRequest) (*media.PlayURL, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.playErr != nil {
		return nil, e.playErr
	}
	if e.play == "" {
		return nil, nil
	}
	return &media.PlayURL{URL: e.play, Protocol: "httpts"}, nil
}

func enabledService(repo storage.DLNARepository, engine media.Engine) *Service {
	return NewService(&config.Config{
		Server: config.ServerConfig{Listen: ":9090"},
		DLNA: config.DLNAConfig{
			Enabled:      boolPtr(true),
			FriendlyName: "Coverage NVR",
			UUID:         "uuid-fixed",
			Port:         18211,
		},
	}, repo, nil, engine)
}

func serveDLNA(t *testing.T, svc *Service) *httptest.Server {
	t.Helper()
	router := chi.NewRouter()
	svc.RegisterRoutes(router)
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv
}

func TestDLNACatalogSOAPAndEvents(t *testing.T) {
	repo := &dlnaRepo{
		cameras: []storage.CameraRow{
			{ID: "cam-1", Name: "Front", StreamID: "cam-1"},
			{ID: "", Name: ""},
			{ID: "blank", StreamID: "blank"},
		},
		recordings: []model.Recording{{ID: "rec/1", FileSize: 10, Duration: 3}},
		created:    []storage.CreatedStream{{StreamID: "push-1", Name: "Push"}},
	}
	engine := &dlnaEngine{streams: []media.StreamInfo{
		{StreamID: "cam-1", AppName: "live", Active: true},
		{StreamID: "push-1", AppName: "live", Active: true},
		{StreamID: "push-1_sub", AppName: "live", Active: true},
		{StreamID: "other", AppName: "vod", Active: true},
		{StreamID: "", AppName: "live", Active: true},
		{StreamID: "idle", AppName: "live", Active: false},
	}}
	svc := enabledService(repo, engine)
	svc.cfg.DLNA.MaxBrowseCount = 7
	srv := serveDLNA(t, svc)
	ctx := context.Background()

	require.False(t, (*Service)(nil).Enabled())
	limited := NewService(&config.Config{DLNA: config.DLNAConfig{MaxMediaViewers: 2}}, nil, nil, nil)
	require.Equal(t, 2, limited.viewerLimit)
	require.False(t, NewService(nil, nil, nil, nil).Enabled())
	require.False(t, NewService(&config.Config{}, nil, nil, nil).Enabled())
	require.True(t, svc.Enabled())
	require.Equal(t, "uuid-fixed", svc.uuid())
	svc.cfg.DLNA.UUID = ""
	require.Equal(t, "550e8400-e29b-41d4-a716-446655440000", svc.uuid())
	svc.cfg.DLNA.UUID = "uuid-fixed"
	require.Equal(t, "", ResolvedBaseURL(nil))
	require.Contains(t, ResolvedBaseURL(svc.cfg), "http://")
	require.Equal(t, " name ", first(" name ", "fallback"))
	require.Equal(t, "fallback", first(" ", "fallback"))

	body := getDLNA(t, srv, "/dlna/device.xml")
	require.Contains(t, body, "Coverage NVR")
	require.Contains(t, getDLNA(t, srv, "/dlna/content-directory.xml"), "Browse")
	require.Contains(t, getDLNA(t, srv, "/dlna/connection-manager.xml"), "GetProtocolInfo")

	root := soap(t, srv, "/dlna/control/content-directory", "Browse", `<ObjectID>0</ObjectID><BrowseFlag>BrowseDirectChildren</BrowseFlag><StartingIndex>0</StartingIndex><RequestedCount>1</RequestedCount>`)
	require.Contains(t, root, "NumberReturned")
	require.Contains(t, root, "live")
	meta := soap(t, srv, "/dlna/control/content-directory", "Browse", `<ObjectID>0</ObjectID><BrowseFlag>BrowseMetadata</BrowseFlag><StartingIndex>5</StartingIndex><RequestedCount>0</RequestedCount>`)
	require.Contains(t, meta, "BrowseResponse")
	live := soap(t, srv, "/dlna/control/content-directory", "Browse", `<ObjectID>live</ObjectID><BrowseFlag>BrowseDirectChildren</BrowseFlag>`)
	require.Contains(t, live, "Push")
	require.Contains(t, live, "Front")
	one := soap(t, srv, "/dlna/control/content-directory", "Browse", `<ObjectID>live:cam-1</ObjectID><BrowseFlag>BrowseDirectChildren</BrowseFlag>`)
	require.Contains(t, one, "live:cam-1")
	recs := soap(t, srv, "/dlna/control/content-directory", "Browse", `<ObjectID>recordings</ObjectID><BrowseFlag>BrowseDirectChildren</BrowseFlag>`)
	require.Contains(t, recs, "rec%2F1")
	require.Equal(t, 7, repo.filter.Limit)
	unknown, err := svc.browse(ctx, browseRequest{ObjectID: "nope"})
	require.NoError(t, err)
	require.Zero(t, unknown.NumberReturned)
	require.Contains(t, soap(t, srv, "/dlna/control/content-directory", "GetSearchCapabilities", ""), "SearchCaps")
	require.Contains(t, soap(t, srv, "/dlna/control/content-directory", "GetSortCapabilities", ""), "SortCaps")
	require.Contains(t, soap(t, srv, "/dlna/control/content-directory", "GetSystemUpdateID", ""), ">1<")
	fault := postSOAP(t, srv, "/dlna/control/content-directory", "", "not-xml")
	require.Contains(t, fault, "Invalid Action")
	badBrowse := postSOAP(t, srv, "/dlna/control/content-directory", `"urn:schemas-upnp-org:service:ContentDirectory:1#Browse"`, "<")
	require.Contains(t, badBrowse, "Invalid Args")
	repo.cameraErr = errors.New("cameras down")
	camFault := soap(t, srv, "/dlna/control/content-directory", "Browse", `<ObjectID>live</ObjectID>`)
	require.Contains(t, camFault, "cameras down")
	repo.cameraErr = nil
	repo.recordErr = errors.New("recs down")
	recFault := soap(t, srv, "/dlna/control/content-directory", "Browse", `<ObjectID>recordings</ObjectID>`)
	require.Contains(t, recFault, "recs down")
	repo.recordErr = nil

	require.Contains(t, soap(t, srv, "/dlna/control/connection-manager", "GetProtocolInfo", ""), "AVC_TS")
	require.Contains(t, soap(t, srv, "/dlna/control/connection-manager", "GetCurrentConnectionIDs", ""), "ConnectionIDs")
	require.Contains(t, soap(t, srv, "/dlna/control/connection-manager", "GetCurrentConnectionInfo", ""), "Output")
	require.Contains(t, postSOAP(t, srv, "/dlna/control/connection-manager", "", "<s:Envelope><s:Body><Nope/></s:Body></s:Envelope>"), "Invalid Action")
	browseReq := httptest.NewRequest(http.MethodPost, "/", nil)
	browseReq.Header.Set("SOAPACTION", `"urn:schemas-upnp-org:service:ContentDirectory:1#Browse"`)
	require.Equal(t, "Browse", soapAction(browseReq, nil))
	require.Equal(t, "Plain", soapAction(requestWith("SOAPACTION", "Plain"), nil))
	require.Equal(t, "", soapAction(httptest.NewRequest(http.MethodPost, "/", strings.NewReader("<nope/>")), []byte("<nope/>")))
	inner := []byte(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><Inner/></s:Body></s:Envelope>`)
	require.Equal(t, "Inner", soapAction(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(inner))), inner))

	off := enabledService(repo, engine)
	off.cfg.DLNA.IncludeLive = boolPtr(false)
	off.cfg.DLNA.IncludeRecordings = boolPtr(false)
	rootOnly, err := off.browse(ctx, browseRequest{ObjectID: "0"})
	require.NoError(t, err)
	require.NotContains(t, rootOnly.DIDL, "container")
	liveOff, err := off.browse(ctx, browseRequest{ObjectID: "live"})
	require.NoError(t, err)
	require.Zero(t, liveOff.NumberReturned)
	recOff, err := off.browse(ctx, browseRequest{ObjectID: "recordings"})
	require.NoError(t, err)
	require.Zero(t, recOff.NumberReturned)
	engine.listErr = errors.New("streams")
	repo.createdErr = errors.New("created")
	pushes := svc.activePushes(ctx, "http://127.0.0.1:8200", map[string]struct{}{})
	require.Empty(t, pushes)
	engine.listErr = nil
	require.NotEmpty(t, svc.activePushes(ctx, "http://127.0.0.1:8200", map[string]struct{}{}))
	require.Nil(t, NewService(svc.cfg, nil, nil, nil).activePushes(ctx, "http://127.0.0.1", nil))

	req := httptest.NewRequest(http.MethodGet, srv.URL+"/dlna/device.xml", nil)
	rr := httptest.NewRecorder()
	disabled := enabledService(repo, engine)
	disabled.cfg.DLNA.Enabled = boolPtr(false)
	disabled.guard(disabled.device)(rr, req)
	require.Equal(t, http.StatusNotFound, rr.Code)
	denied := enabledService(repo, engine)
	denied.cfg.DLNA.AllowedCIDRs = []string{"10.0.0.0/8", "not-a-cidr"}
	rr = httptest.NewRecorder()
	denied.guard(denied.device)(rr, httptest.NewRequest(http.MethodGet, "/dlna/device.xml", nil))
	require.Equal(t, http.StatusForbidden, rr.Code)
	require.False(t, denied.isAllowed("no-port"))
	require.False(t, denied.isAllowed("example.invalid:9"))
	require.True(t, enabledService(repo, engine).isAllowed("ignored"))

	eventsReq := httptest.NewRequest("UNSUBSCRIBE", "/dlna/events/content-directory", nil)
	eventsReq.Header.Set("SID", "uuid:keep")
	rr = httptest.NewRecorder()
	svc.events(rr, eventsReq)
	require.Equal(t, "uuid:keep", rr.Header().Get("SID"))
	renew := httptest.NewRequest("SUBSCRIBE", "/dlna/events/content-directory", nil)
	renew.Header.Set("SID", "uuid:old")
	rr = httptest.NewRecorder()
	svc.events(rr, renew)
	require.Equal(t, "Second-1800", rr.Header().Get("TIMEOUT"))
	rr = httptest.NewRecorder()
	svc.events(rr, httptest.NewRequest(http.MethodGet, "/dlna/events/content-directory", nil))
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Equal(t, "fallback", firstHeader(" ", "fallback"))
	require.Equal(t, "", parseCallback("none"))
	require.Equal(t, "http://192.168.1.9/cb", parseCallback("<http://192.168.1.9/cb>"))
	require.False(t, svc.allowCallback("://bad"))
	require.False(t, svc.allowCallback("https://192.168.1.9/cb"))
	require.False(t, svc.allowCallback("http://127.0.0.1/cb"))
	require.False(t, svc.allowCallback("http://example.test/cb"))
	require.False(t, NewService(nil, nil, nil, nil).allowCallback("http://192.168.1.9/cb"))
	require.True(t, svc.allowCallback("http://192.168.1.9/cb"))
	denied.cfg.DLNA.Enabled = boolPtr(true)
	require.False(t, denied.allowCallback("http://192.168.1.9/cb"))
	require.True(t, denied.allowCallback("http://10.1.1.1/cb"))

	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "NOTIFY", r.Method)
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/done", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(callback.Close)
	svc.sendInitialEvent(callback.URL, "uuid:fixed", "SystemUpdateID", "1 <2>")
	svc.sendInitialEvent("http://127.0.0.1:1/missing", "uuid:fixed", "SystemUpdateID", "1")
	svc.sendInitialEvent("http://[", "uuid:fixed", "SystemUpdateID", "1")

	fresh := httptest.NewRequest("SUBSCRIBE", "/dlna/events/connection-manager", nil)
	fresh.Header.Set("CALLBACK", "<http://192.168.1.9:9/event>")
	rr = httptest.NewRecorder()
	svc.events(rr, fresh)
	require.Contains(t, rr.Header().Get("SID"), "uuid:")
	emptyCallback := httptest.NewRequest("SUBSCRIBE", "/dlna/events/content-directory", nil)
	rr = httptest.NewRecorder()
	svc.events(rr, emptyCallback)
	require.Equal(t, http.StatusOK, rr.Code)

	(*Service)(nil).RegisterRoutes(chi.NewRouter())
	svc.setViewerLimit(0)
	require.Equal(t, config.DefaultDLNAMaxMediaViewers, svc.viewerLimit)
	svc.releaseViewer()
}

func TestDLNAMediaStreaming(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.NewManager(dir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte("mp4-bytes"), 0o644))
	repo := &dlnaRepo{recording: &model.Recording{ID: "clip", FilePath: filepath.Join(dir, "clip.mp4")}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") == "fail" {
			http.Error(w, "nope", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write([]byte("ts-bytes"))
	}))
	t.Cleanup(upstream.Close)
	engine := &dlnaEngine{info: &media.StreamInfo{StreamID: "cam", Active: true}, play: upstream.URL + "?mode=ok"}
	svc := enabledService(repo, engine)
	svc.store = store
	svc.setViewerLimit(1)
	srv := serveDLNA(t, svc)

	resp, err := http.Get(srv.URL + "/dlna/live/cam.ts")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "ts-bytes", string(body))
	require.Contains(t, resp.Header.Get("contentFeatures.dlna.org"), "DLNA.ORG_CI=1")

	head, err := http.Head(srv.URL + "/dlna/live/cam.ts")
	require.NoError(t, err)
	head.Body.Close()
	require.Equal(t, http.StatusOK, head.StatusCode)

	releaseViewer := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	limit := svc.limitMediaViewers(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-releaseViewer
		w.WriteHeader(http.StatusOK)
	})
	go limit(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/dlna/live/cam.ts", nil))
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("viewer slot was not acquired")
	}
	limited := httptest.NewRecorder()
	limit(limited, httptest.NewRequest(http.MethodGet, "/dlna/live/cam.ts", nil))
	require.Equal(t, http.StatusServiceUnavailable, limited.Code)
	require.Equal(t, "5", limited.Header().Get("Retry-After"))
	close(releaseViewer)

	rr := httptest.NewRecorder()
	svc.live(rr, httptest.NewRequest(http.MethodGet, "/dlna/live/.ts", nil))
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	noEngine := enabledService(repo, nil)
	rr = httptest.NewRecorder()
	noEngine.live(rr, requestWithParam("stream", "cam"))
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	engine.getErr = errors.New("missing")
	rr = httptest.NewRecorder()
	svc.live(rr, requestWithParam("stream", "cam"))
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	engine.getErr = nil
	engine.info.Active = false
	rr = httptest.NewRecorder()
	svc.live(rr, requestWithParam("stream", "cam"))
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	engine.info.Active = true
	engine.playErr = errors.New("url")
	rr = httptest.NewRecorder()
	svc.live(rr, requestWithParam("stream", "cam"))
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	engine.playErr = nil
	engine.play = "http://["
	rr = httptest.NewRecorder()
	svc.live(rr, requestWithParam("stream", "cam"))
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	engine.play = upstream.URL + "?mode=fail"
	rr = httptest.NewRecorder()
	svc.live(rr, requestWithParam("stream", "cam"))
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	closed, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	upstream.Close()
	engine.play = closed.String()
	rr = httptest.NewRecorder()
	svc.live(rr, requestWithParam("stream", "cam"))
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)

	okUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "plain")
	}))
	t.Cleanup(okUpstream.Close)
	engine.play = okUpstream.URL
	rr = httptest.NewRecorder()
	svc.live(rr, requestWithParam("stream", "cam"))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "plain", rr.Body.String())
	fw := &failWriter{h: http.Header{}}
	svc.live(fw, requestWithParam("stream", "cam"))
	require.Equal(t, http.StatusOK, fw.code)
	hj := &hijackFail{ResponseWriter: httptest.NewRecorder()}
	svc.live(hj, requestWithParam("stream", "cam"))

	rec, err := http.Get(srv.URL + "/dlna/media/clip")
	require.NoError(t, err)
	recBody, err := io.ReadAll(rec.Body)
	rec.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.StatusCode)
	require.Equal(t, "mp4-bytes", string(recBody))
	require.Contains(t, rec.Header.Get("contentFeatures.dlna.org"), "DLNA.ORG_OP=01")
	headRec, err := http.Head(srv.URL + "/dlna/media/clip")
	require.NoError(t, err)
	headRec.Body.Close()
	require.Equal(t, http.StatusOK, headRec.StatusCode)

	missing, err := http.Get(srv.URL + "/dlna/media/missing")
	require.NoError(t, err)
	missing.Body.Close()
	require.Equal(t, http.StatusNotFound, missing.StatusCode)
	repo.getErr = errors.New("db")
	rr = httptest.NewRecorder()
	svc.recording(rr, requestWithParam("id", "clip"))
	require.Equal(t, http.StatusNotFound, rr.Code)
	repo.getErr = nil
	repo.recording = nil
	rr = httptest.NewRecorder()
	svc.recording(rr, requestWithParam("id", "clip"))
	require.Equal(t, http.StatusNotFound, rr.Code)
	repo.recording = &model.Recording{ID: "clip", FilePath: filepath.Join(dir, "..", "outside.mp4")}
	rr = httptest.NewRecorder()
	svc.recording(rr, requestWithParam("id", "clip"))
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = httptest.NewRecorder()
	svc.recording(rr, httptest.NewRequest(http.MethodGet, "/dlna/media/", nil))
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = httptest.NewRecorder()
	rr.Header().Set("Content-Type", "video/mp4")
	setDLNAHeaders(rr, "video/mpeg")
	require.Equal(t, "video/mp4", rr.Header().Get("Content-Type"))

	client, server := net.Pipe()
	require.NoError(t, client.Close())
	require.Error(t, writeLiveHeader(server, "video/mpeg", "x"))
	server.Close()
}

func TestDLNASSDPAndStartup(t *testing.T) {
	svc := enabledService(&dlnaRepo{}, nil)
	require.Nil(t, svc.matchSearch("missing"))
	require.Equal(t, []string{deviceType}, svc.matchSearch(strings.ToUpper(deviceType)))
	require.Equal(t, "uuid:uuid-fixed", svc.usn("uuid:uuid-fixed"))
	require.Contains(t, svc.usn("upnp:rootdevice"), "::")
	require.Equal(t, "uuid:uuid-fixed", svc.usn("other"))
	require.Equal(t, "", parseST("M-SEARCH * HTTP/1.1\r\nHOST: x\r\n"))
	require.Contains(t, svc.location(), "/dlna/device.xml")

	for _, name := range []string{"docker0", "br-1", "veth0", "virbr0", "vmnet1", "vboxnet0", "utun0", "awdl0", "llw0", "zt0", "tailscale0", "wg0", "tun0", "tap0", "lo0", "gif0", "stf0", "anpi0", "bridge0", "en0"} {
		require.Equal(t, name != "en0", virtualIface(name))
	}
	require.Zero(t, privateRank(nil))
	require.Zero(t, privateRank(net.ParseIP("8.8.8.8")))
	require.Equal(t, 3, privateRank(net.ParseIP("192.168.1.1").To4()))
	require.Equal(t, 2, privateRank(net.ParseIP("10.1.1.1").To4()))
	require.Equal(t, 1, privateRank(net.ParseIP("172.16.1.1").To4()))
	require.Nil(t, privateIPv4(nil))
	require.Nil(t, interfaceByIP(nil))
	require.NotNil(t, interfaceByIP(net.IPv4(127, 0, 0, 1)))
	require.Equal(t, "192.168.1.9", advertiseHost(" http://192.168.1.9:8200/x "))
	require.Equal(t, "", advertiseHost("://"))
	reused, err := listenUDPReuse("127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, reused.Close())
	_, err = listenUDPReuse("bad-address")
	require.Error(t, err)
	sender, err := listenSSDPSender(nil)
	require.NoError(t, err)
	require.NoError(t, sender.Close())
	for _, name := range []string{"lo0", "lo"} {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			continue
		}
		sender, err = listenSSDPSender(iface)
		if err == nil {
			require.NoError(t, sender.Close())
		}
		break
	}
	_, iface, err := detectLANIPv4("no-such-dlna-iface", "")
	require.Error(t, err)
	require.Nil(t, iface)
	ip, _, err := detectLANIPv4("lo0", "http://203.0.113.9:8200")
	if err == nil {
		require.NotNil(t, ip)
	}
	if gotIP, _, err := detectLANIPv4("", ""); err != nil {
		require.ErrorContains(t, err, "no private IPv4")
		legacy, _, err := detectLANIPv4("", "http://203.0.113.9:8200")
		require.NoError(t, err)
		require.True(t, legacy.Equal(net.ParseIP("203.0.113.9")))
	} else {
		require.NotNil(t, gotIP)
	}
	_ = outboundIPv4()
	_, _ = bestPrivateIPv4()

	sock := udpLoopback(t)
	send := udpLoopback(t)
	client := udpLoopback(t)
	ctx, cancel := context.WithCancel(context.Background())
	go svc.readSearches(ctx, nil, send)
	go svc.readSearches(ctx, sock, send)
	_, err = client.WriteToUDP([]byte("NOTIFY * HTTP/1.1\r\n\r\n"), sock.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	_, err = client.WriteToUDP([]byte("M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\n\r\n"), sock.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	_, err = client.WriteToUDP([]byte("M-SEARCH * HTTP/1.1\r\nST: ssdp:all\r\n\r\n"), sock.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	n, _, err := client.ReadFromUDP(buf)
	require.NoError(t, err)
	require.Contains(t, string(buf[:n]), "HTTP/1.1 200 OK")
	cancel()
	svc.respondSearch(nil, client.LocalAddr().(*net.UDPAddr), "ssdp:all")
	svc.respondSearch(send, nil, "ssdp:all")
	closed := udpLoopback(t)
	require.NoError(t, closed.Close())
	svc.respondSearch(closed, client.LocalAddr().(*net.UDPAddr), deviceType)
	svc.sendNotify(nil, "ssdp:alive")
	svc.sendNotify(closed, "alive")

	loopSock := udpLoopback(t)
	loopCtx, loopCancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.ssdpLoop(loopCtx, loopSock, loopSock)
		close(done)
	}()
	loopCancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ssdp loop did not stop")
	}
	notifySock := udpLoopback(t)
	loopCtx, loopCancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() {
		svc.notifyLoop(loopCtx, notifySock)
		close(done)
	}()
	loopCancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notify loop did not stop")
	}
	left := udpLoopback(t)
	right := udpLoopback(t)
	pairCtx, pairCancel := context.WithCancel(context.Background())
	pairDone := make(chan struct{})
	go func() {
		svc.ssdpLoop(pairCtx, left, right)
		close(pairDone)
	}()
	time.Sleep(20 * time.Millisecond)
	pairCancel()
	select {
	case <-pairDone:
	case <-time.After(3 * time.Second):
		t.Fatal("paired ssdp loop did not stop")
	}

	svc.cfg.DLNA.Port = 0
	require.Equal(t, config.DefaultDLNAPort, svc.listenPort())
	svc.cfg.DLNA.Enabled = boolPtr(false)
	require.NoError(t, svc.Start(context.Background()))
	require.NoError(t, svc.Apply(context.Background()))
	svc.cfg.DLNA.Enabled = boolPtr(true)
	svc.cfg.DLNA.Port = 70000
	require.Error(t, svc.Start(context.Background()))
	svc.cfg.DLNA.Interface = "no-such-dlna-iface"
	svc.cfg.DLNA.Port = freeTCPPort(t)
	require.Error(t, svc.Start(nil))
	svc.cfg.DLNA.Interface = ""
	svc.cfg.DLNA.AdvertiseURL = "http://192.168.31.35:8200"
	if _, _, err := detectLANIPv4("", svc.cfg.DLNA.AdvertiseURL); err != nil {
		require.Error(t, svc.Apply(context.Background()))
		return
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", svc.cfg.DLNA.Port))
	require.NoError(t, err)
	require.Error(t, svc.Start(context.Background()))
	require.NoError(t, ln.Close())

	require.NoError(t, svc.Apply(context.Background()))
	require.NoError(t, svc.Stop(context.Background()))
	require.NoError(t, svc.Stop(context.Background()))
}

func getDLNA(t *testing.T, srv *httptest.Server, path string) string {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

func soap(t *testing.T, srv *httptest.Server, path, action, inner string) string {
	t.Helper()
	body := `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:` + action + ` xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1">` + inner + `</u:` + action + `></s:Body></s:Envelope>`
	return postSOAP(t, srv, path, `"`+contentDirectoryType+"#"+action+`"`, body)
}

func postSOAP(t *testing.T, srv *httptest.Server, path, action, body string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	if action != "" {
		req.Header.Set("SOAPACTION", action)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(raw)
}

func requestWith(header, value string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set(header, value)
	return req
}

func requestWithParam(key, value string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/dlna/"+key+"/"+url.PathEscape(value), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func udpLoopback(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

type failWriter struct {
	h    http.Header
	code int
}

func (f *failWriter) Header() http.Header { return f.h }
func (f *failWriter) Write([]byte) (int, error) {
	return 0, errors.New("write")
}
func (f *failWriter) WriteHeader(code int) { f.code = code }
func (f *failWriter) Flush()               {}

type hijackFail struct {
	http.ResponseWriter
}

func (hijackFail) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("hijack")
}
