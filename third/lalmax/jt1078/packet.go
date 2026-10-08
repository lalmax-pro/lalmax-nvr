package jt1078

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

const (
	magicID        = "01cd"
	simBCDLen      = 6  // JT/T 1078-2016 终端手机号
	simBCDLen2019  = 10 // JT/T 808-2019 / 1078 终端手机号
	maxHeaderProbe = 48
)

var magicBytes = []byte{0x30, 0x31, 0x63, 0x64}

var (
	ErrUnqualifiedData   = errors.New("unqualified data")
	ErrHeaderLengthShort = errors.New("header length too short")
	ErrBodyLengthShort   = errors.New("body length too short")
)

type PTType uint8

const (
	PTG711A PTType = 6
	PTG711U PTType = 7
	PTG726  PTType = 8
	PTAAC   PTType = 19
	PTMP3   PTType = 25
	PTH264  PTType = 98
	PTH265  PTType = 99
)

func (p PTType) String() string {
	switch p {
	case PTG711A:
		return "G711A"
	case PTG711U:
		return "G711U"
	case PTG726:
		return "G726"
	case PTAAC:
		return "AAC"
	case PTMP3:
		return "MP3"
	case PTH264:
		return "H264"
	case PTH265:
		return "H265"
	default:
		return fmt.Sprintf("unknown:%d", uint8(p))
	}
}

type DataType uint8

const (
	DataTypeI DataType = iota
	DataTypeP
	DataTypeB
	DataTypeA
	DataTypePenetrate
)

func (d DataType) String() string {
	switch d {
	case DataTypeI:
		return "video-I"
	case DataTypeP:
		return "video-P"
	case DataTypeB:
		return "video-B"
	case DataTypeA:
		return "audio"
	case DataTypePenetrate:
		return "penetrate"
	default:
		return fmt.Sprintf("unknown:%d", uint8(d))
	}
}

func (d DataType) IsVideo() bool {
	return d == DataTypeI || d == DataTypeP || d == DataTypeB
}

type SubcontractType uint8

const (
	SubcontractTypeAtomic SubcontractType = iota
	SubcontractTypeFirst
	SubcontractTypeLast
	SubcontractTypeMiddle
)

func (s SubcontractType) String() string {
	switch s {
	case SubcontractTypeAtomic:
		return "atomic"
	case SubcontractTypeFirst:
		return "first"
	case SubcontractTypeLast:
		return "last"
	case SubcontractTypeMiddle:
		return "middle"
	default:
		return fmt.Sprintf("unknown:%d", uint8(s))
	}
}

type Flag struct {
	V  uint8
	P  uint8
	X  uint8
	CC uint8
	M  uint8
	PT PTType
}

type Packet struct {
	ID              string
	Flag            Flag
	Seq             uint16
	Sim             string
	LogicChannel    uint8
	DataType        DataType
	SubcontractType SubcontractType
	Timestamp       uint64
	LastIFrameMs    uint16
	LastFrameMs     uint16
	DataBodyLen     uint16
	Body            []byte
	// PhoneLen is 6 (2016) or 10 (2019). PhoneLenConfident is set by auto-detect
	// when one header length is a clear fit and the other is not.
	PhoneLen          int
	PhoneLenConfident bool

	headEnd int
}

func StreamName(sim string, channel uint8) string {
	return fmt.Sprintf("%s_%d", sim, channel)
}

// Decode parses one JT1078 frame. Phone number length is detected: 6 bytes for
// 2016, 10 bytes for 2019. A single exact-sized buffer can match both layouts,
// so callers that already know the terminal version should use decodeWith.
func Decode(data []byte) (pkt Packet, remain []byte, err error) {
	return decodeAuto(data)
}

