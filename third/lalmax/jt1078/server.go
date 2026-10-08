package jt1078

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/naza/pkg/nazalog"
)

const (
	defaultListenAddr = ":1078"
	readBufSize       = 4096
	udpReadBufSize    = 64 * 1024
	maxHistoryBytes   = 256 * 1024
	maxFrameBytes     = 4 * 1024 * 1024
	packetChanSize    = 256
	defaultUDPIdle    = 15 * time.Second
)

type customizePubHost interface {
	AddCustomizePubSession(streamName string) (logic.ICustomizePubSessionContext, error)
	DelCustomizePubSession(logic.ICustomizePubSessionContext)
}

type ServerOption func(*Server)

func WithUDP(addr string, idle time.Duration) ServerOption {
	return func(s *Server) {
		s.udpEnabled = true
		s.udpAddr = addr
		if idle > 0 {
			s.udpIdle = idle
		}
	}
}

// WithPhoneLen forces the JT1078 terminal phone width. 6 is 2016, 10 is 2019.
// Zero keeps auto-detect, which is required when 2016 and 2019 devices share one port.
func WithPhoneLen(n int) ServerOption {
	return func(s *Server) {
		if n == simBCDLen || n == simBCDLen2019 {
			s.phoneLen = n
		}
	}
}

// StreamAuthorizer decides whether a {sim}_{channel} name may publish.
// A nil authorizer allows every stream, which is how standalone lalmax behaves.
type StreamAuthorizer interface {
	AllowStream(name string) bool
}

type Server struct {
	addr       string
	udpAddr    string
	udpEnabled bool
	udpIdle    time.Duration
	phoneLen   int
	host       customizePubHost
	hub        *streamHub

	mu         sync.Mutex
	listener   net.Listener
	udpConn    net.PacketConn
	conns      map[net.Conn]struct{}
	authorizer StreamAuthorizer
	denied     map[string]struct{}
}

func NewServer(addr string, host customizePubHost, opts ...ServerOption) *Server {
	if addr == "" {
		addr = defaultListenAddr
	}
	s := &Server{
		addr:    addr,
		host:    host,
		hub:     newStreamHub(host),
		conns:   make(map[net.Conn]struct{}),
		udpIdle: defaultUDPIdle,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	if s.udpEnabled && s.udpAddr == "" {
		s.udpAddr = s.addr
	}
	return s
}

// SetAuthorizer installs the live/playback allowlist. Nil allows every stream.
func (s *Server) SetAuthorizer(a StreamAuthorizer) {
	s.mu.Lock()
	s.authorizer = a
	s.mu.Unlock()
}

func (s *Server) allow(name string) bool {
	s.mu.Lock()
	a := s.authorizer
	s.mu.Unlock()
	if a == nil || a.AllowStream(name) {
		s.mu.Lock()
		delete(s.denied, name)
		s.mu.Unlock()
		return true
	}
	s.mu.Lock()
	if s.denied == nil {
		s.denied = map[string]struct{}{}
	}
	_, seen := s.denied[name]
	if !seen && len(s.denied) < 1024 {
		s.denied[name] = struct{}{}
	}
	s.mu.Unlock()
	if !seen {
		nazalog.Warnf("jt1078 reject unauthorized stream. stream=%s", name)
	}
	return false
}

// Push writes one JT1078 packet to the terminal TCP connection for that stream.
// UDP ingest has no return path.
func (s *Server) Push(pkt Packet) error {
	if pkt.Sim == "" || pkt.LogicChannel == 0 {
		return errors.New("jt1078 downlink needs sim and channel")
	}
	if pkt.PhoneLen == 0 {
		if s.phoneLen == simBCDLen || s.phoneLen == simBCDLen2019 {
			pkt.PhoneLen = s.phoneLen
		} else if n := s.hub.phoneLenOf(pkt.Sim); n != 0 {
			pkt.PhoneLen = n
		}
	}
	return s.hub.writeDownlink(pkt.StreamName(), pkt)
}

func (s *Server) Run(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", s.addr)
	if err != nil {
		nazalog.Errorf("jt1078 tcp listen failed. addr=%s err=%v", s.addr, err)
		return
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()
	defer func() {
		_ = ln.Close()
		s.mu.Lock()
		if s.listener == ln {
			s.listener = nil
		}
		s.mu.Unlock()
	}()

	tcpDone := make(chan struct{})
	defer close(tcpDone)
	go func() {
		select {
		case <-ctx.Done():
			s.Shutdown()
		case <-tcpDone:
		}
	}()

	if s.udpEnabled {
		go s.runUDP(ctx)
	}

	nazalog.Infof("jt1078 tcp listen. addr=%s", ln.Addr().String())
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			nazalog.Warnf("jt1078 accept failed: %v", err)
			continue
		}
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		go func() {
			defer func() {
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
			}()
			s.handleConn(ctx, conn)
		}()
	}
}

