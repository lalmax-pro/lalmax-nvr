package api

// Issue #74 coverage tests for the GB28181 HTTP handlers.
//
// These tests drive internal/api/handlers_gb28181.go, handlers_gb_region.go and
// handlers_gb_group.go through httptest with a real (temp) SQLite database, a
// real in-process gb28181.Server bound to an ephemeral loopback port and a tiny
// fake SIP peer that answers SIP MESSAGE/INVITE with 200 OK. No external
// network access and no real devices are used.
//
// Every helper in this file is prefixed with gb74 to stay unique inside the
// package while other agents add their own test files in parallel.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/lalmax-pro/lalmax-nvr/internal/civilcode"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/gb28181"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// ==================== request helpers ====================

// gb74Request builds a request with optional chi URL parameters.
func gb74Request(method, target, body string, params map[string]string) *http.Request {
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, target, reader)
	if len(params) > 0 {
		rctx := chi.NewRouteContext()
		for k, v := range params {
			rctx.URLParams.Add(k, v)
		}
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	}
	return req
}

// gb74Serve executes handler with the given request and returns the recorder.
func gb74Serve(handler http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

// gb74Call is the common shorthand: build + serve.
func gb74Call(handler http.HandlerFunc, method, target, body string, params map[string]string) *httptest.ResponseRecorder {
	return gb74Serve(handler, gb74Request(method, target, body, params))
}

// gb74Status asserts the recorded status code.
func gb74Status(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("expected status %d, got %d: %s", want, rr.Code, rr.Body.String())
	}
}

// gb74JSON decodes a JSON object body.
func gb74JSON(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON body %q: %v", rr.Body.String(), err)
	}
	return out
}

// gb74FreePort reserves and releases a loopback TCP+UDP port number.
func gb74FreePort(t *testing.T) int {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		pc, err := net.ListenPacket("udp", "0.0.0.0:0")
		if err != nil {
			continue
		}
		port := pc.LocalAddr().(*net.UDPAddr).Port
		_ = pc.Close()
		ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return port
	}
	t.Fatal("unable to find a free TCP+UDP port")
	return 0
}

// ==================== fake SIP peer ====================

// gb74SIPPeer answers every SIP request with "200 OK", copying the dialog
// headers so sipgo client transactions can match the response.
type gb74SIPPeer struct {
	conn net.PacketConn
	addr string
	done chan struct{}
	wg   sync.WaitGroup

	mu sync.Mutex
	// rejectMethod makes the peer answer that SIP method with 500 instead of 200.
	// Guarded by mu because tests update it while serve is running.
	rejectMethod string
	requests     []string
}

func gb74NewSIPPeer(t *testing.T) *gb74SIPPeer {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake SIP peer listen: %v", err)
	}
	p := &gb74SIPPeer{conn: conn, addr: conn.LocalAddr().String(), done: make(chan struct{})}
	p.wg.Add(1)
	go p.serve()
	t.Cleanup(p.close)
	return p
}

func (p *gb74SIPPeer) close() {
	select {
	case <-p.done:
		return
	default:
	}
	close(p.done)
	_ = p.conn.Close()
	p.wg.Wait()
}

// setRejectMethod changes which SIP method the peer rejects with 500.
func (p *gb74SIPPeer) setRejectMethod(method string) {
	p.mu.Lock()
	p.rejectMethod = method
	p.mu.Unlock()
}

// seen returns how many SIP requests the peer received.
func (p *gb74SIPPeer) seen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func (p *gb74SIPPeer) serve() {
	defer p.wg.Done()
	buf := make([]byte, 8192)
	for {
		n, addr, err := p.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		data := string(buf[:n])
		p.mu.Lock()
		p.requests = append(p.requests, data)
		rejected := p.rejectMethod
		p.mu.Unlock()
		if resp := gb74BuildSIPResponse(data, rejected); resp != "" {
			_, _ = p.conn.WriteTo([]byte(resp), addr)
		}
	}
}

// gb74BuildSIPResponse builds a minimal SIP response for a request message,
// answering 500 for rejectMethod and 200 OK otherwise.
func gb74BuildSIPResponse(request, rejectMethod string) string {
	head := request
	if idx := strings.Index(head, "\r\n\r\n"); idx >= 0 {
		head = head[:idx]
	}
	lines := strings.Split(head, "\r\n")
	var vias, from, to, callID, cseq, contact string
	for _, line := range lines[1:] {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		value = strings.TrimSpace(value)
		switch name {
		case "via":
			vias += "Via: " + value + "\r\n"
		case "from":
			from = value
		case "to":
			to = value
		case "call-id":
			callID = value
		case "cseq":
			cseq = value
		case "contact":
			contact = value
		}
	}
	if vias == "" || cseq == "" {
		return ""
	}
	statusLine := "SIP/2.0 200 OK\r\n"
	if rejectMethod != "" {
		// CSeq is "<seq> <METHOD>".
		if fields := strings.Fields(cseq); len(fields) >= 2 && strings.EqualFold(fields[1], rejectMethod) {
			statusLine = "SIP/2.0 500 Server Error\r\n"
		}
	}
	var b strings.Builder
	b.WriteString(statusLine)
	b.WriteString(vias)
	if from != "" {
		b.WriteString("From: " + from + "\r\n")
	}
	if to != "" {
		if !strings.Contains(to, "tag=") {
			to += ";tag=gb74peer"
		}
		b.WriteString("To: " + to + "\r\n")
	}
	if callID != "" {
		b.WriteString("Call-ID: " + callID + "\r\n")
	}
	b.WriteString("CSeq: " + cseq + "\r\n")
	if contact != "" {
		b.WriteString("Contact: " + contact + "\r\n")
	}
	b.WriteString("Content-Length: 0\r\n\r\n")
	return b.String()
}

// ==================== media engine stub ====================

// gb74MediaEngine wraps the shared stubMediaEngine and adds successful RTP
// receive sessions so play/download code paths can be exercised.
type gb74MediaEngine struct {
	*stubMediaEngine
	rtpErr       error
	streamInfo   *media.StreamInfo
	streamErr    error
	stopErr      error
	emptyPlayURL bool
	mu           sync.Mutex
	rtpCalls     int
}

func gb74NewMediaEngine() *gb74MediaEngine {
	return &gb74MediaEngine{
		stubMediaEngine: &stubMediaEngine{playURLs: map[string]string{
			"rtsp":   "rtsp://127.0.0.1/live/gb74",
			"flv":    "http://127.0.0.1/live/gb74.flv",
			"hls":    "http://127.0.0.1/live/gb74.m3u8",
			"webrtc": "http://127.0.0.1/live/gb74.whep",
		}},
	}
}

func (m *gb74MediaEngine) StartRTPReceive(_ context.Context, req media.StartRTPReceiveRequest) (*media.StreamSession, error) {
	m.mu.Lock()
	m.rtpCalls++
	m.mu.Unlock()
	if m.rtpErr != nil {
		return nil, m.rtpErr
	}
	return &media.StreamSession{SessionID: "gb74-rtp-session", StreamID: req.StreamID, Port: 4600}, nil
}

func (m *gb74MediaEngine) StopRTPReceive(context.Context, string) error { return m.stopErr }

// BuildPlayURL optionally returns a URL that is present but empty so the
// fallback branches of the play-URL builders are exercised too.
func (m *gb74MediaEngine) BuildPlayURL(_ context.Context, req media.PlayURLRequest) (*media.PlayURL, error) {
	if m.emptyPlayURL {
		return &media.PlayURL{Protocol: req.Protocol}, nil
	}
	return m.stubMediaEngine.BuildPlayURL(context.Background(), req)
}

func (m *gb74MediaEngine) GetStream(context.Context, string) (*media.StreamInfo, error) {
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	return m.streamInfo, nil
}

// ==================== handler + server helpers ====================

// gb74Config returns a config with a usable GB28181 section.
func gb74Config(version string) *config.Config {
	cfg := &config.Config{}
	cfg.GB28181.StandardVersion = version
	return cfg
}

// gb74NewHandler builds a Handler with the given DB (may be nil), media engine
// and GB28181 server instance.
func gb74NewHandler(db *storage.DB, engine media.Engine, srv *gb28181.Server) *Handler {
	var apiRepo storage.APIRepository
	if db != nil {
		apiRepo = db
	}
	h := NewHandler(apiRepo, nil, noopAuthMW(), gb74Config("2016"), nil, "", nil, nil)
	if engine != nil {
		h.SetMediaEngine(engine)
	}
	if srv != nil {
		h.SetGB28181ServerInstance(srv)
	}
	return h
}

// gb74NewServer starts an in-process GB28181 SIP server on an ephemeral port.
func gb74NewServer(t *testing.T, db *storage.DB, engine media.Engine) *gb28181.Server {
	t.Helper()
	port := gb74FreePort(t)
	cfg := &gb28181.Config{
		Enabled:         true,
		Host:            "127.0.0.1",
		Port:            port,
		ID:              "34020000002000000001",
		Password:        "gb74-secret",
		MediaIP:         "127.0.0.1",
		StandardVersion: "2016",
	}
	var repo storage.GB28181Repository
	if db != nil {
		repo = db
	}
	srv, err := gb28181.NewServerChecked(cfg, engine, repo)
	if err != nil {
		t.Fatalf("start GB28181 server: %v", err)
	}
	t.Cleanup(srv.Stop)
	return srv
}

// gb74Channel builds an in-memory gb28181 channel.
func gb74Channel(channelID, name string) *gb28181.Channel {
	return &gb28181.Channel{ChannelID: channelID, Name: name}
}

// gb74Fixture bundles a temp database, a fake SIP peer, a stub media engine and
// an in-process GB28181 server bound to an ephemeral loopback port.
type gb74Fixture struct {
	t      *testing.T
	db     *storage.DB
	peer   *gb74SIPPeer
	engine *gb74MediaEngine
	srv    *gb28181.Server
	online []gb74DeviceRef
}

type gb74DeviceRef struct {
	deviceID string
	address  string
}

// gb74NewFixture creates a fixture; a nil engine selects the default stub.
func gb74NewFixture(t *testing.T, engine *gb74MediaEngine) *gb74Fixture {
	t.Helper()
	db, _ := setupTestDB(t)
	peer := gb74NewSIPPeer(t)
	if engine == nil {
		engine = gb74NewMediaEngine()
	}
	srv := gb74NewServer(t, db, engine)
	f := &gb74Fixture{t: t, db: db, peer: peer, engine: engine, srv: srv}
	// Registered last so it runs first: stop every play/playback dialog while
	// the fake peer and the server are still alive. The gb28181 stream registry
	// is process-global, so leaking dialogs would poison later tests.
	t.Cleanup(f.stopAllStreams)
	return f
}

// stopAllStreams closes every GB28181 play dialog started by the test.
func (f *gb74Fixture) stopAllStreams() {
	store := f.srv.GetDeviceStore()
	for _, ref := range f.online {
		dev, ok := store.Load(ref.deviceID)
		if !ok {
			continue
		}
		dev.Channels.Range(func(key, _ any) bool {
			channelID, ok := key.(string)
			if !ok {
				return true
			}
			_ = f.srv.StopPlay(&gb28181.StopPlayInput{DeviceID: ref.deviceID, ChannelID: channelID})
			return true
		})
	}
}

// handler returns a Handler wired with every fixture dependency.
func (f *gb74Fixture) handler() *Handler {
	return gb74NewHandler(f.db, f.engine, f.srv)
}

// seed persists an online device (plus channels) and reloads the device store.
func (f *gb74Fixture) seed(deviceID string, channels ...*gb28181.Channel) {
	f.t.Helper()
	f.persist(deviceID, true, channels...)
	f.online = append(f.online, gb74DeviceRef{deviceID: deviceID, address: f.peer.addr})
	f.reload()
}

// seedOffline persists a device that never registers.
func (f *gb74Fixture) seedOffline(deviceID string, channels ...*gb28181.Channel) {
	f.t.Helper()
	f.persist(deviceID, false, channels...)
	f.reload()
}

// persist writes the device and channel rows into the temp database.
func (f *gb74Fixture) persist(deviceID string, isOnline bool, channels ...*gb28181.Channel) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.db.UpsertGB28181Device(ctx, &storage.GB28181DeviceRow{
		DeviceID: deviceID, Name: deviceID, GBVersion: "2016",
		IsOnline: isOnline, Address: f.peer.addr,
	}); err != nil {
		f.t.Fatalf("seed GB28181 device: %v", err)
	}
	for _, ch := range channels {
		if ch == nil {
			continue
		}
		if err := f.db.UpsertGB28181Channel(ctx, &storage.GB28181ChannelRow{
			DeviceID: deviceID, ChannelID: ch.ChannelID, Name: ch.Name, Status: ch.Status,
			CivilCode: ch.CivilCode, ParentID: ch.ParentID, BusinessGroupID: ch.BusinessGroupID,
		}); err != nil {
			f.t.Fatalf("seed GB28181 channel: %v", err)
		}
	}
}

