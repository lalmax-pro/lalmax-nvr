package jt1078

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
)

type fakePub struct {
	uk         string
	streamName string
	mu         sync.Mutex
	packets    []base.AvPacket
	annexb     bool
}

func (p *fakePub) WithOption(mod func(option *base.AvPacketStreamOption)) {
	opt := &base.AvPacketStreamOption{}
	mod(opt)
	p.annexb = opt.VideoFormat == base.AvPacketStreamVideoFormatAnnexb
}
func (p *fakePub) FeedAudioSpecificConfig([]byte) error { return nil }
func (p *fakePub) FeedAvPacket(packet base.AvPacket) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.packets = append(p.packets, packet)
	return nil
}
func (p *fakePub) FeedRtmpMsg(base.RtmpMsg) error { return nil }
func (p *fakePub) UniqueKey() string              { return p.uk }
func (p *fakePub) StreamName() string             { return p.streamName }
func (p *fakePub) snapshot() []base.AvPacket {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]base.AvPacket, len(p.packets))
	copy(out, p.packets)
	return out
}

type fakeHost struct {
	mu   sync.Mutex
	pubs map[string]*fakePub
}

func newFakeHost() *fakeHost {
	return &fakeHost{pubs: make(map[string]*fakePub)}
}

func (h *fakeHost) AddCustomizePubSession(streamName string) (logic.ICustomizePubSessionContext, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.pubs[streamName]; ok {
		return nil, base.ErrDupInStream
	}
	pub := &fakePub{uk: "JT1078PUB", streamName: streamName}
	h.pubs[streamName] = pub
	return pub, nil
}

func (h *fakeHost) DelCustomizePubSession(ctx logic.ICustomizePubSessionContext) {
	if ctx == nil {
		return
	}
	h.mu.Lock()
	delete(h.pubs, ctx.StreamName())
	h.mu.Unlock()
}

func (h *fakeHost) pub(name string) *fakePub {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pubs[name]
}

func encodePacket(t *testing.T, pkt Packet) []byte {
	t.Helper()
	raw, err := pkt.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAssembleSubcontract(t *testing.T) {
	asm := newFrameAssembler()
	if complete, _ := asm.push(Packet{Seq: 1, DataType: DataTypeI, SubcontractType: SubcontractTypeFirst, Body: []byte("aa")}); complete {
		t.Fatal("first should not complete")
	}
	if complete, _ := asm.push(Packet{Seq: 2, DataType: DataTypeI, SubcontractType: SubcontractTypeMiddle, Body: []byte("bb")}); complete {
		t.Fatal("middle should not complete")
	}
	complete, data := asm.push(Packet{Seq: 3, DataType: DataTypeI, SubcontractType: SubcontractTypeLast, Body: []byte("cc")})
	if !complete || string(data) != "aabbcc" {
		t.Fatalf("complete=%v data=%s", complete, data)
	}
	complete, data = asm.push(Packet{DataType: DataTypeI, SubcontractType: SubcontractTypeAtomic, Body: []byte("zz")})
	if !complete || string(data) != "zz" {
		t.Fatalf("atomic data=%s", data)
	}
}

func TestAssembleDropsOnSequenceGap(t *testing.T) {
	asm := newFrameAssembler()
	if complete, _ := asm.push(Packet{Seq: 10, DataType: DataTypeP, SubcontractType: SubcontractTypeFirst, Body: []byte("aa")}); complete {
		t.Fatal("first")
	}
	if complete, _ := asm.push(Packet{Seq: 12, DataType: DataTypeP, SubcontractType: SubcontractTypeMiddle, Body: []byte("bb")}); complete {
		t.Fatal("gap should drop the partial frame")
	}
	if complete, _ := asm.push(Packet{Seq: 13, DataType: DataTypeP, SubcontractType: SubcontractTypeLast, Body: []byte("cc")}); complete {
		t.Fatal("last after gap should not complete")
	}
	complete, data := asm.push(Packet{Seq: 65535, DataType: DataTypeI, SubcontractType: SubcontractTypeFirst, Body: []byte("x")})
	if complete {
		t.Fatal("first")
	}
	complete, data = asm.push(Packet{Seq: 0, DataType: DataTypeI, SubcontractType: SubcontractTypeLast, Body: []byte("y")})
	if !complete || string(data) != "xy" {
		t.Fatalf("wrap data=%s complete=%v", data, complete)
	}
}

func TestPublishFeedsAnnexBAndRelativeTs(t *testing.T) {
	host := newFakeHost()
	session, err := host.AddCustomizePubSession("1003_1")
	if err != nil {
		t.Fatal(err)
	}
	session.WithOption(func(option *base.AvPacketStreamOption) {
		option.VideoFormat = base.AvPacketStreamVideoFormatAnnexb
	})
	ch := make(chan Packet, 8)
	done := make(chan struct{})
	go func() {
		publish(host, session, "1003_1", ch)
		close(done)
	}()

	ch <- Packet{
		Flag:            Flag{PT: PTH264},
		Seq:             1,
		DataType:        DataTypeI,
		SubcontractType: SubcontractTypeFirst,
		Timestamp:       1000,
		Body:            []byte{0x00, 0x00, 0x00, 0x01, 0x67},
	}
	ch <- Packet{
		Flag:            Flag{PT: PTH264},
		Seq:             2,
		DataType:        DataTypeI,
		SubcontractType: SubcontractTypeLast,
		Timestamp:       1000,
		Body:            []byte{0x00, 0x00, 0x00, 0x01, 0x65},
	}
	ch <- Packet{
		Flag:            Flag{PT: PTG711A},
		DataType:        DataTypeA,
		SubcontractType: SubcontractTypeAtomic,
		Timestamp:       1040,
		Body:            []byte{0xd5, 0xd5},
	}
	ch <- Packet{
		DataType: DataTypePenetrate,
		Body:     []byte{0x01},
	}
	close(ch)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish did not exit")
	}

	pub := session.(*fakePub)
	if !pub.annexb {
		t.Fatal("expected annexb video format")
	}
	got := pub.snapshot()
	if len(got) != 2 {
		t.Fatalf("packets=%d", len(got))
	}
	if got[0].PayloadType != base.AvPacketPtAvc || got[0].Timestamp != 0 {
		t.Fatalf("video=%+v", got[0])
	}
	if !bytes.Equal(got[0].Payload, []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x00, 0x00, 0x00, 0x01, 0x65}) {
		t.Fatalf("video payload=%x", got[0].Payload)
	}
	if got[1].PayloadType != base.AvPacketPtG711A || got[1].Timestamp != 40 {
		t.Fatalf("audio=%+v", got[1])
	}
	if host.pub("1003_1") != nil {
		t.Fatal("session should be deleted after publish exits")
	}
}

