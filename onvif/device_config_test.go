package onvif

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetSetNTP(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetNTP"):
			return soapOK(`<GetNTPResponse>
				<NTPInformation>
					<FromDHCP>false</FromDHCP>
					<NTPFromDHCP>
						<Type>IPv4</Type>
						<IPv4Address>192.168.1.1</IPv4Address>
					</NTPFromDHCP>
					<NTPManual>
						<Type>IPv4</Type>
						<IPv4Address>8.8.8.8</IPv4Address>
						<DNSname>time.example</DNSname>
					</NTPManual>
				</NTPInformation>
			</GetNTPResponse>`)
		case strings.Contains(body, "SetNTP"):
			require.Contains(t, body, "<FromDHCP>false</FromDHCP>")
			require.Contains(t, body, "<IPv4Address>1.1.1.1</IPv4Address>")
			require.Contains(t, body, "<DNSname>ntp.local</DNSname>")
			return soapOK(`<SetNTPResponse/>`)
		default:
			return ""
		}
	})
	mgr := NewDeviceManager(newTestClient(t, server))
	ctx := context.Background()
	info, err := mgr.GetNTP(ctx)
	require.NoError(t, err)
	require.Equal(t, false, info["from_dhcp"])
	servers, ok := info["servers"].([]NTPServer)
	require.True(t, ok)
	require.Equal(t, []NTPServer{{Type: "IPv4", IPv4: "8.8.8.8", DNSname: "time.example"}}, servers)
	require.NoError(t, mgr.SetNTP(ctx, false, []NTPServer{{Type: "IPv4", IPv4: "1.1.1.1", DNSname: "ntp.local"}}))
}

func TestGetSetDNS(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetDNS"):
			return soapOK(`<GetDNSResponse>
				<DNSInformation>
					<FromDHCP>true</FromDHCP>
					<SearchDomain>lan</SearchDomain>
					<SearchDomain>corp</SearchDomain>
					<DNSManual>
						<Type>IPv4</Type>
						<IPv4Address>8.8.4.4</IPv4Address>
					</DNSManual>
				</DNSInformation>
			</GetDNSResponse>`)
		case strings.Contains(body, "SetDNS"):
			require.Contains(t, body, "<FromDHCP>false</FromDHCP>")
			require.Contains(t, body, "<IPv4Address>9.9.9.9</IPv4Address>")
			require.Contains(t, body, "<SearchDomain>home</SearchDomain>")
			return soapOK(`<SetDNSResponse/>`)
		default:
			return ""
		}
	})
	mgr := NewDeviceManager(newTestClient(t, server))
	ctx := context.Background()
	info, err := mgr.GetDNS(ctx)
	require.NoError(t, err)
	require.True(t, info.FromDHCP)
	require.Equal(t, []string{"lan", "corp"}, info.SearchDomain)
	require.Equal(t, []DNSServer{{Type: "IPv4", IPv4Address: "8.8.4.4"}}, info.DNSManual)
	require.NoError(t, mgr.SetDNS(ctx, false, []DNSServer{{Type: "IPv4", IPv4Address: "9.9.9.9"}}, []string{"home"}))
}

func TestGetSetHostnameAndScopes(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetHostname"):
			return soapOK(`<GetHostnameResponse>
				<HostnameInformation>
					<FromDHCP>false</FromDHCP>
					<Name>cam-lobby</Name>
				</HostnameInformation>
			</GetHostnameResponse>`)
		case strings.Contains(body, "SetHostname"):
			require.Contains(t, body, "<Name>cam-new</Name>")
			return soapOK(`<SetHostnameResponse/>`)
		case strings.Contains(body, "GetScopes"):
			return soapOK(`<GetScopesResponse>
				<Scopes>
					<ScopeDef>Fixed</ScopeDef>
					<ScopeItem>onvif://www.onvif.org/type/video_encoder</ScopeItem>
				</Scopes>
				<Scopes>
					<ScopeDef>Configurable</ScopeDef>
					<ScopeItem>onvif://www.onvif.org/name/Lobby</ScopeItem>
				</Scopes>
			</GetScopesResponse>`)
		case strings.Contains(body, "SetScopes"):
			require.Contains(t, body, "<Scopes>onvif://www.onvif.org/name/Hall</Scopes>")
			return soapOK(`<SetScopesResponse/>`)
		default:
			return ""
		}
	})
	mgr := NewDeviceManager(newTestClient(t, server))
	ctx := context.Background()
	host, err := mgr.GetHostname(ctx)
	require.NoError(t, err)
	require.False(t, host.FromDHCP)
	require.Equal(t, "cam-lobby", host.Name)
	require.NoError(t, mgr.SetHostname(ctx, "cam-new"))

	scopes, err := mgr.GetScopes(ctx)
	require.NoError(t, err)
	require.Equal(t, []Scope{
		{ScopeDef: "Fixed", ScopeItem: "onvif://www.onvif.org/type/video_encoder"},
		{ScopeDef: "Configurable", ScopeItem: "onvif://www.onvif.org/name/Lobby"},
	}, scopes)
	require.NoError(t, mgr.SetScopes(ctx, []string{"onvif://www.onvif.org/name/Hall"}))
}