func decodeAuto(data []byte) (Packet, []byte, error) {
	a := consider(data, simBCDLen)
	b := consider(data, simBCDLen2019)
	if a.score <= 1 && b.score <= 1 && len(data) >= maxHeaderProbe {
		return Packet{}, data, fmt.Errorf("%w: no jt1078 header", ErrUnqualifiedData)
	}
	pick, other := a, b
	if b.score > a.score || (b.score == a.score && b.extra > a.extra) {
		pick, other = b, a
	}
	if pick.err == nil {
		gap := (pick.score-other.score)*10 + (pick.extra - other.extra)
		pick.pkt.PhoneLenConfident = pick.score >= 6 && gap >= 3
	}
	return pick.pkt, pick.remain, pick.err
}

type considered struct {
	pkt    Packet
	remain []byte
	err    error
	score  int
	extra  int
}

func consider(data []byte, phoneLen int) considered {
	pkt, remain, err := decodeWith(data, phoneLen)
	c := considered{pkt: pkt, remain: remain, err: err, score: decodeScore(remain, err)}
	if err == nil {
		c.extra = tieBreak(pkt) + payloadBias(pkt)
	}
	return c
}

// payloadBias rewards a payload type that matches the data-type nibble.
// A 2016 misread of a 2019 header often keeps the same PT byte but lands the
// data-type nibble inside the phone number, so the two disagree.
func payloadBias(pkt Packet) int {
	switch pkt.Flag.PT {
	case PTH264, PTH265:
		if pkt.DataType.IsVideo() {
			return 4
		}
		return -4
	case PTG711A, PTG711U, PTG726, PTAAC, PTMP3:
		if pkt.DataType == DataTypeA {
			return 4
		}
		return -4
	default:
		return 0
	}
}

func decodeScore(remain []byte, err error) int {
	if err == nil {
		if len(remain) == 0 || bytes.HasPrefix(remain, magicBytes) {
			return 6
		}
		return 3
	}
	if errors.Is(err, ErrHeaderLengthShort) {
		return 1
	}
	if errors.Is(err, ErrBodyLengthShort) {
		return 2
	}
	return 0
}

func tieBreak(pkt Packet) int {
	n := 0
	if pkt.LogicChannel >= 1 && pkt.LogicChannel <= 37 {
		n += 2
	}
	switch pkt.Flag.PT {
	case PTG711A, PTG711U, PTG726, PTAAC, PTMP3, PTH264, PTH265:
		n += 1
	}
	if pkt.Flag.V == 2 {
		n += 1
	}
	return n
}

func decodeWith(data []byte, phoneLen int) (Packet, []byte, error) {
	pkt, err := decodeHead(data, phoneLen)
	if err != nil {
		return Packet{}, data, err
	}
	body := data[pkt.headEnd:]
	if len(body) < int(pkt.DataBodyLen) {
		return Packet{}, data, ErrBodyLengthShort
	}
	pkt.Body = append([]byte(nil), body[:pkt.DataBodyLen]...)
	return pkt, body[pkt.DataBodyLen:], nil
}

func decodeHead(data []byte, phoneLen int) (Packet, error) {
	if phoneLen != simBCDLen && phoneLen != simBCDLen2019 {
		phoneLen = simBCDLen
	}
	minLen := 4 + 2 + 2 + phoneLen + 2
	if len(data) < minLen {
		return Packet{}, ErrHeaderLengthShort
	}
	if string(data[:4]) != magicID {
		return Packet{}, fmt.Errorf("%w: id=%q", ErrUnqualifiedData, string(data[:4]))
	}

	var pkt Packet
	pkt.ID = magicID
	pkt.PhoneLen = phoneLen
	attr := data[4]
	sign := data[5]
	pkt.Flag = Flag{
		V:  (attr >> 6) & 0b11,
		P:  (attr >> 5) & 0b1,
		X:  (attr >> 4) & 0b1,
		CC: attr & 0b1111,
		M:  (sign >> 7) & 0b1,
		PT: PTType(sign & 0b1111111),
	}
	pkt.Seq = binary.BigEndian.Uint16(data[6:8])
	simEnd := 8 + phoneLen
	pkt.Sim = bcdToString(data[8:simEnd])
	pkt.LogicChannel = data[simEnd]
	mark := data[simEnd+1]
	pkt.DataType = DataType((mark >> 4) & 0x0F)
	pkt.SubcontractType = SubcontractType(mark & 0x0F)
	if pkt.DataType > DataTypePenetrate || pkt.SubcontractType > SubcontractTypeMiddle {
		return Packet{}, fmt.Errorf("%w: type=%d sub=%d", ErrUnqualifiedData, pkt.DataType, pkt.SubcontractType)
	}

	start := simEnd + 2
	end := start
	if pkt.DataType != DataTypePenetrate {
		end += 8
	}
	if pkt.DataType.IsVideo() {
		end += 4
	}
	end += 2
	if len(data) < end {
		return Packet{}, ErrHeaderLengthShort
	}
	if pkt.DataType != DataTypePenetrate {
		pkt.Timestamp = binary.BigEndian.Uint64(data[start : start+8])
		start += 8
	}
	if pkt.DataType.IsVideo() {
		pkt.LastIFrameMs = binary.BigEndian.Uint16(data[start : start+2])
		pkt.LastFrameMs = binary.BigEndian.Uint16(data[start+2 : start+4])
		start += 4
	}
	pkt.DataBodyLen = binary.BigEndian.Uint16(data[start : start+2])
	pkt.headEnd = start + 2
	return pkt, nil
}

