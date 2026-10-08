package jt1078

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/naza/pkg/nazalog"
)

type streamHub struct {
	host customizePubHost

	mu       sync.Mutex
	m        map[string]*stream
	phoneLen map[string]int
	stopped  bool
}

type stream struct {
	name     string
	ch       chan Packet
	session  logic.ICustomizePubSessionContext
	tcpRefs  int
	lastUDP  time.Time
	downlink net.Conn
	writer   *downlinkWriter
	closed   bool
}

// One sequence and write lock per TCP connection, shared by multiplexed channels.
type downlinkWriter struct {
	mu  sync.Mutex
	seq uint16
}

func newStreamHub(host customizePubHost) *streamHub {
	return &streamHub{
		host:     host,
		m:        make(map[string]*stream),
		phoneLen: make(map[string]int),
	}
}

func (h *streamHub) phoneLenOf(sim string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.phoneLen[sim]
}

func (h *streamHub) setPhoneLen(sim string, n int) {
	if sim == "" || (n != simBCDLen && n != simBCDLen2019) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.phoneLen[sim] = n
}

func (h *streamHub) acquire(name string, asTCP bool) (chan Packet, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return nil, errors.New("jt1078 server stopped")
	}
	if st, ok := h.m[name]; ok && !st.closed {
		if asTCP {
			st.tcpRefs++
		} else {
			st.lastUDP = time.Now()
		}
		return st.ch, nil
	}

	session, err := h.host.AddCustomizePubSession(name)
	if err != nil {
		return nil, err
	}
	session.WithOption(func(option *base.AvPacketStreamOption) {
		option.VideoFormat = base.AvPacketStreamVideoFormatAnnexb
	})
	ch := make(chan Packet, packetChanSize)
	st := &stream{
		name:    name,
		ch:      ch,
		session: session,
	}
	if asTCP {
		st.tcpRefs = 1
	} else {
		st.lastUDP = time.Now()
	}
	h.m[name] = st
	go publish(h.host, session, name, ch)
	nazalog.Infof("jt1078 stream start. stream=%s tcp=%v", name, asTCP)
	return ch, nil
}

func (h *streamHub) releaseTCP(name string) {
	if name == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.m[name]
	if !ok || st.closed {
		return
	}
	if st.tcpRefs > 0 {
		st.tcpRefs--
	}
	if st.tcpRefs == 0 && udpIdle(st) {
		h.closeLocked(st)
	}
}

// send serializes channel sends with closeLocked: UDP idle reaping and shutdown
// may otherwise close a channel between acquire and the send.
func (h *streamHub) send(name string, ch chan Packet, pkt Packet) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st := h.m[name]; st == nil || st.closed || st.ch != ch {
		return
	}
	select {
	case ch <- pkt:
	default:
		nazalog.Warnf("jt1078 packet channel full, drop. stream=%s", name)
	}
}

func (h *streamHub) touchUDP(name string) (chan Packet, error) {
	return h.acquire(name, false)
}

func (h *streamHub) bindDownlink(name string, conn net.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st := h.m[name]; st != nil && !st.closed {
		st.downlink = conn
		st.writer = nil
		for _, other := range h.m {
			if other != st && other.downlink == conn && other.writer != nil {
				st.writer = other.writer
				break
			}
		}
		if st.writer == nil {
			st.writer = &downlinkWriter{}
		}
	}
}

func (h *streamHub) clearDownlink(name string, conn net.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st := h.m[name]; st != nil && st.downlink == conn {
		st.downlink = nil
		st.writer = nil
	}
}

func (h *streamHub) writeDownlink(name string, pkt Packet) error {
	h.mu.Lock()
	st := h.m[name]
	var conn net.Conn
	var writer *downlinkWriter
	if st != nil && !st.closed {
		conn = st.downlink
		writer = st.writer
	}
	h.mu.Unlock()
	if conn == nil || writer == nil {
		return errors.New("jt1078 downlink not connected")
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	pkt.Seq = writer.seq
	raw, err := pkt.Encode()
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_, err = conn.Write(raw)
	_ = conn.SetWriteDeadline(time.Time{})
	if err == nil {
		writer.seq++
	}
	return err
}

func (h *streamHub) reapUDP(idle time.Duration) {
	if idle <= 0 {
		return
	}
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, st := range h.m {
		if st.closed || st.tcpRefs > 0 {
			continue
		}
		if st.lastUDP.IsZero() || now.Sub(st.lastUDP) < idle {
			continue
		}
		nazalog.Infof("jt1078 udp idle timeout. stream=%s idle=%s", st.name, now.Sub(st.lastUDP))
		h.closeLocked(st)
	}
}

func (h *streamHub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = true
	for _, st := range h.m {
		h.closeLocked(st)
	}
}

func (h *streamHub) closeLocked(st *stream) {
	if st == nil || st.closed {
		return
	}
	st.closed = true
	close(st.ch)
	delete(h.m, st.name)
}

func udpIdle(st *stream) bool {
	return st.lastUDP.IsZero()
}
