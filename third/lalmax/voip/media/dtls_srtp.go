package media

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
	"github.com/pion/srtp/v3"
)

type DTLSSRTPConfig struct {
	Certificate       tls.Certificate
	RemoteFingerprint string
	Client            bool
}

func GenerateDTLSCertificate() (tls.Certificate, string, error) {
	cert, err := selfsign.GenerateSelfSigned()
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("generate dtls certificate failed: %w", err)
	}
	if len(cert.Certificate) == 0 {
		return tls.Certificate{}, "", fmt.Errorf("generate dtls certificate failed: empty certificate")
	}
	return cert, FingerprintSHA256(cert.Certificate[0]), nil
}

func FingerprintSHA256(rawCert []byte) string {
	sum := sha256.Sum256(rawCert)
	return "SHA-256 " + colonHex(sum[:])
}

func colonHex(b []byte) string {
	encoded := strings.ToUpper(hex.EncodeToString(b))
	var out strings.Builder
	out.Grow(len(encoded) + len(encoded)/2)
	for i := 0; i < len(encoded); i += 2 {
		if i > 0 {
			out.WriteByte(':')
		}
		out.WriteString(encoded[i : i+2])
	}
	return out.String()
}

func verifyFingerprint(rawCert []byte, fingerprint string) error {
	fields := strings.Fields(strings.TrimSpace(fingerprint))
	if len(fields) != 2 {
		return fmt.Errorf("invalid dtls fingerprint: %q", fingerprint)
	}
	want, err := hex.DecodeString(strings.ReplaceAll(fields[1], ":", ""))
	if err != nil {
		return fmt.Errorf("decode dtls fingerprint failed: %w", err)
	}

	var got []byte
	switch strings.ToUpper(fields[0]) {
	case "SHA-256":
		sum := sha256.Sum256(rawCert)
		got = sum[:]
	case "SHA-1":
		sum := sha1.Sum(rawCert)
		got = sum[:]
	default:
		return fmt.Errorf("unsupported dtls fingerprint hash: %s", fields[0])
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("dtls fingerprint mismatch")
	}
	return nil
}

func isDTLSPacket(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	return data[0] >= 20 && data[0] <= 23
}

func createSrtpContextFromDTLS(conn *dtls.Conn, profile dtls.SRTPProtectionProfile) (*SrtpContext, error) {
	return createSrtpContextFromDTLSRole(conn, profile, false)
}

func createSrtpContextFromDTLSRole(conn *dtls.Conn, profile dtls.SRTPProtectionProfile, client bool) (*SrtpContext, error) {
	srtpProfile, err := dtlsProfileToSrtp(profile)
	if err != nil {
		return nil, err
	}

	state, ok := conn.ConnectionState()
	if !ok {
		return nil, fmt.Errorf("dtls connection state is not ready")
	}

	cfg := &srtp.Config{Profile: srtpProfile}
	if err := cfg.ExtractSessionKeysFromDTLS(&state, client); err != nil {
		return nil, fmt.Errorf("extract dtls-srtp keys failed: %w", err)
	}

	inboundRTP, inboundRTCP, err := createSrtpContextsFromKeySalt(cfg.Keys.RemoteMasterKey, cfg.Keys.RemoteMasterSalt, srtpProfile)
	if err != nil {
		return nil, fmt.Errorf("create inbound dtls-srtp context failed: %w", err)
	}
	outboundRTP, outboundRTCP, err := createSrtpContextsFromKeySalt(cfg.Keys.LocalMasterKey, cfg.Keys.LocalMasterSalt, srtpProfile)
	if err != nil {
		return nil, fmt.Errorf("create outbound dtls-srtp context failed: %w", err)
	}

	localKeyMaterial := append(append([]byte(nil), cfg.Keys.LocalMasterKey...), cfg.Keys.LocalMasterSalt...)
	return &SrtpContext{
		references:          1,
		inboundRTPContext:   inboundRTP,
		inboundRTCPContext:  inboundRTCP,
		outboundRTPContext:  outboundRTP,
		outboundRTCPContext: outboundRTCP,
		keyMaterial:         localKeyMaterial,
		suite:               srtpProfile.String(),
	}, nil
}

func dtlsProfileToSrtp(profile dtls.SRTPProtectionProfile) (srtp.ProtectionProfile, error) {
	switch profile {
	case dtls.SRTP_AES128_CM_HMAC_SHA1_80:
		return srtp.ProtectionProfileAes128CmHmacSha1_80, nil
	case dtls.SRTP_AES128_CM_HMAC_SHA1_32:
		return srtp.ProtectionProfileAes128CmHmacSha1_32, nil
	default:
		return 0, fmt.Errorf("unsupported dtls-srtp profile: %v", profile)
	}
}

type dtlsPacket struct {
	data []byte
	addr net.Addr
}

type dtlsPacketConn struct {
	udp          *net.UDPConn
	readCh       chan dtlsPacket
	remoteAddr   net.Addr
	localAddr    net.Addr
	mu           sync.Mutex
	readDeadline time.Time
	closeCh      chan struct{}
	closed       bool
}

func newDTLSPacketConn(udp *net.UDPConn, localAddr net.Addr, remoteAddr net.Addr) *dtlsPacketConn {
	return &dtlsPacketConn{
		udp:        udp,
		readCh:     make(chan dtlsPacket, 32),
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
		closeCh:    make(chan struct{}),
	}
}

func (c *dtlsPacketConn) enqueue(data []byte, addr net.Addr) {
	if c == nil {
		return
	}
	if c.remoteAddr != nil && addr.String() != c.remoteAddr.String() {
		return
	}
	pkt := dtlsPacket{data: append([]byte(nil), data...), addr: addr}
	select {
	case c.readCh <- pkt:
	default:
	}
}

func (c *dtlsPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	c.mu.Lock()
	deadline := c.readDeadline
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, nil, net.ErrClosed
	}

	var timer <-chan time.Time
	if !deadline.IsZero() {
		d := time.Until(deadline)
		if d <= 0 {
			return 0, nil, &timeoutError{}
		}
		t := time.NewTimer(d)
		defer t.Stop()
		timer = t.C
	}

	select {
	case pkt := <-c.readCh:
		return copy(p, pkt.data), pkt.addr, nil
	case <-timer:
		return 0, nil, &timeoutError{}
	case <-c.closeCh:
		return 0, nil, net.ErrClosed
	}
}

func (c *dtlsPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok {
		return 0, fmt.Errorf("dtls packet conn expects UDP addr, got %T", addr)
	}
	return c.udp.WriteToUDP(p, udpAddr)
}

func (c *dtlsPacketConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.closeCh)
	c.mu.Unlock()
	return nil
}

func (c *dtlsPacketConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *dtlsPacketConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	c.mu.Unlock()
	return nil
}

func (c *dtlsPacketConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	c.mu.Unlock()
	return nil
}

func (c *dtlsPacketConn) SetWriteDeadline(time.Time) error {
	return nil
}

type timeoutError struct{}

func (*timeoutError) Error() string {
	return "i/o timeout"
}

func (*timeoutError) Timeout() bool {
	return true
}

func (*timeoutError) Temporary() bool {
	return true
}
