package dlna

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

func init() {
	// TVs subscribe to UPnP events before they browse the content directory.
	chi.RegisterMethod("SUBSCRIBE")
	chi.RegisterMethod("UNSUBSCRIBE")
}

const (
	ssdpAddr              = "239.255.255.250:1900"
	deviceType            = "urn:schemas-upnp-org:device:MediaServer:1"
	contentDirectoryType  = "urn:schemas-upnp-org:service:ContentDirectory:1"
	connectionManagerType = "urn:schemas-upnp-org:service:ConnectionManager:1"
	// Sony Bravia plays MPEG-TS when the DLNA profile is AVC TS and the MIME is video/mpeg.
	// CI=1 marks the stream as a live conversion so the TV keeps a short buffer.
	// OP=00: live TS has no byte or time seek.
	liveProtocolInfo   = "http-get:*:video/mpeg:DLNA.ORG_PN=AVC_TS_HD_EU_ISO;DLNA.ORG_OP=00;DLNA.ORG_CI=1;DLNA.ORG_FLAGS=01700000000000000000000000000000"
	liveContentFeature = "DLNA.ORG_PN=AVC_TS_HD_EU_ISO;DLNA.ORG_OP=00;DLNA.ORG_CI=1;DLNA.ORG_FLAGS=01700000000000000000000000000000"
)

type Service struct {
	cfg     *config.Config
	db      *storage.DB
	store   *storage.Manager
	engine  media.Engine
	mu      sync.RWMutex
	conn    *net.UDPConn
	send    *net.UDPConn
	httpSrv *http.Server
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func NewService(cfg *config.Config, db *storage.DB, store *storage.Manager, engine media.Engine) *Service {
	return &Service{cfg: cfg, db: db, store: store, engine: engine}
}

func (s *Service) Enabled() bool {
	return s != nil && s.cfg != nil && s.cfg.DLNA.Enabled != nil && *s.cfg.DLNA.Enabled
}

// Apply starts or stops SSDP after the web settings are changed.
func (s *Service) Apply(ctx context.Context) error {
	// Restart while enabled so changes to the advertised URL/interface take
	// effect immediately and clients receive a fresh SSDP announcement.
	if err := s.Stop(ctx); err != nil {
		return err
	}
	if s.Enabled() {
		return s.Start(ctx)
	}
	return nil
}

func (s *Service) Start(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.Stop(ctx); err != nil {
		return err
	}
	if err := s.validatePort(); err != nil {
		return err
	}
	ip, iface, err := detectLANIPv4(s.cfg.DLNA.Interface, s.cfg.DLNA.AdvertiseURL)
	if err != nil {
		return fmt.Errorf("dlna: %w", err)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.listenPort()))
	if err != nil {
		return fmt.Errorf("dlna listen: %w", err)
	}
	udpAddr, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		_ = ln.Close()
		return err
	}
	conn, err := net.ListenMulticastUDP("udp4", iface, udpAddr)
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("dlna ssdp listen: %w", err)
	}
	_ = conn.SetReadBuffer(64 * 1024)
	// Replies and NOTIFY must leave a unicast socket. Writing them on the
	// multicast membership socket is dropped by macOS and most Linux stacks,
	// so the TV never sees the MediaServer.
	send, err := listenSSDPSender(iface)
	if err != nil {
		_ = conn.Close()
		_ = ln.Close()
		return fmt.Errorf("dlna ssdp send: %w", err)
	}
	mux := chi.NewRouter()
	s.RegisterRoutes(mux)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.conn = conn
	s.send = send
	s.httpSrv = srv
	s.cancel = cancel
	s.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("DLNA HTTP server", "error", err)
		}
	}()
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.ssdpLoop(runCtx, conn, send)
	}()
	go func() {
		defer s.wg.Done()
		s.notifyLoop(runCtx, send)
	}()
	ifaceName := ""
	if iface != nil {
		ifaceName = iface.Name
	}
	slog.Info("DLNA MediaServer started", "friendly_name", s.cfg.DLNA.FriendlyName, "location", s.location(), "address", ip.String(), "port", s.listenPort(), "interface", ifaceName)
	return nil
}

