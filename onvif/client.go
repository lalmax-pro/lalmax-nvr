package onvif

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// ServiceEndpoints holds discovered ONVIF service endpoints.
type ServiceEndpoints struct {
	Device    *url.URL
	Media     *url.URL
	Media2    *url.URL // Profile T
	Recording *url.URL
	Search    *url.URL
	Replay    *url.URL
	PTZ       *url.URL
	Imaging   *url.URL
	Events    *url.URL
}

// Client is the main ONVIF client.
type Client struct {
	endpoint  string // Device service endpoint
	username  string
	password  string
	hostname  string
	port      int
	useTLS    bool
	timeShift time.Duration // Time difference with device

	httpClient *http.Client
	soap       *SOAPClient

	mu        sync.Mutex
	ready     bool
	endpoints *ServiceEndpoints

	// Cached data
	profiles []MediaProfile
	info     *DeviceInfo
	caps     *Capabilities

	// Services
	recordingService *RecordingService
	replayService    *ReplayService
	mediaService     *MediaService
	ptzService       *PTZService
	imagingService   *ImagingService
	eventsService    *EventsService
	deviceManager    *DeviceManager
}

// NewClient creates a new ONVIF client.
func NewClient(endpoint, username, password string, opts ...ClientOption) (*Client, error) {
	u, err := resolveONVIFEndpoint(nil, endpoint)
	if err != nil {
		return nil, fmt.Errorf("onvif: invalid endpoint: %w", err)
	}

	c := &Client{
		endpoint: u.String(),
		username: username,
		password: password,
		hostname: u.Hostname(),
		useTLS:   u.Scheme == "https",
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	if u.Port() != "" {
		fmt.Sscanf(u.Port(), "%d", &c.port)
	}
	if c.port == 0 {
		if c.useTLS {
			c.port = 443
		} else {
			c.port = 80
		}
	}

	for _, opt := range opts {
		opt(c)
	}

	c.soap = NewSOAPClient(c.httpClient, c.username, c.password)
	c.soap.origin = u

	return c, nil
}

// ClientOption is a functional option for Client.
type ClientOption func(*Client)

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// WithTimeout sets the HTTP timeout.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) {
		client := *c.httpClient
		client.Timeout = d
		c.httpClient = &client
	}
}

// Connect establishes connection and discovers services.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ready {
		return nil
	}

	// Step 1: Synchronize time with device
	if err := c.syncTime(ctx); err != nil {
		return fmt.Errorf("onvif: time sync failed: %w", err)
	}

	// Step 2: Discover service endpoints
	if err := c.discoverServices(ctx); err != nil {
		return fmt.Errorf("onvif: service discovery failed: %w", err)
	}

	c.ready = true

	// Initialize services
	c.recordingService = NewRecordingService(c)
	c.replayService = NewReplayService(c)
	c.mediaService = NewMediaService(c)
	c.ptzService = NewPTZService(c)
	c.imagingService = NewImagingService(c)
	c.eventsService = NewEventsService(c)
	c.deviceManager = NewDeviceManager(c)

	return nil
}

// Endpoints returns the discovered service endpoints.
func (c *Client) Endpoints() *ServiceEndpoints {
	return c.endpoints
}

// IsReady returns true if the client is connected and ready.
func (c *Client) IsReady() bool {
	return c.ready
}

// RecordingService returns the recording service.
func (c *Client) RecordingService() *RecordingService {
	return c.recordingService
}

// ReplayService returns the replay service.
func (c *Client) ReplayService() *ReplayService {
	return c.replayService
}

// MediaService returns the media service.
func (c *Client) MediaService() *MediaService {
	return c.mediaService
}

// PTZService returns the PTZ service.
func (c *Client) PTZService() *PTZService {
	return c.ptzService
}

// ImagingService returns the imaging service.
func (c *Client) ImagingService() *ImagingService {
	return c.imagingService
}

// EventsService returns the events service.
func (c *Client) EventsService() *EventsService {
	return c.eventsService
}

// DeviceManager returns the device manager.
func (c *Client) DeviceManager() *DeviceManager {
	return c.deviceManager
}

// Endpoint returns the device service endpoint URL.
func (c *Client) Endpoint() string {
	return c.endpoint
}

// syncTime synchronizes the client's time with the device.
func (c *Client) syncTime(ctx context.Context) error {
	deviceTime, err := c.GetSystemDateAndTime(ctx)
	if err != nil {
		// Some devices don't support this, continue without time sync
		return nil
	}

	// Calculate time shift similar to Node.js library
	// Node.js: timeShift = deviceTime - (process.uptime() * 1000)
	// Go equivalent: timeShift = deviceTime - time.Now()
	c.timeShift = deviceTime.Sub(time.Now())
	c.soap.SetTimeShift(c.timeShift)
	return nil
}

