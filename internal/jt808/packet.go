package jt808

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

const maxBody = 1023

// packet is one unfragmented JT/T 808 (2013 or 2019) message.
type packet struct {
	id      uint16
	sim     string
	seq     uint16
	body    []byte
	version byte // zero: 2013 header, nonzero: 2019 header
	total   uint16
	index   uint16
	noAck   bool
}

func decodePacket(frame []byte) (packet, error) {
	var p packet
	if len(frame) < 2 || frame[0] != 0x7e || frame[len(frame)-1] != 0x7e {
		return p, errors.New("missing JT808 delimiters")
	}
	buf := make([]byte, 0, len(frame)-2)
	for i := 1; i < len(frame)-1; i++ {
		if frame[i] == 0x7d {
			i++
			if i >= len(frame)-1 {
				return p, errors.New("truncated escape")
			}
			switch frame[i] {
			case 1:
				buf = append(buf, 0x7d)
			case 2:
				buf = append(buf, 0x7e)
			default:
				return p, errors.New("invalid escape")
			}
		} else {
			buf = append(buf, frame[i])
		}
	}
	if len(buf) < 13 {
		return p, errors.New("JT808 frame too short")
	}
	var check byte
	for _, b := range buf {
		check ^= b
	}
	if check != 0 {
		return p, errors.New("JT808 checksum mismatch")
	}
	buf = buf[:len(buf)-1]
	p.id = binary.BigEndian.Uint16(buf[:2])
	attr := binary.BigEndian.Uint16(buf[2:4])
	if attr&0x1c00 != 0 {
		return p, errors.New("encrypted JT808 frame not supported")
	}
	start, phoneLen := 4, 6
	if attr&0x4000 != 0 {
		start, phoneLen = 5, 10
		if len(buf) > 4 {
			p.version = buf[4]
		}
	}
	headerEnd := start + phoneLen + 2
	if attr&0x2000 != 0 {
		headerEnd += 4
	}
	if len(buf) < headerEnd {
		return p, errors.New("JT808 header too short")
	}
	var digits strings.Builder
	for _, b := range buf[start : start+phoneLen] {
		if b>>4 > 9 || b&15 > 9 {
			return p, errors.New("invalid SIM BCD")
		}
		digits.WriteByte('0' + (b >> 4))
		digits.WriteByte('0' + (b & 15))
	}
	p.sim = strings.TrimLeft(digits.String(), "0")
	if p.sim == "" {
		p.sim = "0"
	}
	p.seq = binary.BigEndian.Uint16(buf[start+phoneLen : start+phoneLen+2])
	bodyAt := start + phoneLen + 2
	if attr&0x2000 != 0 {
		p.total = binary.BigEndian.Uint16(buf[bodyAt : bodyAt+2])
		p.index = binary.BigEndian.Uint16(buf[bodyAt+2 : bodyAt+4])
		bodyAt += 4
	}
	p.body = buf[bodyAt:]
	if len(p.body) != int(attr&0x3ff) {
		return packet{}, errors.New("JT808 body length mismatch")
	}
	return p, nil
}

func encodePacket(p packet) ([]byte, error) {
	if len(p.body) > maxBody {
		return nil, fmt.Errorf("JT808 body too long: %d", len(p.body))
	}
	phoneLen, start := 6, 4
	attr := uint16(len(p.body))
	sub := 0
	if p.total > 1 {
		sub = 4
		attr |= 0x2000
	}
	if p.version != 0 {
		phoneLen, start = 10, 5
		attr |= 0x4000
	}
	if _, ok := validSIM(p.sim); !ok || len(p.sim) > phoneLen*2 {
		return nil, errors.New("invalid JT808 SIM")
	}
	buf := make([]byte, start+phoneLen+2+sub+len(p.body)+1)
	binary.BigEndian.PutUint16(buf[:2], p.id)
	binary.BigEndian.PutUint16(buf[2:4], attr)
	if p.version != 0 {
		buf[4] = p.version
	}
	sim := strings.Repeat("0", phoneLen*2-len(p.sim)) + p.sim
	for i := 0; i < phoneLen; i++ {
		buf[start+i] = ((sim[i*2] - '0') << 4) | (sim[i*2+1] - '0')
	}
	binary.BigEndian.PutUint16(buf[start+phoneLen:start+phoneLen+2], p.seq)
	bodyAt := start + phoneLen + 2
	if sub > 0 {
		binary.BigEndian.PutUint16(buf[bodyAt:bodyAt+2], p.total)
		binary.BigEndian.PutUint16(buf[bodyAt+2:bodyAt+4], p.index)
		bodyAt += 4
	}
	copy(buf[bodyAt:], p.body)
	for _, b := range buf[:len(buf)-1] {
		buf[len(buf)-1] ^= b
	}
	out := make([]byte, 0, len(buf)+2)
	out = append(out, 0x7e)
	for _, b := range buf {
		switch b {
		case 0x7e:
			out = append(out, 0x7d, 2)
		case 0x7d:
			out = append(out, 0x7d, 1)
		default:
			out = append(out, b)
		}
	}
	return append(out, 0x7e), nil
}
