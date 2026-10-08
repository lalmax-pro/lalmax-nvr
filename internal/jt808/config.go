package jt808

import (
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	TransportTCP = "tcp"
	TransportUDP = "udp"
)

// Config is the NVR-side JT/T 808 signaling configuration.
type Config struct {
	Enabled      bool   `yaml:"enabled"`
	Port         int    `yaml:"port"`           // JT808 listen port, default 808
	MediaIP      string `yaml:"media_ip"`       // IP placed into 0x9101
	AuthCode     string `yaml:"auth_code"`      // Secret issued on registration and checked on 0x0102
	MediaTCPPort int    `yaml:"media_tcp_port"` // JT1078 TCP port, default 1078
	MediaUDPPort int    `yaml:"media_udp_port"` // JT1078 UDP port, default 1078
	Transport    string `yaml:"transport"`      // tcp | udp, default tcp
	Timeout      string `yaml:"timeout"`        // 9101/9102 reply wait, default 3s
	FTPPort      int    `yaml:"ftp_port"`       // optional 0x9206 port; 0 means the caller must set it
	FTPUser      string `yaml:"ftp_user"`
	FTPPassword  string `yaml:"ftp_password"`
}

func (c *Config) ApplyDefaults() {
	if c.Port == 0 {
		c.Port = 808
	}
	if c.MediaTCPPort == 0 {
		c.MediaTCPPort = 1078
	}
	if c.MediaUDPPort == 0 {
		c.MediaUDPPort = 1078
	}
	if strings.TrimSpace(c.Transport) == "" {
		c.Transport = TransportTCP
	}
	if strings.TrimSpace(c.Timeout) == "" {
		c.Timeout = "3s"
	}
}

func (c *Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	c.ApplyDefaults()
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("jt808.port must be 1-65535, got %d", c.Port)
	}
	if c.MediaTCPPort <= 0 || c.MediaTCPPort > 65535 || c.MediaUDPPort <= 0 || c.MediaUDPPort > 65535 {
		return fmt.Errorf("jt808 media ports must be 1-65535")
	}
	if strings.TrimSpace(c.MediaIP) == "" {
		return fmt.Errorf("jt808.media_ip is required")
	}
	if c.AuthCode == "" || len(c.AuthCode) > 128 {
		return fmt.Errorf("jt808.auth_code must be 1-128 bytes")
	}
	if ip := net.ParseIP(c.MediaIP); ip == nil || ip.IsUnspecified() {
		return fmt.Errorf("jt808.media_ip must be a reachable IP, got %s", c.MediaIP)
	}
	c.Transport = strings.ToLower(strings.TrimSpace(c.Transport))
	switch c.Transport {
	case TransportTCP, TransportUDP:
	default:
		return fmt.Errorf("jt808.transport must be tcp or udp, got %s", c.Transport)
	}
	if d, err := time.ParseDuration(c.Timeout); err != nil || d <= 0 {
		return fmt.Errorf("jt808.timeout must be a positive duration, got %q", c.Timeout)
	}
	return nil
}

func (c *Config) TimeoutDuration() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(c.Timeout))
	if err != nil || d <= 0 {
		return 3 * time.Second
	}
	return d
}

func (c *Config) ListenAddr() string {
	return fmt.Sprintf("0.0.0.0:%d", c.Port)
}
