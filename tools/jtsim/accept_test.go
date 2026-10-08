package jtsim

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/jt808"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/lalmax/jt1078"
)

func TestAcceptSignalingAndMedia(t *testing.T) {
	cases := []struct {
		name    string
		version byte
		sim     string
		phone   int
		udp     bool
	}{
		{name: "tcp-2013", sim: "1003"},
		{name: "udp-2013", sim: "1003", udp: true},
		{name: "tcp-2019", version: 1, sim: "13800138000", phone: 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			acceptOnce(t, tc.version, tc.sim, tc.phone, tc.udp)
		})
	}
}

func acceptOnce(t *testing.T, version byte, sim string, phoneLen int, udp bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	mediaTCP := freeAddr(t, "tcp")
	mediaUDP := mediaTCP
	if udp {
		mediaUDP = freeAddr(t, "udp")
	}
	host := newCapture()
	var opts []jt1078.ServerOption
	if udp {
		opts = append(opts, jt1078.WithUDP(mediaUDP, time.Second))
	}
	media := jt1078.NewServer(mediaTCP, host, opts...)
	go media.Run(ctx)
	defer media.Shutdown()
	waitBound(t, media, udp)

	signalPort := portOf(t, freeAddr(t, "tcp"))
	transport := jt808.TransportTCP
	if udp {
		transport = jt808.TransportUDP
	}
	sig, err := jt808.NewServer(jt808.Config{
		Enabled:      true,
		Port:         signalPort,
		MediaIP:      "127.0.0.1",
		AuthCode:     "secret",
		MediaTCPPort: portOf(t, mediaTCP),
		MediaUDPPort: portOf(t, mediaUDP),
		Transport:    transport,
		Timeout:      "3s",
	})
	if err != nil {
		t.Fatal(err)
	}
	media.SetAuthorizer(sig)
	if err := sig.Start(); err != nil {
		t.Fatal(err)
	}
	defer sig.Stop()
	waitListen(t, "tcp", fmt.Sprintf("127.0.0.1:%d", signalPort))

	sess, err := Register(ctx, fmt.Sprintf("127.0.0.1:%d", signalPort), Options{SIM: sim, Version: version})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if sess.AuthCode != "secret" {
		t.Fatalf("auth code=%q", sess.AuthCode)
	}
	waitAuthenticated(t, sig, sim)

	packets := samplePackets(sim, phoneLen)
	// Unrequested media must not create a publisher, even with a valid SIM.
	unauthorized := Command{MediaIP: "127.0.0.1", TCPPort: portOf(t, mediaTCP)}
	if udp {
		unauthorized.TCPPort = 0
		unauthorized.UDPPort = portOf(t, mediaUDP)
	}
	if err := publish(ctx, unauthorized, packets); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(host.snapshot(sim+"_1")) != 0 {
		t.Fatal("unrequested media was accepted")
	}

	done := make(chan error, 1)
	go func() { done <- sess.Serve(ctx, packets) }()

	streamID, err := sig.Play(jt808.PlayInput{Key: sim, Channel: 1, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if streamID != sim+"_1" {
		t.Fatalf("stream id=%s", streamID)
	}
	waitPackets(t, host, streamID)
	if err := sig.StopPlay(jt808.StopPlayInput{Key: sim, Channel: 1}); err != nil {
		t.Fatal(err)
	}
	if sig.AllowStream(streamID) {
		t.Fatal("media remains authorized after stop")
	}
	if udp {
		before := len(host.snapshot(streamID))
		if err := publish(ctx, unauthorized, packets); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
		if len(host.snapshot(streamID)) != before {
			t.Fatal("UDP packets were accepted after stop")
		}
	}

	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !udp {
		waitGone(t, host, streamID)
	}
}

func samplePackets(sim string, phoneLen int) []jt1078.Packet {
	return []jt1078.Packet{
		{
			PhoneLen:        phoneLen,
			Flag:            jt1078.Flag{V: 2, CC: 1, M: 0, PT: jt1078.PTH264},
			Seq:             1,
			Sim:             sim,
			LogicChannel:    1,
			DataType:        jt1078.DataTypeI,
			SubcontractType: jt1078.SubcontractTypeFirst,
			Timestamp:       5000,
			Body:            []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42},
		},
		{
			PhoneLen:        phoneLen,
			Flag:            jt1078.Flag{V: 2, CC: 1, M: 1, PT: jt1078.PTH264},
			Seq:             2,
			Sim:             sim,
			LogicChannel:    1,
			DataType:        jt1078.DataTypeI,
			SubcontractType: jt1078.SubcontractTypeLast,
			Timestamp:       5000,
			Body:            []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88},
		},
		{
			PhoneLen:        phoneLen,
			Flag:            jt1078.Flag{V: 2, CC: 1, M: 1, PT: jt1078.PTG711A},
			Seq:             3,
			Sim:             sim,
			LogicChannel:    1,
			DataType:        jt1078.DataTypeA,
			SubcontractType: jt1078.SubcontractTypeAtomic,
			Timestamp:       5040,
			Body:            []byte{0xd5, 0xd5},
		},
	}
}