func TestGetNetworkProtocolsAndGateway(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetNetworkProtocols"):
			return soapOK(`<GetNetworkProtocolsResponse>
				<NetworkProtocols>
					<Name>HTTP</Name>
					<Enabled>true</Enabled>
					<Port>80</Port>
					<Port>8080</Port>
				</NetworkProtocols>
				<NetworkProtocols>
					<Name>RTSP</Name>
					<Enabled>false</Enabled>
				</NetworkProtocols>
			</GetNetworkProtocolsResponse>`)
		case strings.Contains(body, "GetNetworkDefaultGateway"):
			return soapOK(`<GetNetworkDefaultGatewayResponse>
				<NetworkGateway>
					<IPv4Address>192.168.1.1</IPv4Address>
					<IPv4Address>192.168.1.254</IPv4Address>
				</NetworkGateway>
			</GetNetworkDefaultGatewayResponse>`)
		case strings.Contains(body, "SetNetworkDefaultGateway"):
			require.Contains(t, body, "<IPv4Address>10.0.0.1</IPv4Address>")
			return soapOK(`<SetNetworkDefaultGatewayResponse/>`)
		default:
			return ""
		}
	})
	mgr := NewDeviceManager(newTestClient(t, server))
	ctx := context.Background()
	protos, err := mgr.GetNetworkProtocols(ctx)
	require.NoError(t, err)
	require.Equal(t, []NetworkProtocol{
		{Name: "HTTP", Enabled: true, Port: 80},
		{Name: "RTSP", Enabled: false, Port: 0},
	}, protos)

	gw, err := mgr.GetNetworkDefaultGateway(ctx)
	require.NoError(t, err)
	require.Equal(t, "192.168.1.1", gw.IPv4Address)
	require.NoError(t, mgr.SetNetworkDefaultGateway(ctx, "10.0.0.1"))
}