// Addrs reports the bound TCP listener and UDP socket. Either may be nil
// until Run has finished binding that socket.
func (s *Server) Addrs() (tcp, udp net.Addr) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		tcp = s.listener.Addr()
	}
	if s.udpConn != nil {
		udp = s.udpConn.LocalAddr()
	}
	return tcp, udp
}

func (s *Server) Shutdown() {
	s.mu.Lock()
	ln := s.listener
	pc := s.udpConn
	conns := make([]net.Conn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	if pc != nil {
		_ = pc.Close()
	}
	for _, conn := range conns {
		_ = conn.Close()
	}
	if s.hub != nil {
		s.hub.closeAll()
	}
}

func (s *Server) runUDP(ctx context.Context) {
	lc := net.ListenConfig{}
	pc, err := lc.ListenPacket(ctx, "udp", s.udpAddr)
	if err != nil {
		nazalog.Errorf("jt1078 udp listen failed. addr=%s err=%v", s.udpAddr, err)
		return
	}
	s.mu.Lock()
	s.udpConn = pc
	s.mu.Unlock()
	udpDone := make(chan struct{})
	defer close(udpDone)
	defer func() {
		_ = pc.Close()
		s.mu.Lock()
		if s.udpConn == pc {
			s.udpConn = nil
		}
		s.mu.Unlock()
	}()

	go func() {
		select {
		case <-ctx.Done():
			_ = pc.Close()
		case <-udpDone:
		}
	}()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.hub.reapUDP(s.udpIdle)
			}
		}
	}()

	nazalog.Infof("jt1078 udp listen. addr=%s idle=%s", pc.LocalAddr().String(), s.udpIdle)
	buf := make([]byte, udpReadBufSize)
	for {
		if ctx.Err() != nil {
			return
		}
		n, _, err := pc.ReadFrom(buf)
		if n > 0 {
			s.handleDatagram(append([]byte(nil), buf[:n]...))
		}
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			nazalog.Warnf("jt1078 udp read failed: %v", err)
			continue
		}
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	history := make([]byte, 0, readBufSize)
	buf := make([]byte, readBufSize)
	active := make(map[string]chan Packet)
	var lockedPhone int
	defer func() {
		for name := range active {
			s.hub.clearDownlink(name, conn)
			s.hub.releaseTCP(name)
		}
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		n, err := conn.Read(buf)
		if n > 0 {
			history = append(history, buf[:n]...)
			if len(history) > maxHistoryBytes {
				nazalog.Warnf("jt1078 history overflow, dropping conn. remote=%s", conn.RemoteAddr())
				return
			}
			for {
				pkt, remain, decErr := s.readPacket(history, lockedPhone)
				if decErr != nil {
					if errors.Is(decErr, ErrHeaderLengthShort) || errors.Is(decErr, ErrBodyLengthShort) {
						break
					}
					if errors.Is(decErr, ErrUnqualifiedData) {
						idx := findMagic(history[1:])
						if idx < 0 {
							history = history[:0]
							break
						}
						history = history[idx+1:]
						continue
					}
					nazalog.Warnf("jt1078 decode failed: %v", decErr)
					return
				}
				history = remain
				if lockedPhone == 0 && s.phoneLen == 0 && pkt.PhoneLenConfident {
					lockedPhone = pkt.PhoneLen
					s.hub.setPhoneLen(pkt.Sim, pkt.PhoneLen)
				}
				name := pkt.StreamName()
				if !s.allow(name) {
					continue
				}
				ch, ok := active[name]
				if !ok {
					streamCh, acqErr := s.hub.acquire(name, true)
					if acqErr != nil {
						nazalog.Errorf("jt1078 acquire stream failed. stream=%s err=%v", name, acqErr)
						return
					}
					ch = streamCh
					active[name] = ch
					s.hub.bindDownlink(name, conn)
				}
				s.hub.send(name, ch, pkt)
				if len(history) == 0 {
					break
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
					nazalog.Infof("jt1078 conn closed. remote=%s err=%v", conn.RemoteAddr(), err)
				}
			}
			return
		}
	}
}

