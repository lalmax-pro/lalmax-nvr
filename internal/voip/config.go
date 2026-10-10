package voip

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

type Config struct {
	Enable             bool   `json:"enabled" yaml:"enabled"`
	ManualAnswer       bool   `json:"manual_answer" yaml:"manual_answer"`
	RingTimeoutMs      int    `json:"ring_timeout_ms" yaml:"ring_timeout_ms"`
	PbxServer          string `json:"pbx_server" yaml:"pbx_server"`
	PbxTransport       string `json:"pbx_transport" yaml:"pbx_transport"`
	PbxDomain          string `json:"pbx_domain" yaml:"pbx_domain"`
	PbxTLSServerName   string `json:"pbx_tls_server_name" yaml:"pbx_tls_server_name"`
	PbxTLSCAFile       string `json:"pbx_tls_ca_file" yaml:"pbx_tls_ca_file"`
	PbxUsername        string `json:"pbx_username" yaml:"pbx_username"`
	PbxPassword        string `json:"pbx_password" yaml:"pbx_password"`
	PbxRegisterExpires int    `json:"pbx_register_expires" yaml:"pbx_register_expires"`
	SipListenAddr      string `json:"sip_listen_addr" yaml:"sip_listen_addr"`           // UDP监听地址
	SipTcpListenAddr   string `json:"sip_tcp_listen_addr" yaml:"sip_tcp_listen_addr"`   // TCP监听地址（可选，空则不启用TCP）
	SipTlsListenAddr   string `json:"sip_tls_listen_addr" yaml:"sip_tls_listen_addr"`   // TLS监听地址（可选，空则不启用TLS）
	SipDtlsListenAddr  string `json:"sip_dtls_listen_addr" yaml:"sip_dtls_listen_addr"` // DTLS-UDP监听地址（可选，空则不启用DTLS）
	SipTlsCertFile     string `json:"sip_tls_cert_file" yaml:"sip_tls_cert_file"`       // TLS证书文件
	SipTlsKeyFile      string `json:"sip_tls_key_file" yaml:"sip_tls_key_file"`         // TLS私钥文件
	SipWsListenAddr    string `json:"sip_ws_listen_addr" yaml:"sip_ws_listen_addr"`     // WebSocket监听地址（可选，空则不启用WS）
	SipWssListenAddr   string `json:"sip_wss_listen_addr" yaml:"sip_wss_listen_addr"`   // WebSocket TLS监听地址（可选，空则不启用WSS）
	SipIP              string `json:"sip_ip" yaml:"sip_ip"`                             // SIP服务器公网IP（用于Contact）
	MediaIP            string `json:"media_ip" yaml:"media_ip"`
	MediaPortMin       int    `json:"media_port_min" yaml:"media_port_min"`
	MediaPortMax       int    `json:"media_port_max" yaml:"media_port_max"`
	Realm              string `json:"realm" yaml:"realm"`
	AuthEnable         bool   `json:"auth_enable" yaml:"auth_enable"`
	Users              []User `json:"users" yaml:"users"`
	RtpTimeoutMs       int    `json:"rtp_timeout_ms" yaml:"rtp_timeout_ms"`
	AckTimeoutMs       int    `json:"ack_timeout_ms" yaml:"ack_timeout_ms"`
	SrtpEnable         bool   `json:"srtp_enable" yaml:"srtp_enable"`       // 是否启用SRTP支持
	SrtpMandatory      bool   `json:"srtp_mandatory" yaml:"srtp_mandatory"` // 是否强制SRTP（拒绝非加密连接）
	BundleEnable       bool   `json:"bundle_enable" yaml:"bundle_enable"`   // 是否启用RTP/RTCP BUNDLE复用
}

type User struct {
	Username    string `json:"username" yaml:"username"`
	Password    string `json:"password" yaml:"password"`
	RecordCalls bool   `json:"record_calls" yaml:"record_calls"`
}

func (c *Config) Normalize() {
	if c.PbxTransport == "" {
		c.PbxTransport = "udp"
	} else {
		c.PbxTransport = strings.ToLower(strings.TrimSpace(c.PbxTransport))
	}
	if c.PbxDomain == "" && c.PbxServer != "" {
		if host, _, err := net.SplitHostPort(c.PbxServer); err == nil {
			c.PbxDomain = host
		}
	}
	if c.RingTimeoutMs == 0 {
		c.RingTimeoutMs = 30000
	}
	if c.PbxRegisterExpires == 0 {
		c.PbxRegisterExpires = 3600
	}
	if c.SipListenAddr == "" {
		c.SipListenAddr = "0.0.0.0:5070"
	}
	if c.MediaPortMin == 0 {
		c.MediaPortMin = 41000
	}
	if c.MediaPortMax == 0 {
		c.MediaPortMax = 42000
	}
	if c.Realm == "" {
		c.Realm = "lalmax-nvr"
	}
	if c.RtpTimeoutMs == 0 {
		c.RtpTimeoutMs = 15000
	}
	if c.AckTimeoutMs == 0 {
		c.AckTimeoutMs = 32000
	}
}

