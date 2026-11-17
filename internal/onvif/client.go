package onvif

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	onviflib "github.com/lalmax-pro/lalmax-nvr/onvif"
)

var logger = slog.Default().With("component", "onvif-client")

// Client wraps the standalone onvif library for NVR device operations.
type Client struct {
	endpoint string
	username string
	password string
	client   *onviflib.Client
	mu       sync.Mutex
	soapMu   sync.Mutex // serializes SOAP requests (many cameras mishandle concurrent HTTP)
	ready    bool
}

// newONVIFHTTPClient returns an HTTP client tuned for embedded ONVIF devices.
func newONVIFHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives:     true,
			DisableCompression:    true,
			MaxConnsPerHost:       1,
			ResponseHeaderTimeout: 15 * time.Second,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 0,
			}).DialContext,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// NewClient creates a new ONVIF client for a specific device.
func NewClient(endpoint, username, password string) *Client {
	return &Client{
		endpoint: endpoint,
		username: username,
		password: password,
	}
}

// Connect initializes the ONVIF connection and discovers service endpoints.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	client, err := onviflib.NewClient(c.endpoint, c.username, c.password,
		onviflib.WithHTTPClient(newONVIFHTTPClient()),
	)
	if err != nil {
		return fmt.Errorf("create ONVIF client: %w", err)
	}

	if err := client.Connect(ctx); err != nil {
		return fmt.Errorf("initialize ONVIF client: %w", err)
	}

	c.client = client
	c.ready = true

	logger.Info("connected to ONVIF device", "endpoint", c.endpoint)
	return nil
}

// GetDeviceInformation retrieves device info (manufacturer, model, firmware).
func (c *Client) GetDeviceInformation(ctx context.Context) (*DeviceInfo, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}

	info, err := c.client.GetDeviceInformation(ctx)
	if err != nil {
		return nil, fmt.Errorf("get device information: %w", err)
	}

	return &DeviceInfo{
		Manufacturer: info.Manufacturer,
		Model:        info.Model,
		Firmware:     info.FirmwareVersion,
		SerialNumber: info.SerialNumber,
		HardwareID:   info.HardwareId,
	}, nil
}

// GetProfiles retrieves media profiles from the device.
func (c *Client) GetProfiles(ctx context.Context) ([]DeviceProfile, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}

	profiles, err := c.client.MediaService().GetProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("get profiles: %w", err)
	}

	result := make([]DeviceProfile, 0, len(profiles))
	for _, p := range profiles {
		result = append(result, DeviceProfile{
			Token:            p.Token,
			Name:             p.Name,
			Encoding:         p.Encoding,
			Width:            p.Resolution.Width,
			Height:           p.Resolution.Height,
			VideoSource:      p.VideoSource,
			VideoSourceToken: p.VideoSourceToken,
		})
	}
	return result, nil
}

// GetStreamURI returns the RTSP stream URI for a profile.
func (c *Client) GetStreamURI(ctx context.Context, profileToken string) (*StreamInfo, error) {
	return c.GetStreamURIWithTransport(ctx, profileToken, "")
}

// GetStreamURIWithTransport obtains a URI for the recorder's RTSP transport.
func (c *Client) GetStreamURIWithTransport(ctx context.Context, profileToken, transport string) (*StreamInfo, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}

	service := c.client.MediaService()
	var uri string
	var err error
	switch transport {
	case "tcp":
		uri, err = service.GetStreamURIForTransport(ctx, profileToken, onviflib.StreamOverTCP)
	case "udp":
		uri, err = service.GetStreamURIForTransport(ctx, profileToken, onviflib.StreamOverUDP)
	default:
		uri, err = service.GetStreamURI(ctx, profileToken)
	}
	if err != nil {
		return nil, fmt.Errorf("get stream URI: %w", err)
	}

	return &StreamInfo{
		URI:          uri,
		Protocol:     "RTSP",
		ProfileToken: profileToken,
	}, nil
}

func (c *Client) deviceCapabilities(ctx context.Context) (*onviflib.Capabilities, error) {
	caps, err := c.client.GetCapabilities(ctx)
	if err == nil {
		return caps, nil
	}
	// Media2 devices can advertise all supported services through GetServices.
	endpoints := c.client.Endpoints()
	if endpoints.Media2 == nil || endpoints.Media != nil || ctx.Err() != nil {
		return nil, fmt.Errorf("get capabilities: %w", err)
	}
	caps = &onviflib.Capabilities{Device: &onviflib.DeviceCapabilities{}, Media2: &onviflib.MediaCapabilities{}}
	if endpoints.PTZ != nil {
		caps.PTZ = &onviflib.PTZCapabilities{}
	}
	if endpoints.Imaging != nil {
		caps.Imaging = &onviflib.ImagingCapabilities{}
	}
	if endpoints.Events != nil {
		caps.Events = &onviflib.EventsCapabilities{}
	}
	return caps, nil
}

// GetCapabilities retrieves device capabilities (PTZ, streaming, etc.).
func (c *Client) GetCapabilities(ctx context.Context) (*DeviceCapabilities, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}

	caps, err := c.deviceCapabilities(ctx)
	if err != nil {
		return nil, err
	}

	return &DeviceCapabilities{
		PTZ:       caps.PTZ != nil,
		Streaming: caps.Media != nil || c.client.Endpoints().Media2 != nil,
	}, nil
}

