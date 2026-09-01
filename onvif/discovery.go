package onvif

import (
	"context"
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const defaultDiscoveryTimeout = 5 * time.Second

// WS-Discovery multicast address and port
const (
	wsDiscoveryAddr = "239.255.255.250:3702"
	wsDiscoveryPort = 3702
)

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

	// Create UDP connection
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: wsDiscoveryPort})
	if err != nil {
		// If port 3702 is busy, try any port
		conn, err = net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
		if err != nil {
			return nil, fmt.Errorf("create UDP connection: %w", err)
		}
	}
	defer conn.Close()

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
			// Timeout or connection closed
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				break
			}
			if err == io.EOF {
				break
			}
			// Ignore other errors and continue
			continue
		}

		if n == 0 {
			continue
		}

		device, err := parseProbeMatchResponse(buf[:n], remoteAddr.String())
		if err != nil || device == nil {
			continue
		}

		// Deduplicate by UUID
		if seen[device.UUID] {
			continue
		}
		seen[device.UUID] = true

		// Use first XAddr as endpoint if available
		if len(device.XAddrs) > 0 {
			device.Endpoint = device.XAddrs[0]
		}

		devices = append(devices, device)
	}

	return devices, nil
}

// ProbeDevice sends a direct WS-Discovery Probe via HTTP POST to a specific host:port.
// Returns nil if the device is not ONVIF or doesn't respond.
func ProbeDevice(ctx context.Context, host string, port int, timeout time.Duration) (*DiscoveredDevice, error) {
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}

	endpoint := fmt.Sprintf("http://%s:%d/onvif/device_service", host, port)

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

	return parseProbeMatchResponse(body, endpoint)
}

func parseProbeMatchResponse(data []byte, endpoint string) (*DiscoveredDevice, error) {
	var envelope probeMatchEnvelope
	if err := xml.Unmarshal(data, &envelope); err != nil {
		return nil, nil
	}

	if envelope.Body.Hello != nil {
		return deviceFromHello(*envelope.Body.Hello, endpoint), nil
	}

	if len(envelope.Body.ProbeMatches.ProbeMatch) == 0 {
		return nil, nil
	}

	pm := envelope.Body.ProbeMatches.ProbeMatch[0]
	return deviceFromMatch(pm.EndpointRef.Address, pm.Scopes, pm.XAddrs, endpoint), nil
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
	var listenAddr *net.UDPAddr
	if strings.TrimSpace(networkInterface) != "" {
		ifi, err := net.InterfaceByName(networkInterface)
		if err != nil {
			return fmt.Errorf("lookup interface %q: %w", networkInterface, err)
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			return fmt.Errorf("interface addrs: %w", err)
		}
		var ip net.IP
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				ip = n.IP.To4()
				break
			}
		}
		if ip == nil {
			return fmt.Errorf("interface %q has no IPv4 address", networkInterface)
		}
		listenAddr = &net.UDPAddr{IP: ip, Port: wsDiscoveryPort}
	} else {
		listenAddr = &net.UDPAddr{Port: wsDiscoveryPort}
	}

	conn, err := net.ListenUDP("udp4", listenAddr)
	if err != nil {
		return fmt.Errorf("listen hello on :3702: %w", err)
	}

	go func() {
		defer conn.Close()
		buf := make([]byte, 65535)
		for {
			select {
			case <-ctx.Done():
				_ = conn.SetDeadline(time.Now())
				return
			default:
			}
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, remote, err := conn.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			if n == 0 {
				continue
			}
			device, err := parseProbeMatchResponse(buf[:n], remote.String())
			if err != nil || device == nil {
				continue
			}
			if len(device.XAddrs) > 0 {
				device.Endpoint = device.XAddrs[0]
			}
			handler(device)
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