func TestGetNetworkDefaultGatewayEmpty(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetNetworkDefaultGatewayResponse><NetworkGateway></NetworkGateway></GetNetworkDefaultGatewayResponse>`)
	})
	gw, err := NewDeviceManager(newTestClient(t, server)).GetNetworkDefaultGateway(context.Background())
	require.NoError(t, err)
	require.Empty(t, gw.IPv4Address)
}

func TestSetSystemDateTimeFactoryDefaultAndAux(t *testing.T) {
	var factory string
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "SetSystemDateAndTime"):
			require.Contains(t, body, "<DateTimeType>Manual</DateTimeType>")
			require.Contains(t, body, "<Hour>12</Hour>")
			require.Contains(t, body, "<Year>2024</Year>")
			return soapOK(`<SetSystemDateAndTimeResponse/>`)
		case strings.Contains(body, "SetSystemFactoryDefault"):
			if strings.Contains(body, "<FactoryDefault>Hard</FactoryDefault>") {
				factory = "Hard"
			} else {
				factory = "Soft"
			}
			return soapOK(`<SetSystemFactoryDefaultResponse/>`)
		case strings.Contains(body, "SendAuxiliaryCommand"):
			require.Contains(t, body, "<AuxiliaryData>tt:Wiper|On</AuxiliaryData>")
			return soapOK(`<SendAuxiliaryCommandResponse><AuxiliaryData>tt:Wiper|On</AuxiliaryData></SendAuxiliaryCommandResponse>`)
		default:
			return ""
		}
	})
	mgr := NewDeviceManager(newTestClient(t, server))
	ctx := context.Background()
	require.NoError(t, mgr.SetSystemDateAndTime(ctx, "Manual", 12, 30, 45, 2024, 6, 15))
	require.NoError(t, mgr.SetSystemFactoryDefault(ctx, true))
	require.Equal(t, "Hard", factory)
	require.NoError(t, mgr.SetSystemFactoryDefault(ctx, false))
	require.Equal(t, "Soft", factory)
	out, err := mgr.SendAuxiliaryCommand(ctx, "tt:Wiper|On")
	require.NoError(t, err)
	require.Equal(t, "tt:Wiper|On", out)
}

func TestSetNetworkInterfaces(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		require.Contains(t, body, "<InterfaceToken>eth0</InterfaceToken>")
		require.Contains(t, body, "<Address>192.168.1.20</Address>")
		require.Contains(t, body, "<PrefixLength>24</PrefixLength>")
		return soapOK(`<SetNetworkInterfacesResponse><RebootNeeded>true</RebootNeeded></SetNetworkInterfacesResponse>`)
	})
	needed, err := NewDeviceManager(newTestClient(t, server)).SetNetworkInterfaces(context.Background(), SetNetworkInterfaceRequest{
		Token:   "eth0",
		Enabled: true,
		IPv4: &SetIPv4Request{
			Enabled:      true,
			DHCP:         false,
			Address:      "192.168.1.20",
			PrefixLength: 24,
		},
	})
	require.NoError(t, err)
	require.True(t, needed)

	noIPv4 := newONVIFDeviceServer(t, func(body string) string {
		require.Contains(t, body, "<InterfaceToken>eth1</InterfaceToken>")
		require.NotContains(t, body, "<IPv4")
		return soapOK(`<SetNetworkInterfacesResponse><RebootNeeded>false</RebootNeeded></SetNetworkInterfacesResponse>`)
	})
	needed, err = NewDeviceManager(newTestClient(t, noIPv4)).SetNetworkInterfaces(context.Background(), SetNetworkInterfaceRequest{
		Token: "eth1",
	})
	require.NoError(t, err)
	require.False(t, needed)
}

func TestGetServiceCapabilities(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetServiceCapabilitiesResponse>
			<Capabilities>
				<Network IPFilter="true" ZeroConfiguration="true" IPVersion6="false" DynDNS="true" HostnameFromDHCP="true"/>
				<Security UsernameToken="true" HttpDigest="true"/>
				<System DiscoveryResolve="true" DiscoveryBye="false" FirmwareUpgrade="true"/>
				<Misc AuxiliaryCommands="tt:Wiper"/>
			</Capabilities>
		</GetServiceCapabilitiesResponse>`)
	})
	caps, err := NewDeviceManager(newTestClient(t, server)).GetServiceCapabilities(context.Background())
	require.NoError(t, err)
	require.True(t, caps["network_ip_filter"])
	require.True(t, caps["network_zero_configuration"])
	require.False(t, caps["network_ipv6"])
	require.True(t, caps["security_username_token"])
	require.True(t, caps["system_discovery_resolve"])
	require.False(t, caps["system_discovery_bye"])
	require.True(t, caps["has_auxiliary_commands"])
}

