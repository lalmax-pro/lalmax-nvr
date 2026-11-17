package onvif

import (
	"context"
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultDiscoveryTimeout = 5 * time.Second

// WS-Discovery multicast address and port
const wsDiscoveryAddr = "239.255.255.250:3702"

// DiscoveredDevice represents an ONVIF device found via WS-Discovery.
type DiscoveredDevice struct {
	UUID     string   `json:"uuid"`
	Name     string   `json:"name"`
	XAddrs   []string `json:"xaddrs"`
	Scopes   []string `json:"scopes"`
	Hardware string   `json:"hardware"`
	Endpoint string   `json:"endpoint"`
}

const wsDiscoveryProbe = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing">
  <s:Header>
    <a:Action s:mustUnderstand="1">http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</a:Action>
    <a:MessageID>uuid:%s</a:MessageID>
    <a:ReplyTo><a:Address>http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</a:Address></a:ReplyTo>
    <a:To s:mustUnderstand="1">urn:schemas-xmlsoap-org:ws:2005:04:discovery</a:To>
  </s:Header>
  <s:Body>
    <Probe xmlns="http://schemas.xmlsoap.org/ws/2005/04/discovery">
      <d:Types xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:dp0="http://www.onvif.org/ver10/network/wsdl">dp0:NetworkVideoTransmitter</d:Types>
    </Probe>
  </s:Body>
</s:Envelope>`

type probeMatchEnvelope struct {
	XMLName xml.Name `xml:"Envelope"`
	Header  struct {
		RelatesTo string `xml:"RelatesTo"`
		Action    string `xml:"Action"`
	} `xml:"Header"`
	Body struct {
		ProbeMatches struct {
			ProbeMatch []probeMatchEntry `xml:"ProbeMatch"`
		} `xml:"ProbeMatches"`
		Hello *helloEntry `xml:"Hello"`
	} `xml:"Body"`
}

type helloEntry struct {
	EndpointRef struct {
		Address string `xml:"Address"`
	} `xml:"EndpointReference"`
	Types  string `xml:"Types"`
	Scopes string `xml:"Scopes"`
	XAddrs string `xml:"XAddrs"`
}

type probeMatchEntry struct {
	EndpointRef struct {
		Address string `xml:"Address"`
	} `xml:"EndpointReference"`
	Types  string `xml:"Types"`
	Scopes string `xml:"Scopes"`
	XAddrs string `xml:"XAddrs"`
}

// DiscoverDevices performs WS-Discovery multicast to find ONVIF devices on the local network.
func DiscoverDevices(ctx context.Context, timeout time.Duration) ([]*DiscoveredDevice, error) {
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}

	// Resolve multicast address
	addr, err := net.ResolveUDPAddr("udp4", wsDiscoveryAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve multicast address: %w", err)
	}

	// Each probe gets its own socket so old devices without RelatesTo still
	// have an isolated reply destination.
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, fmt.Errorf("create UDP connection: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	// Set deadline
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}

	// Generate probe message
	messageID := generateProbeUUID()
	probeMsg := fmt.Sprintf(wsDiscoveryProbe, messageID)

	// Send multicast probe
	if _, err := conn.WriteTo([]byte(probeMsg), addr); err != nil {
		return nil, fmt.Errorf("send probe: %w", err)
	}

	// Collect responses
	var devices []*DiscoveredDevice
	seen := make(map[string]bool)
	buf := make([]byte, 65535)

	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return devices, ctx.Err()
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				break
			}
			return devices, fmt.Errorf("receive probe match: %w", err)
		}

		for _, device := range parseDiscoveryMatches(buf[:n], remoteAddr.String(), "uuid:"+messageID) {
			key := device.UUID
			if key == "" {
				key = device.Endpoint
			}
			if !seen[key] {
				seen[key] = true
				devices = append(devices, device)
			}
		}
	}

	return devices, nil
}

// ProbeDevice sends a direct WS-Discovery Probe via HTTP POST to a specific host:port.
// Returns nil if the device is not ONVIF or doesn't respond.
func ProbeDevice(ctx context.Context, host string, port int, timeout time.Duration) (*DiscoveredDevice, error) {
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}

	endpoint := (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, strconv.Itoa(port)), Path: "/onvif/device_service"}).String()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	messageID := generateProbeUUID()
	probeMsg := fmt.Sprintf(wsDiscoveryProbe, messageID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(probeMsg))
	if err != nil {
		return nil, fmt.Errorf("create probe request: %w", err)
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("probe request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read probe response: %w", err)
	}

	matches := parseDiscoveryMatches(body, endpoint, "uuid:"+messageID)
	if len(matches) == 0 {
		// Some older devices answer a directed HTTP probe with a Hello.
		return parseDiscoveryHello(body, endpoint), nil
	}
	return matches[0], nil
}

func parseDiscoveryMatches(data []byte, endpoint, requestID string) []*DiscoveredDevice {
	var envelope probeMatchEnvelope
	if err := xml.Unmarshal(data, &envelope); err != nil {
		return nil
	}
	relatesTo := strings.TrimSpace(envelope.Header.RelatesTo)
	if relatesTo != "" && !strings.EqualFold(strings.TrimPrefix(relatesTo, "urn:"), strings.TrimPrefix(requestID, "urn:")) {
		return nil
	}
	if action := strings.TrimSpace(envelope.Header.Action); action != "" && !strings.HasSuffix(action, "/ProbeMatches") {
		return nil
	}
	var devices []*DiscoveredDevice
	for _, match := range envelope.Body.ProbeMatches.ProbeMatch {
		devices = append(devices, deviceFromMatch(match.EndpointRef.Address, match.Scopes, match.XAddrs, endpoint))
	}
	return devices
}

func parseDiscoveryHello(data []byte, endpoint string) *DiscoveredDevice {
	var envelope probeMatchEnvelope
	if err := xml.Unmarshal(data, &envelope); err != nil || envelope.Body.Hello == nil {
		return nil
	}
	return deviceFromHello(*envelope.Body.Hello, endpoint)
}

func deviceFromHello(h helloEntry, fallback string) *DiscoveredDevice {
	return deviceFromMatch(h.EndpointRef.Address, h.Scopes, h.XAddrs, fallback)
}

func deviceFromMatch(uuid, scopesRaw, xaddrsRaw, fallback string) *DiscoveredDevice {
	scopes := strings.Fields(scopesRaw)
	xaddrs := strings.Fields(xaddrsRaw)

	var name, hardware string
	for _, scope := range scopes {
		if strings.Contains(scope, "/name/") {
			parts := strings.Split(scope, "/")
			name = parts[len(parts)-1]
		}
		if strings.Contains(scope, "/hardware/") {
			parts := strings.Split(scope, "/")
			hardware = parts[len(parts)-1]
		}
	}

	deviceEndpoint := fallback
	if len(xaddrs) > 0 {
		deviceEndpoint = xaddrs[0]
	}

	return &DiscoveredDevice{
		UUID:     uuid,
		Name:     name,
		XAddrs:   xaddrs,
		Scopes:   scopes,
		Hardware: hardware,
		Endpoint: deviceEndpoint,
	}
}

// ListenHello binds UDP 3702 and invokes handler for inbound WS-Discovery Hello messages.
// The caller should cancel ctx to stop. Bind failures are returned so callers can fall back
// to active Probe scanning.
func ListenHello(ctx context.Context, networkInterface string, handler func(*DiscoveredDevice)) error {
	if handler == nil {
		return fmt.Errorf("hello handler is required")
	}
	var ifi *net.Interface
	if name := strings.TrimSpace(networkInterface); name != "" {
		var err error
		ifi, err = net.InterfaceByName(name)
		if err != nil {
			return fmt.Errorf("lookup interface %q: %w", name, err)
		}
	}
	multicast, err := net.ResolveUDPAddr("udp4", wsDiscoveryAddr)
	if err != nil {
		return fmt.Errorf("resolve multicast address: %w", err)
	}
	conn, err := net.ListenMulticastUDP("udp4", ifi, multicast)
	if err != nil {
		return fmt.Errorf("listen hello on :3702: %w", err)
	}

	go func() {
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		buf := make([]byte, 65535)
		for {
			n, remote, err := conn.ReadFromUDP(buf)
			if err != nil || ctx.Err() != nil {
				return
			}
			if device := parseDiscoveryHello(buf[:n], remote.String()); device != nil {
				handler(device)
			}
		}
	}()

	return nil
}

func generateProbeUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
