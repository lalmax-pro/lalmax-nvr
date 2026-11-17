package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
)

// Device configuration operations: NTP, DNS, Hostname, Scopes, NetworkProtocols.

// --- NTP ---

// NTPServer represents an NTP server configuration.
type NTPServer struct {
	Type    string `json:"type"` // "DHCP" or "NTP"
	IPv4    string `json:"ipv4,omitempty"`
	DNSname string `json:"dnsname,omitempty"`
}

// GetNTP retrieves NTP configuration from the device.
func (d *DeviceManager) GetNTP(ctx context.Context) (map[string]interface{}, error) {
	body := `<GetNTP xmlns="http://www.onvif.org/ver10/device/wsdl"/>`

	resp, err := d.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: d.client.endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetNTP failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetNTPResponse"`
		NTPInformation struct {
			FromDHCP bool `xml:"FromDHCP"`
			NTPFromDHCP []struct {
				Type string `xml:"Type"`
				IPv4Address string `xml:"IPv4Address"`
			} `xml:"NTPFromDHCP"`
			NTPManual []struct {
				Type string `xml:"Type"`
				IPv4Address string `xml:"IPv4Address"`
				DNSname string `xml:"DNSname"`
			} `xml:"NTPManual"`
		} `xml:"NTPInformation"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	servers := make([]NTPServer, 0)
	for _, s := range result.NTPInformation.NTPManual {
		servers = append(servers, NTPServer{Type: s.Type, IPv4: s.IPv4Address, DNSname: s.DNSname})
	}

	return map[string]interface{}{
		"from_dhcp": result.NTPInformation.FromDHCP,
		"servers":   servers,
	}, nil
}

// SetNTP sets NTP configuration on the device.
func (d *DeviceManager) SetNTP(ctx context.Context, fromDHCP bool, servers []NTPServer) error {
	serversXML := ""
	for _, s := range servers {
		serversXML += fmt.Sprintf(`<NTPManual>
  <Type>%s</Type>
  <IPv4Address>%s</IPv4Address>
  <DNSname>%s</DNSname>
</NTPManual>`, s.Type, s.IPv4, s.DNSname)
	}

	body := fmt.Sprintf(`<SetNTP xmlns="http://www.onvif.org/ver10/device/wsdl">
  <FromDHCP>%t</FromDHCP>
  %s
</SetNTP>`, fromDHCP, serversXML)

	_, err := d.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: d.client.endpoint, Body: body})
	if err != nil {
		return fmt.Errorf("onvif: SetNTP failed: %w", err)
	}
	return nil
}

// --- DNS ---

// DNSInfo represents DNS configuration.
type DNSInfo struct {
	FromDHCP bool     `json:"from_dhcp"`
	SearchDomain []string `json:"search_domain,omitempty"`
	DNSManual  []DNSServer `json:"dns_manual,omitempty"`
}

// DNSServer represents a DNS server.
type DNSServer struct {
	IPv4Address string `json:"ipv4_address"`
	Type        string `json:"type"` // "IPv4"
}

// GetDNS retrieves DNS configuration from the device.
func (d *DeviceManager) GetDNS(ctx context.Context) (*DNSInfo, error) {
	body := `<GetDNS xmlns="http://www.onvif.org/ver10/device/wsdl"/>`

	resp, err := d.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: d.client.endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetDNS failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetDNSResponse"`
		DNSInformation struct {
			FromDHCP    bool     `xml:"FromDHCP"`
			SearchDomain []string `xml:"SearchDomain"`
			DNSManual   []struct {
				Type        string `xml:"Type"`
				IPv4Address string `xml:"IPv4Address"`
			} `xml:"DNSManual"`
		} `xml:"DNSInformation"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	info := &DNSInfo{
		FromDHCP:     result.DNSInformation.FromDHCP,
		SearchDomain: result.DNSInformation.SearchDomain,
	}
	for _, s := range result.DNSInformation.DNSManual {
		info.DNSManual = append(info.DNSManual, DNSServer{Type: s.Type, IPv4Address: s.IPv4Address})
	}
	return info, nil
}

// SetDNS sets DNS configuration on the device.
func (d *DeviceManager) SetDNS(ctx context.Context, fromDHCP bool, servers []DNSServer, searchDomains []string) error {
	serversXML := ""
	for _, s := range servers {
		serversXML += fmt.Sprintf(`<DNSManual>
  <Type>%s</Type>
  <IPv4Address>%s</IPv4Address>
</DNSManual>`, s.Type, s.IPv4Address)
	}

	domainsXML := ""
	for _, domain := range searchDomains {
		domainsXML += fmt.Sprintf(`<SearchDomain>%s</SearchDomain>`, domain)
	}

	body := fmt.Sprintf(`<SetDNS xmlns="http://www.onvif.org/ver10/device/wsdl">
  <FromDHCP>%t</FromDHCP>
  %s
  %s
</SetDNS>`, fromDHCP, serversXML, domainsXML)

	_, err := d.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: d.client.endpoint, Body: body})
	if err != nil {
		return fmt.Errorf("onvif: SetDNS failed: %w", err)
	}
	return nil
}

// --- Hostname ---

// HostnameInfo represents hostname information.
type HostnameInfo struct {
	FromDHCP bool   `json:"from_dhcp"`
	Name     string `json:"name"`
}

// GetHostname retrieves hostname information from the device.
func (d *DeviceManager) GetHostname(ctx context.Context) (*HostnameInfo, error) {
	body := `<GetHostname xmlns="http://www.onvif.org/ver10/device/wsdl"/>`

	resp, err := d.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: d.client.endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetHostname failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetHostnameResponse"`
		HostnameInformation struct {
			FromDHCP bool   `xml:"FromDHCP"`
			Name     string `xml:"Name"`
		} `xml:"HostnameInformation"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	return &HostnameInfo{
		FromDHCP: result.HostnameInformation.FromDHCP,
		Name:     result.HostnameInformation.Name,
	}, nil
}

// SetHostname sets the hostname on the device.
func (d *DeviceManager) SetHostname(ctx context.Context, name string) error {
	body := fmt.Sprintf(`<SetHostname xmlns="http://www.onvif.org/ver10/device/wsdl">
  <Name>%s</Name>
</SetHostname>`, name)

	_, err := d.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: d.client.endpoint, Body: body})
	if err != nil {
		return fmt.Errorf("onvif: SetHostname failed: %w", err)
	}
	return nil
}

// --- Scopes ---

// Scope represents a device scope.
type Scope struct {
	ScopeDef  string `json:"scope_def"`  // "Fixed" or "Configurable"
	ScopeItem string `json:"scope_item"`
}

// GetScopes retrieves device scopes.
func (d *DeviceManager) GetScopes(ctx context.Context) ([]Scope, error) {
	body := `<GetScopes xmlns="http://www.onvif.org/ver10/device/wsdl"/>`

	resp, err := d.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: d.client.endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetScopes failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetScopesResponse"`
		Scopes  []struct {
			ScopeDef  string `xml:"ScopeDef"`
			ScopeItem string `xml:"ScopeItem"`
		} `xml:"Scopes"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	scopes := make([]Scope, 0, len(result.Scopes))
	for _, s := range result.Scopes {
		scopes = append(scopes, Scope{ScopeDef: s.ScopeDef, ScopeItem: s.ScopeItem})
	}
	return scopes, nil
}

// SetScopes sets device scopes (replaces all configurable scopes).
func (d *DeviceManager) SetScopes(ctx context.Context, scopeItems []string) error {
	itemsXML := ""
	for _, item := range scopeItems {
		itemsXML += fmt.Sprintf(`<Scopes>%s</Scopes>`, item)
	}

	body := fmt.Sprintf(`<SetScopes xmlns="http://www.onvif.org/ver10/device/wsdl">%s</SetScopes>`, itemsXML)

	_, err := d.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: d.client.endpoint, Body: body})
	if err != nil {
		return fmt.Errorf("onvif: SetScopes failed: %w", err)
	}
	return nil
}

// --- Network Protocols ---

// NetworkProtocol represents a network protocol configuration.
type NetworkProtocol struct {
	Name      string `json:"name"` // HTTP, HTTPS, RTSP
	Enabled   bool   `json:"enabled"`
	Port      int    `json:"port"`
}

// GetNetworkProtocols retrieves network protocol configurations.
func (d *DeviceManager) GetNetworkProtocols(ctx context.Context) ([]NetworkProtocol, error) {
	body := `<GetNetworkProtocols xmlns="http://www.onvif.org/ver10/device/wsdl"/>`

	resp, err := d.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: d.client.endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetNetworkProtocols failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetNetworkProtocolsResponse"`
		NetworkProtocols []struct {
			Name    string `xml:"Name"`
			Enabled bool   `xml:"Enabled"`
			Port    []int  `xml:"Port"`
		} `xml:"NetworkProtocols"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	protocols := make([]NetworkProtocol, 0, len(result.NetworkProtocols))
	for _, p := range result.NetworkProtocols {
		port := 0
		if len(p.Port) > 0 {
			port = p.Port[0]
		}
		protocols = append(protocols, NetworkProtocol{
			Name:    p.Name,
			Enabled: p.Enabled,
			Port:    port,
		})
	}
	return protocols, nil
}

// --- System Date and Time ---

// SetSystemDateAndTime sets the system date and time on the device.
func (d *DeviceManager) SetSystemDateAndTime(ctx context.Context, dateTimeType string, hour, minute, second, year, month, day int) error {
	body := fmt.Sprintf(`<SetSystemDateAndTime xmlns="http://www.onvif.org/ver10/device/wsdl">
  <DateTimeType>%s</DateTimeType>
  <DaylightSavings>false</DaylightSavings>
  <UTCDateTime>
    <Time xmlns="http://www.onvif.org/ver10/schema">
      <Hour>%d</Hour>
      <Minute>%d</Minute>
      <Second>%d</Second>
    </Time>
    <Date xmlns="http://www.onvif.org/ver10/schema">
      <Year>%d</Year>
      <Month>%d</Month>
      <Day>%d</Day>
    </Date>
  </UTCDateTime>
</SetSystemDateAndTime>`, dateTimeType, hour, minute, second, year, month, day)

	_, err := d.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: d.client.endpoint, Body: body})
	if err != nil {
		return fmt.Errorf("onvif: SetSystemDateAndTime failed: %w", err)
	}
	return nil
}

// --- Factory Default ---

// SetSystemFactoryDefault resets the device to factory defaults.
func (d *DeviceManager) SetSystemFactoryDefault(ctx context.Context, hardReset bool) error {
	factoryDefault := "Soft"
	if hardReset {
		factoryDefault = "Hard"
	}

	body := fmt.Sprintf(`<SetSystemFactoryDefault xmlns="http://www.onvif.org/ver10/device/wsdl">
  <FactoryDefault>%s</FactoryDefault>
</SetSystemFactoryDefault>`, factoryDefault)

	_, err := d.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: d.client.endpoint, Body: body})
	if err != nil {
		return fmt.Errorf("onvif: SetSystemFactoryDefault failed: %w", err)
	}
	return nil
}

// --- Gateway ---

// NetworkGateway represents a network gateway.
type NetworkGateway struct {
	IPv4Address string `json:"ipv4_address"`
}

// GetNetworkDefaultGateway retrieves the default gateway.
func (d *DeviceManager) GetNetworkDefaultGateway(ctx context.Context) (*NetworkGateway, error) {
	body := `<GetNetworkDefaultGateway xmlns="http://www.onvif.org/ver10/device/wsdl"/>`

	resp, err := d.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: d.client.endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetNetworkDefaultGateway failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetNetworkDefaultGatewayResponse"`
		NetworkGateway struct {
			IPv4Address []string `xml:"IPv4Address"`
		} `xml:"NetworkGateway"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	gw := &NetworkGateway{}
	if len(result.NetworkGateway.IPv4Address) > 0 {
		gw.IPv4Address = result.NetworkGateway.IPv4Address[0]
	}
	return gw, nil
}

// SetNetworkDefaultGateway sets the default gateway.
func (d *DeviceManager) SetNetworkDefaultGateway(ctx context.Context, gateway string) error {
	body := fmt.Sprintf(`<SetNetworkDefaultGateway xmlns="http://www.onvif.org/ver10/device/wsdl">
  <NetworkGateway>
    <IPv4Address>%s</IPv4Address>
  </NetworkGateway>
</SetNetworkDefaultGateway>`, gateway)

	_, err := d.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: d.client.endpoint, Body: body})
	if err != nil {
		return fmt.Errorf("onvif: SetNetworkDefaultGateway failed: %w", err)
	}
	return nil
}

// --- SetNetworkInterfaces ---

// SetNetworkInterfaceRequest represents a network interface set request.
type SetNetworkInterfaceRequest struct {
	Token   string `json:"token"`
	Enabled bool   `json:"enabled"`
	IPv4    *SetIPv4Request `json:"ipv4,omitempty"`
}

// SetIPv4Request represents IPv4 configuration to set.
type SetIPv4Request struct {
	Enabled bool   `json:"enabled"`
	DHCP    bool   `json:"dhcp"`
	Address string `json:"address,omitempty"`
	PrefixLength int `json:"prefix_length,omitempty"`
}

// SetNetworkInterfaces configures network interfaces.
func (d *DeviceManager) SetNetworkInterfaces(ctx context.Context, iface SetNetworkInterfaceRequest) (bool, error) {
	ipv4XML := ""
	if iface.IPv4 != nil {
		ipv4XML = fmt.Sprintf(`<NetworkInterface>
  <Enabled xmlns="http://www.onvif.org/ver10/schema">%t</Enabled>
  <IPv4 xmlns="http://www.onvif.org/ver10/schema">
    <Enabled>%t</Enabled>
    <Manual>
      <Address>%s</Address>
      <PrefixLength>%d</PrefixLength>
    </Manual>
    <DHCP>%t</DHCP>
  </IPv4>
</NetworkInterface>`, iface.Enabled, iface.IPv4.Enabled, iface.IPv4.Address, iface.IPv4.PrefixLength, iface.IPv4.DHCP)
	}

	body := fmt.Sprintf(`<SetNetworkInterfaces xmlns="http://www.onvif.org/ver10/device/wsdl">
  <InterfaceToken>%s</InterfaceToken>
  %s
</SetNetworkInterfaces>`, iface.Token, ipv4XML)

	resp, err := d.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: d.client.endpoint, Body: body})
	if err != nil {
		return false, fmt.Errorf("onvif: SetNetworkInterfaces failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"SetNetworkInterfacesResponse"`
		RebootNeeded bool `xml:"RebootNeeded"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return false, err
	}

	return result.RebootNeeded, nil
}

// --- Device Service Capabilities ---

// GetServiceCapabilities retrieves device service capabilities.
func (d *DeviceManager) GetServiceCapabilities(ctx context.Context) (map[string]bool, error) {
	body := `<GetServiceCapabilities xmlns="http://www.onvif.org/ver10/device/wsdl"/>`

	resp, err := d.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: d.client.endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetServiceCapabilities failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetServiceCapabilitiesResponse"`
		Caps    struct {
			Network struct {
				IPFilter          bool `xml:"IPFilter,attr"`
				ZeroConfiguration bool `xml:"ZeroConfiguration,attr"`
				IPVersion6        bool `xml:"IPVersion6,attr"`
				DynDNS            bool `xml:"DynDNS,attr"`
				Dot11Configuration bool `xml:"Dot11Configuration,attr"`
				Dot1XConfigurations int `xml:"Dot1XConfigurations,attr"`
				HostnameFromDHCP  bool `xml:"HostnameFromDHCP,attr"`
				NTP               int  `xml:"NTP,attr"`
				DHCPv6            bool `xml:"DHCPv6,attr"`
			} `xml:"Network"`
			Security struct {
				TLS1_1                bool `xml:"TLS1.1,attr"`
				TLS1_2                bool `xml:"TLS1.2,attr"`
				OnboardKeyGeneration  bool `xml:"OnboardKeyGeneration,attr"`
				AccessPolicyConfig    bool `xml:"AccessPolicyConfig,attr"`
				DefaultAccessPolicy   bool `xml:"DefaultAccessPolicy,attr"`
				Dot1X                 bool `xml:"Dot1X,attr"`
				RemoteUserHandling    bool `xml:"RemoteUserHandling,attr"`
				X_509Token            bool `xml:"X.509Token,attr"`
				SAMLToken             bool `xml:"SAMLToken,attr"`
				KerberosToken         bool `xml:"KerberosToken,attr"`
				UsernameToken         bool `xml:"UsernameToken,attr"`
				HttpDigest            bool `xml:"HttpDigest,attr"`
				RELToken              bool `xml:"RELToken,attr"`
				SupportedEAPMethods   int  `xml:"SupportedEAPMethods,attr"`
				MaxUsers              int  `xml:"MaxUsers,attr"`
				MaxUserNameLength     int  `xml:"MaxUserNameLength,attr"`
				MaxPasswordLength     int  `xml:"MaxPasswordLength,attr"`
			} `xml:"Security"`
			System struct {
				DiscoveryResolve         bool `xml:"DiscoveryResolve,attr"`
				DiscoveryBye             bool `xml:"DiscoveryBye,attr"`
				RemoteDiscovery          bool `xml:"RemoteDiscovery,attr"`
				SystemBackup             bool `xml:"SystemBackup,attr"`
				SystemLogging            bool `xml:"SystemLogging,attr"`
				FirmwareUpgrade          bool `xml:"FirmwareUpgrade,attr"`
				HttpFirmwareUpgrade      bool `xml:"HttpFirmwareUpgrade,attr"`
				HttpSystemBackup         bool `xml:"HttpSystemBackup,attr"`
				HttpSystemLogging        bool `xml:"HttpSystemLogging,attr"`
				HttpSupportInformation   bool `xml:"HttpSupportInformation,attr"`
				StorageConfiguration     bool `xml:"StorageConfiguration,attr"`
				MaxStorageConfigurations int  `xml:"MaxStorageConfigurations,attr"`
				GeoLocationEntries       int  `xml:"GeoLocationEntries,attr"`
				AutoGeo                  string `xml:"AutoGeo,attr"`
			} `xml:"System"`
			Misc struct {
				AuxiliaryCommands string `xml:"AuxiliaryCommands,attr"`
			} `xml:"Misc"`
		} `xml:"Capabilities"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	return map[string]bool{
		"network_ip_filter":          result.Caps.Network.IPFilter,
		"network_zero_configuration": result.Caps.Network.ZeroConfiguration,
		"network_ipv6":               result.Caps.Network.IPVersion6,
		"network_dyn_dns":            result.Caps.Network.DynDNS,
		"network_hostname_from_dhcp": result.Caps.Network.HostnameFromDHCP,
		"security_tls_1_1":           result.Caps.Security.TLS1_1,
		"security_tls_1_2":           result.Caps.Security.TLS1_2,
		"security_username_token":    result.Caps.Security.UsernameToken,
		"security_http_digest":       result.Caps.Security.HttpDigest,
		"system_discovery_resolve":   result.Caps.System.DiscoveryResolve,
		"system_discovery_bye":       result.Caps.System.DiscoveryBye,
		"system_firmware_upgrade":    result.Caps.System.FirmwareUpgrade,
		"has_auxiliary_commands":      result.Caps.Misc.AuxiliaryCommands != "",
	}, nil
}

// SendAuxiliaryCommand sends an auxiliary command to the device.
func (d *DeviceManager) SendAuxiliaryCommand(ctx context.Context, auxiliaryData string) (string, error) {
	body := fmt.Sprintf(`<SendAuxiliaryCommand xmlns="http://www.onvif.org/ver10/device/wsdl">
  <AuxiliaryData>%s</AuxiliaryData>
</SendAuxiliaryCommand>`, auxiliaryData)

	resp, err := d.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: d.client.endpoint, Body: body})
	if err != nil {
		return "", fmt.Errorf("onvif: SendAuxiliaryCommand failed: %w", err)
	}

	var result struct {
		XMLName       xml.Name `xml:"SendAuxiliaryCommandResponse"`
		AuxiliaryData string   `xml:"AuxiliaryData"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return "", err
	}

	return result.AuxiliaryData, nil
}

// Helper to extract scope name/hardware from scope URIs.
func extractScopeValue(scopes []string, prefix string) string {
	for _, scope := range scopes {
		if strings.Contains(scope, prefix) {
			parts := strings.Split(scope, "/")
			if len(parts) > 0 {
				return parts[len(parts)-1]
			}
		}
	}
	return ""
}