// discoverServices discovers ONVIF service endpoints.
func (c *Client) discoverServices(ctx context.Context) error {
	// Try GetServices first (ONVIF 2.0+)
	svcs, err := c.GetServices(ctx)
	if err == nil && len(svcs) > 0 {
		c.endpoints = c.parseServiceEndpoints(svcs)
		return nil
	}

	// Fallback to GetCapabilities
	caps, err := c.GetCapabilities(ctx)
	if err != nil {
		return fmt.Errorf("onvif: GetCapabilities failed: %w", err)
	}

	c.endpoints = c.capabilitiesToEndpoints(caps)
	return nil
}

// parseServiceEndpoints parses service URLs from GetServices response.
func (c *Client) parseServiceEndpoints(svcs []Service) *ServiceEndpoints {
	endpoints := &ServiceEndpoints{
		Device: c.baseURL(),
	}

	for _, svc := range svcs {
		svcURL, err := url.Parse(svc.XAddr)
		if err != nil {
			continue
		}

		// Map service by namespace
		// Media1: http://www.onvif.org/ver10/media/wsdl
		// Media2: http://www.onvif.org/ver20/media/wsdl
		switch {
		case svc.Namespace == "http://www.onvif.org/ver10/media/wsdl":
			endpoints.Media = svcURL
		case svc.Namespace == "http://www.onvif.org/ver20/media/wsdl":
			endpoints.Media2 = svcURL
		case contains(svc.Namespace, "/recording/wsdl"):
			endpoints.Recording = svcURL
		case contains(svc.Namespace, "/search/wsdl"):
			endpoints.Search = svcURL
		case contains(svc.Namespace, "/replay/wsdl"):
			endpoints.Replay = svcURL
		case contains(svc.Namespace, "/ptz/wsdl"):
			endpoints.PTZ = svcURL
		case contains(svc.Namespace, "/imaging/wsdl"):
			endpoints.Imaging = svcURL
		case contains(svc.Namespace, "/events/wsdl"):
			endpoints.Events = svcURL
		}
	}

	// Fallback: derive endpoints from base URL if not discovered
	if endpoints.Recording == nil {
		endpoints.Recording = c.deriveEndpoint("recording_service")
	}
	if endpoints.Search == nil {
		endpoints.Search = c.deriveEndpoint("search_service")
	}
	if endpoints.Replay == nil {
		endpoints.Replay = c.deriveEndpoint("replay_service")
	}

	return endpoints
}

// capabilitiesToEndpoints converts capabilities to service endpoints.
func (c *Client) capabilitiesToEndpoints(caps *Capabilities) *ServiceEndpoints {
	endpoints := &ServiceEndpoints{
		Device: c.baseURL(),
	}

	if caps.Media != nil && caps.Media.XAddr != "" {
		u, _ := url.Parse(caps.Media.XAddr)
		endpoints.Media = u
	}
	if caps.Media2 != nil && caps.Media2.XAddr != "" {
		u, _ := url.Parse(caps.Media2.XAddr)
		endpoints.Media2 = u
	}
	if caps.Recording != nil && caps.Recording.XAddr != "" {
		u, _ := url.Parse(caps.Recording.XAddr)
		endpoints.Recording = u
	}
	if caps.Search != nil && caps.Search.XAddr != "" {
		u, _ := url.Parse(caps.Search.XAddr)
		endpoints.Search = u
	}
	if caps.Replay != nil && caps.Replay.XAddr != "" {
		u, _ := url.Parse(caps.Replay.XAddr)
		endpoints.Replay = u
	}
	if caps.PTZ != nil && caps.PTZ.XAddr != "" {
		u, _ := url.Parse(caps.PTZ.XAddr)
		endpoints.PTZ = u
	}
	if caps.Imaging != nil && caps.Imaging.XAddr != "" {
		u, _ := url.Parse(caps.Imaging.XAddr)
		endpoints.Imaging = u
	}
	if caps.Events != nil && caps.Events.XAddr != "" {
		u, _ := url.Parse(caps.Events.XAddr)
		endpoints.Events = u
	}

	// Fallback for missing endpoints
	if endpoints.Recording == nil {
		endpoints.Recording = c.deriveEndpoint("recording_service")
	}
	if endpoints.Search == nil {
		endpoints.Search = c.deriveEndpoint("search_service")
	}
	if endpoints.Replay == nil {
		endpoints.Replay = c.deriveEndpoint("replay_service")
	}

	return endpoints
}

// baseURL returns the base device service URL.
func (c *Client) baseURL() *url.URL {
	scheme := "http"
	if c.useTLS {
		scheme = "https"
	}
	return &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(c.hostname, fmt.Sprint(c.port)),
		Path:   "/onvif/device_service",
	}
}

// deriveEndpoint derives a service endpoint from the base URL.
func (c *Client) deriveEndpoint(serviceName string) *url.URL {
	u := c.baseURL()
	u.Path = "/onvif/" + serviceName
	return u
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