func waitAuthenticated(t *testing.T, sig *jt808.Server, sim string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, term := range sig.ListTerminals() {
			if term.Key == sim && term.Authenticated {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("terminal %s not authenticated: %+v", sim, sig.ListTerminals())
}

func waitPackets(t *testing.T, host *capture, streamID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := host.snapshot(streamID)
		if len(got) >= 2 {
			if got[0].PayloadType != base.AvPacketPtAvc || got[0].Timestamp != 0 {
				t.Fatalf("video=%+v", got[0])
			}
			want := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x00, 0x00, 0x01, 0x65, 0x88}
			if !bytes.Equal(got[0].Payload, want) {
				t.Fatalf("video payload=%x", got[0].Payload)
			}
			if got[1].PayloadType != base.AvPacketPtG711A || got[1].Timestamp != 40 || !bytes.Equal(got[1].Payload, []byte{0xd5, 0xd5}) {
				t.Fatalf("audio=%+v payload=%x", got[1], got[1].Payload)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("stream %s packets=%d", streamID, len(host.snapshot(streamID)))
}

func waitGone(t *testing.T, host *capture, streamID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !host.has(streamID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("stream %s still published after 0x9102", streamID)
}

func freeAddr(t *testing.T, network string) string {
	t.Helper()
	if network == "udp" {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := pc.LocalAddr().String()
		pc.Close()
		return addr
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func portOf(t *testing.T, addr string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if _, err := fmt.Sscan(port, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitBound(t *testing.T, media *jt1078.Server, udp bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tcp, u := media.Addrs()
		if tcp != nil && (!udp || u != nil) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("jt1078 socket was not bound")
}

func waitListen(t *testing.T, network, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if network == "udp" {
			conn, err := net.DialTimeout("udp", addr, 50*time.Millisecond)
			if err == nil {
				conn.Close()
				return
			}
		} else {
			conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
			if err == nil {
				conn.Close()
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s %s did not listen", network, addr)
}

type capture struct {
	mu   sync.Mutex
	pubs map[string]*pub
	done map[string]*pub
}

func newCapture() *capture {
	return &capture{pubs: make(map[string]*pub), done: make(map[string]*pub)}
}

func (h *capture) AddCustomizePubSession(streamName string) (logic.ICustomizePubSessionContext, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.pubs[streamName]; ok {
		return nil, base.ErrDupInStream
	}
	p := &pub{name: streamName}
	h.pubs[streamName] = p
	return p, nil
}

func (h *capture) DelCustomizePubSession(ctx logic.ICustomizePubSessionContext) {
	if ctx == nil {
		return
	}
	h.mu.Lock()
	name := ctx.StreamName()
	if p := h.pubs[name]; p != nil {
		h.done[name] = p
	}
	delete(h.pubs, name)
	h.mu.Unlock()
}

func (h *capture) snapshot(name string) []base.AvPacket {
	h.mu.Lock()
	p := h.pubs[name]
	if p == nil {
		p = h.done[name]
	}
	h.mu.Unlock()
	if p == nil {
		return nil
	}
	return p.snapshot()
}

func (h *capture) has(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.pubs[name]
	return ok
}

type pub struct {
	name string
	mu   sync.Mutex
	pkts []base.AvPacket
}

func (p *pub) WithOption(func(*base.AvPacketStreamOption)) {}
func (p *pub) FeedAudioSpecificConfig([]byte) error        { return nil }
func (p *pub) FeedAvPacket(pkt base.AvPacket) error {
	p.mu.Lock()
	p.pkts = append(p.pkts, pkt)
	p.mu.Unlock()
	return nil
}
func (p *pub) FeedRtmpMsg(base.RtmpMsg) error { return nil }
func (p *pub) UniqueKey() string              { return "sim" }
func (p *pub) StreamName() string             { return p.name }
func (p *pub) snapshot() []base.AvPacket {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]base.AvPacket, len(p.pkts))
	copy(out, p.pkts)
	return out
}
