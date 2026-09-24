package iptv

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

var allowedSchemes = map[string]bool{
	"http":  true,
	"https": true,
}

func validatePublicURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid URL")
	}
	if !allowedSchemes[strings.ToLower(u.Scheme)] {
		return fmt.Errorf("url scheme %q is not allowed", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("invalid URL host")
	}
	if isBlockedHost(host) {
		return fmt.Errorf("url host is not allowed")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		// DNS failure is reported later by the HTTP client; only block literal IPs here.
		if ip := net.ParseIP(host); ip != nil && isBlockedIP(ip) {
			return fmt.Errorf("url host is not allowed")
		}
		return nil
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("url host is not allowed")
		}
	}
	return nil
}

func isBlockedHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	switch h {
	case "localhost", "localhost.localdomain":
		return true
	}
	return false
}

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// Home NVRs commonly ingest LAN playlists; still block loopback and link-local.
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 169 && ip4[1] == 254 {
		return true
	}
	return false
}

func allowedHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "user-agent", "referer", "origin", "authorization", "cookie":
		return true
	default:
		return false
	}
}

func sanitizeHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if !allowedHeader(k) {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
