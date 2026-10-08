package jt808

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	cmdRegister      = 0x0100
	cmdAuth          = 0x0102
	cmdHeartbeat     = 0x0002
	cmdGeneralReply  = 0x0001
	cmdRegisterReply = 0x8100
	cmdGeneralAck    = 0x8001
	cmdPlay          = 0x9101
	cmdStop          = 0x9102
)

type inbound struct {
	result byte
	body   []byte
}

type waiter struct {
	replyTo uint16
	ch      chan inbound
}

type Terminal struct {
	Key           string        `json:"key"`
	Online        bool          `json:"online"`
	Authenticated bool          `json:"authenticated"`
	JoinedAt      time.Time     `json:"joined_at"`
	LastSeen      time.Time     `json:"last_seen"`
	Province      uint16        `json:"province,omitempty"`
	City          uint16        `json:"city,omitempty"`
	Maker         string        `json:"maker,omitempty"`
	Model         string        `json:"model,omitempty"`
	TerminalID    string        `json:"terminal_id,omitempty"`
	PlateColor    byte          `json:"plate_color,omitempty"`
	Plate         string        `json:"plate,omitempty"`
	AV            *AVProperties `json:"av,omitempty"`
	Uploads       []UploadTask  `json:"uploads,omitempty"`
}
type PlayInput struct {
	Key       string
	Channel   byte
	Transport string
	DataType  byte
	Stream    byte
}
type StopPlayInput struct {
	Key     string
	Channel byte
}

type commandSender interface {
	send(key string, id uint16, body []byte, timeout time.Duration) error
}

type Server struct {
	cfg       Config
	mu        sync.RWMutex
	listener  net.Listener
	sessions  map[string]*session
	conns     map[net.Conn]struct{}
	terminals map[string]*Terminal
	grants    map[string]int
	controls  sync.Map // per-terminal command serialization
	sender    commandSender
	stopped   bool
	wg        sync.WaitGroup
}

type session struct {
	conn    net.Conn
	sim     string
	version byte
	mu      sync.Mutex // protects write and sequence
	seq     uint16
	pending map[uint16]waiter
	avMu    sync.Mutex // serializes queries whose responses have no request sequence
	frags   map[uint16]*fragGroup
}

type fragGroup struct {
	total uint16
	seq0  uint16
	parts [][]byte
	got   int
	size  int
}