func (p Packet) Encode() ([]byte, error) {
	if p.ID != "" && p.ID != magicID {
		return nil, fmt.Errorf("%w: id=%q", ErrUnqualifiedData, p.ID)
	}

	phoneLen := p.PhoneLen
	if phoneLen != simBCDLen2019 {
		phoneLen = simBCDLen
	}
	data := make([]byte, 0, 34+len(p.Body))
	data = append(data, magicBytes...)

	frag := p.Flag
	if frag.V == 0 {
		frag.V = 2
	}
	if frag.CC == 0 {
		frag.CC = 1
	}
	attr := (frag.V&0b11)<<6 | (frag.P&0b1)<<5 | (frag.X&0b1)<<4 | (frag.CC & 0b1111)
	sign := (frag.M&0b1)<<7 | (uint8(frag.PT) & 0b1111111)
	data = append(data, attr, sign)
	data = binary.BigEndian.AppendUint16(data, p.Seq)
	data = append(data, stringToBCD(p.Sim, phoneLen)...)
	data = append(data, p.LogicChannel)
	data = append(data, uint8(p.DataType<<4)|uint8(p.SubcontractType))

	if p.DataType != DataTypePenetrate {
		data = binary.BigEndian.AppendUint64(data, p.Timestamp)
	}
	if p.DataType.IsVideo() {
		data = binary.BigEndian.AppendUint16(data, p.LastIFrameMs)
		data = binary.BigEndian.AppendUint16(data, p.LastFrameMs)
	}
	data = binary.BigEndian.AppendUint16(data, uint16(len(p.Body)))
	data = append(data, p.Body...)
	return data, nil
}

func (p Packet) StreamName() string {
	return StreamName(p.Sim, p.LogicChannel)
}

func bcdToString(bcd []byte) string {
	out := make([]byte, 0, len(bcd)*2)
	for _, b := range bcd {
		out = append(out, '0'+((b>>4)&0x0f), '0'+(b&0x0f))
	}
	s := strings.TrimLeft(string(out), "0")
	if s == "" {
		return "0"
	}
	return s
}

func stringToBCD(s string, size int) []byte {
	s = strings.TrimSpace(s)
	for _, c := range s {
		if c < '0' || c > '9' {
			s = "0"
			break
		}
	}
	width := size * 2
	if len(s) > width {
		s = s[len(s)-width:]
	}
	if len(s)%2 != 0 {
		s = "0" + s
	}
	for len(s) < width {
		s = "0" + s
	}
	out := make([]byte, size)
	for i := 0; i < size; i++ {
		out[i] = ((s[i*2] - '0') << 4) | (s[i*2+1] - '0')
	}
	return out
}

func findMagic(data []byte) int {
	return bytes.Index(data, magicBytes)
}
