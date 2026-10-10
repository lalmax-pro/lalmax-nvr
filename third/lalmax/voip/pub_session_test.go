package voip

import (
	"encoding/binary"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lalmax/voip/media"
	"github.com/q191201771/lalmax/voip/sdp"
	"net"
	"sync"
	"testing"
	"time"
)

func TestDisposeWaitsForInFlightCallback(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	session, err := NewPubSession(PubSessionConfig{
		StreamName: "concurrent-close", MediaIP: "127.0.0.1", PortAllocator: media.NewPortAllocator(41000, 42000),
		Negotiated: sdp.Negotiated{Audio: &sdp.Payload{PayloadType: 8, Codec: base.AvPacketPtG711A, ClockRate: 8000}},
		OnAvPacket: func(base.AvPacket) { once.Do(func() { close(entered) }); <-release },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Dispose()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: session.AudioPort()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	pkt := make([]byte, 172)
	pkt[0] = 0x80
	pkt[1] = 8
	binary.BigEndian.PutUint16(pkt[2:], 1)
	binary.BigEndian.PutUint32(pkt[8:], 42)
	conn.Write(pkt)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("callback was not invoked")
	}
	done := make(chan struct{})
	go func() { session.Dispose(); close(done) }()
	session.SetOnAvPacket(func(base.AvPacket) {})
	select {
	case <-done:
		t.Fatal("Dispose returned while callback was active")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Dispose deadlocked with callback")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); session.Dispose() }()
	}
	wg.Wait()
}

func TestFailedSessionReturnsPreallocatedPorts(t *testing.T) {
	allocator := media.NewPortAllocator(41000, 42000)
	audio, err := allocator.Alloc()
	if err != nil {
		t.Fatal(err)
	}
	video, err := allocator.Alloc()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: audio})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewPubSession(PubSessionConfig{MediaIP: "127.0.0.1", PortAllocator: allocator, AudioPort: audio, VideoPort: video,
		Negotiated: sdp.Negotiated{Audio: &sdp.Payload{PayloadType: 8, Codec: base.AvPacketPtG711A, ClockRate: 8000}}, OnAvPacket: func(base.AvPacket) {}})
	conn.Close()
	if err == nil {
		t.Fatal("expected occupied port to fail")
	}
	got, err := allocator.Alloc()
	if err != nil || got != audio {
		t.Fatalf("audio port leaked: %d %v", got, err)
	}
	got, err = allocator.Alloc()
	if err != nil || got != video {
		t.Fatalf("video port leaked: %d %v", got, err)
	}
}
