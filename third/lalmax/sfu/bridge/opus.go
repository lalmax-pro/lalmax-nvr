package bridge

import (
	"bytes"
	"errors"

	"github.com/pion/rtp"
	"github.com/q191201771/lal/pkg/base"
)

var (
	ErrOpusSSRCChanged         = errors.New("bridge: Opus source SSRC changed")
	ErrOpusTimestampRegression = errors.New("bridge: Opus RTP timestamp regressed")
)

// Opus converts a single Opus RTP stream into independently owned AvPackets.
// Its 48 kHz clock starts at zero on the first media packet. It is not a shared
// audio/video clock; callers must establish one before combining streams.
// The caller validates the negotiated codec and channels. Opus is not safe for
// concurrent use and does not reorder or conceal lost packets.
type Opus struct {
	ssrcSet       bool
	ssrc          uint32
	seqSet        bool
	lastSeq       uint16
	clockSet      bool
	lastTimestamp uint32
	elapsedTicks  uint64
}

// Push ignores duplicate, late, and padding-only packets. Timestamp and Pts
// are in milliseconds; the payload does not alias the input packet.
func (o *Opus) Push(pkt *rtp.Packet) (*base.AvPacket, error) {
	if o == nil || pkt == nil {
		return nil, ErrInvalidRTP
	}
	if o.ssrcSet && o.ssrc != pkt.SSRC {
		return nil, ErrOpusSSRCChanged
	}
	if len(pkt.Payload) == 0 && (!pkt.Padding || pkt.PaddingSize == 0) {
		return nil, ErrInvalidRTP
	}
	if o.seqSet && int16(pkt.SequenceNumber-o.lastSeq) <= 0 {
		return nil, nil
	}
	o.ssrc, o.ssrcSet = pkt.SSRC, true
	o.lastSeq, o.seqSet = pkt.SequenceNumber, true
	if len(pkt.Payload) == 0 {
		return nil, nil
	}
	if o.clockSet {
		delta := int32(pkt.Timestamp - o.lastTimestamp)
		if delta < 0 {
			return nil, ErrOpusTimestampRegression
		}
		o.elapsedTicks += uint64(delta)
	} else {
		o.clockSet = true
	}
	o.lastTimestamp = pkt.Timestamp
	ms := int64(o.elapsedTicks / 48)
	return &base.AvPacket{PayloadType: base.AvPacketPtOpus, Timestamp: ms, Pts: ms, Payload: bytes.Clone(pkt.Payload)}, nil
}
