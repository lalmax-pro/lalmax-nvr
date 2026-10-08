package jtsim

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

const (
	msgRegister      = 0x0100
	msgAuth          = 0x0102
	msgGeneralReply  = 0x0001
	msgRegisterReply = 0x8100
	msgPlatformAck   = 0x8001
	msgPlay          = 0x9101
	msgStop          = 0x9102
)

// message is one unfragmented JT/T 808 frame, 2013 (Version == 0) or 2019.
type message struct {
	id      uint16
	sim     string
	seq     uint16
	version byte
	body    []byte
}

func encodeMessage(m message) ([]byte, error) {
	if len(m.body) > 1023 {
		return nil, fmt.Errorf("jt808 body too long: %d", len(m.body))
	}
	phoneLen, start := 6, 4
	attr := uint16(len(m.body))
	if m.version != 0 {
		phoneLen, start = 10, 5
		attr |= 0x4000
	}
	if !digits(m.sim) || len(m.sim) > phoneLen*2 {
		return nil, errors.New("invalid jt808 sim")
	}
	buf := make([]byte, start+phoneLen+2+len(m.body)+1)
	binary.BigEndian.PutUint16(buf[:2], m.id)
	binary.BigEndian.PutUint16(buf[2:4], attr)
	if m.version != 0 {
		buf[4] = m.version
	}
	sim := strings.Repeat("0", phoneLen*2-len(m.sim)) + m.sim
	for i := 0; i < phoneLen; i++ {
		buf[start+i] = ((sim[i*2] - '0') << 4) | (sim[i*2+1] - '0')
	}
	binary.BigEndian.PutUint16(buf[start+phoneLen:start+phoneLen+2], m.seq)
	copy(buf[start+phoneLen+2:], m.body)
	for _, b := range buf[:len(buf)-1] {
		buf[len(buf)-1] ^= b
	}
	out := make([]byte, 0, len(buf)+2)
	out = append(out, 0x7e)
	for _, b := range buf {
		switch b {
		case 0x7e:
			out = append(out, 0x7d, 0x02)
		case 0x7d:
			out = append(out, 0x7d, 0x01)
		default:
			out = append(out, b)
		}
	}
	return append(out, 0x7e), nil
}

func decodeMessage(frame []byte) (message, error) {
	var m message
	if len(frame) < 2 || frame[0] != 0x7e || frame[len(frame)-1] != 0x7e {
		return m, errors.New("missing jt808 delimiters")
	}
	buf := make([]byte, 0, len(frame)-2)
	for i := 1; i < len(frame)-1; i++ {
		if frame[i] == 0x7d {
			i++
			if i >= len(frame)-1 {
				return m, errors.New("truncated jt808 escape")
			}
			switch frame[i] {
			case 0x01:
				buf = append(buf, 0x7d)
			case 0x02:
				buf = append(buf, 0x7e)
			default:
				return m, errors.New("invalid jt808 escape")
			}
		} else {
			buf = append(buf, frame[i])
		}
	}
	if len(buf) < 13 {
		return m, errors.New("jt808 frame too short")
	}
	var check byte
	for _, b := range buf {
		check ^= b
	}
	if check != 0 {
		return m, errors.New("jt808 checksum mismatch")
	}
	buf = buf[:len(buf)-1]
	m.id = binary.BigEndian.Uint16(buf[:2])
	attr := binary.BigEndian.Uint16(buf[2:4])
	if attr&0x1c00 != 0 {
		return m, errors.New("encrypted jt808 frame")
	}
	if attr&0x2000 != 0 {
		return m, errors.New("fragmented jt808 frame")
	}
	start, phoneLen := 4, 6
	if attr&0x4000 != 0 {
		start, phoneLen = 5, 10
		if len(buf) > 4 {
			m.version = buf[4]
		}
	}
	if len(buf) < start+phoneLen+2 {
		return m, errors.New("jt808 header too short")
	}
	var digits strings.Builder
	for _, b := range buf[start : start+phoneLen] {
		if b>>4 > 9 || b&0x0f > 9 {
			return m, errors.New("invalid sim bcd")
		}
		digits.WriteByte('0' + (b >> 4))
		digits.WriteByte('0' + (b & 0x0f))
	}
	m.sim = strings.TrimLeft(digits.String(), "0")
	if m.sim == "" {
		m.sim = "0"
	}
	m.seq = binary.BigEndian.Uint16(buf[start+phoneLen : start+phoneLen+2])
	m.body = append([]byte(nil), buf[start+phoneLen+2:]...)
	if len(m.body) != int(attr&0x3ff) {
		return message{}, errors.New("jt808 body length mismatch")
	}
	return m, nil
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