func (s *Service) listenPort() int {
	if s.cfg != nil && s.cfg.DLNA.Port > 0 {
		return s.cfg.DLNA.Port
	}
	return config.DefaultDLNAPort
}

func (s *Service) validatePort() error {
	port := s.listenPort()
	if port < 1 || port > 65535 {
		return fmt.Errorf("dlna port %d is invalid", port)
	}
	httpPort := config.ParseListenPort(s.cfg.Server.Listen)
	if port == httpPort {
		return fmt.Errorf("dlna port %d must be different from the HTTP listen port", port)
	}
	return nil
}

func (s *Service) Stop(_ context.Context) error {
	s.mu.Lock()
	cancel, conn, send, srv := s.cancel, s.conn, s.send, s.httpSrv
	s.cancel = nil
	s.conn = nil
	s.send = nil
	s.httpSrv = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close()
	}
	s.wg.Wait()
	if send != nil {
		s.sendNotify(send, "ssdp:byebye")
		_ = send.Close()
	}
	if srv != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}
	return nil
}

func (s *Service) notifyLoop(ctx context.Context, conn *net.UDPConn) {
	s.sendNotify(conn, "ssdp:alive")
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sendNotify(conn, "ssdp:alive")
		}
	}
}

func (s *Service) ssdpLoop(ctx context.Context, conn, send *net.UDPConn) {
	var wg sync.WaitGroup
	// The unicast socket is bound to port 1900 so NOTIFY and search replies
	// use that source port. On macOS that socket also receives M-SEARCH, and
	// the multicast membership socket then never sees it. Read both.
	read := func(sock *net.UDPConn) {
		defer wg.Done()
		s.readSearches(ctx, sock, send)
	}
	wg.Add(1)
	go read(conn)
	if send != nil && send != conn {
		wg.Add(1)
		go read(send)
	}
	wg.Wait()
}

func (s *Service) readSearches(ctx context.Context, sock, send *net.UDPConn) {
	if sock == nil {
		return
	}
	buf := make([]byte, 8192)
	for {
		if ctx.Err() != nil {
			return
		}
		_ = sock.SetReadDeadline(time.Now().Add(time.Second))
		n, remote, err := sock.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			continue
		}
		if !strings.Contains(strings.ToUpper(string(buf[:n])), "M-SEARCH") {
			continue
		}
		st := parseST(string(buf[:n]))
		if st != "" {
			s.respondSearch(send, remote, st)
		}
	}
}

func parseST(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		p := strings.SplitN(line, ":", 2)
		if len(p) == 2 && strings.EqualFold(strings.TrimSpace(p[0]), "ST") {
			return strings.TrimSpace(p[1])
		}
	}
	return ""
}
func (s *Service) location() string {
	return s.baseURL() + "/dlna/device.xml"
}
func (s *Service) uuid() string {
	if s.cfg.DLNA.UUID != "" {
		return s.cfg.DLNA.UUID
	}
	return "550e8400-e29b-41d4-a716-446655440000"
}
func (s *Service) advertisements() []string {
	return []string{
		"upnp:rootdevice",
		"uuid:" + s.uuid(),
		deviceType,
		contentDirectoryType,
		connectionManagerType,
	}
}

func (s *Service) matchSearch(st string) []string {
	st = strings.TrimSpace(st)
	if st == "ssdp:all" || st == "ssdp:discover" {
		return s.advertisements()
	}
	for _, ad := range s.advertisements() {
		if strings.EqualFold(ad, st) {
			return []string{ad}
		}
	}
	return nil
}