// reload re-reads devices from the database (so channels carry their device
// pointer) and restores the online state of every seeded device.
func (f *gb74Fixture) reload() {
	f.t.Helper()
	if err := f.srv.GetDeviceStore().LoadFromDB(); err != nil {
		f.t.Fatalf("reload GB28181 devices: %v", err)
	}
	for _, ref := range f.online {
		ref := ref
		if err := f.srv.GetDeviceStore().Change(ref.deviceID, func(dev *gb28181.Device) {
			dev.IsOnline = true
			dev.Address = ref.address
			dev.Transport = "UDP"
		}); err != nil {
			f.t.Fatalf("bring GB28181 device online: %v", err)
		}
	}
}

// ==================== PTZ / device control coverage ====================

func TestIssue74GB28181PTZControl(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	const (
		deviceID  = "34020000001110000001"
		channelID = "34020000001320000001"
	)
	f.seed(deviceID, gb74Channel(channelID, "Front door"))
	params := map[string]string{"deviceID": deviceID, "channelID": channelID}

	// Success: a valid continuous move command reaches the device.
	rr := gb74Call(h.handleGB28181PTZControl, "POST", "/api/gb28181/devices/x/channels/y/ptz",
		`{"direction":"up","speed":1}`, params)
	gb74Status(t, rr, http.StatusOK)
	if body := gb74JSON(t, rr); body["status"] != "ok" {
		t.Fatalf("expected ok status, got %v", body)
	}
	if f.peer.seen() == 0 {
		t.Fatal("expected the fake SIP f.peer to receive the PTZ MESSAGE")
	}

	// Stop command uses the stop code.
	rr = gb74Call(h.handleGB28181PTZControl, "POST", "/ptz", `{"direction":"stop"}`, params)
	gb74Status(t, rr, http.StatusOK)

	// Unknown direction is rejected by the PTZ command builder.
	rr = gb74Call(h.handleGB28181PTZControl, "POST", "/ptz", `{"direction":"sideways"}`, params)
	gb74Status(t, rr, http.StatusInternalServerError)

	// Malformed body.
	rr = gb74Call(h.handleGB28181PTZControl, "POST", "/ptz", `{`, params)
	gb74Status(t, rr, http.StatusBadRequest)

	// Missing route parameters.
	rr = gb74Call(h.handleGB28181PTZControl, "POST", "/ptz", `{"direction":"up"}`, nil)
	gb74Status(t, rr, http.StatusBadRequest)

	// Unavailable server.
	noServer := gb74NewHandler(f.db, nil, nil)
	rr = gb74Call(noServer.handleGB28181PTZControl, "POST", "/ptz", `{"direction":"up"}`, params)
	gb74Status(t, rr, http.StatusServiceUnavailable)

	// Unknown device -> 500 from the signaling layer.
	unknown := map[string]string{"deviceID": "34020000001110009999", "channelID": channelID}
	rr = gb74Call(h.handleGB28181PTZControl, "POST", "/ptz", `{"direction":"up"}`, unknown)
	gb74Status(t, rr, http.StatusInternalServerError)

	// Offline device -> 500.
	f.seedOffline("34020000001110008888")
	offline := map[string]string{"deviceID": "34020000001110008888", "channelID": channelID}
	rr = gb74Call(h.handleGB28181PTZControl, "POST", "/ptz", `{"direction":"up"}`, offline)
	gb74Status(t, rr, http.StatusInternalServerError)
}

func TestIssue74GB28181PTZPositionControl(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	const (
		deviceID  = "34020000001110000002"
		channelID = "34020000001320000002"
	)
	f.seed(deviceID, gb74Channel(channelID, "Position"))
	params := map[string]string{"deviceID": deviceID, "channelID": channelID}

	rr := gb74Call(h.handleGB28181PTZPositionControl, "POST", "/ptz/position",
		`{"horizontal_angle":90,"vertical_angle":45,"zoom_level":2}`, params)
	gb74Status(t, rr, http.StatusOK)

	// Angle out of range is rejected by the signaling layer.
	rr = gb74Call(h.handleGB28181PTZPositionControl, "POST", "/ptz/position",
		`{"horizontal_angle":400,"vertical_angle":45,"zoom_level":2}`, params)
	gb74Status(t, rr, http.StatusInternalServerError)

	rr = gb74Call(h.handleGB28181PTZPositionControl, "POST", "/ptz/position", `not-json`, params)
	gb74Status(t, rr, http.StatusBadRequest)

	rr = gb74Call(h.handleGB28181PTZPositionControl, "POST", "/ptz/position", `{}`, nil)
	gb74Status(t, rr, http.StatusBadRequest)

	noServer := gb74NewHandler(f.db, nil, nil)
	rr = gb74Call(noServer.handleGB28181PTZPositionControl, "POST", "/ptz/position", `{}`, params)
	gb74Status(t, rr, http.StatusServiceUnavailable)

	unknown := map[string]string{"deviceID": "34020000001110009999", "channelID": channelID}
	rr = gb74Call(h.handleGB28181PTZPositionControl, "POST", "/ptz/position", `{}`, unknown)
	gb74Status(t, rr, http.StatusInternalServerError)
}

func TestIssue74GB28181PTZQueryPosition(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	const (
		deviceID  = "34020000001110000003"
		channelID = "34020000001320000003"
	)
	f.seed(deviceID, gb74Channel(channelID, "Query"))
	params := map[string]string{"deviceID": deviceID, "channelID": channelID}

	rr := gb74Call(h.handleGB28181PTZQueryPosition, "GET", "/ptz/position", "", params)
	gb74Status(t, rr, http.StatusOK)

	rr = gb74Call(h.handleGB28181PTZQueryPosition, "GET", "/ptz/position", "", nil)
	gb74Status(t, rr, http.StatusBadRequest)

	noServer := gb74NewHandler(f.db, nil, nil)
	rr = gb74Call(noServer.handleGB28181PTZQueryPosition, "GET", "/ptz/position", "", params)
	gb74Status(t, rr, http.StatusServiceUnavailable)

	unknown := map[string]string{"deviceID": "34020000001110009999", "channelID": channelID}
	rr = gb74Call(h.handleGB28181PTZQueryPosition, "GET", "/ptz/position", "", unknown)
	gb74Status(t, rr, http.StatusInternalServerError)
}

func TestIssue74GB28181Presets(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	const (
		deviceID  = "34020000001110000004"
		channelID = "34020000001320000004"
	)
	f.seed(deviceID, gb74Channel(channelID, "Preset"))
	base := map[string]string{"deviceID": deviceID, "channelID": channelID, "presetID": "5"}

	rr := gb74Call(h.handleGB28181PresetSet, "POST", "/preset/5", "", base)
	gb74Status(t, rr, http.StatusOK)
	rr = gb74Call(h.handleGB28181PresetCall, "POST", "/preset/5/call", "", base)
	gb74Status(t, rr, http.StatusOK)
	rr = gb74Call(h.handleGB28181PresetDelete, "DELETE", "/preset/5", "", base)
	gb74Status(t, rr, http.StatusOK)

	// Route/parameter validation.
	for _, tc := range []struct {
		name   string
		params map[string]string
	}{
		{"missing preset", map[string]string{"deviceID": deviceID, "channelID": channelID}},
		{"non numeric preset", map[string]string{"deviceID": deviceID, "channelID": channelID, "presetID": "abc"}},
		{"preset out of range", map[string]string{"deviceID": deviceID, "channelID": channelID, "presetID": "300"}},
		{"preset zero", map[string]string{"deviceID": deviceID, "channelID": channelID, "presetID": "0"}},
		{"missing device", map[string]string{"channelID": channelID, "presetID": "5"}},
	} {
		t.Run("set/"+tc.name, func(t *testing.T) {
			rr := gb74Call(h.handleGB28181PresetSet, "POST", "/preset", "", tc.params)
			gb74Status(t, rr, http.StatusBadRequest)
		})
		t.Run("call/"+tc.name, func(t *testing.T) {
			rr := gb74Call(h.handleGB28181PresetCall, "POST", "/preset", "", tc.params)
			gb74Status(t, rr, http.StatusBadRequest)
		})
		t.Run("delete/"+tc.name, func(t *testing.T) {
			rr := gb74Call(h.handleGB28181PresetDelete, "DELETE", "/preset", "", tc.params)
			gb74Status(t, rr, http.StatusBadRequest)
		})
	}

	// Signaling failure: unknown device.
	unknown := map[string]string{"deviceID": "34020000001110009999", "channelID": channelID, "presetID": "5"}
	rr = gb74Call(h.handleGB28181PresetSet, "POST", "/preset/5", "", unknown)
	gb74Status(t, rr, http.StatusInternalServerError)
	rr = gb74Call(h.handleGB28181PresetCall, "POST", "/preset/5/call", "", unknown)
	gb74Status(t, rr, http.StatusInternalServerError)
	rr = gb74Call(h.handleGB28181PresetDelete, "DELETE", "/preset/5", "", unknown)
	gb74Status(t, rr, http.StatusInternalServerError)

	// Unavailable server.
	noServer := gb74NewHandler(f.db, nil, nil)
	rr = gb74Call(noServer.handleGB28181PresetSet, "POST", "/preset/5", "", base)
	gb74Status(t, rr, http.StatusServiceUnavailable)
	rr = gb74Call(noServer.handleGB28181PresetCall, "POST", "/preset/5/call", "", base)
	gb74Status(t, rr, http.StatusServiceUnavailable)
	rr = gb74Call(noServer.handleGB28181PresetDelete, "DELETE", "/preset/5", "", base)
	gb74Status(t, rr, http.StatusServiceUnavailable)
}

func TestIssue74GB28181CruiseControl(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	const (
		deviceID  = "34020000001110000005"
		channelID = "34020000001320000005"
	)
	f.seed(deviceID, gb74Channel(channelID, "Cruise"))
	base := map[string]string{"deviceID": deviceID, "channelID": channelID, "cruiseID": "1"}

	rr := gb74Call(h.handleGB28181CruiseStart, "POST", "/cruise/1/start", "", base)
	gb74Status(t, rr, http.StatusOK)
	rr = gb74Call(h.handleGB28181CruiseStop, "POST", "/cruise/1/stop", "", base)
	gb74Status(t, rr, http.StatusOK)

	body := `{"cruise_id":1,"preset_id":3}`
	rr = gb74Call(h.handleGB28181CruiseAddPoint, "POST", "/cruise/point", body, base)
	gb74Status(t, rr, http.StatusOK)
	rr = gb74Call(h.handleGB28181CruiseDeletePoint, "DELETE", "/cruise/point", body, base)
	gb74Status(t, rr, http.StatusOK)

	// Validation failures.
	rr = gb74Call(h.handleGB28181CruiseAddPoint, "POST", "/cruise/point", `{"cruise_id":300,"preset_id":3}`, base)
	gb74Status(t, rr, http.StatusBadRequest)
	rr = gb74Call(h.handleGB28181CruiseAddPoint, "POST", "/cruise/point", `{"cruise_id":1,"preset_id":0}`, base)
	gb74Status(t, rr, http.StatusBadRequest)
	rr = gb74Call(h.handleGB28181CruiseAddPoint, "POST", "/cruise/point", `{`, base)
	gb74Status(t, rr, http.StatusBadRequest)
	rr = gb74Call(h.handleGB28181CruiseAddPoint, "POST", "/cruise/point", `{"cruise_id":0}`, nil)
	gb74Status(t, rr, http.StatusBadRequest)

	rr = gb74Call(h.handleGB28181CruiseDeletePoint, "DELETE", "/cruise/point", `{"cruise_id":300,"preset_id":3}`, base)
	gb74Status(t, rr, http.StatusBadRequest)
	rr = gb74Call(h.handleGB28181CruiseDeletePoint, "DELETE", "/cruise/point", `{"cruise_id":1,"preset_id":300}`, base)
	gb74Status(t, rr, http.StatusBadRequest)
	rr = gb74Call(h.handleGB28181CruiseDeletePoint, "DELETE", "/cruise/point", `{`, base)
	gb74Status(t, rr, http.StatusBadRequest)
	rr = gb74Call(h.handleGB28181CruiseDeletePoint, "DELETE", "/cruise/point", `{}`, nil)
	gb74Status(t, rr, http.StatusBadRequest)

	// Cruise start/stop validation.
	for _, tc := range []struct {
		name   string
		params map[string]string
	}{
		{"missing cruise id", map[string]string{"deviceID": deviceID, "channelID": channelID}},
		{"non numeric cruise id", map[string]string{"deviceID": deviceID, "channelID": channelID, "cruiseID": "x"}},
		{"cruise id out of range", map[string]string{"deviceID": deviceID, "channelID": channelID, "cruiseID": "999"}},
		{"missing device", map[string]string{"channelID": channelID, "cruiseID": "1"}},
	} {
		t.Run("start/"+tc.name, func(t *testing.T) {
			rr := gb74Call(h.handleGB28181CruiseStart, "POST", "/cruise", "", tc.params)
			gb74Status(t, rr, http.StatusBadRequest)
		})
		t.Run("stop/"+tc.name, func(t *testing.T) {
			rr := gb74Call(h.handleGB28181CruiseStop, "POST", "/cruise", "", tc.params)
			gb74Status(t, rr, http.StatusBadRequest)
		})
	}

	// Signaling failures for unknown devices.
	unknown := map[string]string{"deviceID": "34020000001110009999", "channelID": channelID, "cruiseID": "1"}
	rr = gb74Call(h.handleGB28181CruiseStart, "POST", "/cruise/1/start", "", unknown)
	gb74Status(t, rr, http.StatusInternalServerError)
	rr = gb74Call(h.handleGB28181CruiseStop, "POST", "/cruise/1/stop", "", unknown)
	gb74Status(t, rr, http.StatusInternalServerError)
	unknownBody := `{"cruise_id":1,"preset_id":3}`
	rr = gb74Call(h.handleGB28181CruiseAddPoint, "POST", "/cruise/point", unknownBody, unknown)
	gb74Status(t, rr, http.StatusInternalServerError)
	rr = gb74Call(h.handleGB28181CruiseDeletePoint, "DELETE", "/cruise/point", unknownBody, unknown)
	gb74Status(t, rr, http.StatusInternalServerError)

	// Unavailable server.
	noServer := gb74NewHandler(f.db, nil, nil)
	rr = gb74Call(noServer.handleGB28181CruiseStart, "POST", "/cruise/1/start", "", base)
	gb74Status(t, rr, http.StatusServiceUnavailable)
	rr = gb74Call(noServer.handleGB28181CruiseStop, "POST", "/cruise/1/stop", "", base)
	gb74Status(t, rr, http.StatusServiceUnavailable)
	rr = gb74Call(noServer.handleGB28181CruiseAddPoint, "POST", "/cruise/point", body, base)
	gb74Status(t, rr, http.StatusServiceUnavailable)
	rr = gb74Call(noServer.handleGB28181CruiseDeletePoint, "DELETE", "/cruise/point", body, base)
	gb74Status(t, rr, http.StatusServiceUnavailable)
}

