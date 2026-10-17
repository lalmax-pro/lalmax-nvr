package mediaserver

import (
	"net"
	"testing"
	"time"
)

func TestClaimConnReplacesPriorConnection(t *testing.T) {
	s := &GB28181MediaServer{}
	oldSide, peer := net.Pipe()
	defer peer.Close()
	old := &Conn{conn: oldSide, streamName: "device:channel", done: make(chan struct{})}
	close(old.done) // Simulates completed publisher cleanup.
	s.conns.Store(old.streamName, old)

	newSide, newPeer := net.Pipe()
	defer newSide.Close()
	defer newPeer.Close()
	next := &Conn{conn: newSide, streamName: old.streamName, done: make(chan struct{})}
	if !s.claimConn(next) {
		t.Fatal("TCP successor should be accepted")
	}

	current, ok := s.conns.Load(old.streamName)
	if !ok || current != next {
		t.Fatal("new RTP connection was not installed")
	}
	// An exiting former connection cannot remove its replacement.
	if s.conns.CompareAndDelete(old.streamName, old) {
		t.Fatal("stale RTP connection deleted active mapping")
	}
}

// udpTestConn retains net.Pipe's deterministic close behavior but exposes
// a UDP peer address, as provided by the RTP virtual UDP listener.
type udpTestConn struct{ net.Conn }

func (c udpTestConn) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 9000}
}

func TestUDPActiveSourceCannotBePreempted(t *testing.T) {
	s := &GB28181MediaServer{}
	oldSide, peer := net.Pipe()
	defer peer.Close()
	old := &Conn{conn: udpTestConn{oldSide}, streamName: "cam", done: make(chan struct{})}
	old.lastPacket.Store(time.Now().UnixNano())
	s.conns.Store("cam", old)
	nextSide, nextPeer := net.Pipe()
	defer nextSide.Close()
	defer nextPeer.Close()
	next := &Conn{conn: udpTestConn{nextSide}, streamName: "cam", done: make(chan struct{})}
	for i := 0; i < 100; i++ {
		if s.claimConn(next) {
			t.Fatal("active UDP source was preempted")
		}
	}
	value, _ := s.conns.Load("cam")
	if value != old {
		t.Fatal("source ownership changed")
	}
	select {
	case <-old.done:
		t.Fatal("old source closed")
	default:
	}
	_ = oldSide.Close()
}

func TestUDPIdleSourceCanBeReplaced(t *testing.T) {
	s := &GB28181MediaServer{}
	oldSide, peer := net.Pipe()
	defer peer.Close()
	old := &Conn{conn: udpTestConn{oldSide}, streamName: "cam", done: make(chan struct{})}
	old.lastPacket.Store(time.Now().Add(-udpSourceIdleTimeout - time.Second).UnixNano())
	s.conns.Store("cam", old)
	nextSide, nextPeer := net.Pipe()
	defer nextSide.Close()
	defer nextPeer.Close()
	next := &Conn{conn: udpTestConn{nextSide}, streamName: "cam", done: make(chan struct{})}
	go func() {
		<-time.After(10 * time.Millisecond)
		close(old.done) // simulate the exiting Serve defer after Close
	}()
	if !s.claimConn(next) {
		t.Fatal("idle source was not replaced")
	}
	value, _ := s.conns.Load("cam")
	if value != next {
		t.Fatal("replacement was not installed")
	}
}