func (s *Service) usn(st string) string {
	u := "uuid:" + s.uuid()
	if st == u {
		return u
	}
	if st == "upnp:rootdevice" || st == deviceType || st == contentDirectoryType || st == connectionManagerType {
		return u + "::" + st
	}
	return u
}
func (s *Service) respondSearch(conn *net.UDPConn, remote *net.UDPAddr, st string) {
	targets := s.matchSearch(st)
	if len(targets) == 0 || conn == nil || remote == nil {
		return
	}
	date := time.Now().UTC().Format(http.TimeFormat)
	for _, target := range targets {
		msg := ssdpSearchResponse(date, s.location(), target, s.usn(target))
		if _, err := conn.WriteToUDP([]byte(msg), remote); err != nil {
			slog.Warn("DLNA SSDP search reply", "error", err, "remote", remote.String(), "st", target)
		}
	}
}
func (s *Service) sendNotify(conn *net.UDPConn, nts string) {
	if conn == nil {
		return
	}
	if !strings.HasPrefix(nts, "ssdp:") {
		nts = "ssdp:" + nts
	}
	dst, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		return
	}
	for _, st := range s.advertisements() {
		msg := ssdpNotify(s.location(), st, nts, s.usn(st))
		if _, err := conn.WriteToUDP([]byte(msg), dst); err != nil {
			slog.Warn("DLNA SSDP notify", "error", err, "nt", st, "nts", nts)
		}
	}
}

func (s *Service) RegisterRoutes(r chi.Router) {
	if s == nil {
		return
	}
	prefix := "/dlna"
	r.Get(prefix+"/device.xml", s.guard(s.device))
	r.Get(prefix+"/content-directory.xml", s.guard(s.contentSCPD))
	r.Get(prefix+"/connection-manager.xml", s.guard(s.connectionSCPD))
	r.Post(prefix+"/control/content-directory", s.guard(s.contentControl))
	r.Post(prefix+"/control/connection-manager", s.guard(s.connectionControl))
	for _, p := range []string{prefix + "/events/content-directory", prefix + "/events/connection-manager"} {
		r.Method("SUBSCRIBE", p, s.guard(s.events))
		r.Method("UNSUBSCRIBE", p, s.guard(s.events))
	}
	r.Get(prefix+"/live/{stream}.ts", s.guard(s.live))
	r.Head(prefix+"/live/{stream}.ts", s.guard(s.live))
	r.Get(prefix+"/media/{id}", s.guard(s.recording))
	r.Head(prefix+"/media/{id}", s.guard(s.recording))
}