func TestIssue74GB28181DeviceControl(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	const (
		deviceID  = "34020000001110000006"
		channelID = "34020000001320000006"
	)
	f.seed(deviceID, gb74Channel(channelID, "Control"))
	params := map[string]string{"deviceID": deviceID, "channelID": channelID}

	// snapshot / reset / record / home-position success paths.
	gb74Status(t, gb74Call(h.handleGB28181Snapshot, "POST", "/snapshot", "", params), http.StatusOK)
	gb74Status(t, gb74Call(h.handleGB28181DeviceReset, "POST", "/reset", "", params), http.StatusOK)
	gb74Status(t, gb74Call(h.handleGB28181RecordControl, "POST", "/record", `{"command":"record"}`, params), http.StatusOK)
	gb74Status(t, gb74Call(h.handleGB28181RecordControl, "POST", "/record", `{"command":"stop"}`, params), http.StatusOK)
	gb74Status(t, gb74Call(h.handleGB28181HomePosition, "POST", "/home-position",
		`{"enabled":1,"reset_time":30,"preset_index":2}`, params), http.StatusOK)
	gb74Status(t, gb74Call(h.handleGB28181DeviceInfoQuery, "GET", "/info", "", params), http.StatusOK)
	gb74Status(t, gb74Call(h.handleGB28181DeviceStatusQuery, "GET", "/status", "", params), http.StatusOK)
	gb74Status(t, gb74Call(h.handleGB28181DeviceConfigQuery, "GET", "/config", "", params), http.StatusOK)

	// Validation failures.
	gb74Status(t, gb74Call(h.handleGB28181Snapshot, "POST", "/snapshot", "", nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181DeviceReset, "POST", "/reset", "", nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181DeviceInfoQuery, "GET", "/info", "", nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181DeviceStatusQuery, "GET", "/status", "", nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181DeviceConfigQuery, "GET", "/config", "", nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181RecordControl, "POST", "/record", `{`, params), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181RecordControl, "POST", "/record", `{"command":"pause"}`, params), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181RecordControl, "POST", "/record", `{"command":"record"}`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181HomePosition, "POST", "/home-position", `{`, params), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181HomePosition, "POST", "/home-position",
		`{"enabled":2,"preset_index":2}`, params), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181HomePosition, "POST", "/home-position",
		`{"enabled":1,"preset_index":0}`, params), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181HomePosition, "POST", "/home-position",
		`{"enabled":1,"preset_index":2}`, nil), http.StatusBadRequest)

	// Signaling failures (unknown device).
	unknown := map[string]string{"deviceID": "34020000001110009999", "channelID": channelID}
	gb74Status(t, gb74Call(h.handleGB28181Snapshot, "POST", "/snapshot", "", unknown), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGB28181DeviceReset, "POST", "/reset", "", unknown), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGB28181RecordControl, "POST", "/record", `{"command":"record"}`, unknown), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGB28181HomePosition, "POST", "/home-position",
		`{"enabled":1,"reset_time":10,"preset_index":1}`, unknown), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGB28181DeviceInfoQuery, "GET", "/info", "", unknown), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGB28181DeviceStatusQuery, "GET", "/status", "", unknown), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGB28181DeviceConfigQuery, "GET", "/config", "", unknown), http.StatusInternalServerError)

	// Unavailable server.
	noServer := gb74NewHandler(f.db, nil, nil)
	gb74Status(t, gb74Call(noServer.handleGB28181Snapshot, "POST", "/snapshot", "", params), http.StatusServiceUnavailable)
	gb74Status(t, gb74Call(noServer.handleGB28181DeviceReset, "POST", "/reset", "", params), http.StatusServiceUnavailable)
	gb74Status(t, gb74Call(noServer.handleGB28181RecordControl, "POST", "/record", `{"command":"record"}`, params), http.StatusServiceUnavailable)
	gb74Status(t, gb74Call(noServer.handleGB28181HomePosition, "POST", "/home-position",
		`{"enabled":1,"preset_index":1}`, params), http.StatusServiceUnavailable)
	gb74Status(t, gb74Call(noServer.handleGB28181DeviceInfoQuery, "GET", "/info", "", params), http.StatusServiceUnavailable)
	gb74Status(t, gb74Call(noServer.handleGB28181DeviceStatusQuery, "GET", "/status", "", params), http.StatusServiceUnavailable)
	gb74Status(t, gb74Call(noServer.handleGB28181DeviceConfigQuery, "GET", "/config", "", params), http.StatusServiceUnavailable)

	// Record control uses the configured 2022 device version when the DB says so.
	if err := f.db.UpsertGB28181Device(context.Background(), &storage.GB28181DeviceRow{
		DeviceID: deviceID, GBVersion: string(gb28181.GBVersion2022), Name: "2022 device",
	}); err != nil {
		t.Fatalf("seed gb28181 device row: %v", err)
	}
	gb74Status(t, gb74Call(h.handleGB28181RecordControl, "POST", "/record", `{"command":"record"}`, params), http.StatusOK)
	gb74Status(t, gb74Call(h.handleGB28181PTZControl, "POST", "/ptz", `{"direction":"left"}`, params), http.StatusOK)
}

// ==================== device list / delete ====================

func TestIssue74GB28181ListAndDeleteDevices(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	rr := gb74Call(h.handleGB28181ListDevices, "GET", "/api/gb28181/devices", "", nil)
	gb74Status(t, rr, http.StatusOK)
	if _, ok := gb74JSON(t, rr)["devices"]; !ok {
		t.Fatal("expected devices key")
	}

	noServer := gb74NewHandler(f.db, nil, nil)
	rr = gb74Call(noServer.handleGB28181ListDevices, "GET", "/api/gb28181/devices", "", nil)
	gb74Status(t, rr, http.StatusOK)

	// Delete requires a device ID and an offline device.
	rr = gb74Call(h.handleGB28181DeleteDevice, "DELETE", "/api/gb28181/devices/", "", nil)
	gb74Status(t, rr, http.StatusBadRequest)

	rr = gb74Call(h.handleGB28181DeleteDevice, "DELETE", "/api/gb28181/devices/34020000001110007777", "",
		map[string]string{"deviceID": "34020000001110007777"})
	gb74Status(t, rr, http.StatusNotFound)

	// An online device cannot be removed.
	f.seed("34020000001110007771", gb74Channel("34020000001320007771", "Online"))
	rr = gb74Call(h.handleGB28181DeleteDevice, "DELETE", "/api/gb28181/devices/34020000001110007771", "",
		map[string]string{"deviceID": "34020000001110007771"})
	gb74Status(t, rr, http.StatusConflict)

	// An offline device with no channels can be removed.
	f.seedOffline("34020000001110007772")
	rr = gb74Call(h.handleGB28181DeleteDevice, "DELETE", "/api/gb28181/devices/34020000001110007772", "",
		map[string]string{"deviceID": "34020000001110007772"})
	gb74Status(t, rr, http.StatusOK)

	noServerRR := gb74Call(noServer.handleGB28181DeleteDevice, "DELETE", "/api/gb28181/devices/x", "",
		map[string]string{"deviceID": "34020000001110007773"})
	gb74Status(t, noServerRR, http.StatusServiceUnavailable)
}

// ==================== play / playback lifecycle ====================

func TestIssue74GB28181PlayLifecycle(t *testing.T) {
	f := gb74NewFixture(t, gb74NewMediaEngine())
	h := f.handler()

	const (
		deviceID  = "34020000001110000010"
		channelID = "34020000001320000010"
	)
	f.seed(deviceID, gb74Channel(channelID, "Lobby cam"))
	playBody := fmt.Sprintf(`{"device_id":%q,"channel_id":%q}`, deviceID, channelID)

	// Validation.
	gb74Status(t, gb74Call(h.handleGB28181Play, "POST", "/play", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181Play, "POST", "/play", `{"device_id":""}`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181Play, "POST", "/play",
		fmt.Sprintf(`{"device_id":%q,"channel_id":"34020000001320009999"}`, deviceID), nil), http.StatusInternalServerError)

	// Success: the SIP INVITE is answered by the fake f.peer and a camera row is created.
	rr := gb74Call(h.handleGB28181Play, "POST", "/play", playBody, nil)
	gb74Status(t, rr, http.StatusOK)
	body := gb74JSON(t, rr)
	streamID, _ := body["stream_id"].(string)
	if streamID != gb28181.StreamID(deviceID, channelID) {
		t.Fatalf("unexpected stream_id %v", body["stream_id"])
	}
	if ssrc, _ := body["ssrc"].(string); ssrc == "" {
		t.Fatalf("expected ssrc, got %v", body)
	}
	cam, err := f.db.GetCamera(context.Background(), streamID)
	if err != nil || cam == nil {
		t.Fatalf("expected GB28181 camera to be registered: %v %v", cam, err)
	}
	if cam.Name != "Lobby cam" {
		t.Fatalf("expected the GB28181 channel name, got %q", cam.Name)
	}

	// A second play refreshes the existing camera row (default-name update path).
	gb74Status(t, gb74Call(h.handleGB28181Play, "POST", "/play", playBody, nil), http.StatusOK)

	// Explicit stream_id is honoured.
	custom := fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"stream_id":"gb74-custom-stream"}`, deviceID, channelID)
	rr = gb74Call(h.handleGB28181Play, "POST", "/play", custom, nil)
	gb74Status(t, rr, http.StatusOK)
	if got := gb74JSON(t, rr)["stream_id"]; got != "gb74-custom-stream" {
		t.Fatalf("expected explicit stream_id, got %v", got)
	}

	// Ensure on a channel without a session: the media readiness query fails -> 503.
	const (
		deviceID2  = "34020000001110000011"
		channelID2 = "34020000001320000011"
	)
	f.seed(deviceID2, gb74Channel(channelID2, "Ensure target"))
	ensureBody := fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"ensure":true}`, deviceID2, channelID2)
	f.engine.streamErr = errors.New("media f.engine unavailable")
	gb74Status(t, gb74Call(h.handleGB28181Play, "POST", "/play", ensureBody, nil), http.StatusServiceUnavailable)

	// Ensure again with a publishing stream reports "ready".
	f.engine.streamErr = nil
	f.engine.streamInfo = &media.StreamInfo{
		StreamID:      gb28181.StreamID(deviceID2, channelID2),
		Active:        true,
		Publisher:     &media.SessionInfo{SessionID: "pub"},
		InFPS:         25,
		LastFrameTime: time.Now(),
	}
	rr = gb74Call(h.handleGB28181Play, "POST", "/play", ensureBody, nil)
	gb74Status(t, rr, http.StatusOK)
	if state := gb74JSON(t, rr)["state"]; state != "ready" {
		t.Fatalf("expected ready state, got %v", state)
	}

	// An active but not yet publishing stream reports "starting".
	f.engine.streamInfo = &media.StreamInfo{StreamID: gb28181.StreamID(deviceID2, channelID2), Active: true}
	rr = gb74Call(h.handleGB28181Play, "POST", "/play", ensureBody, nil)
	gb74Status(t, rr, http.StatusOK)
	if state := gb74JSON(t, rr)["state"]; state != "starting" {
		t.Fatalf("expected starting state, got %v", state)
	}

	// Stop the session (BYE + RTP teardown).
	gb74Status(t, gb74Call(h.handleGB28181StopPlay, "POST", "/stop", playBody, nil), http.StatusOK)
	// Stopping an unknown session is a no-op.
	gb74Status(t, gb74Call(h.handleGB28181StopPlay, "POST", "/stop",
		fmt.Sprintf(`{"device_id":%q,"channel_id":"34020000001320009999"}`, deviceID), nil), http.StatusOK)
	gb74Status(t, gb74Call(h.handleGB28181StopPlay, "POST", "/stop", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181StopPlay, "POST", "/stop", `{}`, nil), http.StatusBadRequest)

	// Unavailable server.
	noServer := gb74NewHandler(f.db, f.engine, nil)
	gb74Status(t, gb74Call(noServer.handleGB28181Play, "POST", "/play", playBody, nil), http.StatusServiceUnavailable)
	gb74Status(t, gb74Call(noServer.handleGB28181StopPlay, "POST", "/stop", playBody, nil), http.StatusServiceUnavailable)
}

