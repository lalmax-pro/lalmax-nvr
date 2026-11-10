package rtpforward

import (
	"testing"
	"time"

	"github.com/pion/rtp"
)

func packetForLayer(seq uint16, timestamp uint32, payload byte) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: timestamp}, Payload: []byte{payload}}
}

func receiveLayerPacket(t *testing.T, sub *LayeredSubscriber) *rtp.Packet {
	t.Helper()
	select {
	case packet, ok := <-sub.Packets():
		if !ok {
			t.Fatalf("subscriber closed: %v", sub.Err())
		}
		return packet
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for layered RTP")
		return nil
	}
}

func TestLayeredSubscriberSwitchKeepsSequenceContinuous(t *testing.T) {
	f, err := NewLayered(4, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, layer := range []LayerInfo{{RID: "l", MaxBitrate: 150_000}, {RID: "m", MaxBitrate: 500_000}, {RID: "h", MaxBitrate: 1_500_000}} {
		if err = f.AddLayer(layer); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := f.Subscribe("viewer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	if got := sub.SelectedLayer(); got != "l" {
		t.Fatalf("initial RID %q, want l", got)
	}
	if err = f.WriteRTP("l", packetForLayer(100, 9000, 1)); err != nil {
		t.Fatal(err)
	}
	first := receiveLayerPacket(t, sub)
	if first.SequenceNumber != 100 || first.Payload[0] != 1 {
		t.Fatalf("first packet: seq=%d payload=%v", first.SequenceNumber, first.Payload)
	}
	if err = sub.SelectLayer("m"); err != nil {
		t.Fatal(err)
	}
	if err = f.WriteRTP("m", packetForLayer(4000, 9000, 2)); err != nil {
		t.Fatal(err)
	}
	second := receiveLayerPacket(t, sub)
	if second.SequenceNumber != first.SequenceNumber+1 || second.Timestamp != first.Timestamp || second.Payload[0] != 2 {
		t.Fatalf("switched packet: seq=%d ts=%d payload=%v", second.SequenceNumber, second.Timestamp, second.Payload)
	}
}

func TestRemovingSelectedLayerImmediatelyFallsBack(t *testing.T) {
	f, err := NewLayered(2, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, layer := range []LayerInfo{{RID: "l", MaxBitrate: 150_000}, {RID: "m", MaxBitrate: 500_000}} {
		if err = f.AddLayer(layer); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := f.Subscribe("viewer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	if err = sub.SelectLayer("m"); err != nil {
		t.Fatal(err)
	}
	if err = f.RemoveLayer("m"); err != nil {
		t.Fatal(err)
	}
	if got := sub.SelectedLayer(); got != "l" {
		t.Fatalf("selected RID after removal %q, want l", got)
	}
}