// GetCapabilitiesDetailed retrieves detailed device capabilities.
func (c *Client) GetCapabilitiesDetailed(ctx context.Context) (*DeviceCapabilitiesDetailed, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}

	caps, err := c.deviceCapabilities(ctx)
	if err != nil {
		return nil, err
	}

	result := &DeviceCapabilitiesDetailed{
		PTZ:       caps.PTZ != nil,
		Imaging:   caps.Imaging != nil,
		Events:    caps.Events != nil,
		Snapshot:  caps.Media != nil || c.client.Endpoints().Media2 != nil,
		Streaming: caps.Media != nil || c.client.Endpoints().Media2 != nil,
		Device:    caps.Device != nil,
	}

	// Get device information
	deviceInfo, infoErr := c.GetDeviceInformation(ctx)
	if infoErr == nil {
		result.DeviceInfo = deviceInfo
	}

	// Query detailed PTZ capabilities if PTZ is supported
	if caps.PTZ != nil {
		ptzCaps, ptzErr := c.getPTZCapabilitiesDetailed(ctx)
		if ptzErr == nil {
			result.PTZDetail = ptzCaps
		}
	}

	// Test if Imaging service actually works
	if result.Imaging {
		imaging := c.client.ImagingService()
		if imaging != nil {
			// Try to get imaging settings with first profile to test if service works
			profiles, _ := c.GetProfiles(ctx)
			if len(profiles) > 0 {
				_, testErr := imaging.GetImagingSettings(ctx, c.ResolveVideoSourceToken(ctx, profiles[0]))
				if testErr != nil {
					// Imaging service declared but not functional
					result.Imaging = false
				}
			}
		} else {
			result.Imaging = false
		}
	}

	return result, nil
}

// getPTZCapabilitiesDetailed queries detailed PTZ capabilities from the device.
func (c *Client) getPTZCapabilitiesDetailed(ctx context.Context) (PTZCapabilitiesDetailed, error) {
	ptzCaps := PTZCapabilitiesDetailed{
		Supported: true,
	}

	ptzService := c.client.PTZService()

	// Try to get PTZ nodes to check Pan/Tilt and Zoom support
	nodes, err := ptzService.GetPTZNodes(ctx)
	if err != nil {
		return ptzCaps, err
	}

	if len(nodes) > 0 {
		// Most PTZ devices support Pan/Tilt and Zoom
		ptzCaps.PanTilt = true
		ptzCaps.Zoom = true
		ptzCaps.Home = nodes[0].HomeSupported
		ptzCaps.Presets = nodes[0].MaximumNumberOfPresets > 0
	}

	// Try to get PTZ configurations to verify specific capabilities
	configs, err := ptzService.GetPTZConfigurations(ctx)
	if err == nil && len(configs) > 0 {
		// Check if Pan/Tilt spaces are configured
		if configs[0].DefaultAbsolutePantTiltPositionSpace != "" ||
			configs[0].DefaultContinuousPanTiltVelocitySpace != "" {
			ptzCaps.PanTilt = true
		}
		// Check if Zoom spaces are configured
		if configs[0].DefaultAbsoluteZoomPositionSpace != "" ||
			configs[0].DefaultContinuousZoomVelocitySpace != "" {
			ptzCaps.Zoom = true
		}
	}

	return ptzCaps, nil
}

// GetEndpoint returns the device service endpoint URL.
func (c *Client) GetEndpoint() string {
	return c.endpoint
}

// GetRawClient returns the underlying onvif library client.
func (c *Client) GetRawClient() *onviflib.Client {
	return c.client
}

// NewPTZController creates a PTZController backed by this client.
func (c *Client) NewPTZController(profileToken string) PTZController {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	return newSerializedPTZController(&c.soapMu, c.client, profileToken)
}

// ResolveVideoSourceToken resolves the physical video source for Imaging.
func (c *Client) ResolveVideoSourceToken(ctx context.Context, profile DeviceProfile) string {
	if profile.VideoSourceToken != "" {
		return profile.VideoSourceToken
	}
	if profile.VideoSource != "" {
		configs, err := c.client.MediaService().GetVideoSourceConfigurations(ctx)
		if err == nil {
			for _, config := range configs {
				if config.Token == profile.VideoSource && config.SourceToken != "" {
					return config.SourceToken
				}
			}
		}
		return profile.VideoSource // Legacy devices can use the same token for both.
	}
	return profile.Token // Older devices can omit the video source configuration.
}

// NewImagingController creates an ImagingController backed by this client.
func (c *Client) NewImagingController(profileToken string) *ImagingControllerImpl {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	return NewImagingControllerImpl(c.client, profileToken)
}

// NewSnapshotProvider creates a SnapshotProvider backed by this client.
func (c *Client) NewSnapshotProvider(profileToken string) SnapshotProvider {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	return NewSnapshotProviderImpl(c.client, profileToken)
}

// NewEventSubscriber creates an EventSubscriber backed by this client.
func (c *Client) NewEventSubscriber(opts ...EventSubscriberOption) EventSubscriber {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	return NewEventSubscriberImpl(c.client, opts...)
}

// NewDeviceManager creates a DeviceManager backed by this client.
func (c *Client) NewDeviceManager() DeviceManager {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	return NewDeviceManagerImpl(c.client)
}

// GetRecordingService returns the recording service from the underlying client.
func (c *Client) GetRecordingService() *onviflib.RecordingService {
	if c.client == nil {
		return nil
	}
	return c.client.RecordingService()
}

// GetReplayService returns the replay service from the underlying client.
func (c *Client) GetReplayService() *onviflib.ReplayService {
	if c.client == nil {
		return nil
	}
	return c.client.ReplayService()
}

// GetMediaService returns the media service from the underlying client.
func (c *Client) GetMediaService() *onviflib.MediaService {
	if c.client == nil {
		return nil
	}
	return c.client.MediaService()
}