func TestIssue74GB28181EnsureCameraFallbacks(t *testing.T) {
	f := gb74NewFixture(t, gb74NewMediaEngine())

	const (
		deviceID  = "34020000001110000012"
		channelID = "34020000001320000012"
	)
	f.seed(deviceID, gb74Channel(channelID, ""))
	playBody := fmt.Sprintf(`{"device_id":%q,"channel_id":%q}`, deviceID, channelID)

	// Media f.engine without play URLs: the camera falls back to the loopback URL.
	emptyEngine := gb74NewMediaEngine()
	emptyEngine.emptyPlayURL = true
	hEmpty := gb74NewHandler(f.db, emptyEngine, f.srv)
	rr := gb74Call(hEmpty.handleGB28181Play, "POST", "/play", playBody, nil)
	gb74Status(t, rr, http.StatusOK)
	streamID := gb28181.StreamID(deviceID, channelID)
	cam, err := f.db.GetCamera(context.Background(), streamID)
	if err != nil || cam == nil {
		t.Fatalf("expected camera row: %v %v", cam, err)
	}
	if !strings.HasPrefix(cam.URL, "rtsp://127.0.0.1:5544/live/") {
		t.Fatalf("expected fallback camera URL, got %q", cam.URL)
	}
	// The default name is used because the channel has no name.
	if want := "GB28181 " + channelID; cam.Name != want {
		t.Fatalf("expected default camera name %q, got %q", want, cam.Name)
	}

	// Archived camera rows are unarchived on play.
	const (
		deviceID3  = "34020000001110000013"
		channelID3 = "34020000001320000013"
	)
	f.seed(deviceID3, gb74Channel(channelID3, "Archived cam"))
	archivedID := gb28181.StreamID(deviceID3, channelID3)
	if err := f.db.UpsertCamera(context.Background(), archivedID, "Archived cam", "gb28181", "h264",
		"rtsp://127.0.0.1:5544/live/old", "", "", true, "", "", ""); err != nil {
		t.Fatalf("seed archived camera: %v", err)
	}
	if err := f.db.ArchiveCameraDB(context.Background(), archivedID); err != nil {
		t.Fatalf("archive camera: %v", err)
	}
	h := gb74NewHandler(f.db, f.engine, f.srv)
	rr = gb74Call(h.handleGB28181Play, "POST", "/play",
		fmt.Sprintf(`{"device_id":%q,"channel_id":%q}`, deviceID3, channelID3), nil)
	gb74Status(t, rr, http.StatusOK)
	cam, err = f.db.GetCamera(context.Background(), archivedID)
	if err != nil || cam == nil {
		t.Fatalf("expected archived camera row: %v %v", cam, err)
	}
	if cam.Archived {
		t.Fatal("expected the archived camera to be restored")
	}

	// A handler without a database still starts play; camera registration is skipped.
	hNoDB := gb74NewHandler(nil, f.engine, f.srv)
	gb74Status(t, gb74Call(hNoDB.handleGB28181Play, "POST", "/play", playBody, nil), http.StatusOK)
}