func (s *Server) handleDatagram(data []byte) {
	remain := data
	for len(remain) > 0 {
		pkt, next, err := s.readPacket(remain, 0)
		if err != nil {
			if errors.Is(err, ErrUnqualifiedData) && len(remain) > 1 {
				idx := findMagic(remain[1:])
				if idx < 0 {
					return
				}
				remain = remain[idx+1:]
				continue
			}
			return
		}
		if s.phoneLen == 0 {
			if locked := s.hub.phoneLenOf(pkt.Sim); locked != 0 && locked != pkt.PhoneLen {
				if again, next2, err2 := decodeWith(remain, locked); err2 == nil {
					pkt, next = again, next2
				}
			} else if locked == 0 && pkt.PhoneLenConfident {
				s.hub.setPhoneLen(pkt.Sim, pkt.PhoneLen)
			}
		}
		remain = next
		if !s.allow(pkt.StreamName()) {
			continue
		}
		ch, acqErr := s.hub.touchUDP(pkt.StreamName())
		if acqErr != nil {
			nazalog.Errorf("jt1078 udp acquire failed. stream=%s err=%v", pkt.StreamName(), acqErr)
			return
		}
		s.hub.send(pkt.StreamName(), ch, pkt)
	}
}

func (s *Server) readPacket(data []byte, locked int) (Packet, []byte, error) {
	switch {
	case s.phoneLen == simBCDLen || s.phoneLen == simBCDLen2019:
		return decodeWith(data, s.phoneLen)
	case locked == simBCDLen || locked == simBCDLen2019:
		return decodeWith(data, locked)
	default:
		return decodeAuto(data)
	}
}

func publish(host customizePubHost, session logic.ICustomizePubSessionContext, name string, ch <-chan Packet) {
	defer host.DelCustomizePubSession(session)

	asm := newFrameAssembler()
	var (
		startOnce sync.Once
		startTs   int64
		lastTs    int64
		sawADTS   bool
	)
	for pkt := range ch {
		if pkt.DataType == DataTypePenetrate {
			continue
		}
		complete, data := asm.push(pkt)
		if !complete {
			continue
		}
		if unsupportedPT(pkt.Flag.PT) {
			nazalog.Warnf("jt1078 reject unsupported pt. stream=%s pt=%s", name, pkt.Flag.PT)
			continue
		}
		pt, ok := avPacketPt(pkt.Flag.PT)
		if !ok || !ptMatchesData(pt, pkt.DataType) {
			nazalog.Warnf("jt1078 reject payload. stream=%s pt=%s data=%s", name, pkt.Flag.PT, pkt.DataType)
			continue
		}
		if pt == base.AvPacketPtAvc || pt == base.AvPacketPtHevc {
			data = ensureAnnexB(data)
		}
		if pt == base.AvPacketPtAac && !sawADTS && isADTS(data) {
			sawADTS = true
			session.WithOption(func(option *base.AvPacketStreamOption) {
				option.AudioFormat = base.AvPacketStreamAudioFormatAdtsAac
			})
		}
		startOnce.Do(func() {
			startTs = int64(pkt.Timestamp)
		})
		ts := int64(pkt.Timestamp) - startTs
		if ts < 0 {
			ts = lastTs
		} else {
			lastTs = ts
		}
		if err := session.FeedAvPacket(base.AvPacket{
			PayloadType: pt,
			Timestamp:   ts,
			Pts:         ts,
			Payload:     data,
		}); err != nil {
			nazalog.Warnf("jt1078 FeedAvPacket failed. stream=%s err=%v", name, err)
		}
	}
}