func TestHandleConnCreatesStream(t *testing.T) {
	host := newFakeHost()
	svr := NewServer(":0", host)
	c1, c2 := net.Pipe()
	defer c2.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svr.handleConn(ctx, c1)

	pkt := Packet{
		Flag: Flag{
			V:  2,
			CC: 1,
			M:  1,
			PT: PTH264,
		},
		Sim:             "1003",
		LogicChannel:    1,
		DataType:        DataTypeI,
		SubcontractType: SubcontractTypeAtomic,
		Timestamp:       50,
		Body:            []byte{0x00, 0x00, 0x00, 0x01, 0x67},
	}
	if _, err := c2.Write(encodePacket(t, pkt)); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var pub *fakePub
	for time.Now().Before(deadline) {
		pub = host.pub("1003_1")
		if pub != nil && len(pub.snapshot()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pub == nil {
		t.Fatal("stream not created")
	}
	got := pub.snapshot()
	if len(got) != 1 || got[0].PayloadType != base.AvPacketPtAvc {
		t.Fatalf("packets=%+v", got)
	}
	_ = c2.Close()
}

func TestHandleConnResyncsAfterGarbage(t *testing.T) {
	host := newFakeHost()
	svr := NewServer(":0", host)
	c1, c2 := net.Pipe()
	defer c2.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svr.handleConn(ctx, c1)

	pkt := Packet{
		Flag:            Flag{V: 2, CC: 1, M: 1, PT: PTH265},
		Sim:             "2004",
		LogicChannel:    2,
		DataType:        DataTypeP,
		SubcontractType: SubcontractTypeAtomic,
		Body:            []byte{0x00, 0x00, 0x01, 0x26},
	}
	raw := append([]byte{0xaa, 0xbb}, encodePacket(t, pkt)...)
	if _, err := c2.Write(raw); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pub := host.pub("2004_2"); pub != nil && len(pub.snapshot()) == 1 {
			if pub.snapshot()[0].PayloadType != base.AvPacketPtHevc {
				t.Fatalf("pt=%v", pub.snapshot()[0].PayloadType)
			}
			_ = c2.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("did not resync to hevc packet")
}

func TestServerRunAndShutdown(t *testing.T) {
	host := newFakeHost()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	svr := NewServer(addr, host)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		svr.Run(ctx)
		close(done)
	}()

	var conn net.Conn
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("dial: %v", err)
	}
	pkt := Packet{
		Flag:            Flag{V: 2, CC: 1, PT: PTG711U},
		Sim:             "9",
		LogicChannel:    3,
		DataType:        DataTypeA,
		SubcontractType: SubcontractTypeAtomic,
		Body:            []byte{0x01, 0x02},
	}
	if _, err := conn.Write(encodePacket(t, pkt)); err != nil {
		t.Fatal(err)
	}

	ok := false
	for time.Now().Before(deadline) {
		if pub := host.pub("9_3"); pub != nil && len(pub.snapshot()) == 1 {
			ok = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = conn.Close()
	if !ok {
		t.Fatal("server did not ingest packet")
	}
	svr.Shutdown()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
}

func TestHandleConnPartialReads(t *testing.T) {
	host := newFakeHost()
	svr := NewServer(":0", host)
	c1, c2 := net.Pipe()
	defer c2.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svr.handleConn(ctx, c1)

	pkt := Packet{
		Flag:            Flag{V: 2, CC: 1, PT: PTH264},
		Sim:             "55",
		LogicChannel:    1,
		DataType:        DataTypeI,
		SubcontractType: SubcontractTypeAtomic,
		Body:            bytes.Repeat([]byte{0x00, 0x00, 0x00, 0x01, 0x65}, 8),
	}
	raw := encodePacket(t, pkt)
	if _, err := c2.Write(raw[:10]); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if host.pub("55_1") != nil {
		t.Fatal("should wait for remaining bytes")
	}
	if _, err := c2.Write(raw[10:]); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pub := host.pub("55_1"); pub != nil && len(pub.snapshot()) == 1 {
			_ = c2.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("partial packet was not completed")
}

func TestAvPacketPt(t *testing.T) {
	cases := map[PTType]base.AvPacketPt{
		PTG711A: base.AvPacketPtG711A,
		PTG711U: base.AvPacketPtG711U,
		PTAAC:   base.AvPacketPtAac,
		PTH264:  base.AvPacketPtAvc,
		PTH265:  base.AvPacketPtHevc,
	}
	for in, want := range cases {
		got, ok := avPacketPt(in)
		if !ok || got != want {
			t.Fatalf("pt=%s got=%v ok=%v", in, got, ok)
		}
	}
	if _, ok := avPacketPt(PTMP3); ok || !unsupportedPT(PTMP3) {
		t.Fatal("mp3 should be rejected")
	}
	if _, ok := avPacketPt(PTG726); ok || !unsupportedPT(PTG726) {
		t.Fatal("g726 should be rejected")
	}
}

func TestPublishRejectsUnsupportedAudio(t *testing.T) {
	host := newFakeHost()
	session, err := host.AddCustomizePubSession("1003_1")
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan Packet, 4)
	done := make(chan struct{})
	go func() {
		publish(host, session, "1003_1", ch)
		close(done)
	}()
	ch <- Packet{Flag: Flag{PT: PTMP3}, DataType: DataTypeA, SubcontractType: SubcontractTypeAtomic, Body: []byte{0x01}}
	ch <- Packet{Flag: Flag{PT: PTG726}, DataType: DataTypeA, SubcontractType: SubcontractTypeAtomic, Body: []byte{0x02}}
	ch <- Packet{Flag: Flag{PT: PTG711A}, DataType: DataTypeA, SubcontractType: SubcontractTypeAtomic, Timestamp: 20, Body: []byte{0xd5}}
	close(ch)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish did not exit")
	}
	got := session.(*fakePub).snapshot()
	if len(got) != 1 || got[0].PayloadType != base.AvPacketPtG711A {
		t.Fatalf("packets=%+v", got)
	}
}

func TestHandleDatagramCreatesStream(t *testing.T) {
	host := newFakeHost()
	svr := NewServer(":0", host)
	pkt := Packet{
		Flag:            Flag{V: 2, CC: 1, M: 1, PT: PTH264},
		Sim:             "1003",
		LogicChannel:    1,
		DataType:        DataTypeI,
		SubcontractType: SubcontractTypeAtomic,
		Timestamp:       50,
		Body:            []byte{0x00, 0x00, 0x00, 0x01, 0x67},
	}
	svr.handleDatagram(encodePacket(t, pkt))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pub := host.pub("1003_1"); pub != nil && len(pub.snapshot()) == 1 {
			if pub.snapshot()[0].PayloadType != base.AvPacketPtAvc {
				t.Fatalf("pt=%v", pub.snapshot()[0].PayloadType)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("udp datagram did not create stream")
}

func TestUDPNetworkIngest(t *testing.T) {
	// Bind a real UDP socket to exercise the listener path, not only handleDatagram.
	host := newFakeHost()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()
	svr := NewServer("127.0.0.1:0", host, WithUDP(addr, time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { svr.runUDP(ctx); close(done) }()
	defer func() { cancel(); svr.Shutdown(); <-done }()
	pkt := Packet{Flag: Flag{V: 2, CC: 1, PT: PTG711A}, Sim: "3005", LogicChannel: 1,
		DataType: DataTypeA, SubcontractType: SubcontractTypeAtomic, Body: []byte{0xd5}}
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		svr.mu.Lock()
		ready := svr.udpConn != nil
		svr.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := conn.Write(encodePacket(t, pkt)); err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) {
		if pub := host.pub("3005_1"); pub != nil && len(pub.snapshot()) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("UDP listener did not publish packet")
}

func TestUDPIdleReap(t *testing.T) {
	host := newFakeHost()
	hub := newStreamHub(host)
	if _, err := hub.touchUDP("9_3"); err != nil {
		t.Fatal(err)
	}
	hub.mu.Lock()
	st := hub.m["9_3"]
	st.lastUDP = time.Now().Add(-time.Minute)
	hub.mu.Unlock()

	hub.reapUDP(time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if host.pub("9_3") == nil {
			hub.mu.Lock()
			_, ok := hub.m["9_3"]
			hub.mu.Unlock()
			if ok {
				t.Fatal("idle stream still in hub")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("udp idle stream was not reaped")
}

func TestTCPAndUDPShareStream(t *testing.T) {
	host := newFakeHost()
	svr := NewServer(":0", host)
	c1, c2 := net.Pipe()
	defer c2.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svr.handleConn(ctx, c1)

	tcpPkt := Packet{
		Flag:            Flag{V: 2, CC: 1, M: 1, PT: PTH264},
		Sim:             "2004",
		LogicChannel:    2,
		DataType:        DataTypeI,
		SubcontractType: SubcontractTypeAtomic,
		Body:            []byte{0x00, 0x00, 0x00, 0x01, 0x67},
	}
	if _, err := c2.Write(encodePacket(t, tcpPkt)); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if host.pub("2004_2") != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if host.pub("2004_2") == nil {
		t.Fatal("tcp stream not created")
	}

	udpPkt := Packet{
		Flag:            Flag{V: 2, CC: 1, PT: PTG711A},
		Sim:             "2004",
		LogicChannel:    2,
		DataType:        DataTypeA,
		SubcontractType: SubcontractTypeAtomic,
		Timestamp:       40,
		Body:            []byte{0xd5, 0xd5},
	}
	svr.handleDatagram(encodePacket(t, udpPkt))

	ok := false
	for time.Now().Before(deadline) {
		if pub := host.pub("2004_2"); pub != nil && len(pub.snapshot()) >= 2 {
			ok = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ok {
		t.Fatal("udp packet did not share tcp stream")
	}
	host.mu.Lock()
	n := len(host.pubs)
	host.mu.Unlock()
	if n != 1 {
		t.Fatalf("expected 1 session, got %d", n)
	}
	_ = c2.Close()
}

func TestHandleConnMultiplexesChannels(t *testing.T) {
	host := newFakeHost()
	svr := NewServer(":0", host)
	c1, c2 := net.Pipe()
	defer c2.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svr.handleConn(ctx, c1)

	for _, pkt := range []Packet{
		{Flag: Flag{V: 2, CC: 1, PT: PTH264}, Sim: "1003", LogicChannel: 1, DataType: DataTypeI, SubcontractType: SubcontractTypeAtomic, Body: []byte{0x00, 0x00, 0x00, 0x01, 0x65}},
		{Flag: Flag{V: 2, CC: 1, PT: PTG711A}, Sim: "1003", LogicChannel: 2, DataType: DataTypeA, SubcontractType: SubcontractTypeAtomic, Body: []byte{0xd5}},
	} {
		if _, err := c2.Write(encodePacket(t, pkt)); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		video := host.pub("1003_1")
		audio := host.pub("1003_2")
		if video != nil && audio != nil && len(video.snapshot()) == 1 && len(audio.snapshot()) == 1 {
			if video.snapshot()[0].PayloadType != base.AvPacketPtAvc || audio.snapshot()[0].PayloadType != base.AvPacketPtG711A {
				t.Fatalf("video=%v audio=%v", video.snapshot()[0].PayloadType, audio.snapshot()[0].PayloadType)
			}
			_ = c2.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("one TCP connection should publish both channels")
}

type streamGate struct {
	mu    sync.Mutex
	allow bool
	calls int
}

func (g *streamGate) AllowStream(string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	return g.allow
}

func (g *streamGate) set(allow bool) {
	g.mu.Lock()
	g.allow = allow
	g.mu.Unlock()
}

func (g *streamGate) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func TestAuthorizerRejectsThenAllows(t *testing.T) {
	host := newFakeHost()
	svr := NewServer(":0", host)
	gate := &streamGate{}
	svr.SetAuthorizer(gate)
	c1, c2 := net.Pipe()
	defer c2.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svr.handleConn(ctx, c1)

	pkt := Packet{
		Flag: Flag{V: 2, CC: 1, PT: PTH264}, Sim: "1003", LogicChannel: 1,
		DataType: DataTypeI, SubcontractType: SubcontractTypeAtomic,
		Body: []byte{0x00, 0x00, 0x00, 0x01, 0x65},
	}
	raw := encodePacket(t, pkt)
	if _, err := c2.Write(raw); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if gate.count() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if gate.count() < 1 {
		t.Fatal("authorizer was not consulted")
	}
	if host.pub("1003_1") != nil {
		t.Fatal("unauthorized stream was published")
	}
	gate.set(true)
	if _, err := c2.Write(raw); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pub := host.pub("1003_1"); pub != nil && len(pub.snapshot()) == 1 {
			_ = c2.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("authorized stream was not published")
}

func TestPushWritesDownlink(t *testing.T) {
	host := newFakeHost()
	svr := NewServer(":0", host)
	if err := svr.Push(Packet{Sim: "1003", LogicChannel: 1, Body: []byte{0xd5}}); err == nil {
		t.Fatal("push before connect should fail")
	}
	c1, c2 := net.Pipe()
	defer c2.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svr.handleConn(ctx, c1)

	up := Packet{
		Flag: Flag{V: 2, CC: 1, PT: PTH264}, Sim: "1003", LogicChannel: 1,
		DataType: DataTypeI, SubcontractType: SubcontractTypeAtomic,
		Body: []byte{0x00, 0x00, 0x00, 0x01, 0x65},
	}
	if _, err := c2.Write(encodePacket(t, up)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pub := host.pub("1003_1"); pub != nil && len(pub.snapshot()) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if host.pub("1003_1") == nil {
		t.Fatal("uplink stream missing")
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- svr.Push(Packet{
			Sim: "1003", LogicChannel: 1, Flag: Flag{PT: PTG711A},
			DataType: DataTypeA, SubcontractType: SubcontractTypeAtomic, Body: []byte{0xd5},
		})
	}()
	_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, err := c2.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(buf[:n], []byte("01cd")) {
		t.Fatalf("downlink=%x", buf[:n])
	}
}

func TestPublishBareNALGetsStartCode(t *testing.T) {
	host := newFakeHost()
	session, err := host.AddCustomizePubSession("1003_1")
	if err != nil {
		t.Fatal(err)
	}
	session.WithOption(func(option *base.AvPacketStreamOption) {
		option.VideoFormat = base.AvPacketStreamVideoFormatAnnexb
	})
	ch := make(chan Packet, 2)
	done := make(chan struct{})
	go func() {
		publish(host, session, "1003_1", ch)
		close(done)
	}()
	ch <- Packet{
		Flag:            Flag{PT: PTH264},
		DataType:        DataTypeI,
		SubcontractType: SubcontractTypeAtomic,
		Body:            []byte{0x67, 0x42},
	}
	close(ch)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish did not exit")
	}
	got := session.(*fakePub).snapshot()
	if len(got) != 1 || !bytes.Equal(got[0].Payload, []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42}) {
		t.Fatalf("payload=%x", got)
	}
}
