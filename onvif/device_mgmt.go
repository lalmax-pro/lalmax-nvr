package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
	"net"
)

// DeviceManager handles device-level management operations.
type DeviceManager struct {
	client *Client
}

// NewDeviceManager creates a new device manager.
func NewDeviceManager(client *Client) *DeviceManager {
	return &DeviceManager{client: client}
}

// SystemReboot reboots the ONVIF device.
func (d *DeviceManager) SystemReboot(ctx context.Context) (string, error) {
	body := `<SystemReboot xmlns="http://www.onvif.org/ver10/device/wsdl"/>`

	resp, err := d.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: d.client.endpoint,
		Body:       body,
	})
	if err != nil {
		return "", fmt.Errorf("onvif: SystemReboot failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"SystemRebootResponse"`
		Message string   `xml:"Message"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return "", err
	}

	return result.Message, nil
}

// NetworkInterface represents a network interface configuration.
type NetworkInterface struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	IPv4    NetworkIPv4 `json:"ipv4"`
	IPv6    NetworkIPv6 `json:"ipv6,omitempty"`
}

// NetworkIPv4 represents IPv4 configuration.
type NetworkIPv4 struct {
	Enabled bool   `json:"enabled"`
	DHCP    bool   `json:"dhcp"`
	Address string `json:"address,omitempty"`
	Netmask string `json:"netmask,omitempty"`
	Gateway string `json:"gateway,omitempty"`
}

// NetworkIPv6 represents IPv6 configuration.
type NetworkIPv6 struct {
	Enabled bool   `json:"enabled"`
	DHCP    bool   `json:"dhcp"`
	Address string `json:"address,omitempty"`
}

// GetNetworkInterfaces retrieves network interface configurations.
func (d *DeviceManager) GetNetworkInterfaces(ctx context.Context) ([]NetworkInterface, error) {
	body := `<GetNetworkInterfaces xmlns="http://www.onvif.org/ver10/device/wsdl"/>`

	resp, err := d.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: d.client.endpoint,
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetNetworkInterfaces failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetNetworkInterfacesResponse"`
		NetworkInterfaces []struct {
			Token   string `xml:"token,attr"`
			Enabled bool   `xml:"Enabled"`
			Info    struct {
				Name string `xml:"Name"`
			} `xml:"Info"`
			IPv4 struct {
				Enabled bool `xml:"Enabled"`
				Config  struct {
					Manual []struct {
						Address      string `xml:"Address"`
						PrefixLength int    `xml:"PrefixLength"`
					} `xml:"Manual"`
					DHCP bool `xml:"DHCP"`
				} `xml:"Config"`
			} `xml:"IPv4"`
			IPv6 struct {
				Enabled bool `xml:"Enabled"`
				Config  struct {
					DHCP bool `xml:"DHCP"`
				} `xml:"Config"`
			} `xml:"IPv6"`
		} `xml:"NetworkInterfaces"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	ifaces := make([]NetworkInterface, 0, len(result.NetworkInterfaces))
	for _, iface := range result.NetworkInterfaces {
		n := NetworkInterface{
			Name:    iface.Info.Name,
			Enabled: iface.Enabled,
		}
		n.IPv4.Enabled = iface.IPv4.Enabled
		n.IPv4.DHCP = iface.IPv4.Config.DHCP
		for _, addr := range iface.IPv4.Config.Manual {
			if addr.Address != "" {
				n.IPv4.Address = addr.Address
				n.IPv4.Netmask = formatPrefixMask(addr.PrefixLength)
				break
			}
		}
		n.IPv6.Enabled = iface.IPv6.Enabled
		n.IPv6.DHCP = iface.IPv6.Config.DHCP
		ifaces = append(ifaces, n)
	}

	return ifaces, nil
}

// User represents an ONVIF device user.
type User struct {
	Username  string `json:"username"`
	Password  string `json:"password,omitempty"`
	UserLevel string `json:"user_level"`
}

// GetUsers retrieves user accounts from the device.
func (d *DeviceManager) GetUsers(ctx context.Context) ([]User, error) {
	body := `<GetUsers xmlns="http://www.onvif.org/ver10/device/wsdl"/>`

	resp, err := d.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: d.client.endpoint,
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetUsers failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetUsersResponse"`
		User    []struct {
			Username  string `xml:"Username"`
			UserLevel string `xml:"UserLevel"`
		} `xml:"User"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	users := make([]User, 0, len(result.User))
	for _, u := range result.User {
		users = append(users, User{
			Username:  u.Username,
			UserLevel: u.UserLevel,
		})
	}

	return users, nil
}

// CreateUsers creates new user accounts on the device.
func (d *DeviceManager) CreateUsers(ctx context.Context, users []User) error {
	usersXML := ""
	for _, u := range users {
		usersXML += fmt.Sprintf(`<User>
  <Username>%s</Username>
  <Password>%s</Password>
  <UserLevel>%s</UserLevel>
</User>`, u.Username, u.Password, u.UserLevel)
	}

	body := fmt.Sprintf(`<CreateUsers xmlns="http://www.onvif.org/ver10/device/wsdl">%s</CreateUsers>`, usersXML)

	_, err := d.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: d.client.endpoint,
		Body:       body,
	})
	if err != nil {
		return fmt.Errorf("onvif: CreateUsers failed: %w", err)
	}

	return nil
}

// DeleteUsers deletes user accounts from the device.
func (d *DeviceManager) DeleteUsers(ctx context.Context, usernames []string) error {
	usersXML := ""
	for _, u := range usernames {
		usersXML += fmt.Sprintf(`<Username>%s</Username>`, u)
	}

	body := fmt.Sprintf(`<DeleteUsers xmlns="http://www.onvif.org/ver10/device/wsdl">%s</DeleteUsers>`, usersXML)

	_, err := d.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: d.client.endpoint,
		Body:       body,
	})
	if err != nil {
		return fmt.Errorf("onvif: DeleteUsers failed: %w", err)
	}

	return nil
}

// SetUser modifies an existing user account.
func (d *DeviceManager) SetUser(ctx context.Context, username, password string) error {
	body := fmt.Sprintf(`<SetUser xmlns="http://www.onvif.org/ver10/device/wsdl">
<User>
  <Username>%s</Username>
  <Password>%s</Password>
</User>
</SetUser>`, username, password)

	_, err := d.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: d.client.endpoint,
		Body:       body,
	})
	if err != nil {
		return fmt.Errorf("onvif: SetUser failed: %w", err)
	}

	return nil
}

func formatPrefixMask(prefixLength int) string {
	if prefixLength <= 0 || prefixLength > 32 {
		return ""
	}
	mask := net.IPv4Mask(0, 0, 0, 0)
	for i := 0; i < 4; i++ {
		bits := prefixLength - i*8
		if bits >= 8 {
			mask[i] = 0xFF
		} else if bits > 0 {
			mask[i] = ^byte(0xFF >> bits)
		}
	}
	return mask.String()
}
