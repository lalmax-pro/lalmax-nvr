// Package bridge provides media adapters for optional SFU-to-lal bridging.
// Authentication, subscription ownership and Group lifecycle belong to the caller.
package bridge

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/bluenviron/gortsplib/v4/pkg/format/rtph264"
	"github.com/pion/rtp"
	"github.com/q191201771/lal/pkg/avc"
	"github.com/q191201771/lal/pkg/base"
)

var (
	ErrInvalidRTP           = errors.New("bridge: invalid RTP packet")
	ErrSSRCChanged          = errors.New("bridge: H264 source SSRC changed")
	ErrTimestampRegression  = errors.New("bridge: H264 RTP timestamp regressed")
	ErrParameterSetTooLarge = errors.New("bridge: H264 parameter set too large")
	ErrUnsupportedSlice     = errors.New("bridge: unsupported H264 slice")
)

// H264 converts one packetization-mode=1 RTP stream into Annex-B AvPackets.
// It accepts a single SSRC and sequential packets: reordered/duplicate packets
// are discarded and a gap or malformed fragment requires a fresh keyframe.
// The caller must validate negotiated codec/profile, ensure the source does not
// use B frames, and request keyframes when necessary. PTS and DTS are equal;
// this video-only clock is not a common clock for audio/video synchronization.
// H264 is not safe for concurrent use.
type H264 struct {
	decoder             *rtph264.Decoder
	sps, pps            []byte
	ready               bool
	ssrcSet             bool
	ssrc                uint32
	seqSet              bool
	lastSeq             uint16
	discarding          bool
	discardTimestamp    uint32
	timestampSet        bool
	lastPacketTimestamp uint32
	clockSet            bool
	lastFrameTimestamp  uint32
	elapsedTicks        uint64
}

func NewH264() *H264 { return &H264{decoder: &rtph264.Decoder{PacketizationMode: 1}} }

func (h *H264) resetFrame() {
	h.decoder = &rtph264.Decoder{PacketizationMode: 1}
	h.ready = false
}

// Push consumes one packet. A nil packet result means another packet or a
// keyframe is required. The returned payload owns its bytes independently of
// the input. Malformed packets and timestamp regression are reported as errors.
func (h *H264) Push(pkt *rtp.Packet) (*base.AvPacket, error) {
	if h == nil || h.decoder == nil || pkt == nil {
		return nil, ErrInvalidRTP
	}
	if h.ssrcSet && pkt.SSRC != h.ssrc {
		return nil, ErrSSRCChanged
	}
	h.ssrc, h.ssrcSet = pkt.SSRC, true
	if len(pkt.Payload) == 0 && (!pkt.Padding || pkt.PaddingSize == 0) {
		h.resetFrame()
		h.timestampSet = false
		return nil, ErrInvalidRTP
	}
	if h.seqSet {
		delta := int16(pkt.SequenceNumber - h.lastSeq)
		if delta <= 0 {
			return nil, nil
		}
		if delta > 1 {
			h.resetFrame()
			h.timestampSet = false
			h.discarding, h.discardTimestamp = true, pkt.Timestamp
		}
	}
	h.seqSet = true
	h.lastSeq = pkt.SequenceNumber
	// WebRTC bandwidth probing can send padding-only RTP packets. They occupy
	// sequence numbers but do not belong to an H264 access unit or its clock.
	if len(pkt.Payload) == 0 {
		return nil, nil
	}
	if h.discarding {
		if pkt.Timestamp == h.discardTimestamp {
			return nil, nil
		}
		h.discarding = false
	}
	if h.timestampSet && pkt.Timestamp != h.lastPacketTimestamp {
		// A timestamp change without the previous marker means an incomplete AU.
		h.resetFrame()
	}
	h.timestampSet = !pkt.Marker
	h.lastPacketTimestamp = pkt.Timestamp
	nalus, err := h.decoder.Decode(pkt)
	if err != nil {
		if errors.Is(err, rtph264.ErrMorePacketsNeeded) {
			if pkt.Marker {
				h.resetFrame()
				h.timestampSet = false
				return nil, ErrInvalidRTP
			}
			return nil, nil
		}
		if errors.Is(err, rtph264.ErrNonStartingPacketAndNoPrevious) {
			h.resetFrame()
			h.timestampSet = false
			return nil, nil
		}
		h.resetFrame()
		h.timestampSet = false
		return nil, fmt.Errorf("bridge: H264 decode: %w", err)
	}
	var idr, picture bool
	for _, nalu := range nalus {
		if len(nalu) == 0 {
			h.resetFrame()
			return nil, ErrInvalidRTP
		}
		switch nalu[0] & 0x1f {
		case 7, 8:
			if len(nalu) > 65536 {
				h.resetFrame()
				return nil, ErrParameterSetTooLarge
			}
			var dst *[]byte
			if nalu[0]&0x1f == 7 {
				dst = &h.sps
			} else {
				dst = &h.pps
			}
			if !bytes.Equal(*dst, nalu) {
				*dst = bytes.Clone(nalu)
				h.ready = false
			}
		case 1, 5:
			sliceType, err := avc.ParseSliceType(nalu)
			if err != nil || sliceType == avc.SliceTypeB || (nalu[0]&0x1f == 5 && sliceType != avc.SliceTypeI) {
				h.resetFrame()
				return nil, ErrUnsupportedSlice
			}
			idr = idr || nalu[0]&0x1f == 5
			picture = true
		case 2, 3, 4, 19, 20, 21:
			// Partitioned, auxiliary and extension slices need extra parsing.
			h.resetFrame()
			return nil, ErrUnsupportedSlice
		}
	}
	if idr && len(h.sps) > 0 && len(h.pps) > 0 {
		h.ready = true
	}
	if !picture || !h.ready {
		return nil, nil
	}
	if !h.clockSet {
		h.clockSet = true
		h.lastFrameTimestamp = pkt.Timestamp
	} else {
		delta := int32(pkt.Timestamp - h.lastFrameTimestamp)
		if delta < 0 {
			h.resetFrame()
			return nil, ErrTimestampRegression
		}
		h.elapsedTicks += uint64(delta)
		h.lastFrameTimestamp = pkt.Timestamp
	}
	payload := make([]byte, 0)
	appendNALU := func(nalu []byte) { payload = append(payload, 0, 0, 0, 1); payload = append(payload, nalu...) }
	if idr {
		appendNALU(h.sps)
		appendNALU(h.pps)
	}
	for _, nalu := range nalus {
		if nalu[0]&0x1f != 7 && nalu[0]&0x1f != 8 {
			appendNALU(nalu)
		}
	}
	ms := int64(h.elapsedTicks / 90)
	return &base.AvPacket{PayloadType: base.AvPacketPtAvc, Timestamp: ms, Pts: ms, Payload: payload}, nil
}
