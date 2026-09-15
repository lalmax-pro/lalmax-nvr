package udpts

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/net/ipv4"
)

const multicastTTL = 16

type MulticastConn interface {
	net.PacketConn
	SetReadBuffer(int) error
}

type SingleInterfaceMulticastConn struct {
	addr   *net.UDPAddr
	conn   *net.UDPConn
	connIP *ipv4.PacketConn
}

func NewSingleInterfaceMulticastConn(intf *net.Interface, address string) (MulticastConn, error) {
	addr, err := net.ResolveUDPAddr("udp4", address)
	if err != nil {
		return nil, err
	}

	tmp, err := net.ListenPacket("udp4", "224.0.0.0:"+strconv.FormatInt(int64(addr.Port), 10))
	if err != nil {
		return nil, err
	}
	conn := tmp.(*net.UDPConn)
	connIP := ipv4.NewPacketConn(conn)

	if err = connIP.JoinGroup(intf, &net.UDPAddr{IP: addr.IP}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to join multicast group: %w", err)
	}
	if err = connIP.SetMulticastInterface(intf); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to set multicast interface: %w", err)
	}
	if err = connIP.SetMulticastTTL(multicastTTL); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to set multicast TTL: %w", err)
	}

	return &SingleInterfaceMulticastConn{addr: addr, conn: conn, connIP: connIP}, nil
}

func (c *SingleInterfaceMulticastConn) Close() error { return c.conn.Close() }
func (c *SingleInterfaceMulticastConn) SetReadBuffer(bytes int) error {
	return c.conn.SetReadBuffer(bytes)
}
func (c *SingleInterfaceMulticastConn) LocalAddr() net.Addr { return c.conn.LocalAddr() }
func (c *SingleInterfaceMulticastConn) SetDeadline(t time.Time) error {
	return c.conn.SetDeadline(t)
}
func (c *SingleInterfaceMulticastConn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}
func (c *SingleInterfaceMulticastConn) SetWriteDeadline(t time.Time) error {
	return c.conn.SetWriteDeadline(t)
}
func (c *SingleInterfaceMulticastConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	return c.conn.WriteTo(b, addr)
}
func (c *SingleInterfaceMulticastConn) ReadFrom(b []byte) (int, net.Addr, error) {
	return c.conn.ReadFrom(b)
}

type MultiInterfaceMulticastConn struct {
	addr         *net.UDPAddr
	readConn     *net.UDPConn
	readConnIP   *ipv4.PacketConn
	writeConns   []*net.UDPConn
	writeConnIPs []*ipv4.PacketConn
}

func NewMultiInterfaceMulticastConn(address string, readOnly bool) (MulticastConn, error) {
	addr, err := net.ResolveUDPAddr("udp4", address)
	if err != nil {
		return nil, err
	}

	tmp, err := net.ListenPacket("udp4", "224.0.0.0:"+strconv.FormatInt(int64(addr.Port), 10))
	if err != nil {
		return nil, err
	}
	readConn := tmp.(*net.UDPConn)
	readConnIP := ipv4.NewPacketConn(readConn)

	intfs, err := net.Interfaces()
	if err != nil {
		_ = readConn.Close()
		return nil, err
	}

	var enabledInterfaces []*net.Interface
	for i := range intfs {
		intf := intfs[i]
		if intf.Flags&net.FlagMulticast == 0 {
			continue
		}
		if err = readConnIP.JoinGroup(&intf, &net.UDPAddr{IP: addr.IP}); err != nil {
			continue
		}
		enabledInterfaces = append(enabledInterfaces, &intf)
	}
	if len(enabledInterfaces) == 0 {
		_ = readConn.Close()
		return nil, fmt.Errorf("no multicast-capable interfaces found")
	}

	var writeConns []*net.UDPConn
	var writeConnIPs []*ipv4.PacketConn
	if !readOnly {
		writeConns = make([]*net.UDPConn, len(enabledInterfaces))
		writeConnIPs = make([]*ipv4.PacketConn, len(enabledInterfaces))
		for i, intf := range enabledInterfaces {
			tmp, err := net.ListenPacket("udp4", "224.0.0.0:"+strconv.FormatInt(int64(addr.Port), 10))
			if err != nil {
				for j := 0; j < i; j++ {
					_ = writeConns[j].Close()
				}
				_ = readConn.Close()
				return nil, err
			}
			writeConn := tmp.(*net.UDPConn)
			writeConnIP := ipv4.NewPacketConn(writeConn)
			if err = writeConnIP.SetMulticastInterface(intf); err != nil {
				_ = writeConn.Close()
				for j := 0; j < i; j++ {
					_ = writeConns[j].Close()
				}
				_ = readConn.Close()
				return nil, err
			}
			if err = writeConnIP.SetMulticastTTL(multicastTTL); err != nil {
				_ = writeConn.Close()
				for j := 0; j < i; j++ {
					_ = writeConns[j].Close()
				}
				_ = readConn.Close()
				return nil, err
			}
			writeConns[i] = writeConn
			writeConnIPs[i] = writeConnIP
		}
	}

	return &MultiInterfaceMulticastConn{
		addr:         addr,
		readConn:     readConn,
		readConnIP:   readConnIP,
		writeConns:   writeConns,
		writeConnIPs: writeConnIPs,
	}, nil
}

func (c *MultiInterfaceMulticastConn) Close() error {
	for _, conn := range c.writeConns {
		_ = conn.Close()
	}
	return c.readConn.Close()
}
func (c *MultiInterfaceMulticastConn) SetReadBuffer(bytes int) error {
	return c.readConn.SetReadBuffer(bytes)
}
func (c *MultiInterfaceMulticastConn) LocalAddr() net.Addr { return c.readConn.LocalAddr() }
func (c *MultiInterfaceMulticastConn) SetDeadline(t time.Time) error {
	return c.readConn.SetDeadline(t)
}
func (c *MultiInterfaceMulticastConn) SetReadDeadline(t time.Time) error {
	return c.readConn.SetReadDeadline(t)
}
func (c *MultiInterfaceMulticastConn) SetWriteDeadline(t time.Time) error {
	var err error
	for _, conn := range c.writeConns {
		if err2 := conn.SetWriteDeadline(t); err == nil {
			err = err2
		}
	}
	return err
}
func (c *MultiInterfaceMulticastConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	var n int
	var err error
	for _, conn := range c.writeConns {
		var err2 error
		n, err2 = conn.WriteTo(b, addr)
		if err == nil {
			err = err2
		}
	}
	return n, err
}
func (c *MultiInterfaceMulticastConn) ReadFrom(b []byte) (int, net.Addr, error) {
	return c.readConn.ReadFrom(b)
}
