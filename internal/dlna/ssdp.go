package dlna

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/ipv4"
)

const ssdpServer = "lalmax-nvr/1.0 UPnP/1.0 LalmaxNVR/1.0"

func ssdpNotify(location, nt, nts, usn string) string {
	return fmt.Sprintf("NOTIFY * HTTP/1.1\r\nHOST: %s\r\nCACHE-CONTROL: max-age=1800\r\nLOCATION: %s\r\nNT: %s\r\nNTS: %s\r\nSERVER: %s\r\nUSN: %s\r\n\r\n", ssdpAddr, location, nt, nts, ssdpServer, usn)
}

func ssdpSearchResponse(date, location, st, usn string) string {
	return fmt.Sprintf("HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nDATE: %s\r\nEXT:\r\nLOCATION: %s\r\nSERVER: %s\r\nST: %s\r\nUSN: %s\r\n\r\n", date, location, ssdpServer, st, usn)
}

func reuseAddrPort(_, _ string, c syscall.RawConn) error {
	var sockErr error
	err := c.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		if sockErr != nil {
			return
		}
		_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEPORT, 1)
	})
	if err != nil {
		return err
	}
	return sockErr
}

func listenUDPReuse(address string) (*net.UDPConn, error) {
	lc := net.ListenConfig{Control: reuseAddrPort}
	pc, err := lc.ListenPacket(context.Background(), "udp4", address)
	if err != nil {
		return nil, err
	}
	conn, ok := pc.(*net.UDPConn)
	if !ok {
		_ = pc.Close()
		return nil, fmt.Errorf("unexpected packet conn %T", pc)
	}
	return conn, nil
}

// listenSSDPSender binds a unicast socket on the LAN interface. M-SEARCH replies
// and NOTIFY packets sent from the multicast membership socket are dropped.
func listenSSDPSender(iface *net.Interface) (*net.UDPConn, error) {
	ip := net.IPv4zero
	if v4 := privateIPv4(iface); v4 != nil {
		ip = v4
	}
	conn, err := listenUDPReuse(net.JoinHostPort(ip.String(), "1900"))
	if err != nil {
		slog.Warn("DLNA SSDP port 1900 unavailable, using an ephemeral port", "error", err)
		conn, err = listenUDPReuse(net.JoinHostPort(ip.String(), "0"))
		if err != nil {
			return nil, err
		}
	}
	p := ipv4.NewPacketConn(conn)
	if iface != nil {
		if err := p.SetMulticastInterface(iface); err != nil {
			slog.Warn("DLNA multicast interface", "interface", iface.Name, "error", err)
		}
	}
	if err := p.SetMulticastTTL(4); err != nil {
		slog.Warn("DLNA multicast TTL", "error", err)
	}
	return conn, nil
}

func detectLANIPv4(ifaceName, legacyURL string) (net.IP, *net.Interface, error) {
	if name := strings.TrimSpace(ifaceName); name != "" {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			return nil, nil, err
		}
		ip := privateIPv4(iface)
		if ip == nil {
			return nil, nil, fmt.Errorf("interface %s has no private IPv4 address", name)
		}
		return ip, iface, nil
	}
	if ip := outboundIPv4(); ip != nil {
		if iface := interfaceByIP(ip); iface != nil && !virtualIface(iface.Name) {
			return ip, iface, nil
		}
	}
	if ip, iface := bestPrivateIPv4(); ip != nil {
		return ip, iface, nil
	}
	if host := advertiseHost(legacyURL); host != "" {
		ip := net.ParseIP(host)
		if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
			return ip.To4(), interfaceByIP(ip), nil
		}
	}
	return nil, nil, fmt.Errorf("no private IPv4 address")
}

func advertiseHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func outboundIPv4() net.IP {
	conn, err := net.DialTimeout("udp4", "192.0.2.1:80", 500*time.Millisecond)
	if err != nil {
		return nil
	}
	defer conn.Close()
	ua, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || ua.IP == nil {
		return nil
	}
	ip := ua.IP.To4()
	if ip == nil || ip.IsLoopback() || !ip.IsPrivate() {
		return nil
	}
	return ip
}

func bestPrivateIPv4() (net.IP, *net.Interface) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil
	}
	var bestIP net.IP
	var bestIface *net.Interface
	bestRank := 0
	for _, iface := range ifaces {
		if virtualIface(iface.Name) || iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		ip := privateIPv4(&iface)
		rank := privateRank(ip)
		if rank > bestRank {
			found := iface
			bestIP = ip
			bestIface = &found
			bestRank = rank
		}
	}
	return bestIP, bestIface
}

func privateIPv4(iface *net.Interface) net.IP {
	if iface == nil {
		return nil
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil
	}
	var best net.IP
	bestRank := 0
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP.To4()
		rank := privateRank(ip)
		if rank > bestRank {
			best = ip
			bestRank = rank
		}
	}
	return best
}

func privateRank(ip net.IP) int {
	if ip == nil || ip.IsLoopback() || !ip.IsPrivate() {
		return 0
	}
	if ip[0] == 192 && ip[1] == 168 {
		return 3
	}
	if ip[0] == 10 {
		return 2
	}
	return 1
}

func interfaceByIP(ip net.IP) *net.Interface {
	if ip == nil {
		return nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if ok && ipnet.IP.Equal(ip) {
				found := iface
				return &found
			}
		}
	}
	return nil
}

func virtualIface(name string) bool {
	n := strings.ToLower(name)
	for _, prefix := range []string{
		"docker", "br-", "veth", "virbr", "vmnet", "vboxnet", "utun", "awdl",
		"llw", "zt", "tailscale", "wg", "tun", "tap", "lo", "gif", "stf", "anpi", "bridge",
	} {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}
