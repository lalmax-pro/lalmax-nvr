package jt1078

import (
	"io"
	"net"
	"testing"
	"time"
)

func TestDownlinkSequenceSharedAcrossChannels(t *testing.T) {
	hub := newStreamHub(newFakeHost())
	defer hub.closeAll()
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	for _, name := range []string{"1003_1", "1003_2"} {
		if _, err := hub.acquire(name, true); err != nil {
			t.Fatal(err)
		}
		hub.bindDownlink(name, server)
	}
	for i, channel := range []byte{1, 2, 1} {
		pkt := Packet{Sim: "1003", LogicChannel: channel, Flag: Flag{PT: PTG711A}, DataType: DataTypeA, Body: []byte{0xd5}}
		done := make(chan error, 1)
		go func() { done <- hub.writeDownlink(pkt.StreamName(), pkt) }()
		raw := make([]byte, 27)
		client.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := io.ReadFull(client, raw); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		got, _, err := decodeWith(raw, 6)
		if err != nil {
			t.Fatal(err)
		}
		if got.Seq != uint16(i) || got.LogicChannel != channel {
			t.Fatalf("packet=%+v", got)
		}
	}
}