// Validate rejects unusable settings before opening any listeners.
func (c Config) Validate() error {
	if c.MediaPortMin < 1024 || c.MediaPortMax > 65535 || c.MediaPortMax < c.MediaPortMin {
		return fmt.Errorf("voip media port range must be within 1024..65535")
	}
	if c.RtpTimeoutMs <= 0 || c.AckTimeoutMs <= 0 {
		return fmt.Errorf("voip timeouts must be positive")
	}
	if c.RingTimeoutMs < 1000 || c.RingTimeoutMs > 300000 {
		return fmt.Errorf("voip ring timeout must be within 1000..300000 ms")
	}
	if c.PbxServer != "" {
		serverHost, serverPort, err := net.SplitHostPort(c.PbxServer)
		if err != nil {
			return fmt.Errorf("voip PBX server must be host:port: %w", err)
		}
		if serverHost == "" || strings.ContainsAny(c.PbxServer, "\r\n") {
			return fmt.Errorf("voip PBX server must contain a valid host and port")
		}
		port, err := strconv.Atoi(serverPort)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("voip PBX server port must be within 1..65535")
		}
		switch strings.ToLower(strings.TrimSpace(c.PbxTransport)) {
		case "udp", "tcp", "tls":
		default:
			return fmt.Errorf("voip PBX transport must be udp, tcp, or tls")
		}
		if !validPBXExtension(c.PbxUsername) || strings.TrimSpace(c.PbxPassword) == "" {
			return fmt.Errorf("voip PBX username and password are required when PBX server is set")
		}
		if strings.ContainsAny(c.PbxDomain, "\r\n <>@;:") {
			return fmt.Errorf("voip PBX SIP domain contains invalid characters")
		}
		if c.PbxRegisterExpires < 60 || c.PbxRegisterExpires > 86400 {
			return fmt.Errorf("voip PBX registration expiry must be within 60..86400 seconds")
		}
		if strings.ContainsAny(c.PbxTLSServerName, "\r\n /;:@") {
			return fmt.Errorf("voip PBX TLS server name contains invalid characters")
		}
		if c.PbxTransport != "tls" && (c.PbxTLSServerName != "" || c.PbxTLSCAFile != "") {
			return fmt.Errorf("voip PBX TLS certificate settings require tls transport")
		}
	} else if c.PbxDomain != "" || c.PbxUsername != "" || c.PbxPassword != "" || c.PbxTLSServerName != "" || c.PbxTLSCAFile != "" {
		return fmt.Errorf("voip PBX server is required when PBX registration fields are set")
	}
	for _, addr := range []string{c.SipListenAddr, c.SipTcpListenAddr, c.SipTlsListenAddr, c.SipDtlsListenAddr, c.SipWsListenAddr, c.SipWssListenAddr} {
		if addr == "" {
			continue
		}
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("voip invalid listen address %q: %w", addr, err)
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 0 || n > 65535 {
			return fmt.Errorf("voip invalid listen port %q", port)
		}
	}
	for _, ip := range []string{c.MediaIP, c.SipIP} {
		if ip != "" && (net.ParseIP(ip) == nil || net.ParseIP(ip).To4() == nil || net.ParseIP(ip).IsUnspecified()) {
			return fmt.Errorf("voip advertised IP must be a usable IPv4 address: %q", ip)
		}
	}
	if c.SrtpMandatory && !c.SrtpEnable {
		return fmt.Errorf("voip srtp_mandatory requires srtp_enable")
	}
	if c.SipTlsListenAddr != "" || c.SipDtlsListenAddr != "" || c.SipWssListenAddr != "" {
		if c.SipTlsCertFile == "" || c.SipTlsKeyFile == "" {
			return fmt.Errorf("voip TLS transports require certificate and key")
		}
	}
	if c.AuthEnable && len(c.Users) == 0 {
		return fmt.Errorf("voip auth_enable requires users")
	}
	seen := make(map[string]bool)
	for _, u := range c.Users {
		if u.RecordCalls && !c.AuthEnable {
			return fmt.Errorf("voip automatic recording requires user authentication")
		}
		if strings.TrimSpace(u.Username) == "" || u.Password == "" || seen[u.Username] {
			return fmt.Errorf("voip users require unique usernames and non-empty passwords")
		}
		seen[u.Username] = true
	}
	return nil
}

// Clone isolates user credentials from configuration edits and encrypted saves.
func (c Config) Clone() Config { c.Users = append([]User(nil), c.Users...); return c }