func TestIssue74GB28181PlaybackControls(t *testing.T) {
	f := gb74NewFixture(t, gb74NewMediaEngine())
	h := f.handler()

	const (
		deviceID  = "34020000001110000020"
		channelID = "34020000001320000020"
	)
	f.seed(deviceID, gb74Channel(channelID, "Playback"))
	playbackBody := fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"start_time":"2024-01-01T00:00:00Z","end_time":"2024-01-01T01:00:00Z"}`, deviceID, channelID)
	controlBody := fmt.Sprintf(`{"device_id":%q,"channel_id":%q}`, deviceID, channelID)

	// Validation.
	gb74Status(t, gb74Call(h.handleGB28181Playback, "POST", "/playback", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181Playback, "POST", "/playback", `{"device_id":""}`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181Playback, "POST", "/playback",
		fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"start_time":"nope","end_time":"2024-01-01"}`, deviceID, channelID), nil),
		http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181Playback, "POST", "/playback",
		fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"start_time":"2024-01-01","end_time":"nope"}`, deviceID, channelID), nil),
		http.StatusBadRequest)

	// Playback for an unknown channel is rejected by the signaling layer.
	gb74Status(t, gb74Call(h.handleGB28181Playback, "POST", "/playback",
		fmt.Sprintf(`{"device_id":%q,"channel_id":"34020000001320009999","start_time":"2024-01-01","end_time":"2024-01-02"}`, deviceID), nil),
		http.StatusInternalServerError)

	// Success: INVITE is answered, play URLs are built for every supported protocol.
	rr := gb74Call(h.handleGB28181Playback, "POST", "/playback", playbackBody, nil)
	gb74Status(t, rr, http.StatusOK)
	body := gb74JSON(t, rr)
	urls, _ := body["urls"].([]any)
	if len(urls) == 0 {
		t.Fatalf("expected play URLs, got %v", body)
	}

	// Explicit stream ID plus an f.engine that exposes no URLs.
	explicit := fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"stream_id":"gb74-pb","start_time":"2024-01-01","end_time":"2024-01-02"}`, deviceID, channelID)
	gb74Status(t, gb74Call(h.handleGB28181Playback, "POST", "/playback", explicit, nil), http.StatusOK)

	// A handler without a media f.engine returns no URLs.
	hNoEngine := gb74NewHandler(f.db, nil, f.srv)
	rr = gb74Call(hNoEngine.handleGB28181Playback, "POST", "/playback", explicit, nil)
	gb74Status(t, rr, http.StatusOK)
	if urls, ok := gb74JSON(t, rr)["urls"]; ok && urls != nil {
		if list, isList := urls.([]any); isList && len(list) > 0 {
			t.Fatalf("expected no URLs without a media f.engine, got %v", list)
		}
	}

	// Speed / seek / pause / resume against the active playback session.
	gb74Status(t, gb74Call(h.handleGB28181PlaySpeed, "POST", "/playback/speed",
		fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"speed":2}`, deviceID, channelID), nil), http.StatusOK)
	gb74Status(t, gb74Call(h.handleGB28181PlaySeek, "POST", "/playback/seek",
		fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"seek_time":100}`, deviceID, channelID), nil), http.StatusOK)
	gb74Status(t, gb74Call(h.handleGB28181PlayPause, "POST", "/playback/pause", controlBody, nil), http.StatusOK)
	gb74Status(t, gb74Call(h.handleGB28181PlayResume, "POST", "/playback/resume", controlBody, nil), http.StatusOK)

	// Validation for each control endpoint.
	gb74Status(t, gb74Call(h.handleGB28181PlaySpeed, "POST", "/playback/speed", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181PlaySpeed, "POST", "/playback/speed", `{"device_id":""}`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181PlaySpeed, "POST", "/playback/speed",
		fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"speed":3}`, deviceID, channelID), nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181PlaySeek, "POST", "/playback/seek", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181PlaySeek, "POST", "/playback/seek", `{}`, nil), http.StatusBadRequest)
	for _, handler := range []http.HandlerFunc{h.handleGB28181PlayPause, h.handleGB28181PlayResume} {
		gb74Status(t, gb74Call(handler, "POST", "/playback", `{`, nil), http.StatusBadRequest)
		gb74Status(t, gb74Call(handler, "POST", "/playback", `{}`, nil), http.StatusBadRequest)
	}

	// Controls without an active session report a signaling error.
	unknown := fmt.Sprintf(`{"device_id":%q,"channel_id":"34020000001320009998","speed":1}`, deviceID)
	gb74Status(t, gb74Call(h.handleGB28181PlaySpeed, "POST", "/playback/speed", unknown, nil), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGB28181PlaySeek, "POST", "/playback/seek",
		fmt.Sprintf(`{"device_id":%q,"channel_id":"34020000001320009998","seek_time":5}`, deviceID), nil), http.StatusInternalServerError)
	for _, handler := range []http.HandlerFunc{h.handleGB28181PlayPause, h.handleGB28181PlayResume} {
		gb74Status(t, gb74Call(handler, "POST", "/playback",
			fmt.Sprintf(`{"device_id":%q,"channel_id":"34020000001320009998"}`, deviceID), nil), http.StatusInternalServerError)
	}

	// Unavailable server for every playback endpoint.
	noServer := gb74NewHandler(f.db, f.engine, nil)
	gb74Status(t, gb74Call(noServer.handleGB28181Playback, "POST", "/playback", playbackBody, nil), http.StatusServiceUnavailable)
	gb74Status(t, gb74Call(noServer.handleGB28181PlaySpeed, "POST", "/playback/speed", controlBody, nil), http.StatusServiceUnavailable)
	gb74Status(t, gb74Call(noServer.handleGB28181PlaySeek, "POST", "/playback/seek", controlBody, nil), http.StatusServiceUnavailable)
	gb74Status(t, gb74Call(noServer.handleGB28181PlayPause, "POST", "/playback/pause", controlBody, nil), http.StatusServiceUnavailable)
	gb74Status(t, gb74Call(noServer.handleGB28181PlayResume, "POST", "/playback/resume", controlBody, nil), http.StatusServiceUnavailable)
}

func TestIssue74GB28181RecordInfoValidation(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	const (
		deviceID  = "34020000001110000021"
		channelID = "34020000001320000021"
	)
	f.seed(deviceID, gb74Channel(channelID, "Records"))

	gb74Status(t, gb74Call(h.handleGB28181RecordInfo, "POST", "/record_info", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181RecordInfo, "POST", "/record_info", `{}`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181RecordInfo, "POST", "/record_info",
		fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"start_time":"x","end_time":"2024-01-01"}`, deviceID, channelID), nil),
		http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181RecordInfo, "POST", "/record_info",
		fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"start_time":"2024-01-01","end_time":"x"}`, deviceID, channelID), nil),
		http.StatusBadRequest)
	// Unknown device fails fast inside the signaling layer (no 10s query wait).
	gb74Status(t, gb74Call(h.handleGB28181RecordInfo, "POST", "/record_info",
		fmt.Sprintf(`{"device_id":"34020000001110009999","channel_id":%q,"start_time":"2024-01-01","end_time":"2024-01-02"}`, channelID), nil),
		http.StatusInternalServerError)

	noServer := gb74NewHandler(f.db, nil, nil)
	gb74Status(t, gb74Call(noServer.handleGB28181RecordInfo, "POST", "/record_info", `{}`, nil), http.StatusServiceUnavailable)
}

// ==================== upstream platforms ====================

func TestIssue74GB28181Platforms(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	// Empty platform list.
	rr := gb74Call(h.handleGB28181ListPlatforms, "GET", "/api/gb28181/platforms", "", nil)
	gb74Status(t, rr, http.StatusOK)
	if list, _ := gb74JSON(t, rr)["platforms"].([]any); len(list) != 0 {
		t.Fatalf("expected empty platform list, got %v", list)
	}

	// Validation.
	gb74Status(t, gb74Call(h.handleGB28181AddPlatform, "POST", "/api/gb28181/platforms", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181AddPlatform, "POST", "/api/gb28181/platforms", `{"server_ip":"127.0.0.1"}`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181AddPlatform, "POST", "/api/gb28181/platforms", `{"server_gb_id":"x"}`, nil), http.StatusBadRequest)

	// Minimal body: every optional field falls back to the server config.
	rr = gb74Call(h.handleGB28181AddPlatform, "POST", "/api/gb28181/platforms",
		`{"server_gb_id":"34020000002000000002","server_ip":"127.0.0.1"}`, nil)
	gb74Status(t, rr, http.StatusOK)
	created := gb74JSON(t, rr)
	platformID, _ := created["id"].(float64)
	if platformID <= 0 {
		t.Fatalf("expected a created platform id, got %v", created)
	}

	// Full body: explicit values are stored as given.
	rr = gb74Call(h.handleGB28181AddPlatform, "POST", "/api/gb28181/platforms",
		`{"name":"Upstream","enable":false,"server_gb_id":"34020000002000000003","server_gb_domain":"3402000000",
		  "server_ip":"10.0.0.1","server_port":5061,"device_gb_id":"34020000002000000004",
		  "device_gb_domain":"3402000000","device_ip":"10.0.0.2","device_port":5062,
		  "username":"u","password":"p","transport":"TCP","character_set":"GB2312",
		  "expires":1800,"keep_timeout":30,"max_timeout_count":5}`, nil)
	gb74Status(t, rr, http.StatusOK)

	rr = gb74Call(h.handleGB28181ListPlatforms, "GET", "/api/gb28181/platforms", "", nil)
	gb74Status(t, rr, http.StatusOK)
	list, _ := gb74JSON(t, rr)["platforms"].([]any)
	if len(list) != 2 {
		t.Fatalf("expected two platforms, got %v", list)
	}

	// Delete validation and success.
	gb74Status(t, gb74Call(h.handleGB28181DeletePlatform, "DELETE", "/api/gb28181/platforms?id=abc", "", nil), http.StatusBadRequest)
	rr = gb74Call(h.handleGB28181DeletePlatform, "DELETE",
		fmt.Sprintf("/api/gb28181/platforms?id=%d", int64(platformID)), "", nil)
	gb74Status(t, rr, http.StatusOK)
	rr = gb74Call(h.handleGB28181ListPlatforms, "GET", "/api/gb28181/platforms", "", nil)
	list, _ = gb74JSON(t, rr)["platforms"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected one platform after delete, got %v", list)
	}

	// Unavailable server for every platform endpoint.
	noServer := gb74NewHandler(f.db, nil, nil)
	gb74Status(t, gb74Call(noServer.handleGB28181ListPlatforms, "GET", "/api/gb28181/platforms", "", nil), http.StatusOK)
	gb74Status(t, gb74Call(noServer.handleGB28181AddPlatform, "POST", "/api/gb28181/platforms", `{}`, nil), http.StatusServiceUnavailable)
	gb74Status(t, gb74Call(noServer.handleGB28181DeletePlatform, "DELETE", "/api/gb28181/platforms?id=1", "", nil), http.StatusServiceUnavailable)

	// Storage failures surface as 500.
	if err := f.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	gb74Status(t, gb74Call(h.handleGB28181AddPlatform, "POST", "/api/gb28181/platforms",
		`{"server_gb_id":"34020000002000000009","server_ip":"127.0.0.1"}`, nil), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGB28181DeletePlatform, "DELETE", "/api/gb28181/platforms?id=1", "", nil), http.StatusInternalServerError)
}

// ==================== broadcast ====================

func TestIssue74GB28181Broadcast(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	const (
		deviceID  = "34020000001110000030"
		channelID = "34020000001320000030"
	)
	f.seed(deviceID, gb74Channel(channelID, "Broadcast"))
	body := fmt.Sprintf(`{"device_id":%q,"channel_id":%q}`, deviceID, channelID)

	rr := gb74Call(h.handleGB28181StartBroadcast, "POST", "/api/gb28181/broadcast/start", body, nil)
	gb74Status(t, rr, http.StatusOK)
	started := gb74JSON(t, rr)
	if started["session_id"] != deviceID+"_"+channelID {
		t.Fatalf("unexpected broadcast session: %v", started)
	}

	gb74Status(t, gb74Call(h.handleGB28181StopBroadcast, "POST", "/api/gb28181/broadcast/stop", body, nil), http.StatusOK)

	// Validation.
	gb74Status(t, gb74Call(h.handleGB28181StartBroadcast, "POST", "/api/gb28181/broadcast/start", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181StopBroadcast, "POST", "/api/gb28181/broadcast/stop", `{`, nil), http.StatusBadRequest)

	// Offline device and missing channel.
	f.seedOffline("34020000001110000031", gb74Channel("34020000001320000031", "Offline broadcast"))
	offline := `{"device_id":"34020000001110000031","channel_id":"34020000001320000031"}`
	gb74Status(t, gb74Call(h.handleGB28181StartBroadcast, "POST", "/api/gb28181/broadcast/start", offline, nil), http.StatusInternalServerError)
	missing := fmt.Sprintf(`{"device_id":%q,"channel_id":"34020000001320009999"}`, deviceID)
	gb74Status(t, gb74Call(h.handleGB28181StartBroadcast, "POST", "/api/gb28181/broadcast/start", missing, nil), http.StatusInternalServerError)

	// Stopping a session that does not exist is a no-op.
	gb74Status(t, gb74Call(h.handleGB28181StopBroadcast, "POST", "/api/gb28181/broadcast/stop", missing, nil), http.StatusOK)

	// Unavailable server.
	noServer := gb74NewHandler(f.db, nil, nil)
	gb74Status(t, gb74Call(noServer.handleGB28181StartBroadcast, "POST", "/api/gb28181/broadcast/start", body, nil), http.StatusServiceUnavailable)
	gb74Status(t, gb74Call(noServer.handleGB28181StopBroadcast, "POST", "/api/gb28181/broadcast/stop", body, nil), http.StatusServiceUnavailable)
}

// ==================== talk ====================

func TestIssue74GB28181Talk(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	const (
		deviceID  = "34020000001110000040"
		channelID = "34020000001320000040"
	)
	f.seed(deviceID, gb74Channel(channelID, "Talk"))
	body := fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"stream_name":"talk-stream"}`, deviceID, channelID)

	// Start (UDP), start again with an existing session, then stop.
	rr := gb74Call(h.handleGB28181StartTalkWhip, "POST", "/api/gb28181/talk/start", body, nil)
	gb74Status(t, rr, http.StatusOK)
	talk := gb74JSON(t, rr)
	if talk["session_id"] != deviceID+"_"+channelID {
		t.Fatalf("unexpected talk session: %v", talk)
	}
	gb74Status(t, gb74Call(h.handleGB28181StartTalkWhip, "POST", "/api/gb28181/talk/start", body, nil), http.StatusOK)
	gb74Status(t, gb74Call(h.handleGB28181StopTalkWhip, "POST", "/api/gb28181/talk/stop", body, nil), http.StatusOK)

	// TCP passive and active transports.
	for _, transport := range []string{"1", "2"} {
		payload := fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"transport":%q}`, deviceID, channelID, transport)
		rr := gb74Call(h.handleGB28181StartTalkWhip, "POST", "/api/gb28181/talk/start", payload, nil)
		gb74Status(t, rr, http.StatusOK)
		gb74Status(t, gb74Call(h.handleGB28181StopTalkWhip, "POST", "/api/gb28181/talk/stop", payload, nil), http.StatusOK)
	}

	// Validation.
	gb74Status(t, gb74Call(h.handleGB28181StartTalkWhip, "POST", "/api/gb28181/talk/start", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181StartTalkWhip, "POST", "/api/gb28181/talk/start", `{}`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181StopTalkWhip, "POST", "/api/gb28181/talk/stop", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181StopTalkWhip, "POST", "/api/gb28181/talk/stop", `{}`, nil), http.StatusBadRequest)

	// Offline device.
	f.seedOffline("34020000001110000041", gb74Channel("34020000001320000041", "Offline talk"))
	offline := `{"device_id":"34020000001110000041","channel_id":"34020000001320000041"}`
	gb74Status(t, gb74Call(h.handleGB28181StartTalkWhip, "POST", "/api/gb28181/talk/start", offline, nil), http.StatusInternalServerError)

	// Stopping a session that does not exist succeeds.
	gb74Status(t, gb74Call(h.handleGB28181StopTalkWhip, "POST", "/api/gb28181/talk/stop",
		fmt.Sprintf(`{"device_id":%q,"channel_id":"34020000001320009999"}`, deviceID), nil), http.StatusOK)

	// Unavailable server.
	noServer := gb74NewHandler(f.db, nil, nil)
	gb74Status(t, gb74Call(noServer.handleGB28181StartTalkWhip, "POST", "/api/gb28181/talk/start", body, nil), http.StatusServiceUnavailable)
	gb74Status(t, gb74Call(noServer.handleGB28181StopTalkWhip, "POST", "/api/gb28181/talk/stop", body, nil), http.StatusServiceUnavailable)
}

func TestIssue74GB28181TalkWebsocket(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	const (
		deviceID  = "34020000001110000042"
		channelID = "34020000001320000042"
	)
	f.seed(deviceID, gb74Channel(channelID, "Talk WS"))

	// Validation.
	gb74Status(t, gb74Call(h.handleGB28181TalkWS, "GET", "/api/gb28181/talk/ws", "", nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181TalkWS, "GET", "/api/gb28181/talk/ws?device_id="+deviceID, "", nil), http.StatusBadRequest)

	// A plain HTTP request cannot upgrade: the session is created and torn down.
	target := fmt.Sprintf("/api/gb28181/talk/ws?device_id=%s&channel_id=%s", deviceID, channelID)
	gb74Serve(h.handleGB28181TalkWS, gb74Request("GET", target, "", nil))

	// Same for the TCP transports.
	target = fmt.Sprintf("/api/gb28181/talk/ws?device_id=%s&channel_id=%s&transport=1", deviceID, channelID)
	gb74Serve(h.handleGB28181TalkWS, gb74Request("GET", target, "", nil))
	target = fmt.Sprintf("/api/gb28181/talk/ws?device_id=%s&channel_id=%s&transport=2", deviceID, channelID)
	gb74Serve(h.handleGB28181TalkWS, gb74Request("GET", target, "", nil))

	// An already running talk session is reused before the upgrade attempt.
	whipBody := fmt.Sprintf(`{"device_id":%q,"channel_id":%q}`, deviceID, channelID)
	gb74Status(t, gb74Call(h.handleGB28181StartTalkWhip, "POST", "/api/gb28181/talk/start", whipBody, nil), http.StatusOK)
	target = fmt.Sprintf("/api/gb28181/talk/ws?device_id=%s&channel_id=%s", deviceID, channelID)
	gb74Serve(h.handleGB28181TalkWS, gb74Request("GET", target, "", nil))

	// Offline device cannot start a talk session.
	f.seedOffline("34020000001110000043", gb74Channel("34020000001320000043", "Offline talk WS"))
	target = "/api/gb28181/talk/ws?device_id=34020000001110000043&channel_id=34020000001320000043"
	rr := gb74Serve(h.handleGB28181TalkWS, gb74Request("GET", target, "", nil))
	gb74Status(t, rr, http.StatusInternalServerError)

	// Unavailable server.
	noServer := gb74NewHandler(f.db, nil, nil)
	target = fmt.Sprintf("/api/gb28181/talk/ws?device_id=%s&channel_id=%s", deviceID, channelID)
	gb74Status(t, gb74Serve(noServer.handleGB28181TalkWS, gb74Request("GET", target, "", nil)), http.StatusServiceUnavailable)
}

// ==================== downloads ====================

func TestIssue74GB28181Downloads(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	const (
		deviceID  = "34020000001110000050"
		channelID = "34020000001320000050"
	)
	f.seed(deviceID, gb74Channel(channelID, "Download"))
	downloadBody := fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"start_time":"2024-01-01T00:00:00Z","end_time":"2024-01-01T01:00:00Z"}`, deviceID, channelID)

	// Validation.
	gb74Status(t, gb74Call(h.handleGB28181StartDownload, "POST", "/api/gb28181/download/start", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181StartDownload, "POST", "/api/gb28181/download/start",
		fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"start_time":"x","end_time":"2024-01-01"}`, deviceID, channelID), nil),
		http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181StartDownload, "POST", "/api/gb28181/download/start",
		fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"start_time":"2024-01-01","end_time":"x"}`, deviceID, channelID), nil),
		http.StatusBadRequest)

	// Unknown channel is rejected before any file is created.
	gb74Status(t, gb74Call(h.handleGB28181StartDownload, "POST", "/api/gb28181/download/start",
		fmt.Sprintf(`{"device_id":%q,"channel_id":"34020000001320009999","start_time":"2024-01-01","end_time":"2024-01-02"}`, deviceID), nil),
		http.StatusInternalServerError)

	// Stop: unknown sessions are a no-op, malformed bodies are rejected.
	gb74Status(t, gb74Call(h.handleGB28181StopDownload, "POST", "/api/gb28181/download/stop", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181StopDownload, "POST", "/api/gb28181/download/stop",
		fmt.Sprintf(`{"device_id":%q,"channel_id":%q,"download_id":42}`, deviceID, channelID), nil), http.StatusOK)

	// Batch download: per-segment validation and signaling errors are reported
	// instead of aborting the whole batch. The channel is unknown on purpose so
	// no download directory is created inside the repository.
	batch := fmt.Sprintf(`{"device_id":%q,"channel_id":"34020000001320009999","segments":[
		{"start_time":"x","end_time":"2024-01-01"},
		{"start_time":"2024-01-01","end_time":"x"},
		{"start_time":"2024-01-01","end_time":"2024-01-02"}]}`, deviceID)
	rr := gb74Call(h.handleGB28181BatchDownload, "POST", "/api/gb28181/download/batch", batch, nil)
	gb74Status(t, rr, http.StatusOK)
	batchBody := gb74JSON(t, rr)
	errorsList, _ := batchBody["errors"].([]any)
	if len(errorsList) != 3 {
		t.Fatalf("expected three segment errors, got %v", batchBody)
	}
	if total, _ := batchBody["total"].(float64); total != 0 {
		t.Fatalf("expected no started downloads, got %v", batchBody)
	}

	gb74Status(t, gb74Call(h.handleGB28181BatchDownload, "POST", "/api/gb28181/download/batch", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181BatchDownload, "POST", "/api/gb28181/download/batch", `{}`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGB28181BatchDownload, "POST", "/api/gb28181/download/batch",
		fmt.Sprintf(`{"device_id":%q,"channel_id":%q}`, deviceID, channelID), nil), http.StatusBadRequest)

	// Unavailable server.
	noServer := gb74NewHandler(f.db, nil, nil)
	gb74Status(t, gb74Call(noServer.handleGB28181StartDownload, "POST", "/api/gb28181/download/start", downloadBody, nil), http.StatusServiceUnavailable)
	gb74Status(t, gb74Call(noServer.handleGB28181StopDownload, "POST", "/api/gb28181/download/stop", `{}`, nil), http.StatusServiceUnavailable)
	gb74Status(t, gb74Call(noServer.handleGB28181BatchDownload, "POST", "/api/gb28181/download/batch", `{}`, nil), http.StatusServiceUnavailable)

	// Downloads listing.
	rr = gb74Call(h.handleGB28181ListDownloads, "GET", "/api/gb28181/downloads", "", nil)
	gb74Status(t, rr, http.StatusOK)
	if _, ok := gb74JSON(t, rr)["downloads"]; !ok {
		t.Fatal("expected downloads key")
	}
	if _, err := f.db.CreateDownload(context.Background(), &storage.DownloadRecordRow{
		DeviceID: deviceID, ChannelID: channelID, FilePath: "/tmp/gb74.mp4", Status: "downloading",
	}); err != nil {
		t.Fatalf("seed download row: %v", err)
	}
	rr = gb74Call(h.handleGB28181ListDownloads, "GET",
		"/api/gb28181/downloads?device_id=x&channel_id=y&limit=1&offset=0", "", nil)
	gb74Status(t, rr, http.StatusOK)
	rr = gb74Call(h.handleGB28181ListDownloads, "GET", "/api/gb28181/downloads?limit=abc&offset=xyz", "", nil)
	gb74Status(t, rr, http.StatusOK)

	// Listings without a database return empty collections.
	noDB := gb74NewHandler(nil, nil, f.srv)
	gb74Status(t, gb74Call(noDB.handleGB28181ListDownloads, "GET", "/api/gb28181/downloads", "", nil), http.StatusOK)
	gb74Status(t, gb74Call(noDB.handleGB28181ListAlarms, "GET", "/api/gb28181/alarms", "", nil), http.StatusOK)
	gb74Status(t, gb74Call(noDB.handleGB28181ListPlatformEvents, "GET", "/api/gb28181/platform/events", "", nil), http.StatusOK)
	gb74Status(t, gb74Call(noDB.handleGB28181GetPlatformStatus, "GET", "/api/gb28181/platform/status", "", nil), http.StatusOK)

	// Listing failures surface as 500.
	if err := f.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	gb74Status(t, gb74Call(h.handleGB28181ListDownloads, "GET", "/api/gb28181/downloads", "", nil), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGB28181ListAlarms, "GET", "/api/gb28181/alarms", "", nil), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGB28181ListPlatformEvents, "GET", "/api/gb28181/platform/events", "", nil), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGB28181GetPlatformStatus, "GET", "/api/gb28181/platform/status", "", nil), http.StatusInternalServerError)
}

