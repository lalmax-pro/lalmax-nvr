package rediscovery

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
)

// Result is the outcome of a rediscovery scan.
type Result struct {
	Found    bool
	Endpoint string
	Host     string
}

// ConfirmFunc verifies a candidate endpoint belongs to the expected serial.
type ConfirmFunc func(ctx context.Context, endpoint string) (serial string, err error)

// Engine probes candidate IPs over HTTP looking for a matching ONVIF serial.
type Engine struct {
	cfg     config.RediscoveryConfig
	probeFn func(ctx context.Context, host string, port int, timeout time.Duration) (*onvif.DiscoveredDevice, error)
}

func New(cfg config.RediscoveryConfig) *Engine {
	return &Engine{
		cfg:     cfg,
		probeFn: onvif.ProbeDevice,
	}
}

// DiscoverByStableID scans candidate hosts for an ONVIF device with the given serial.
func (e *Engine) DiscoverByStableID(ctx context.Context, stableID, lastEndpoint string, subnetHints []string, confirm ConfirmFunc) (Result, error) {
	if strings.TrimSpace(stableID) == "" {
		return Result{}, fmt.Errorf("stable_id is required")
	}
	timeout := e.cfg.ProbeTimeoutDuration()
	candidates := CandidateHosts(lastEndpoint, subnetHints)
	if len(candidates) == 0 {
		return Result{}, nil
	}

	maxParallel := e.cfg.MaxParallel
	if maxParallel <= 0 {
		maxParallel = 16
	}
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	found := Result{}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	for _, host := range candidates {
		host := host
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			device, err := e.probeFn(ctx, host, 80, timeout)
			if err != nil || device == nil {
				return
			}
			endpoint := device.Endpoint
			if endpoint == "" {
				endpoint = fmt.Sprintf("http://%s/onvif/device_service", host)
			}
			if confirm != nil {
				serial, err := confirm(ctx, endpoint)
				if err != nil || !strings.EqualFold(strings.TrimSpace(serial), stableID) {
					return
				}
			}
			mu.Lock()
			if !found.Found {
				found = Result{Found: true, Endpoint: endpoint, Host: host}
				cancel()
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return found, nil
}

// CandidateHosts builds IPv4 addresses to probe: last host, local /24, then hints.
func CandidateHosts(lastEndpoint string, subnetHints []string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(ip string) {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			return
		}
		parsed, err := netip.ParseAddr(ip)
		if err != nil || !parsed.Is4() {
			return
		}
		s := parsed.String()
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}

	if host := hostFromEndpoint(lastEndpoint); host != "" {
		add(host)
	}

	for _, hint := range subnetHints {
		prefix, err := parseHint(hint)
		if err != nil {
			continue
		}
		for ip := prefix.Addr(); prefix.Contains(ip); ip = ip.Next() {
			add(ip.String())
		}
	}

	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil {
				continue
			}
			ones, bits := n.Mask.Size()
			if bits != 32 || ones < 24 {
				continue
			}
			prefix, err := netip.ParsePrefix(n.String())
			if err != nil {
				continue
			}
			for ip := prefix.Addr(); prefix.Contains(ip); ip = ip.Next() {
				add(ip.String())
			}
		}
	}
	return out
}

func hostFromEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	if !strings.Contains(endpoint, "://") {
		if host, _, err := net.SplitHostPort(endpoint); err == nil {
			return host
		}
		return endpoint
	}
	uHost := endpoint
	if i := strings.Index(uHost, "://"); i >= 0 {
		uHost = uHost[i+3:]
	}
	if i := strings.IndexAny(uHost, "/:"); i >= 0 {
		if uHost[i] == ':' {
			host, _, err := net.SplitHostPort(strings.Split(uHost, "/")[0])
			if err == nil {
				return host
			}
		}
		uHost = uHost[:i]
	}
	return uHost
}

func parseHint(hint string) (netip.Prefix, error) {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return netip.Prefix{}, fmt.Errorf("empty")
	}
	if !strings.Contains(hint, "/") {
		hint += "/24"
	}
	prefix, err := netip.ParsePrefix(hint)
	if err != nil {
		return netip.Prefix{}, err
	}
	if !prefix.Addr().Is4() || prefix.Bits() < 24 {
		return netip.Prefix{}, fmt.Errorf("only IPv4 /24 or smaller is allowed")
	}
	return prefix, nil
}
