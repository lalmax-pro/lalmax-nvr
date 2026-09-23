package dlna

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
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

const (
	ssdpAddr              = "239.255.255.250:1900"
	deviceType            = "urn:schemas-upnp-org:device:MediaServer:1"
	contentDirectoryType  = "urn:schemas-upnp-org:service:ContentDirectory:1"
	connectionManagerType = "urn:schemas-upnp-org:service:ConnectionManager:1"
)

type Service struct {
	cfg    *config.Config
	db     *storage.DB
	store  *storage.Manager
	engine media.Engine
	mu     sync.RWMutex
	conn   *net.UDPConn
	cancel context.CancelFunc
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
	udpAddr, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		return err
	}
	conn, err := net.ListenMulticastUDP("udp4", s.multicastInterface(), udpAddr)
	if err != nil {
		return fmt.Errorf("dlna ssdp: %w", err)
	}
	s.mu.Lock()
	s.conn = conn
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.mu.Unlock()
	go s.ssdpLoop(runCtx, conn)
	go s.notifyLoop(runCtx, conn)
	slog.Info("DLNA MediaServer started", "friendly_name", s.cfg.DLNA.FriendlyName)
	return nil
}

func (s *Service) Stop(_ context.Context) error {
	s.mu.Lock()
	cancel, conn := s.cancel, s.conn
	s.cancel = nil
	s.conn = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		s.sendNotify(conn, "ssdp:byebye")
		return conn.Close()
	}
	return nil
}

func (s *Service) multicastInterface() *net.Interface {
	name := strings.TrimSpace(s.cfg.DLNA.Interface)
	if name == "" {
		return nil
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		slog.Warn("DLNA interface unavailable", "interface", name, "error", err)
		return nil
	}
	return iface
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

func (s *Service) ssdpLoop(ctx context.Context, conn *net.UDPConn) {
	buf := make([]byte, 8192)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			continue
		}
		st := parseST(string(buf[:n]))
		if st != "" {
			s.respondSearch(conn, remote, st)
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
	if strings.TrimSpace(s.cfg.DLNA.AdvertiseURL) != "" {
		return strings.TrimRight(s.cfg.DLNA.AdvertiseURL, "/") + "/dlna/device.xml"
	}
	return "http://127.0.0.1:9090/dlna/device.xml"
}
func (s *Service) uuid() string {
	if s.cfg.DLNA.UUID != "" {
		return s.cfg.DLNA.UUID
	}
	return "550e8400-e29b-41d4-a716-446655440000"
}
func (s *Service) usn(st string) string {
	u := "uuid:" + s.uuid()
	if st == "upnp:rootdevice" {
		return u + "::upnp:rootdevice"
	}
	if st == deviceType {
		return u + "::" + deviceType
	}
	if st == contentDirectoryType {
		return u + "::" + contentDirectoryType
	}
	if st == connectionManagerType {
		return u + "::" + connectionManagerType
	}
	return u
}
func (s *Service) respondSearch(conn *net.UDPConn, remote *net.UDPAddr, st string) {
	if st != "ssdp:all" && st != "ssdp:discover" && st != "upnp:rootdevice" && st != deviceType && st != contentDirectoryType && st != connectionManagerType {
		return
	}
	msg := fmt.Sprintf("HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nDATE: %s\r\nEXT:\r\nLOCATION: %s\r\nSERVER: lalmax-nvr/1.0 UPnP/1.0 DLNADOC/1.50\r\nST: %s\r\nUSN: %s\r\n\r\n", time.Now().UTC().Format(http.TimeFormat), s.location(), st, s.usn(st))
	_, _ = conn.WriteToUDP([]byte(msg), remote)
}
func (s *Service) sendNotify(conn *net.UDPConn, nts string) {
	dst, _ := net.ResolveUDPAddr("udp4", ssdpAddr)
	for _, st := range []string{"upnp:rootdevice", deviceType, contentDirectoryType, connectionManagerType} {
		msg := fmt.Sprintf("NOTIFY * HTTP/1.1\r\nHOST: %s\r\nCACHE-CONTROL: max-age=1800\r\nLOCATION: %s\r\nNT: %s\r\nNTS: ssdp:%s\r\nSERVER: lalmax-nvr/1.0 UPnP/1.0 DLNADOC/1.50\r\nUSN: %s\r\n\r\n", ssdpAddr, s.location(), st, nts, s.usn(st))
		_, _ = conn.WriteToUDP([]byte(msg), dst)
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
	writeXML(w, deviceXML{XMLNS: "urn:schemas-upnp-org:device-1-0", SpecVersion: specVersion{1, 0}, Device: deviceInfo{DeviceType: deviceType, FriendlyName: s.cfg.DLNA.FriendlyName, Manufacturer: "lalmax-pro", ModelName: "lalmax-nvr", UDN: "uuid:" + s.uuid(), ServiceList: serviceList{Services: []serviceInfo{{ServiceType: contentDirectoryType, SCPDURL: base + "/dlna/content-directory.xml", ControlURL: base + "/dlna/control/content-directory", EventSubURL: base + "/dlna/events/content-directory"}, {ServiceType: connectionManagerType, SCPDURL: base + "/dlna/connection-manager.xml", ControlURL: base + "/dlna/control/connection-manager", EventSubURL: base + "/dlna/events/connection-manager"}}}}})
}
func (s *Service) baseURL() string {
	u := strings.TrimRight(s.cfg.DLNA.AdvertiseURL, "/")
	if u != "" {
		return u
	}
	return "http://127.0.0.1:9090"
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
	action := soapAction(r, body)
	if action == "GetProtocolInfo" {
		soapResponseService(w, connectionManagerType, "GetProtocolInfoResponse", map[string]string{"Source": "http-get:*:video/mp2t:*", "Sink": ""})
		return
	}
	soapFault(w, 401, "Invalid Action")
}
func soapAction(r *http.Request, b []byte) string {
	a := r.Header.Get("SOAPACTION")
	a = strings.Trim(a, "\"")
	if i := strings.LastIndex(a, "#"); i >= 0 {
		return a[i+1:]
	}
	var env struct {
		Body struct {
			Action struct {
				XMLName xml.Name `xml:"Browse"`
			} `xml:",any`
		} `xml:"Body"`
	}
	_ = xml.Unmarshal(b, &env)
	return ""
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
		for _, c := range cams {
			sid := c.StreamID
			if sid == "" {
				sid = c.ID
			}
			out = append(out, didlObject{ID: "live:" + sid, Parent: "live", Title: first(c.Name, sid), Class: "object.item.videoItem", URL: s.baseURL() + "/dlna/live/" + url.PathEscape(sid) + ".ts", Protocol: "http-get:*:video/mp2t:*"})
		}
		return out, nil
	}
	if strings.HasPrefix(id, "live:") {
		return []didlObject{{ID: id, Parent: "live", Title: strings.TrimPrefix(id, "live:"), Class: "object.item.videoItem", URL: s.baseURL() + "/dlna/live/" + url.PathEscape(strings.TrimPrefix(id, "live:")) + ".ts", Protocol: "http-get:*:video/mp2t:*"}}, nil
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
	u, err := url.Parse(p.URL)
	if err != nil {
		http.Error(w, "stream unavailable", 503)
		return
	}
	req, _ := http.NewRequestWithContext(r.Context(), r.Method, u.String(), nil)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "stream unavailable", 503)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, resp.Body)
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
	http.ServeFile(w, r, p)
}
func writeXML(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_ = xml.NewEncoder(w).Encode(v)
}

var _ = path.Clean