func TestIssue74GB28181AlarmsAndPlatformEvents(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	// Empty result sets (alarms == nil path).
	rr := gb74Call(h.handleGB28181ListAlarms, "GET", "/api/gb28181/alarms?device_id=34020000001110000060&limit=10&offset=5", "", nil)
	gb74Status(t, rr, http.StatusOK)
	if _, ok := gb74JSON(t, rr)["alarms"]; !ok {
		t.Fatal("expected alarms key")
	}
	rr = gb74Call(h.handleGB28181ListAlarms, "GET", "/api/gb28181/alarms?limit=bad&offset=bad", "", nil)
	gb74Status(t, rr, http.StatusOK)

	// A stored alarm is returned as-is.
	if _, err := f.db.CreateAlarm(context.Background(), &storage.AlarmRow{
		DeviceID: "34020000001110000060", ChannelID: "34020000001320000060",
		AlarmType: "Intrusion", AlarmTime: time.Now(), Priority: 1, Method: "5", Description: "gb74 alarm",
	}); err != nil {
		t.Fatalf("seed alarm: %v", err)
	}
	rr = gb74Call(h.handleGB28181ListAlarms, "GET", "/api/gb28181/alarms", "", nil)
	gb74Status(t, rr, http.StatusOK)
	alarms, _ := gb74JSON(t, rr)["alarms"].([]any)
	if len(alarms) != 1 {
		t.Fatalf("expected one alarm, got %v", gb74JSON(t, rr))
	}

	rr = gb74Call(h.handleGB28181ListPlatformEvents, "GET",
		"/api/gb28181/platform/events?platform_id=1&event_type=register&limit=10&offset=0", "", nil)
	gb74Status(t, rr, http.StatusOK)
	if _, ok := gb74JSON(t, rr)["events"]; !ok {
		t.Fatal("expected events key")
	}
	rr = gb74Call(h.handleGB28181ListPlatformEvents, "GET", "/api/gb28181/platform/events?platform_id=bad", "", nil)
	gb74Status(t, rr, http.StatusOK)

	rr = gb74Call(h.handleGB28181GetPlatformStatus, "GET", "/api/gb28181/platform/status", "", nil)
	gb74Status(t, rr, http.StatusOK)
	if _, ok := gb74JSON(t, rr)["platforms"]; !ok {
		t.Fatal("expected platform status key")
	}
}

// ==================== administrative regions ====================

// gb74ProvinceCode returns the first built-in top-level civil code.
func gb74ProvinceCode(t *testing.T) string {
	t.Helper()
	children := civilcode.Children("")
	if len(children) == 0 {
		t.Fatal("expected built-in civil codes")
	}
	return children[0].Code
}

// gb74RegionIDs returns the ids of the seeded top-level and child regions.
func TestIssue74GBRegionCrud(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := gb74NewHandler(f.db, nil, nil)
	ctx := context.Background()

	// Civil-code helpers do not touch the database.
	candidates := gb74Call(h.handleGBCivilCodeCandidates, "GET", "/api/gb28181/regions/candidates", "", nil)
	gb74Status(t, candidates, http.StatusOK)
	list, _ := gb74JSON(t, candidates)["candidates"].([]any)
	if len(list) == 0 {
		t.Fatal("expected built-in civil code candidates")
	}
	first, _ := list[0].(map[string]any)
	provinceCode, _ := first["code"].(string)
	if provinceCode == "" {
		t.Fatalf("unexpected candidate shape: %v", first)
	}

	// No children for an unknown parent returns an empty list.
	unknownParent := gb74Call(h.handleGBCivilCodeCandidates, "GET", "/api/gb28181/regions/candidates?parent=999999", "", nil)
	gb74Status(t, unknownParent, http.StatusOK)
	if list, _ := gb74JSON(t, unknownParent)["candidates"].([]any); len(list) != 0 {
		t.Fatalf("expected no candidates for unknown parent, got %v", list)
	}

	// Descriptions.
	gb74Status(t, gb74Call(h.handleGBCivilCodeDescription, "GET", "/api/gb28181/regions/description", "", nil), http.StatusBadRequest)
	desc := gb74Call(h.handleGBCivilCodeDescription, "GET", "/api/gb28181/regions/description?civil_code="+provinceCode, "", nil)
	gb74Status(t, desc, http.StatusOK)
	if body := gb74JSON(t, desc); body["found"] != true || body["name"] == "" {
		t.Fatalf("expected a known civil code, got %v", body)
	}
	notFound := gb74Call(h.handleGBCivilCodeDescription, "GET", "/api/gb28181/regions/description?civil_code=zzz", "", nil)
	gb74Status(t, notFound, http.StatusOK)
	if body := gb74JSON(t, notFound); body["found"] != false {
		t.Fatalf("expected found=false, got %v", body)
	}

	// Empty tree.
	tree := gb74Call(h.handleGBRegionTree, "GET", "/api/gb28181/regions/tree", "", nil)
	gb74Status(t, tree, http.StatusOK)
	if nodes, _ := gb74JSON(t, tree)["nodes"].([]any); len(nodes) != 0 {
		t.Fatalf("expected an empty tree, got %v", nodes)
	}

	// Create validation.
	gb74Status(t, gb74Call(h.handleGBRegionCreate, "POST", "/api/gb28181/regions", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBRegionCreate, "POST", "/api/gb28181/regions", `{"device_id":"440000"}`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBRegionCreate, "POST", "/api/gb28181/regions", `{"name":"No code"}`, nil), http.StatusBadRequest)

	// Create a province and a city below it.
	rr := gb74Call(h.handleGBRegionCreate, "POST", "/api/gb28181/regions",
		`{"device_id":"440000","name":"Guangdong"}`, nil)
	gb74Status(t, rr, http.StatusOK)
	provinceRegion, _ := gb74JSON(t, rr)["region"].(map[string]any)
	provinceID, _ := provinceRegion["id"].(float64)
	if provinceID <= 0 {
		t.Fatalf("expected a region id, got %v", provinceRegion)
	}

	// Duplicate codes conflict.
	gb74Status(t, gb74Call(h.handleGBRegionCreate, "POST", "/api/gb28181/regions",
		`{"device_id":"440000","name":"Duplicate"}`, nil), http.StatusConflict)

	rr = gb74Call(h.handleGBRegionCreate, "POST", "/api/gb28181/regions",
		`{"device_id":"440100","name":"Guangzhou","parent_device_id":"440000"}`, nil)
	gb74Status(t, rr, http.StatusOK)
	cityRegion, _ := gb74JSON(t, rr)["region"].(map[string]any)
	cityID, _ := cityRegion["id"].(float64)
	if cityID <= 0 {
		t.Fatalf("expected a city region id, got %v", cityRegion)
	}

	// The top-level tree lists the province node created above.
	tree = gb74Call(h.handleGBRegionTree, "GET", "/api/gb28181/regions/tree?parent=0", "", nil)
	gb74Status(t, tree, http.StatusOK)
	if nodes, _ := gb74JSON(t, tree)["nodes"].([]any); len(nodes) != 1 {
		t.Fatalf("expected one top-level region, got %v", nodes)
	}
	tree = gb74Call(h.handleGBRegionTree, "GET", "/api/gb28181/regions/tree?parent=bad", "", nil)
	gb74Status(t, tree, http.StatusOK)

	// Channels attached to the city are exposed as tree leaves.
	for _, ch := range []struct{ deviceID, channelID, name string }{
		{"34020000001110000070", "34020000001320000070", "Cam A"},
		{"34020000001110000071", "34020000001320000071", "Cam B"},
	} {
		if err := f.db.UpsertGB28181Channel(ctx, &storage.GB28181ChannelRow{
			DeviceID: ch.deviceID, ChannelID: ch.channelID, Name: ch.name, Status: "ON", CivilCode: "440100",
		}); err != nil {
			t.Fatalf("seed channel: %v", err)
		}
	}

	// Tree with a parent expands channels into leaves.
	tree = gb74Call(h.handleGBRegionTree, "GET",
		fmt.Sprintf("/api/gb28181/regions/tree?parent=%d&has_channel=true", int64(cityID)), "", nil)
	gb74Status(t, tree, http.StatusOK)
	nodes, _ := gb74JSON(t, tree)["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("expected two channel leaves, got %v", nodes)
	}
	// An unknown parent silently yields no leaves.
	tree = gb74Call(h.handleGBRegionTree, "GET", "/api/gb28181/regions/tree?parent=99999&has_channel=true", "", nil)
	gb74Status(t, tree, http.StatusOK)

	// Update validation.
	gb74Status(t, gb74Call(h.handleGBRegionUpdate, "PUT", "/api/gb28181/regions/x", `{}`, map[string]string{"id": "x"}), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBRegionUpdate, "PUT", "/api/gb28181/regions/99999", `{}`, map[string]string{"id": "99999"}), http.StatusNotFound)
	gb74Status(t, gb74Call(h.handleGBRegionUpdate, "PUT", "/api/gb28181/regions/1", `{`,
		map[string]string{"id": strconv.FormatInt(int64(cityID), 10)}), http.StatusBadRequest)

	// Rename only.
	rr = gb74Call(h.handleGBRegionUpdate, "PUT", "/api/gb28181/regions/1", `{"name":"Guangzhou City"}`,
		map[string]string{"id": strconv.FormatInt(int64(cityID), 10)})
	gb74Status(t, rr, http.StatusOK)
	if region, _ := gb74JSON(t, rr)["region"].(map[string]any); region["name"] != "Guangzhou City" {
		t.Fatalf("expected renamed region, got %v", region)
	}

	// Changing the code to an existing code conflicts.
	rr = gb74Call(h.handleGBRegionUpdate, "PUT", "/api/gb28181/regions/1", `{"device_id":"440000"}`,
		map[string]string{"id": strconv.FormatInt(int64(cityID), 10)})
	gb74Status(t, rr, http.StatusConflict)

	// Changing the code migrates the attached channels.
	rr = gb74Call(h.handleGBRegionUpdate, "PUT", "/api/gb28181/regions/1", `{"device_id":"440101"}`,
		map[string]string{"id": strconv.FormatInt(int64(cityID), 10)})
	gb74Status(t, rr, http.StatusOK)
	moved, err := f.db.ListChannelsByCivilCode(ctx, "440101")
	if err != nil {
		t.Fatalf("list migrated channels: %v", err)
	}
	if len(moved) != 2 {
		t.Fatalf("expected two channels migrated to the new code, got %v", moved)
	}

	// Delete validation.
	gb74Status(t, gb74Call(h.handleGBRegionDelete, "DELETE", "/api/gb28181/regions/x", "", map[string]string{"id": "x"}), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBRegionDelete, "DELETE", "/api/gb28181/regions/99999", "", map[string]string{"id": "99999"}), http.StatusNotFound)

	// Deleting the province cascades to the city and detaches channels.
	rr = gb74Call(h.handleGBRegionDelete, "DELETE", "/api/gb28181/regions/1", "",
		map[string]string{"id": strconv.FormatInt(int64(provinceID), 10)})
	gb74Status(t, rr, http.StatusOK)
	if _, err := f.db.GetGBRegion(ctx, int64(cityID)); err == nil {
		t.Fatal("expected the descendant region to be deleted")
	}
	remaining, err := f.db.ListChannelsByCivilCode(ctx, "440101")
	if err != nil {
		t.Fatalf("list channels after delete: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected detached channels, got %v", remaining)
	}

	// Sync from channel civil codes.
	rr = gb74Call(h.handleGBRegionSync, "POST", "/api/gb28181/regions/sync", "", nil)
	gb74Status(t, rr, http.StatusOK)
	if _, ok := gb74JSON(t, rr)["created"]; !ok {
		t.Fatal("expected created count")
	}

	// Add a region from the standard civil code table.
	gb74Status(t, gb74Call(h.handleGBRegionAddByCivilCode, "POST", "/api/gb28181/regions/by-civil-code", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBRegionAddByCivilCode, "POST", "/api/gb28181/regions/by-civil-code", `{}`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBRegionAddByCivilCode, "POST", "/api/gb28181/regions/by-civil-code",
		`{"civil_code":"999999"}`, nil), http.StatusBadRequest)
	rr = gb74Call(h.handleGBRegionAddByCivilCode, "POST", "/api/gb28181/regions/by-civil-code",
		fmt.Sprintf(`{"civil_code":%q}`, provinceCode), nil)
	gb74Status(t, rr, http.StatusOK)
	if created, _ := gb74JSON(t, rr)["created"].(float64); created < 1 {
		t.Fatalf("expected created regions, got %v", gb74JSON(t, rr))
	}
}