func TestGetServiceCapabilitiesNoAux(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetServiceCapabilitiesResponse>
			<Capabilities>
				<Network/>
				<Security/>
				<System/>
				<Misc AuxiliaryCommands=""/>
			</Capabilities>
		</GetServiceCapabilitiesResponse>`)
	})
	caps, err := NewDeviceManager(newTestClient(t, server)).GetServiceCapabilities(context.Background())
	require.NoError(t, err)
	require.False(t, caps["has_auxiliary_commands"])
}

func TestDeviceConfigSOAPAndParseErrors(t *testing.T) {
	fault := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "denied")
	})
	mgr := NewDeviceManager(newTestClient(t, fault))
	ctx := context.Background()
	_, err := mgr.GetNTP(ctx)
	require.ErrorContains(t, err, "GetNTP failed")
	require.ErrorContains(t, mgr.SetNTP(ctx, false, nil), "SetNTP failed")
	_, err = mgr.GetDNS(ctx)
	require.ErrorContains(t, err, "GetDNS failed")
	require.ErrorContains(t, mgr.SetDNS(ctx, false, nil, nil), "SetDNS failed")
	_, err = mgr.GetHostname(ctx)
	require.ErrorContains(t, err, "GetHostname failed")
	require.ErrorContains(t, mgr.SetHostname(ctx, "x"), "SetHostname failed")
	_, err = mgr.GetScopes(ctx)
	require.ErrorContains(t, err, "GetScopes failed")
	require.ErrorContains(t, mgr.SetScopes(ctx, []string{"x"}), "SetScopes failed")
	_, err = mgr.GetNetworkProtocols(ctx)
	require.ErrorContains(t, err, "GetNetworkProtocols failed")
	require.ErrorContains(t, mgr.SetSystemDateAndTime(ctx, "Manual", 0, 0, 0, 2024, 1, 1), "SetSystemDateAndTime failed")
	require.ErrorContains(t, mgr.SetSystemFactoryDefault(ctx, false), "SetSystemFactoryDefault failed")
	_, err = mgr.GetNetworkDefaultGateway(ctx)
	require.ErrorContains(t, err, "GetNetworkDefaultGateway failed")
	require.ErrorContains(t, mgr.SetNetworkDefaultGateway(ctx, "1.1.1.1"), "SetNetworkDefaultGateway failed")
	_, err = mgr.SetNetworkInterfaces(ctx, SetNetworkInterfaceRequest{Token: "eth0"})
	require.ErrorContains(t, err, "SetNetworkInterfaces failed")
	_, err = mgr.GetServiceCapabilities(ctx)
	require.ErrorContains(t, err, "GetServiceCapabilities failed")
	_, err = mgr.SendAuxiliaryCommand(ctx, "x")
	require.ErrorContains(t, err, "SendAuxiliaryCommand failed")

	parseCalls := map[string]func(*DeviceManager) error{
		"GetNTP":                   func(m *DeviceManager) error { _, err := m.GetNTP(ctx); return err },
		"GetDNS":                   func(m *DeviceManager) error { _, err := m.GetDNS(ctx); return err },
		"GetHostname":              func(m *DeviceManager) error { _, err := m.GetHostname(ctx); return err },
		"GetScopes":                func(m *DeviceManager) error { _, err := m.GetScopes(ctx); return err },
		"GetNetworkProtocols":      func(m *DeviceManager) error { _, err := m.GetNetworkProtocols(ctx); return err },
		"GetNetworkDefaultGateway": func(m *DeviceManager) error { _, err := m.GetNetworkDefaultGateway(ctx); return err },
		"SetNetworkInterfaces": func(m *DeviceManager) error {
			_, err := m.SetNetworkInterfaces(ctx, SetNetworkInterfaceRequest{Token: "eth0"})
			return err
		},
		"GetServiceCapabilities": func(m *DeviceManager) error { _, err := m.GetServiceCapabilities(ctx); return err },
		"SendAuxiliaryCommand":   func(m *DeviceManager) error { _, err := m.SendAuxiliaryCommand(ctx, "x"); return err },
	}
	for name, call := range parseCalls {
		name, call := name, call
		t.Run("parse_"+name, func(t *testing.T) {
			srv := newONVIFDeviceServer(t, func(body string) string {
				return soapOK(`<` + name + `Response><`)
			})
			require.Error(t, call(NewDeviceManager(newTestClient(t, srv))))
		})
	}
}

func TestExtractScopeValue(t *testing.T) {
	require.Equal(t, "DS-2CD", extractScopeValue([]string{
		"onvif://www.onvif.org/type/video_encoder",
		"onvif://www.onvif.org/hardware/DS-2CD",
	}, "hardware"))
	require.Equal(t, "Lobby", extractScopeValue([]string{"onvif://www.onvif.org/name/Lobby"}, "name"))
	require.Empty(t, extractScopeValue([]string{"other"}, "hardware"))
	require.Empty(t, extractScopeValue(nil, "hardware"))
}
