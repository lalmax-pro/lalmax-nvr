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

func waitForBufferedLayerPackets(t *testing.T, sub *LayeredSubscriber, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(sub.Packets()) < want {
		if time.Now().After(deadline) {
			t.Fatalf("buffered RTP packets = %d, want at least %d", len(sub.Packets()), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLayeredSubscriberSwitchDropsOldRIDQueueAndKeepsSequenceContinuous(t *testing.T) {
	f, err := NewLayered(2, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, layer := range []LayerInfo{{RID: "l", MaxBitrate: 150_000}, {RID: "m", MaxBitrate: 500_000}} {
		if err := f.AddLayer(layer); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := f.Subscribe("viewer")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	if err := f.WriteRTP("l", packetForLayer(100, 9000, 1)); err != nil {
		t.Fatal(err)
	}
	first := receiveLayerPacket(t, sub)
	for _, seq := range []uint16{101, 102} {
		if err := f.WriteRTP("l", packetForLayer(seq, 9000, 1)); err != nil {
			t.Fatal(err)
		}
	}
	waitForBufferedLayerPackets(t, sub, 2)
	if err := sub.SelectLayer("m"); err != nil {
		t.Fatal(err)
	}
	if buffered := len(sub.Packets()); buffered != 0 {
		t.Fatalf("stale old-RID RTP remained after switch: %d packets", buffered)
	}
	if err := f.WriteRTP("m", packetForLayer(4000, 12000, 2)); err != nil {
		t.Fatal(err)
	}
	got := receiveLayerPacket(t, sub)
	if got.Payload[0] != 2 {
		t.Fatalf("received stale RID after selecting m: payload=%v", got.Payload)
	}
	if got.SequenceNumber != first.SequenceNumber+1 {
		t.Fatalf("sequence discontinuity after dropping old queue: first=%d next=%d", first.SequenceNumber, got.SequenceNumber)
	}
}

func TestLayeredSubscriberSwitchDoesNotDrainAnotherViewer(t *testing.T) {
	f, err := NewLayered(2, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, layer := range []LayerInfo{{RID: "l", MaxBitrate: 150_000}, {RID: "m", MaxBitrate: 500_000}} {
		if err := f.AddLayer(layer); err != nil {
			t.Fatal(err)
		}
	}
	viewerA, err := f.Subscribe("viewer-a")
	if err != nil {
		t.Fatal(err)
	}
	defer viewerA.Close()
	viewerB, err := f.Subscribe("viewer-b")
	if err != nil {
		t.Fatal(err)
	}
	defer viewerB.Close()

	if err := f.WriteRTP("l", packetForLayer(20, 9000, 1)); err != nil {
		t.Fatal(err)
	}
	waitForBufferedLayerPackets(t, viewerA, 1)
	waitForBufferedLayerPackets(t, viewerB, 1)
	if err := viewerA.SelectLayer("m"); err != nil {
		t.Fatal(err)
	}
	if got := len(viewerA.Packets()); got != 0 {
		t.Fatalf("viewer-a retained old layer: %d packets", got)
	}
	if pkt := receiveLayerPacket(t, viewerB); pkt.Payload[0] != 1 {
		t.Fatalf("viewer-b lost its independent low layer: %v", pkt.Payload)
	}
	if err := f.WriteRTP("m", packetForLayer(500, 12000, 2)); err != nil {
		t.Fatal(err)
	}
	if pkt := receiveLayerPacket(t, viewerA); pkt.Payload[0] != 2 {
		t.Fatalf("viewer-a did not receive the selected mid layer: %v", pkt.Payload)
	}
	if err := f.WriteRTP("l", packetForLayer(21, 12000, 1)); err != nil {
		t.Fatal(err)
	}
	if pkt := receiveLayerPacket(t, viewerB); pkt.Payload[0] != 1 {
		t.Fatalf("viewer-b low-layer subscription changed unexpectedly: %v", pkt.Payload)
	}
}