// TestIssue74GBRegionChannels covers channel attach/detach and the catalog list.
func TestIssue74GBRegionChannels(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := gb74NewHandler(f.db, nil, nil)
	ctx := context.Background()

	if err := f.db.UpsertGB28181Device(ctx, &storage.GB28181DeviceRow{DeviceID: "34020000001110000080", Name: "catalog"}); err != nil {
		t.Fatalf("seed catalog device: %v", err)
	}
	for _, ch := range []struct{ channelID, name string }{
		{"34020000001320000080", "Lobby"},
		{"34020000001320000081", "Gate"},
	} {
		if err := f.db.UpsertGB28181Channel(ctx, &storage.GB28181ChannelRow{
			DeviceID: "34020000001110000080", ChannelID: ch.channelID, Name: ch.name, Status: "ON",
		}); err != nil {
			t.Fatalf("seed catalog channel: %v", err)
		}
	}

	// Catalog listing (all / filtered / unassigned).
	rr := gb74Call(h.handleGBCatalogChannels, "GET", "/api/gb28181/catalog/channels", "", nil)
	gb74Status(t, rr, http.StatusOK)
	channels, _ := gb74JSON(t, rr)["channels"].([]any)
	if len(channels) != 2 {
		t.Fatalf("expected two catalog channels, got %v", channels)
	}
	gb74Status(t, gb74Call(h.handleGBCatalogChannels, "GET",
		"/api/gb28181/catalog/channels?q=Lobby&unassigned_region=true&unassigned_group=true", "", nil), http.StatusOK)

	// An empty database returns an empty (non-nil) list.
	empty := gb74NewFixture(t, nil)
	emptyHandler := gb74NewHandler(empty.db, nil, nil)
	rr = gb74Call(emptyHandler.handleGBCatalogChannels, "GET", "/api/gb28181/catalog/channels", "", nil)
	gb74Status(t, rr, http.StatusOK)
	if list, ok := gb74JSON(t, rr)["channels"].([]any); !ok || len(list) != 0 {
		t.Fatalf("expected an empty channel list, got %v", gb74JSON(t, rr))
	}

	// Attach validation and success.
	gb74Status(t, gb74Call(h.handleGBChannelAttachRegion, "POST", "/api/gb28181/channels/region", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBChannelAttachRegion, "POST", "/api/gb28181/channels/region",
		`{"civil_code":"440000"}`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBChannelAttachRegion, "POST", "/api/gb28181/channels/region",
		`{"channels":[{"device_id":"a","channel_id":"b"}]}`, nil), http.StatusBadRequest)
	rr = gb74Call(h.handleGBChannelAttachRegion, "POST", "/api/gb28181/channels/region",
		`{"civil_code":"440000","channels":[{"device_id":"34020000001110000080","channel_id":"34020000001320000080"},
			{"device_id":"34020000001110000080","channel_id":"34020000001320000081"}]}`, nil)
	gb74Status(t, rr, http.StatusOK)
	attached, err := f.db.ListChannelsByCivilCode(ctx, "440000")
	if err != nil || len(attached) != 2 {
		t.Fatalf("expected attached channels, got %v %v", attached, err)
	}

	// Detach validation and success.
	gb74Status(t, gb74Call(h.handleGBChannelDetachRegion, "DELETE", "/api/gb28181/channels/region", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBChannelDetachRegion, "DELETE", "/api/gb28181/channels/region", `{}`, nil), http.StatusBadRequest)
	rr = gb74Call(h.handleGBChannelDetachRegion, "DELETE", "/api/gb28181/channels/region",
		`{"channels":[{"device_id":"34020000001110000080","channel_id":"34020000001320000080"}]}`, nil)
	gb74Status(t, rr, http.StatusOK)
	detached, err := f.db.ListChannelsByCivilCode(ctx, "440000")
	if err != nil || len(detached) != 1 {
		t.Fatalf("expected one remaining attached channel, got %v %v", detached, err)
	}

	// Storage failures surface as 500.
	if err := f.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	gb74Status(t, gb74Call(h.handleGBRegionTree, "GET", "/api/gb28181/regions/tree", "", nil), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGBRegionCreate, "POST", "/api/gb28181/regions",
		`{"device_id":"441000","name":"Broken"}`, nil), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGBRegionSync, "POST", "/api/gb28181/regions/sync", "", nil), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGBRegionAddByCivilCode, "POST", "/api/gb28181/regions/by-civil-code",
		fmt.Sprintf(`{"civil_code":%q}`, gb74ProvinceCode(t)), nil), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGBChannelAttachRegion, "POST", "/api/gb28181/channels/region",
		`{"civil_code":"440000","channels":[{"device_id":"a","channel_id":"b"}]}`, nil), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGBChannelDetachRegion, "DELETE", "/api/gb28181/channels/region",
		`{"channels":[{"device_id":"a","channel_id":"b"}]}`, nil), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGBCatalogChannels, "GET", "/api/gb28181/catalog/channels", "", nil), http.StatusInternalServerError)
}

// ==================== business groups / virtual organizations ====================

func TestIssue74GBGroups(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := gb74NewHandler(f.db, nil, nil)
	ctx := context.Background()

	const (
		businessID = "34020000002150000001" // type code 215
		orgID      = "34020000002160000001" // type code 216
		childOrgID = "34020000002160000002"
	)

	// Empty tree.
	tree := gb74Call(h.handleGBGroupTree, "GET", "/api/gb28181/groups/tree", "", nil)
	gb74Status(t, tree, http.StatusOK)
	if nodes, _ := gb74JSON(t, tree)["nodes"].([]any); len(nodes) != 0 {
		t.Fatalf("expected an empty group tree, got %v", nodes)
	}

	// Create validation.
	gb74Status(t, gb74Call(h.handleGBGroupCreate, "POST", "/api/gb28181/groups", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBGroupCreate, "POST", "/api/gb28181/groups", `{"device_id":"x"}`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBGroupCreate, "POST", "/api/gb28181/groups",
		`{"device_id":"123","name":"short"}`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBGroupCreate, "POST", "/api/gb28181/groups",
		`{"device_id":"34020000001110000001","name":"camera type code"}`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBGroupCreate, "POST", "/api/gb28181/groups",
		fmt.Sprintf(`{"device_id":%q,"name":"org without business group"}`, orgID), nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBGroupCreate, "POST", "/api/gb28181/groups",
		fmt.Sprintf(`{"device_id":%q,"name":"org with unknown business group","business_group":"34020000002150009999"}`, orgID), nil),
		http.StatusBadRequest)

	// Business group (215).
	rr := gb74Call(h.handleGBGroupCreate, "POST", "/api/gb28181/groups",
		fmt.Sprintf(`{"device_id":%q,"name":"Business","civil_code":"440000"}`, businessID), nil)
	gb74Status(t, rr, http.StatusOK)
	business := gb74JSON(t, rr)
	group, _ := business["group"].(map[string]any)
	businessPK, _ := group["id"].(float64)
	if businessPK <= 0 {
		t.Fatalf("expected a created business group, got %v", business)
	}
	if group["business_group"] != businessID {
		t.Fatalf("expected the business group to reference itself, got %v", group)
	}

	// Duplicate business group conflicts.
	gb74Status(t, gb74Call(h.handleGBGroupCreate, "POST", "/api/gb28181/groups",
		fmt.Sprintf(`{"device_id":%q,"name":"Duplicate"}`, businessID), nil), http.StatusConflict)

	// Virtual organization (216) under the business group.
	rr = gb74Call(h.handleGBGroupCreate, "POST", "/api/gb28181/groups",
		fmt.Sprintf(`{"device_id":%q,"name":"Org","business_group":%q}`, orgID, businessID), nil)
	gb74Status(t, rr, http.StatusOK)
	orgNode, _ := gb74JSON(t, rr)["group"].(map[string]any)
	orgPK, _ := orgNode["id"].(float64)
	if orgPK <= 0 {
		t.Fatalf("expected a created virtual organization, got %v", orgNode)
	}

	// Nested organization: an unknown parent is rejected.
	gb74Status(t, gb74Call(h.handleGBGroupCreate, "POST", "/api/gb28181/groups",
		fmt.Sprintf(`{"device_id":%q,"name":"Bad parent","business_group":%q,"parent_device_id":"34020000002160009999"}`, childOrgID, businessID), nil),
		http.StatusBadRequest)
	rr = gb74Call(h.handleGBGroupCreate, "POST", "/api/gb28181/groups",
		fmt.Sprintf(`{"device_id":%q,"name":"Child org","business_group":%q,"parent_device_id":%q}`, childOrgID, businessID, orgID), nil)
	gb74Status(t, rr, http.StatusOK)
	childNode, _ := gb74JSON(t, rr)["group"].(map[string]any)
	childPK, _ := childNode["id"].(float64)
	if childPK <= 0 {
		t.Fatalf("expected a created child organization, got %v", childNode)
	}

	// The tree lists the two top-level nodes.
	tree = gb74Call(h.handleGBGroupTree, "GET", "/api/gb28181/groups/tree?parent=0", "", nil)
	gb74Status(t, tree, http.StatusOK)
	if nodes, _ := gb74JSON(t, tree)["nodes"].([]any); len(nodes) != 2 {
		t.Fatalf("expected two top-level groups, got %v", nodes)
	}

	// Channels attached to the child organization are exposed as leaves.
	for _, channelID := range []string{"34020000001320000090", "34020000001320000091"} {
		if err := f.db.UpsertGB28181Channel(ctx, &storage.GB28181ChannelRow{
			DeviceID: "34020000001110000090", ChannelID: channelID, Name: "group " + channelID,
			Status: "ON", ParentID: childOrgID, BusinessGroupID: businessID,
		}); err != nil {
			t.Fatalf("seed grouped channel: %v", err)
		}
	}
	tree = gb74Call(h.handleGBGroupTree, "GET",
		fmt.Sprintf("/api/gb28181/groups/tree?parent=%d&has_channel=true", int64(childPK)), "", nil)
	gb74Status(t, tree, http.StatusOK)
	if nodes, _ := gb74JSON(t, tree)["nodes"].([]any); len(nodes) != 2 {
		t.Fatalf("expected two channel leaves, got %v", nodes)
	}
	tree = gb74Call(h.handleGBGroupTree, "GET", "/api/gb28181/groups/tree?parent=99999&has_channel=true", "", nil)
	gb74Status(t, tree, http.StatusOK)

	// Update validation and rename.
	gb74Status(t, gb74Call(h.handleGBGroupUpdate, "PUT", "/api/gb28181/groups/x", `{}`, map[string]string{"id": "x"}), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBGroupUpdate, "PUT", "/api/gb28181/groups/99999", `{}`, map[string]string{"id": "99999"}), http.StatusNotFound)
	gb74Status(t, gb74Call(h.handleGBGroupUpdate, "PUT", "/api/gb28181/groups/1", `{`,
		map[string]string{"id": strconv.FormatInt(int64(orgPK), 10)}), http.StatusBadRequest)
	rr = gb74Call(h.handleGBGroupUpdate, "PUT", "/api/gb28181/groups/1", `{"name":"Renamed org"}`,
		map[string]string{"id": strconv.FormatInt(int64(orgPK), 10)})
	gb74Status(t, rr, http.StatusOK)
	if node, _ := gb74JSON(t, rr)["group"].(map[string]any); node["name"] != "Renamed org" {
		t.Fatalf("expected renamed group, got %v", node)
	}

	// Attach / detach channels.
	gb74Status(t, gb74Call(h.handleGBChannelAttachGroup, "POST", "/api/gb28181/channels/group", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBChannelAttachGroup, "POST", "/api/gb28181/channels/group",
		`{"parent_id":"x"}`, nil), http.StatusBadRequest)
	rr = gb74Call(h.handleGBChannelAttachGroup, "POST", "/api/gb28181/channels/group",
		fmt.Sprintf(`{"parent_id":%q,"business_group":%q,"channels":[{"device_id":"34020000001110000090","channel_id":"34020000001320000090"}]}`, childOrgID, businessID), nil)
	gb74Status(t, rr, http.StatusOK)
	grouped, err := f.db.ListChannelsByParentID(ctx, childOrgID)
	if err != nil || len(grouped) != 2 {
		t.Fatalf("expected two grouped channels, got %v %v", grouped, err)
	}

	gb74Status(t, gb74Call(h.handleGBChannelDetachGroup, "DELETE", "/api/gb28181/channels/group", `{`, nil), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBChannelDetachGroup, "DELETE", "/api/gb28181/channels/group", `{}`, nil), http.StatusBadRequest)
	rr = gb74Call(h.handleGBChannelDetachGroup, "DELETE", "/api/gb28181/channels/group",
		`{"channels":[{"device_id":"34020000001110000090","channel_id":"34020000001320000091"}]}`, nil)
	gb74Status(t, rr, http.StatusOK)
	grouped, err = f.db.ListChannelsByParentID(ctx, childOrgID)
	if err != nil || len(grouped) != 1 {
		t.Fatalf("expected one remaining grouped channel, got %v %v", grouped, err)
	}

	// Delete validation.
	gb74Status(t, gb74Call(h.handleGBGroupDelete, "DELETE", "/api/gb28181/groups/x", "", map[string]string{"id": "x"}), http.StatusBadRequest)
	gb74Status(t, gb74Call(h.handleGBGroupDelete, "DELETE", "/api/gb28181/groups/99999", "", map[string]string{"id": "99999"}), http.StatusNotFound)

	// Deleting a virtual organization cascades to its subtree.
	rr = gb74Call(h.handleGBGroupDelete, "DELETE", "/api/gb28181/groups/1", "",
		map[string]string{"id": strconv.FormatInt(int64(childPK), 10)})
	gb74Status(t, rr, http.StatusOK)
	if _, err := f.db.GetGBGroup(ctx, int64(childPK)); err == nil {
		t.Fatal("expected the child organization to be deleted")
	}

	// Deleting the business group removes every node below it.
	rr = gb74Call(h.handleGBGroupDelete, "DELETE", "/api/gb28181/groups/1", "",
		map[string]string{"id": strconv.FormatInt(int64(businessPK), 10)})
	gb74Status(t, rr, http.StatusOK)
	tree = gb74Call(h.handleGBGroupTree, "GET", "/api/gb28181/groups/tree?parent=0", "", nil)
	gb74Status(t, tree, http.StatusOK)
	if nodes, _ := gb74JSON(t, tree)["nodes"].([]any); len(nodes) != 0 {
		t.Fatalf("expected every group to be deleted, got %v", nodes)
	}

	// Storage failures surface as 500.
	if err := f.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	gb74Status(t, gb74Call(h.handleGBGroupTree, "GET", "/api/gb28181/groups/tree", "", nil), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGBGroupCreate, "POST", "/api/gb28181/groups",
		fmt.Sprintf(`{"device_id":%q,"name":"Broken"}`, "34020000002150000002"), nil), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGBChannelAttachGroup, "POST", "/api/gb28181/channels/group",
		`{"parent_id":"x","business_group":"y","channels":[{"device_id":"a","channel_id":"b"}]}`, nil), http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGBChannelDetachGroup, "DELETE", "/api/gb28181/channels/group",
		`{"channels":[{"device_id":"a","channel_id":"b"}]}`, nil), http.StatusInternalServerError)
}