type framePart struct {
	buf  []byte
	next uint16
}

type frameAssembler struct {
	cur map[DataType]*framePart
}

func newFrameAssembler() *frameAssembler {
	return &frameAssembler{cur: make(map[DataType]*framePart)}
}

// push joins JT1078 subcontracts. A sequence gap discards the partial frame
// instead of concatenating bytes from the next packet.
func (a *frameAssembler) push(pkt Packet) (bool, []byte) {
	switch pkt.SubcontractType {
	case SubcontractTypeAtomic:
		delete(a.cur, pkt.DataType)
		return true, append([]byte(nil), pkt.Body...)
	case SubcontractTypeFirst:
		a.cur[pkt.DataType] = &framePart{
			buf:  append([]byte(nil), pkt.Body...),
			next: pkt.Seq + 1,
		}
		return false, nil
	case SubcontractTypeMiddle, SubcontractTypeLast:
		part := a.cur[pkt.DataType]
		if part == nil || pkt.Seq != part.next || len(part.buf)+len(pkt.Body) > maxFrameBytes {
			delete(a.cur, pkt.DataType)
			return false, nil
		}
		part.buf = append(part.buf, pkt.Body...)
		part.next++
		if pkt.SubcontractType == SubcontractTypeMiddle {
			return false, nil
		}
		out := part.buf
		delete(a.cur, pkt.DataType)
		return true, out
	default:
		nazalog.Warnf("jt1078 unknown subcontract type: %d", pkt.SubcontractType)
		return false, nil
	}
}

func ptMatchesData(pt base.AvPacketPt, data DataType) bool {
	switch pt {
	case base.AvPacketPtAvc, base.AvPacketPtHevc:
		return data.IsVideo()
	case base.AvPacketPtG711A, base.AvPacketPtG711U, base.AvPacketPtAac:
		return data == DataTypeA
	default:
		return false
	}
}

func ensureAnnexB(data []byte) []byte {
	if hasAnnexBStart(data) {
		return data
	}
	out := make([]byte, 4+len(data))
	out[3] = 1
	copy(out[4:], data)
	return out
}

func hasAnnexBStart(data []byte) bool {
	if len(data) >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 0 && data[3] == 1 {
		return true
	}
	return len(data) >= 3 && data[0] == 0 && data[1] == 0 && data[2] == 1
}

func isADTS(data []byte) bool {
	return len(data) >= 7 && data[0] == 0xFF && data[1]&0xF0 == 0xF0
}

// unsupportedPT is a payload type we can parse but will not feed into lal.
func unsupportedPT(pt PTType) bool {
	switch pt {
	case PTG726, PTMP3:
		return true
	default:
		return false
	}
}

func avPacketPt(pt PTType) (base.AvPacketPt, bool) {
	switch pt {
	case PTG711A:
		return base.AvPacketPtG711A, true
	case PTG711U:
		return base.AvPacketPtG711U, true
	case PTAAC:
		return base.AvPacketPtAac, true
	case PTH264:
		return base.AvPacketPtAvc, true
	case PTH265:
		return base.AvPacketPtHevc, true
	default:
		return base.AvPacketPtUnknown, false
	}
}
