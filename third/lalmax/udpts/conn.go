package udpts

import (
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/q191201771/naza/pkg/nazalog"
)

const (
	udpKernelReadBufferSize = 0x80000
	udpReadBufSize          = 2048
	tsPacketSize            = 188
)

var errUDPTimeout = fmt.Errorf("UDP read timeout")

type udpReader struct {
	conn          net.PacketConn
	readTimeoutMs int
	midbuf        []byte
	midbufpos     int
}

func newUdpReader(conn net.PacketConn, readTimeoutMs int) *udpReader {
	return &udpReader{
		conn:          conn,
		readTimeoutMs: readTimeoutMs,
		midbuf:        make([]byte, 0, udpReadBufSize),
	}
}

func (r *udpReader) Read(p []byte) (n int, err error) {
	if r.midbufpos < len(r.midbuf) {
		n = copy(p, r.midbuf[r.midbufpos:])
		r.midbufpos += n
		return n, nil
	}

	for {
		if r.readTimeoutMs > 0 {
			if err = r.conn.SetReadDeadline(time.Now().Add(time.Duration(r.readTimeoutMs) * time.Millisecond)); err != nil {
				nazalog.Warnf("set udp read deadline failed: %v", err)
			}
		}

		if cap(r.midbuf) < udpReadBufSize {
			r.midbuf = make([]byte, udpReadBufSize)
		} else {
			r.midbuf = r.midbuf[:cap(r.midbuf)]
		}

		mn, _, err := r.conn.ReadFrom(r.midbuf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				return 0, errUDPTimeout
			}
			if errors.Is(err, net.ErrClosed) {
				return 0, io.EOF
			}
			return 0, err
		}
		if mn < tsPacketSize {
			continue
		}
		r.midbuf = r.midbuf[:mn]
		if r.midbuf[0] != 0x47 || mn%tsPacketSize != 0 {
			continue
		}
		n = copy(p, r.midbuf)
		r.midbufpos = n
		return n, nil
	}
}

func createPacketConn(addr *net.UDPAddr, interfaceName string) (net.PacketConn, error) {
	if ip4 := addr.IP.To4(); ip4 != nil && addr.IP.IsMulticast() {
		if interfaceName != "" {
			intf, err := net.InterfaceByName(interfaceName)
			if err != nil {
				return nil, fmt.Errorf("interface %s not found: %w", interfaceName, err)
			}
			conn, err := NewSingleInterfaceMulticastConn(intf, addr.String())
			if err != nil {
				return nil, err
			}
			_ = conn.SetReadBuffer(udpKernelReadBufferSize)
			return conn, nil
		}
		conn, err := NewMultiInterfaceMulticastConn(addr.String(), true)
		if err != nil {
			return nil, err
		}
		_ = conn.SetReadBuffer(udpKernelReadBufferSize)
		return conn, nil
	}
	return net.ListenPacket("udp", addr.String())
}
