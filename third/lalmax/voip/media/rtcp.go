package media

import (
	"fmt"
	"sort"
	"time"

	"github.com/pion/rtcp"
)

type receptionStats struct {
	started                                      bool
	badSequence                                  uint16
	badSequenceSet                               bool
	base, highest                                uint32
	received, previousExpected, previousReceived uint32
	seen                                         map[uint32]struct{}
	jitter                                       float64
	lastArrival                                  time.Time
	lastTimestamp                                uint32
	lastSR                                       uint32
	lastSRAt                                     time.Time
}

// Track extended sequence numbers, including wrap, reordered and duplicate RTP.
// Extremely large jumps are ignored rather than reporting billions of losses.
func (s *receptionStats) receive(seq uint16, timestamp uint32, clock int, now time.Time) (bool, bool) {
	ext := uint32(seq)
	if !s.started {
		s.started, s.base, s.highest = true, ext, ext
		s.seen = make(map[uint32]struct{})
	} else {
		delta := int32(int16(seq - uint16(s.highest)))
		if delta > 3000 || delta < -8192 {
			if s.badSequenceSet && seq == s.badSequence {
				lastSR, lastSRAt := s.lastSR, s.lastSRAt
				*s = receptionStats{lastSR: lastSR, lastSRAt: lastSRAt}
				return s.receive(seq, timestamp, clock, now)
			}
			s.badSequence, s.badSequenceSet = seq+1, true
			return false, false
		}
		if delta < 0 && uint32(-delta) > s.highest {
			return false, false
		}
		ext = uint32(int64(s.highest) + int64(delta))
	}
	if _, duplicate := s.seen[ext]; duplicate {
		return false, false
	}
	gap := ext > s.highest+1
	if ext > s.highest {
		s.highest = ext
	}
	if ext < s.base {
		return false, false
	}
	s.seen[ext] = struct{}{}
	// Bound memory while retaining a window for reordering and duplicate checks.
	if len(s.seen) > 8192 {
		for value := range s.seen {
			if value+8192 < s.highest {
				delete(s.seen, value)
			}
		}
	}
	s.received++
	if clock > 0 && !s.lastArrival.IsZero() {
		d := now.Sub(s.lastArrival).Seconds()*float64(clock) - float64(int32(timestamp-s.lastTimestamp))
		if d < 0 {
			d = -d
		}
		s.jitter += (d - s.jitter) / 16
	}
	s.lastArrival, s.lastTimestamp = now, timestamp
	return gap, true
}

func (s *receptionStats) report(ssrc uint32, now time.Time) rtcp.ReceptionReport {
	expected := s.highest - s.base + 1
	if !s.started {
		expected = 0
	}
	lost := int64(expected) - int64(s.received)
	if lost < 0 {
		lost = 0
	}
	if lost > 0x7fffff {
		lost = 0x7fffff
	}
	interval := expected - s.previousExpected
	intervalLost := int64(interval) - int64(s.received-s.previousReceived)
	var fraction uint8
	if interval > 0 && intervalLost > 0 {
		fraction = uint8(intervalLost * 256 / int64(interval))
	}
	s.previousExpected, s.previousReceived = expected, s.received
	var delay uint32
	if !s.lastSRAt.IsZero() {
		delay = uint32(now.Sub(s.lastSRAt).Seconds() * 65536)
	}
	return rtcp.ReceptionReport{SSRC: ssrc, FractionLost: fraction, TotalLost: uint32(lost), LastSequenceNumber: s.highest,
		Jitter: uint32(s.jitter), LastSenderReport: s.lastSR, Delay: delay}
}

func (s *RtpSession) runReceiveRTCP() {
	defer s.wg.Done()
	buf := make([]byte, 2048)
	for {
		n, addr, err := s.rtcpConn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		// SRTCP is authenticated by handleRTCPPacket. Learn symmetric RTCP
		// after receiving a valid packet, keeping RTP and RTCP peers separate.
		if !s.handleRTCPPacket(buf[:n]) {
			continue
		}
		s.mutex.Lock()
		s.rtcpRemote = addr
		s.mutex.Unlock()
	}
}

func (s *RtpSession) sendPLI(ssrc uint32) {
	s.mutex.Lock()
	if !s.videoPLI || time.Since(s.lastPLI) < time.Second {
		s.mutex.Unlock()
		return
	}
	s.lastPLI = time.Now()
	id := s.receiverSSRC
	s.mutex.Unlock()
	s.sendRTCP(&rtcp.PictureLossIndication{SenderSSRC: id, MediaSSRC: ssrc})
}

// Send compound RR + SDES, with optional negotiated PLI. Bundle sessions keep
// separate report blocks for each source SSRC rather than mixing audio/video.
func (s *RtpSession) sendRTCP(feedback rtcp.Packet) {
	s.mutex.Lock()
	addr, conn := s.rtcpRemote, s.rtcpConn
	if s.rtcpMux || conn == nil {
		addr, conn = s.remoteAddr, s.conn
	}
	if addr == nil && !s.rtcpMux && conn != nil && s.remoteAddr != nil && s.remoteAddr.Port < 65535 {
		peer := *s.remoteAddr
		peer.Port++
		addr = &peer
	}
	if addr == nil || conn == nil {
		s.mutex.Unlock()
		return
	}
	ids := make([]uint32, 0, len(s.reports))
	for id, stats := range s.reports {
		if stats.started {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > 31 {
		ids = ids[:31]
	}
	reports := make([]rtcp.ReceptionReport, 0, len(ids))
	for _, id := range ids {
		reports = append(reports, s.reports[id].report(id, time.Now()))
	}
	packets := []rtcp.Packet{
		&rtcp.ReceiverReport{SSRC: s.receiverSSRC, Reports: reports},
		&rtcp.SourceDescription{Chunks: []rtcp.SourceDescriptionChunk{{Source: s.receiverSSRC,
			Items: []rtcp.SourceDescriptionItem{{Type: rtcp.SDESCNAME, Text: fmt.Sprintf("lalmax-%08x", s.receiverSSRC)}}}}},
	}
	if s.sentPackets > 0 {
		now := time.Now()
		sec := uint64(now.Unix() + 2208988800)
		frac := uint64(now.Nanosecond()) * (1 << 32) / 1000000000
		timestamp := s.lastSentTimestamp
		if s.payload != nil {
			timestamp += uint32(now.Sub(s.lastSentAt).Seconds() * float64(s.payload.ClockRate))
		}
		packets[0] = &rtcp.SenderReport{SSRC: s.sendSSRC, NTPTime: sec<<32 | frac, RTPTime: timestamp, PacketCount: s.sentPackets, OctetCount: s.sentOctets, Reports: reports}
	}
	s.mutex.Unlock()
	if feedback != nil {
		packets = append(packets, feedback)
	}
	data, err := rtcp.Marshal(packets)
	if err != nil {
		return
	}
	if ctx, secure := s.getSrtpContext(); secure {
		if ctx == nil {
			return
		}
		data, err = ctx.EncryptRTCP(data)
		if err != nil {
			return
		}
	}
	if _, err := conn.WriteToUDP(data, addr); err != nil {
		s.log.Debugf("send RTCP failed: %v", err)
	}
}