// guard keeps all DLNA endpoints unavailable until the feature is enabled and,
// when configured, restricts requests to trusted LAN CIDRs.
func (s *Service) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.Enabled() {
			http.NotFound(w, r)
			return
		}
		if !s.isAllowed(r.RemoteAddr) {
			http.Error(w, "DLNA access denied", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (s *Service) isAllowed(remote string) bool {
	if len(s.cfg.DLNA.AllowedCIDRs) == 0 {
		return true
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, raw := range s.cfg.DLNA.AllowedCIDRs {
		_, cidr, err := net.ParseCIDR(raw)
		if err == nil && cidr.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Service) device(w http.ResponseWriter, _ *http.Request) {
	base := s.baseURL()
	writeXML(w, deviceXML{
		XMLNS:       "urn:schemas-upnp-org:device-1-0",
		SpecVersion: specVersion{Major: 1, Minor: 0},
		URLBase:     base + "/",
		Device: deviceInfo{
			DeviceType:       deviceType,
			FriendlyName:     s.cfg.DLNA.FriendlyName,
			Manufacturer:     "lalmax-pro",
			ManufacturerURL:  base + "/",
			ModelDescription: "Lalmax NVR",
			ModelName:        "lalmax-nvr",
			ModelNumber:      "1",
			SerialNumber:     s.uuid(),
			UDN:              "uuid:" + s.uuid(),
			DLNADoc:          `<dlna:X_DLNADOC xmlns:dlna="urn:schemas-dlna-org:device-1-0">DMS-1.50</dlna:X_DLNADOC>`,
			ServiceList: serviceList{Services: []serviceInfo{
				{ServiceType: contentDirectoryType, ServiceID: "urn:upnp-org:serviceId:ContentDirectory", SCPDURL: "/dlna/content-directory.xml", ControlURL: "/dlna/control/content-directory", EventSubURL: "/dlna/events/content-directory"},
				{ServiceType: connectionManagerType, ServiceID: "urn:upnp-org:serviceId:ConnectionManager", SCPDURL: "/dlna/connection-manager.xml", ControlURL: "/dlna/control/connection-manager", EventSubURL: "/dlna/events/connection-manager"},
			}},
		},
	})
}
func (s *Service) baseURL() string {
	port := s.listenPort()
	ifaceName := ""
	legacy := ""
	if s.cfg != nil {
		ifaceName = s.cfg.DLNA.Interface
		legacy = s.cfg.DLNA.AdvertiseURL
	}
	ip, _, err := detectLANIPv4(ifaceName, legacy)
	if err != nil || ip == nil {
		return fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	return fmt.Sprintf("http://%s:%d", ip.String(), port)
}

// ResolvedBaseURL is the LAN address TVs use. The host is detected; the port is dlna.port.
func ResolvedBaseURL(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return NewService(cfg, nil, nil, nil).baseURL()
}
func (s *Service) contentSCPD(w http.ResponseWriter, _ *http.Request) { writeXML(w, contentSCPDXML()) }
func (s *Service) connectionSCPD(w http.ResponseWriter, _ *http.Request) {
	writeXML(w, connectionSCPDXML())
}

func (s *Service) contentControl(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	action := soapAction(r, body)
	switch action {
	case "Browse":
		s.browseSOAP(w, r, body)
	case "GetSearchCapabilities":
		soapResponse(w, "GetSearchCapabilitiesResponse", map[string]string{"SearchCaps": ""})
	case "GetSortCapabilities":
		soapResponse(w, "GetSortCapabilitiesResponse", map[string]string{"SortCaps": ""})
	case "GetSystemUpdateID":
		soapResponse(w, "GetSystemUpdateIDResponse", map[string]string{"Id": "1"})
	default:
		soapFault(w, 401, "Invalid Action")
	}
}
func (s *Service) connectionControl(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	switch soapAction(r, body) {
	case "GetProtocolInfo":
		soapResponseService(w, connectionManagerType, "GetProtocolInfoResponse", map[string]string{"Source": liveProtocolInfo + ",http-get:*:video/mp4:*", "Sink": ""})
	case "GetCurrentConnectionIDs":
		soapResponseService(w, connectionManagerType, "GetCurrentConnectionIDsResponse", map[string]string{"ConnectionIDs": "0"})
	case "GetCurrentConnectionInfo":
		soapResponseService(w, connectionManagerType, "GetCurrentConnectionInfoResponse", map[string]string{
			"RcsID": "0", "AVTransportID": "0", "ProtocolInfo": liveProtocolInfo,
			"PeerConnectionManager": "", "PeerConnectionID": "-1", "Direction": "Output", "Status": "OK",
		})
	default:
		soapFault(w, 401, "Invalid Action")
	}
}

func (s *Service) events(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "UNSUBSCRIBE":
		w.Header().Set("SID", firstHeader(r.Header.Get("SID"), "uuid:"+s.uuid()))
		w.WriteHeader(http.StatusOK)
	case "SUBSCRIBE":
		sid := firstHeader(r.Header.Get("SID"), "uuid:"+s.uuid())
		w.Header().Set("SID", sid)
		w.Header().Set("TIMEOUT", "Second-1800")
		w.WriteHeader(http.StatusOK)
		if r.Header.Get("SID") != "" {
			return
		}
		callback := parseCallback(r.Header.Get("CALLBACK"))
		if callback == "" || !s.allowCallback(callback) {
			return
		}
		prop, value := "SystemUpdateID", "1"
		if strings.Contains(r.URL.Path, "connection-manager") {
			prop, value = "SourceProtocolInfo", liveProtocolInfo
		}
		go s.sendInitialEvent(callback, sid, prop, value)
	default:
		http.NotFound(w, r)
	}
}

func firstHeader(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func parseCallback(header string) string {
	start := strings.Index(header, "<")
	end := strings.Index(header, ">")
	if start >= 0 && end > start {
		return strings.TrimSpace(header[start+1 : end])
	}
	return ""
}

func (s *Service) allowCallback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || s.cfg == nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || ip.IsLoopback() || (!ip.IsPrivate() && !ip.IsLinkLocalUnicast()) {
		return false
	}
	if len(s.cfg.DLNA.AllowedCIDRs) == 0 {
		return true
	}
	return s.isAllowed(net.JoinHostPort(ip.String(), "9"))
}

func (s *Service) sendInitialEvent(callback, sid, prop, value string) {
	body := `<?xml version="1.0"?><e:propertyset xmlns:e="urn:schemas-upnp-org:event-1-0"><e:property><` + prop + `>` + xmlEscape(value) + `</` + prop + `></e:property></e:propertyset>`
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "NOTIFY", callback, strings.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("NT", "upnp:event")
	req.Header.Set("NTS", "upnp:propchange")
	req.Header.Set("SID", sid)
	req.Header.Set("SEQ", "0")
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	client := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Debug("DLNA event notify", "error", err)
		return
	}
	_ = resp.Body.Close()
}

func soapAction(r *http.Request, b []byte) string {
	a := strings.Trim(r.Header.Get("SOAPACTION"), `"`)
	if i := strings.LastIndex(a, "#"); i >= 0 && i+1 < len(a) {
		return a[i+1:]
	}
	if a != "" && !strings.Contains(a, ":") {
		return a
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	inBody := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local == "Body" {
			inBody = true
			continue
		}
		if inBody {
			return start.Name.Local
		}
	}
}

type browseRequest struct {
	ObjectID       string `xml:"ObjectID"`
	BrowseFlag     string `xml:"BrowseFlag"`
	StartingIndex  uint32 `xml:"StartingIndex"`
	RequestedCount uint32 `xml:"RequestedCount"`
	SortCriteria   string `xml:"SortCriteria"`
}

func (s *Service) browseSOAP(w http.ResponseWriter, r *http.Request, b []byte) {
	var env struct {
		Body struct {
			Browse browseRequest `xml:"Browse"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal(b, &env); err != nil {
		soapFault(w, 501, "Invalid Args")
		return
	}
	result, err := s.browse(r.Context(), env.Body.Browse)
	if err != nil {
		soapFault(w, 701, err.Error())
		return
	}
	soapResponse(w, "BrowseResponse", map[string]string{
		"Result":         result.DIDL,
		"NumberReturned": strconv.Itoa(result.NumberReturned),
		"TotalMatches":   strconv.Itoa(result.TotalMatches),
		"UpdateID":       "1",
	})
}

type browseResult struct {
	DIDL           string
	NumberReturned int
	TotalMatches   int
}

func (s *Service) browse(ctx context.Context, req browseRequest) (browseResult, error) {
	items, err := s.items(ctx, req.ObjectID)
	if err != nil {
		return browseResult{}, err
	}
	if req.BrowseFlag == "BrowseMetadata" {
		if len(items) > 0 {
			items = items[:1]
		}
	}
	total := len(items)
	start := int(req.StartingIndex)
	if start > total {
		start = total
	}
	end := total
	if req.RequestedCount > 0 && start+int(req.RequestedCount) < end {
		end = start + int(req.RequestedCount)
	}
	items = items[start:end]
	return browseResult{DIDL: didl(items), NumberReturned: len(items), TotalMatches: total}, nil
}
func (s *Service) items(ctx context.Context, id string) ([]didlObject, error) {
	var out []didlObject
	if id == "0" {
		if s.cfg.DLNA.IncludeLive == nil || *s.cfg.DLNA.IncludeLive {
			out = append(out, didlObject{ID: "live", Parent: "0", Title: "Live", Class: "object.container"})
		}
		if s.cfg.DLNA.IncludeRecordings == nil || *s.cfg.DLNA.IncludeRecordings {
			out = append(out, didlObject{ID: "recordings", Parent: "0", Title: "Recordings", Class: "object.container"})
		}
		return out, nil
	}
	if id == "live" {
		if s.cfg.DLNA.IncludeLive != nil && !*s.cfg.DLNA.IncludeLive {
			return out, nil
		}
		cams, err := s.db.ListCameras(ctx)
		if err != nil {
			return nil, err
		}
		seen := map[string]struct{}{}
		base := s.baseURL()
		for _, c := range cams {
			sid := c.StreamID
			if sid == "" {
				sid = c.ID
			}
			if sid == "" {
				continue
			}
			seen[sid] = struct{}{}
			out = append(out, liveItem(base, sid, first(c.Name, sid)))
		}
		out = append(out, s.activePushes(ctx, base, seen)...)
		return out, nil
	}
	if strings.HasPrefix(id, "live:") {
		sid := strings.TrimPrefix(id, "live:")
		return []didlObject{liveItem(s.baseURL(), sid, sid)}, nil
	}
	if id == "recordings" {
		if s.cfg.DLNA.IncludeRecordings != nil && !*s.cfg.DLNA.IncludeRecordings {
			return out, nil
		}
		recs, err := s.db.ListRecordings(ctx, model.RecordingFilter{Limit: s.cfg.DLNA.MaxBrowseCount})
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			out = append(out, didlObject{ID: "rec:" + rec.ID, Parent: "recordings", Title: rec.ID, Class: "object.item.videoItem", URL: s.baseURL() + "/dlna/media/" + url.PathEscape(rec.ID), Protocol: "http-get:*:video/mp4:*", Size: rec.FileSize, Duration: rec.Duration})
		}
		return out, nil
	}
	return nil, nil
}
func liveItem(base, id, title string) didlObject {
	return didlObject{
		ID: "live:" + id, Parent: "live", Title: title, Class: "object.item.videoItem",
		URL: base + "/dlna/live/" + url.PathEscape(id) + ".ts", Protocol: liveProtocolInfo,
	}
}

// activePushes adds streams that are publishing now but are not cameras.
// DLNA otherwise only walks the camera table, so a direct push never appears on the TV.
func (s *Service) activePushes(ctx context.Context, base string, seen map[string]struct{}) []didlObject {
	if s.engine == nil {
		return nil
	}
	streams, err := s.engine.ListStreams(ctx)
	if err != nil {
		slog.Warn("DLNA list live streams", "error", err)
		return nil
	}
	names := map[string]string{}
	if s.db != nil {
		created, err := s.db.ListCreatedStreams(ctx)
		if err != nil {
			slog.Warn("DLNA list created streams", "error", err)
		} else {
			for _, stream := range created {
				names[stream.StreamID] = stream.Name
			}
		}
	}
	return activePushItems(base, seen, streams, names)
}

func activePushItems(base string, seen map[string]struct{}, streams []media.StreamInfo, names map[string]string) []didlObject {
	var out []didlObject
	for _, info := range streams {
		if info.StreamID == "" || !info.Active || media.IsSubStreamID(info.StreamID) {
			continue
		}
		if info.AppName != "" && info.AppName != "live" {
			continue
		}
		if _, ok := seen[info.StreamID]; ok {
			continue
		}
		seen[info.StreamID] = struct{}{}
		out = append(out, liveItem(base, info.StreamID, first(names[info.StreamID], info.StreamID)))
	}
	return out
}

func first(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func (s *Service) live(w http.ResponseWriter, r *http.Request) {
	stream, err := url.PathUnescape(chi.URLParam(r, "stream"))
	if err != nil || stream == "" || s.engine == nil {
		http.Error(w, "stream unavailable", 503)
		return
	}
	info, err := s.engine.GetStream(r.Context(), stream)
	if err != nil || info == nil || !info.Active {
		http.Error(w, "stream unavailable", http.StatusServiceUnavailable)
		return
	}
	p, err := s.engine.BuildPlayURL(r.Context(), media.PlayURLRequest{StreamID: stream, AppName: "live", Protocol: "httpts"})
	if err != nil || p == nil {
		http.Error(w, "stream unavailable", 503)
		return
	}
	if r.Method == http.MethodHead {
		setLiveHeaders(w, "video/mpeg", liveContentFeature)
		w.WriteHeader(http.StatusOK)
		return
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		http.Error(w, "stream unavailable", 503)
		return
	}
	// Live MPEG-TS is not seekable. Forwarding a Range probe makes the TV wait
	// for Content-Range instead of decoding the first keyframe.
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		http.Error(w, "stream unavailable", 503)
		return
	}
	req.Header.Set("Connection", "close")
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableCompression: true}}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "stream unavailable", 503)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "stream unavailable", 503)
		return
	}
	// net/http chunk-encodes a body without Content-Length. Sony Bravia buffers
	// chunked MPEG-TS before it shows a picture. lal writes raw TS after the
	// headers; do the same on the hijacked connection.
	hj, ok := w.(http.Hijacker)
	if !ok {
		setLiveHeaders(w, "video/mpeg", liveContentFeature)
		w.WriteHeader(http.StatusOK)
		copyFlush(w, resp.Body)
		return
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		http.Error(w, "stream unavailable", 503)
		return
	}
	defer conn.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
	if bufrw != nil && bufrw.Writer.Buffered() > 0 {
		_ = bufrw.Flush()
	}
	if err := writeLiveHeader(conn, "video/mpeg", liveContentFeature); err != nil {
		return
	}
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := conn.Write(buf[:n]); werr != nil {
				return
			}
		}
		if rerr != nil {
			return
		}
	}
}

func (s *Service) recording(w http.ResponseWriter, r *http.Request) {
	id, err := url.PathUnescape(chi.URLParam(r, "id"))
	if err != nil || id == "" {
		http.NotFound(w, r)
		return
	}
	rec, err := s.db.GetRecording(r.Context(), id)
	if err != nil || rec == nil {
		http.NotFound(w, r)
		return
	}
	p, err := storage.ValidatePath(s.store.RootDir(), rec.FilePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	setDLNAHeaders(w, "video/mp4")
	http.ServeFile(w, r, p)
}

func copyFlush(w http.ResponseWriter, src io.Reader) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func setLiveHeaders(w http.ResponseWriter, contentType, features string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Connection", "close")
	h.Set("Cache-Control", "no-cache")
	h.Set("Pragma", "no-cache")
	h.Set("Accept-Ranges", "none")
	h.Set("transferMode.dlna.org", "Streaming")
	h.Set("contentFeatures.dlna.org", features)
}

func writeLiveHeader(conn net.Conn, contentType, features string) error {
	_, err := io.WriteString(conn, "HTTP/1.1 200 OK\r\n"+
		"Content-Type: "+contentType+"\r\n"+
		"Connection: close\r\n"+
		"Cache-Control: no-cache\r\n"+
		"Pragma: no-cache\r\n"+
		"Accept-Ranges: none\r\n"+
		"transferMode.dlna.org: Streaming\r\n"+
		"contentFeatures.dlna.org: "+features+"\r\n"+
		"\r\n")
	return err
}

func setDLNAHeaders(w http.ResponseWriter, contentType string) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("transferMode.dlna.org", "Streaming")
	w.Header().Set("contentFeatures.dlna.org", "DLNA.ORG_OP=01;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000")
}

func writeXML(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_ = xml.NewEncoder(w).Encode(v)
}