// ==================== helper / failure-path coverage ====================

func TestIssue74GB28181Helpers(t *testing.T) {
	f := gb74NewFixture(t, nil)

	// Signaling helpers report an unavailable server instead of panicking.
	noServer := gb74NewHandler(f.db, nil, nil)
	if err := noServer.sendGB28181PTZControl(context.Background(), "d", "c", "up", 1); err == nil {
		t.Fatal("expected an error when the GB28181 server is unavailable")
	}
	if err := noServer.sendGB28181RecordControl(context.Background(), "d", "c", "record"); err == nil {
		t.Fatal("expected an error when the GB28181 server is unavailable")
	}

	// PTZ command builders.
	if got := buildPTZCommand("up", 1); got == "" {
		t.Fatal("expected a continuous move command")
	}
	if got := buildPTZCommand("stop", 0); got == "" {
		t.Fatal("expected a stop command")
	}
	if got := buildPTZCommand("sideways", 0); got != "" {
		t.Fatalf("expected no command for an unknown direction, got %q", got)
	}
	if got := buildGB28181PTZCommand("zoomout", 1); got == "" {
		t.Fatal("expected a zoom command")
	}
	if got := buildGB28181PTZCommand("nope", 1); got != "" {
		t.Fatalf("expected no command for an unknown direction, got %q", got)
	}

	// A 2022 device uses the 2022 control path; invalid directions are rejected.
	const (
		deviceID  = "34020000001110000100"
		channelID = "34020000001320000100"
	)
	f.seed(deviceID, gb74Channel(channelID, "V2022"))
	if err := f.db.UpsertGB28181Device(context.Background(), &storage.GB28181DeviceRow{
		DeviceID: deviceID, Name: deviceID, GBVersion: string(gb28181.GBVersion2022), IsOnline: true,
	}); err != nil {
		t.Fatalf("mark device as 2022: %v", err)
	}
	f.reload()
	h := f.handler()
	params := map[string]string{"deviceID": deviceID, "channelID": channelID}
	gb74Status(t, gb74Call(h.handleGB28181PTZControl, "POST", "/ptz", `{"direction":"sideways"}`, params),
		http.StatusInternalServerError)
	gb74Status(t, gb74Call(h.handleGB28181PTZControl, "POST", "/ptz", `{"direction":"up","speed":2}`, params),
		http.StatusOK)

	// Without a config the version lookup falls back to the 2016 default.
	noConfigHandler := NewHandler(f.db, nil, noopAuthMW(), nil, nil, "", nil, nil)
	noConfigHandler.SetGB28181ServerInstance(f.srv)
	if v := noConfigHandler.gb28181DeviceVersion(context.Background(), "34020000001110009999"); v != gb28181.GBVersion2016 {
		t.Fatalf("expected the 2016 fallback, got %q", v)
	}
	if v := h.gb28181DeviceVersion(context.Background(), deviceID); v != gb28181.GBVersion2022 {
		t.Fatalf("expected the stored 2022 version, got %q", v)
	}
}

func TestIssue74GB28181StopPlayFailure(t *testing.T) {
	f := gb74NewFixture(t, nil)
	f.peer.setRejectMethod("BYE")
	// Let the fixture cleanup close the retained dialog once the assertions ran.
	t.Cleanup(func() { f.peer.setRejectMethod("") })
	h := f.handler()

	const (
		deviceID  = "34020000001110000110"
		channelID = "34020000001320000110"
	)
	f.seed(deviceID, gb74Channel(channelID, "Stop failure"))
	body := fmt.Sprintf(`{"device_id":%q,"channel_id":%q}`, deviceID, channelID)

	gb74Status(t, gb74Call(h.handleGB28181Play, "POST", "/play", body, nil), http.StatusOK)
	// The device rejects the BYE, so the session is retained and the request fails.
	gb74Status(t, gb74Call(h.handleGB28181StopPlay, "POST", "/stop", body, nil), http.StatusInternalServerError)
}

func TestIssue74GB28181CameraRegistrationFailure(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	const (
		deviceID  = "34020000001110000120"
		channelID = "34020000001320000120"
	)
	f.seed(deviceID, gb74Channel(channelID, "DB failure"))
	// Play still succeeds: camera registration failures are warnings only.
	if err := f.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	gb74Status(t, gb74Call(h.handleGB28181Play, "POST", "/play",
		fmt.Sprintf(`{"device_id":%q,"channel_id":%q}`, deviceID, channelID), nil), http.StatusOK)
}

func TestIssue74GB28181DeleteDeviceStorageFailure(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	const deviceID = "34020000001110000130"
	f.seedOffline(deviceID)
	if err := f.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	// The device exists in memory but the storage delete fails.
	gb74Status(t, gb74Call(h.handleGB28181DeleteDevice, "DELETE", "/api/gb28181/devices/"+deviceID, "",
		map[string]string{"deviceID": deviceID}), http.StatusInternalServerError)
}

// gb74SendDeviceInvite sends a device-initiated SIP INVITE (with SDP) to the
// in-process GB28181 server, which marks a pending talk session ready.
func gb74SendDeviceInvite(t *testing.T, srv *gb28181.Server, deviceID, channelID string) net.Conn {
	t.Helper()
	serverAddr := fmt.Sprintf("127.0.0.1:%d", srv.GetConfig().Port)
	conn, err := net.Dial("udp", serverAddr)
	if err != nil {
		t.Fatalf("dial GB28181 server: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	sdp := fmt.Sprintf("v=0\r\no=%s 0 0 IN IP4 127.0.0.1\r\ns=Talk\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n"+
		"m=audio 40000 RTP/AVP 8\r\na=sendrecv\r\na=rtpmap:8 PCMA/8000\r\n", channelID)
	msg := fmt.Sprintf("INVITE sip:%s@%s SIP/2.0\r\n"+
		"Via: SIP/2.0/UDP 127.0.0.1:40000;branch=z9hG4bKgb74talk\r\n"+
		"Max-Forwards: 70\r\n"+
		"From: <sip:%s@127.0.0.1>;tag=gb74device\r\n"+
		"To: <sip:%s@127.0.0.1>\r\n"+
		"Call-ID: gb74-talk@127.0.0.1\r\n"+
		"CSeq: 1 INVITE\r\n"+
		"Contact: <sip:%s@127.0.0.1:40000>\r\n"+
		"Content-Type: application/sdp\r\n"+
		"Content-Length: %d\r\n\r\n%s",
		channelID, serverAddr, deviceID, channelID, deviceID, len(sdp), sdp)
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatalf("send device INVITE: %v", err)
	}
	return conn
}

// TestIssue74GB28181TalkWebsocketStream drives the talk websocket end to end:
// a device INVITE marks the pending talk session ready, then the websocket
// streams binary audio until the client disconnects.
func TestIssue74GB28181TalkWebsocketStream(t *testing.T) {
	f := gb74NewFixture(t, nil)
	h := f.handler()

	const (
		deviceID  = "34020000001110000140"
		channelID = "34020000001320000140"
	)
	f.seed(deviceID, gb74Channel(channelID, "Talk WS stream"))

	startBody := fmt.Sprintf(`{"device_id":%q,"channel_id":%q}`, deviceID, channelID)
	gb74Status(t, gb74Call(h.handleGB28181StartTalkWhip, "POST", "/api/gb28181/talk/start", startBody, nil), http.StatusOK)

	tm := f.srv.GetTalkManager()
	session, ok := tm.GetSession(deviceID, channelID)
	if !ok {
		t.Fatal("expected a talk session")
	}

	// The device answers the broadcast notify with its own INVITE, which marks
	// the session ready for media.
	gb74SendDeviceInvite(t, f.srv, deviceID, channelID)
	deadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case <-session.ReadyCh:
		default:
			if time.Now().After(deadline) {
				t.Fatal("talk session never became ready")
			}
			time.Sleep(5 * time.Millisecond)
			continue
		}
		break
	}

	server := httptest.NewServer(http.HandlerFunc(h.handleGB28181TalkWS))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") +
		"/api/gb28181/talk/ws?device_id=" + deviceID + "&channel_id=" + channelID
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial talk websocket: %v", err)
	}

	var hello map[string]any
	if err := ws.ReadJSON(&hello); err != nil {
		t.Fatalf("read talk handshake: %v", err)
	}
	if hello["status"] != "connected" {
		t.Fatalf("unexpected talk handshake: %v", hello)
	}

	// Binary frames are forwarded to the session, text frames are ignored.
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte("gb74-audio")); err != nil {
		t.Fatalf("write binary frame: %v", err)
	}
	if err := ws.WriteMessage(websocket.TextMessage, []byte("ignored")); err != nil {
		t.Fatalf("write text frame: %v", err)
	}
	_ = ws.Close()

	// The handler tears the session down when the websocket closes.
	deadline = time.Now().Add(3 * time.Second)
	for {
		if _, stillThere := tm.GetSession(deviceID, channelID); !stillThere {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("talk session was not stopped after the websocket closed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