func NewServer(cfg Config) (*Server, error) {
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, sessions: make(map[string]*session), conns: make(map[net.Conn]struct{}), terminals: make(map[string]*Terminal), grants: map[string]int{}}
	s.sender = s
	return s, nil
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.ListenAddr())
	if err != nil {
		return fmt.Errorf("jt808 listen: %w", err)
	}
	s.mu.Lock()
	if s.listener != nil || s.stopped {
		s.mu.Unlock()
		ln.Close()
		return errors.New("jt808 already started or stopped")
	}
	s.listener = ln
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			if s.stopped {
				s.mu.Unlock()
				conn.Close()
				return
			}
			s.conns[conn] = struct{}{}
			s.wg.Add(1)
			s.mu.Unlock()
			go func() { defer s.wg.Done(); s.handle(conn) }()
		}
	}()
	slog.Info("JT808 signaling listening", "addr", ln.Addr())
	return nil
}
func (s *Server) Stop() {
	s.mu.Lock()
	s.stopped = true
	ln := s.listener
	s.listener = nil
	connections := make([]net.Conn, 0, len(s.conns))
	for conn := range s.conns {
		connections = append(connections, conn)
	}
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	for _, c := range connections {
		_ = c.Close()
	}
	s.wg.Wait()
}
func (s *Server) ListTerminals() []Terminal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Terminal, 0, len(s.terminals))
	for _, t := range s.terminals {
		cp := *t
		if t.AV != nil {
			av := *t.AV
			cp.AV = &av
		}
		if len(t.Uploads) > 0 {
			cp.Uploads = append([]UploadTask(nil), t.Uploads...)
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
func validSIM(key string) (string, bool) {
	if len(key) == 0 || len(key) > 20 {
		return "", false
	}
	for _, r := range key {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return key, true
}
func StreamID(key string, channel byte) string {
	sim := strings.TrimLeft(key, "0")
	if sim == "" {
		sim = "0"
	}
	return fmt.Sprintf("%s_%d", sim, channel)
}
func (s *Server) Play(in PlayInput) (string, error) {
	unlock := s.lockControl(in.Key)
	defer unlock()
	if _, ok := validSIM(in.Key); !ok {
		return "", fmt.Errorf("%w: invalid SIM", ErrBadInput)
	}
	if in.Channel == 0 {
		in.Channel = 1
	}
	transport := strings.ToLower(strings.TrimSpace(in.Transport))
	if transport == "" {
		transport = s.cfg.Transport
	}
	if transport != TransportTCP && transport != TransportUDP {
		return "", fmt.Errorf("%w: transport must be tcp or udp", ErrBadInput)
	}
	if in.DataType > 5 || in.Stream > 1 {
		return "", fmt.Errorf("%w: invalid data or stream type", ErrBadInput)
	}
	ip := []byte(s.cfg.MediaIP)
	body := make([]byte, 1+len(ip)+7)
	body[0] = byte(len(ip))
	copy(body[1:], ip)
	pos := 1 + len(ip)
	if transport == TransportTCP {
		binary.BigEndian.PutUint16(body[pos:], uint16(s.cfg.MediaTCPPort))
	} else {
		binary.BigEndian.PutUint16(body[pos+2:], uint16(s.cfg.MediaUDPPort))
	}
	body[pos+4] = in.Channel
	body[pos+5] = in.DataType
	body[pos+6] = in.Stream
	id := StreamID(in.Key, in.Channel)
	previous := s.grant(grantLive, id)
	if err := s.sender.send(in.Key, cmdPlay, body, s.cfg.TimeoutDuration()); err != nil {
		s.restoreGrant(grantLive, id, previous)
		return "", fmt.Errorf("send 0x9101: %w", err)
	}
	return id, nil
}
func (s *Server) StopPlay(in StopPlayInput) error {
	return s.LiveControl(LiveControlInput{Key: in.Key, Channel: in.Channel})
}
func (s *Server) send(key string, id uint16, body []byte, timeout time.Duration) error {
	_, in, err := s.sendWait(key, id, body, cmdGeneralReply, timeout)
	if err != nil {
		return err
	}
	if in.result != 0 {
		return fmt.Errorf("terminal rejected command: result=%d", in.result)
	}
	return nil
}

func (s *Server) sendSeq(key string, id uint16, body []byte, timeout time.Duration) (uint16, error) {
	seq, in, err := s.sendWait(key, id, body, cmdGeneralReply, timeout)
	if err != nil {
		return 0, err
	}
	if in.result != 0 {
		return 0, fmt.Errorf("terminal rejected command: result=%d", in.result)
	}
	return seq, nil
}

func (s *Server) sendBody(key string, id uint16, body []byte, replyTo uint16, timeout time.Duration) ([]byte, error) {
	_, in, err := s.sendWait(key, id, body, replyTo, timeout)
	if err != nil {
		return nil, err
	}
	return in.body, nil
}

func (s *Server) sendWait(key string, id uint16, body []byte, replyTo uint16, timeout time.Duration) (uint16, inbound, error) {
	s.mu.RLock()
	ss := s.sessions[key]
	t := s.terminals[key]
	active := !s.stopped && ss != nil && t != nil && t.Authenticated && t.Online
	s.mu.RUnlock()
	if !active {
		return 0, inbound{}, fmt.Errorf("terminal %s offline or unauthenticated", key)
	}
	if replyTo == cmdAVReport {
		ss.avMu.Lock()
		defer ss.avMu.Unlock()
	}
	ss.mu.Lock()
	if ss.pending == nil {
		ss.mu.Unlock()
		return 0, inbound{}, io.EOF
	}
	ss.seq++
	seq := ss.seq
	response := make(chan inbound, 1)
	ss.pending[seq] = waiter{replyTo: replyTo, ch: response}
	p, err := encodePacket(packet{id: id, sim: key, seq: seq, version: ss.version, body: body})
	if err == nil {
		_ = ss.conn.SetWriteDeadline(time.Now().Add(timeout))
		_, err = ss.conn.Write(p)
		_ = ss.conn.SetWriteDeadline(time.Time{})
	}
	if err != nil {
		delete(ss.pending, seq)
	}
	ss.mu.Unlock()
	if err != nil {
		return 0, inbound{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case in, ok := <-response:
		if !ok {
			return 0, inbound{}, io.EOF
		}
		return seq, in, nil
	case <-ctx.Done():
		ss.mu.Lock()
		delete(ss.pending, seq)
		ss.mu.Unlock()
		return 0, inbound{}, ctx.Err()
	}
}
func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.conns[conn] = struct{}{}
	s.mu.Unlock()
	ss := &session{conn: conn, pending: make(map[uint16]waiter), frags: make(map[uint16]*fragGroup)}
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		if cur := s.sessions[ss.sim]; cur == ss {
			delete(s.sessions, ss.sim)
			s.clearGrantsLocked(ss.sim)
			if t := s.terminals[ss.sim]; t != nil {
				t.Online = false
				t.Authenticated = false
			}
		}
		s.mu.Unlock()
		ss.mu.Lock()
		for _, w := range ss.pending {
			close(w.ch)
		}
		ss.pending = nil
		ss.mu.Unlock()
	}()
	buf := make([]byte, 2048)
	frame := make([]byte, 0, 2048)
	started := false
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		n, err := conn.Read(buf)
		for _, b := range buf[:n] {
			if b == 0x7e {
				if started && len(frame) > 1 {
					frame = append(frame, 0x7e)
					if p, e := decodePacket(frame); e == nil {
						s.ingest(ss, p)
					}
				}
				frame = frame[:0]
				frame = append(frame, 0x7e)
				started = true
			} else if started {
				if len(frame) >= 8192 {
					started = false
					frame = frame[:0]
				} else {
					frame = append(frame, b)
				}
			}
		}
		if err != nil {
			return
		}
	}
}
func (s *Server) ingest(ss *session, p packet) {
	if p.total > 1 {
		_ = ss.writeReply(cmdGeneralAck, ackBody(p.seq, p.id, 0))
		full, ok := ss.pushFrag(p)
		if !ok {
			return
		}
		p = full
	}
	s.handlePacket(ss, p)
}

func (ss *session) pushFrag(p packet) (packet, bool) {
	if p.index == 0 || p.index > p.total || p.total > 64 {
		return packet{}, false
	}
	g := ss.frags[p.id]
	if g == nil || g.total != p.total {
		g = &fragGroup{total: p.total, seq0: p.seq, parts: make([][]byte, p.total)}
		ss.frags[p.id] = g
	}
	if p.index == 1 {
		g.seq0 = p.seq
	}
	i := int(p.index - 1)
	if g.size+len(p.body) > 1<<20 {
		delete(ss.frags, p.id)
		return packet{}, false
	}
	if g.parts[i] == nil {
		g.got++
	} else {
		g.size -= len(g.parts[i])
	}
	cp := append([]byte(nil), p.body...)
	g.parts[i] = cp
	g.size += len(cp)
	if g.got < int(g.total) {
		return packet{}, false
	}
	var body []byte
	for _, part := range g.parts {
		body = append(body, part...)
	}
	delete(ss.frags, p.id)
	p.body = body
	p.seq = g.seq0
	p.total = 0
	p.index = 0
	p.noAck = true
	return p, true
}

func ackBody(seq, id uint16, result byte) []byte {
	body := make([]byte, 5)
	binary.BigEndian.PutUint16(body, seq)
	binary.BigEndian.PutUint16(body[2:], id)
	body[4] = result
	return body
}

func (s *Server) handlePacket(ss *session, p packet) {
	if _, ok := validSIM(p.sim); !ok {
		return
	}
	if ss.sim == "" {
		s.mu.Lock()
		if s.stopped || s.sessions[p.sim] != nil {
			s.mu.Unlock()
			_ = ss.conn.Close()
			return
		}
		ss.sim = p.sim
		ss.version = p.version
		s.sessions[p.sim] = ss
		s.terminals[p.sim] = &Terminal{Key: p.sim, Online: true, JoinedAt: time.Now(), LastSeen: time.Now()}
		s.mu.Unlock()
	} else if p.sim != ss.sim {
		_ = ss.conn.Close()
		return
	}
	s.mu.Lock()
	if t := s.terminals[p.sim]; t != nil {
		t.LastSeen = time.Now()
	}
	s.mu.Unlock()
	switch p.id {
	case cmdRegister:
		// Only acknowledge valid unfragmented registration bodies. The standard
		// minimum for 2011 is 25 bytes; 2013/2019 are longer.
		if len(p.body) < 25 {
			return
		}
		s.mu.Lock()
		if t := s.terminals[p.sim]; t != nil {
			applyRegister(t, p.body, p.version)
		}
		s.mu.Unlock()
		body := make([]byte, 3+len(s.cfg.AuthCode))
		binary.BigEndian.PutUint16(body[:2], p.seq)
		copy(body[3:], s.cfg.AuthCode)
		_ = ss.writeReply(cmdRegisterReply, body)
	case cmdAuth:
		auth := p.body
		if p.version != 0 {
			if len(auth) < 1 {
				return
			}
			size := int(auth[0])
			if size > len(auth)-1 {
				return
			}
			auth = auth[1 : 1+size]
		}
		valid := subtle.ConstantTimeCompare(auth, []byte(s.cfg.AuthCode)) == 1
		result := byte(1)
		if valid {
			result = 0
		}
		if err := ss.writeReply(cmdGeneralAck, ackBody(p.seq, p.id, result)); err == nil {
			s.mu.Lock()
			if t := s.terminals[p.sim]; t != nil {
				t.Authenticated = valid
				if !valid {
					s.clearGrantsLocked(p.sim)
				}
			}
			s.mu.Unlock()
		}
	case cmdLogout:
		_ = ss.writeReply(cmdGeneralAck, ackBody(p.seq, p.id, 0))
		s.mu.Lock()
		if t := s.terminals[p.sim]; t != nil {
			t.Online = false
			t.Authenticated = false
		}
		s.mu.Unlock()
		_ = ss.conn.Close()
	case cmdGeneralReply:
		if len(p.body) != 5 {
			return
		}
		seq := binary.BigEndian.Uint16(p.body[:2])
		s.deliver(ss, seq, cmdGeneralReply, inbound{result: p.body[4], body: append([]byte(nil), p.body...)})
	case cmdAVReport:
		if _, err := parseAV(p.body); err != nil {
			return
		}
		if !p.noAck {
			_ = ss.writeReply(cmdGeneralAck, ackBody(p.seq, p.id, 0))
		}
		ss.mu.Lock()
		for seq, w := range ss.pending {
			if w.replyTo == cmdAVReport {
				delete(ss.pending, seq)
				w.ch <- inbound{body: append([]byte(nil), p.body...)}
				break
			}
		}
		ss.mu.Unlock()
	case cmdResourceList, cmdUploadDone:
		if p.id == cmdUploadDone {
			s.noteUpload(ss.sim, p.body)
		}
		if len(p.body) >= 2 {
			seq := binary.BigEndian.Uint16(p.body[:2])
			s.deliver(ss, seq, p.id, inbound{body: append([]byte(nil), p.body...)})
		}
		if !p.noAck {
			_ = ss.writeReply(cmdGeneralAck, ackBody(p.seq, p.id, 0))
		}
	case cmdHeartbeat:
		_ = ss.writeReply(cmdGeneralAck, ackBody(p.seq, p.id, 0))
	default:
		if !p.noAck {
			_ = ss.writeReply(cmdGeneralAck, ackBody(p.seq, p.id, 0))
		}
	}
}

func (s *Server) deliver(ss *session, seq, replyTo uint16, in inbound) {
	ss.mu.Lock()
	w, ok := ss.pending[seq]
	if ok && w.replyTo == replyTo {
		delete(ss.pending, seq)
		w.ch <- in
	}
	ss.mu.Unlock()
}

func (s *Server) noteUpload(sim string, body []byte) {
	if len(body) < 3 {
		return
	}
	seq := binary.BigEndian.Uint16(body[:2])
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.terminals[sim]
	if t == nil {
		return
	}
	for i := range t.Uploads {
		if t.Uploads[i].Serial == seq {
			t.Uploads[i].Done = true
			t.Uploads[i].Result = body[2]
			t.Uploads[i].At = time.Now()
			return
		}
	}
}
func (ss *session) writeReply(id uint16, body []byte) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.seq++
	p, err := encodePacket(packet{id: id, sim: ss.sim, version: ss.version, seq: ss.seq, body: body})
	if err != nil {
		return err
	}
	_ = ss.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_, err = ss.conn.Write(p)
	_ = ss.conn.SetWriteDeadline(time.Time{})
	return err
}
