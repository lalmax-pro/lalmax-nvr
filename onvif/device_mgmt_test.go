package onvif

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeviceManagerGetUsersAndReboot(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetUsers"):
			return soapOK(`<GetUsersResponse>
				<User><Username>admin</Username><UserLevel>Administrator</UserLevel></User>
				<User><Username>guest</Username><UserLevel>User</UserLevel></User>
			</GetUsersResponse>`)
		case strings.Contains(body, "SystemReboot"):
			return soapOK(`<SystemRebootResponse><Message>Rebooting</Message></SystemRebootResponse>`)
		default:
			return ""
		}
	})
	mgr := NewDeviceManager(newTestClient(t, server))
	ctx := context.Background()

	users, err := mgr.GetUsers(ctx)
	require.NoError(t, err)
	require.Len(t, users, 2)
	require.Equal(t, "admin", users[0].Username)
	require.Equal(t, "Administrator", users[0].UserLevel)
	require.Equal(t, "guest", users[1].Username)

	msg, err := mgr.SystemReboot(ctx)
	require.NoError(t, err)
	require.Equal(t, "Rebooting", msg)
}

func TestDeviceManagerUserCRUD(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "CreateUsers"):
			require.Contains(t, body, "<Username>op</Username>")
			require.Contains(t, body, "<Password>pw</Password>")
			require.Contains(t, body, "<UserLevel>Operator</UserLevel>")
			return soapOK(`<CreateUsersResponse/>`)
		case strings.Contains(body, "DeleteUsers"):
			require.Contains(t, body, "<Username>op</Username>")
			return soapOK(`<DeleteUsersResponse/>`)
		case strings.Contains(body, "SetUser"):
			require.Contains(t, body, "<Username>admin</Username>")
			require.Contains(t, body, "<Password>newpw</Password>")
			return soapOK(`<SetUserResponse/>`)
		default:
			return ""
		}
	})
	mgr := NewDeviceManager(newTestClient(t, server))
	ctx := context.Background()
	require.NoError(t, mgr.CreateUsers(ctx, []User{{Username: "op", Password: "pw", UserLevel: "Operator"}}))
	require.NoError(t, mgr.DeleteUsers(ctx, []string{"op"}))
	require.NoError(t, mgr.SetUser(ctx, "admin", "newpw"))
}

func TestDeviceManagerGetNetworkInterfaces(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetNetworkInterfacesResponse>
			<NetworkInterfaces token="eth0">
				<Enabled>true</Enabled>
				<Info><Name>eth0</Name></Info>
				<IPv4>
					<Enabled>true</Enabled>
					<Config>
						<Manual><Address></Address><PrefixLength>24</PrefixLength></Manual>
						<Manual><Address>192.168.1.10</Address><PrefixLength>24</PrefixLength></Manual>
						<DHCP>false</DHCP>
					</Config>
				</IPv4>
				<IPv6>
					<Enabled>false</Enabled>
					<Config><DHCP>true</DHCP></Config>
				</IPv6>
			</NetworkInterfaces>
		</GetNetworkInterfacesResponse>`)
	})
	ifaces, err := NewDeviceManager(newTestClient(t, server)).GetNetworkInterfaces(context.Background())
	require.NoError(t, err)
	require.Len(t, ifaces, 1)
	require.Equal(t, "eth0", ifaces[0].Name)
	require.True(t, ifaces[0].Enabled)
	require.True(t, ifaces[0].IPv4.Enabled)
	require.False(t, ifaces[0].IPv4.DHCP)
	require.Equal(t, "192.168.1.10", ifaces[0].IPv4.Address)
	require.Equal(t, "ffffff00", ifaces[0].IPv4.Netmask)
	require.False(t, ifaces[0].IPv6.Enabled)
	require.True(t, ifaces[0].IPv6.DHCP)
}

func TestDeviceManagerSOAPErrors(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "denied")
	})
	mgr := NewDeviceManager(newTestClient(t, server))
	ctx := context.Background()
	_, err := mgr.SystemReboot(ctx)
	require.ErrorContains(t, err, "SystemReboot failed")
	_, err = mgr.GetNetworkInterfaces(ctx)
	require.ErrorContains(t, err, "GetNetworkInterfaces failed")
	_, err = mgr.GetUsers(ctx)
	require.ErrorContains(t, err, "GetUsers failed")
	require.ErrorContains(t, mgr.CreateUsers(ctx, []User{{Username: "u"}}), "CreateUsers failed")
	require.ErrorContains(t, mgr.DeleteUsers(ctx, []string{"u"}), "DeleteUsers failed")
	require.ErrorContains(t, mgr.SetUser(ctx, "u", "p"), "SetUser failed")

	bad := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<SystemRebootResponse><`)
	})
	_, err = NewDeviceManager(newTestClient(t, bad)).SystemReboot(ctx)
	require.Error(t, err)
}
