// SPDX-License-Identifier: MIT

package tutk

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestLocalDeviceHandshakeAndCommands(t *testing.T) {
	for _, version := range []byte{16, 25} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			done := make(chan error, 1)
			go func() { done <- serveTestCamera(server, version) }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, err := DialContext(ctx, server.LocalAddr().String(), "test-uid", "Miss", "client")
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetIdleTimeout(time.Second)
			if err := conn.WriteCommandContext(ctx, 0x100, []byte("request")); err != nil {
				t.Fatal(err)
			}
			cmd, data, err := conn.ReadCommand()
			if err != nil {
				t.Fatal(err)
			}
			if cmd != 0x101 || string(data) != "response" {
				t.Fatalf("unexpected command %x %q", cmd, data)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func serveTestCamera(server *net.UDPConn, version byte) error {
	if err := server.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	buf := make([]byte, 2048)
	var peer *net.UDPAddr
	stage := 0
	send := func(msg []byte) error {
		_, err := server.WriteToUDP(TransCodePartial(nil, msg), peer)
		return err
	}
	for {
		n, addr, err := server.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		peer = addr
		data := make([]byte, n)
		ReverseTransCodePartial(data, buf[:n])
		if stage == 0 && n >= 68 && data[64] == stageBroadcast {
			reply := make([]byte, 32)
			reply[2] = version
			copy(reply[8:], []byte{2, 6, 0x12, 0})
			if err = send(reply); err != nil {
				return err
			}
			stage = 1
			continue
		}
		if stage == 1 && n >= 84 && data[28] == 0 && (data[29] == 0 || data[29] == 0x20) {
			reply := make([]byte, 84)
			reply[2] = version
			reply[28], reply[29] = 0, 0x14
			if err = send(reply); err != nil {
				return err
			}
			stage = 2
			continue
		}
		if stage == 2 && n >= 52 && data[28] == 0 && data[29] == 0x70 {
			header := 24
			if version >= 25 {
				header = 28
			}
			ack := make([]byte, 28+header)
			ack[8], ack[28], ack[29] = 8, 0, 0x71
			copy(ack[32:34], data[32:34])
			if err = send(ack); err != nil {
				return err
			}
			response := make([]byte, 28+header+4+len("response"))
			response[8], response[28], response[29] = 8, 0, 0x70
			binary.LittleEndian.PutUint32(response[28+header:], 0x101)
			copy(response[28+header+4:], "response")
			if err = send(response); err != nil {
				return err
			}
			return nil
		}
	}
}
