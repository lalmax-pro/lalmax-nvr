package rtc

import (
	"fmt"

	"github.com/pion/rtp"
	"github.com/q191201771/lal/pkg/avc"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/hevc"
	"github.com/q191201771/lal/pkg/rtprtcp"
	"github.com/q191201771/naza/pkg/nazalog"
)

const (
	PacketH264 = "H264"
	PacketHEVC = "HEVC"
	PacketPCMA = "PCMA"
	PacketPCMU = "PCMU"
	PacketOPUS = "OPUS"
)

type Packer struct {
	enc IRtpEncoder
}

func NewPacker(mimeType string, codec []byte) *Packer {
	p := &Packer{}

	switch mimeType {
	case PacketH264:
		if enc := NewH264RtpEncoder(codec); enc != nil {
			p.enc = enc
		}
	case PacketPCMA:
		p.enc = NewG711RtpEncoder(8)
	case PacketPCMU:
		p.enc = NewG711RtpEncoder(0)
	case PacketHEVC:
		if enc := NewHevcRtpEncoder(codec); enc != nil {
			p.enc = enc
		}
	case PacketOPUS:
		p.enc = NewOpusRtpEncoder(111)
	}
	return p
}

func (p *Packer) Encode(msg base.RtmpMsg) ([]*rtp.Packet, error) {
	if p == nil || p.enc == nil {
		return nil, fmt.Errorf("packer encoder is nil")
	}
	switch enc := p.enc.(type) {
	case *H264RtpEncoder:
		if enc == nil { return nil, fmt.Errorf("packer encoder is nil") }
	case *HevcRtpEncoder:
		if enc == nil { return nil, fmt.Errorf("packer encoder is nil") }
	}
	return p.enc.Encode(msg)
}

func (p *Packer) UpdateVideoCodec(vps, sps, pps []byte) {
	if p == nil || p.enc == nil {
		return
	}

	if h264Encoder, ok := p.enc.(*H264RtpEncoder); ok && h264Encoder != nil {
		h264Encoder.UpdateVideoCodec(vps, sps, pps)
		return
	}

	if hevcEncoder, ok := p.enc.(*HevcRtpEncoder); ok && hevcEncoder != nil {
		hevcEncoder.UpdateVideoCodec(vps, sps, pps)
	}
}

type IRtpEncoder interface {
	Encode(msg base.RtmpMsg) ([]*rtp.Packet, error)
}

type H264RtpEncoder struct {
	IRtpEncoder
	sps       []byte
	pps       []byte
	rtpPacker *rtprtcp.RtpPacker
}

func NewH264RtpEncoder(codec []byte) *H264RtpEncoder {
	sps, pps, err := avc.ParseSpsPpsFromSeqHeader(codec)
	if err != nil {
		nazalog.Error(err)
		return nil
	}

	pp := rtprtcp.NewRtpPackerPayloadAvc(func(option *rtprtcp.RtpPackerPayloadAvcHevcOption) {
		option.Typ = rtprtcp.RtpPackerPayloadAvcHevcTypeAnnexb
	})

	return &H264RtpEncoder{
		sps:       sps,
		pps:       pps,
		rtpPacker: rtprtcp.NewRtpPacker(pp, 90000, 0),
	}
}

func (enc *H264RtpEncoder) Encode(msg base.RtmpMsg) ([]*rtp.Packet, error) {
	var out []byte
	err := avc.IterateNaluAvcc(msg.Payload[5:], func(nal []byte) {
		t := avc.ParseNaluType(nal[0])
		if t == avc.NaluTypeSei {
			return
		}

		if t == avc.NaluTypeIdrSlice {
			out = append(out, avc.NaluStartCode3...)
			out = append(out, enc.sps...)
			out = append(out, avc.NaluStartCode3...)
			out = append(out, enc.pps...)
		}

		out = append(out, avc.NaluStartCode3...)
		out = append(out, nal...)
	})

	if err != nil {
		return nil, fmt.Errorf("Packetize failed")
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("Packetize failed")
	}

	avpacket := base.AvPacket{
		Timestamp: int64(msg.Dts()),
		Payload:   out,
	}

	var pkts []*rtp.Packet
	rtpPkts := enc.rtpPacker.Pack(avpacket)
	for _, pkt := range rtpPkts {
		var newRtpPkt rtp.Packet
		err := newRtpPkt.Unmarshal(pkt.Raw)
		if err != nil {
			nazalog.Error(err)
			continue
		}

		pkts = append(pkts, &newRtpPkt)
	}

	if len(pkts) == 0 {
		return nil, fmt.Errorf("Packetize failed")
	}

	return pkts, nil
}

func (enc *H264RtpEncoder) UpdateVideoCodec(_ []byte, sps, pps []byte) {
	enc.sps = sps
	enc.pps = pps
}

type G711RtpEncoder struct {
	IRtpEncoder
	rtpPacker *rtprtcp.RtpPacker
}

func NewG711RtpEncoder(pt uint8) *G711RtpEncoder {
	// TODO 暂时采样率设置为8000
	pp := rtprtcp.NewRtpPackerPayloadPcm()

	return &G711RtpEncoder{
		rtpPacker: rtprtcp.NewRtpPacker(pp, 8000, 0),
	}
}

func (enc *G711RtpEncoder) Encode(msg base.RtmpMsg) ([]*rtp.Packet, error) {
	avpacket := base.AvPacket{
		Timestamp: int64(msg.Dts()),
		Payload:   msg.Payload[1:],
	}

	var pkts []*rtp.Packet
	rtpPkts := enc.rtpPacker.Pack(avpacket)
	for _, pkt := range rtpPkts {
		var newRtpPkt rtp.Packet
		err := newRtpPkt.Unmarshal(pkt.Raw)
		if err != nil {
			nazalog.Error(err)
			continue
		}

		pkts = append(pkts, &newRtpPkt)
	}

	if len(pkts) == 0 {
		return nil, fmt.Errorf("Packetize failed")
	}

	return pkts, nil
}

type HevcRtpEncoder struct {
	IRtpEncoder
	vps       []byte
	sps       []byte
	pps       []byte
	rtpPacker *rtprtcp.RtpPacker
}

func NewHevcRtpEncoder(codec []byte) *HevcRtpEncoder {
	// LAL supports both legacy 0x1c and Enhanced RTMP hvc1 HEVC headers.
	if len(codec) < 5 {
		return nil
	}
	var vps, sps, pps []byte
	var err error
	switch {
	case codec[0] == 0x1c:
		vps, sps, pps, err = hevc.ParseVpsSpsPpsFromSeqHeader(codec)
	case codec[0]&0x80 != 0 && string(codec[1:5]) == "hvc1":
		vps, sps, pps, err = hevc.ParseVpsSpsPpsFromEnhancedSeqHeader(codec)
	default:
		return nil
	}
	if err != nil {
		nazalog.Error(err)
		return nil
	}

	pp := rtprtcp.NewRtpPackerPayloadHevc(func(option *rtprtcp.RtpPackerPayloadAvcHevcOption) {
		option.Typ = rtprtcp.RtpPackerPayloadAvcHevcTypeAnnexb
	})

	return &HevcRtpEncoder{
		vps:       vps,
		sps:       sps,
		pps:       pps,
		rtpPacker: rtprtcp.NewRtpPacker(pp, 90000, 0),
	}
}

func hevcNALPayload(msg base.RtmpMsg) ([]byte, error) {
	if len(msg.Payload) < 6 {
		return nil, fmt.Errorf("HEVC packet payload too short: %d", len(msg.Payload))
	}
	index := msg.GetEnchanedHevcNaluIndex()
	if index == 0 {
		// Legacy HEVC RTMP packets keep the three-byte composition time
		// at bytes 2..4, so their AVCC NAL data also starts at byte 5.
		index = 5
	}
	if index >= len(msg.Payload) {
		return nil, fmt.Errorf("HEVC packet has no NAL payload: index=%d payload_len=%d", index, len(msg.Payload))
	}
	return msg.Payload[index:], nil
}

func (enc *HevcRtpEncoder) Encode(msg base.RtmpMsg) ([]*rtp.Packet, error) {
	nalPayload, err := hevcNALPayload(msg)
	if err != nil {
		return nil, err
	}
	var out []byte
	naluCount := 0
	var malformedNAL int
	err = avc.IterateNaluAvcc(nalPayload, func(nal []byte) {
		naluCount++
		if len(nal) < 2 {
			malformedNAL++
			return
		}
		t := hevc.ParseNaluType(nal[0])
		if t == hevc.NaluTypeSei || t == hevc.NaluTypeSeiSuffix {
			return
		}

		if hevc.IsIrapNalu(t) {
			out = append(out, avc.NaluStartCode3...)
			out = append(out, enc.vps...)
			out = append(out, avc.NaluStartCode3...)
			out = append(out, enc.sps...)
			out = append(out, avc.NaluStartCode3...)
			out = append(out, enc.pps...)
		}

		out = append(out, avc.NaluStartCode3...)
		out = append(out, nal...)
	})

	if err != nil {
		return nil, fmt.Errorf("HEVC AVCC NAL parse failed: %w (payload_len=%d nals=%d)", err, len(msg.Payload), naluCount)
	}

	if malformedNAL != 0 {
		return nil, fmt.Errorf("HEVC AVCC contained %d malformed NAL units", malformedNAL)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("HEVC AVCC contained no video NAL units: payload_len=%d nals=%d", len(msg.Payload), naluCount)
	}

	avpacket := base.AvPacket{
		Timestamp: int64(msg.Dts()),
		Payload:   out,
	}

	var pkts []*rtp.Packet
	rtpPkts := enc.rtpPacker.Pack(avpacket)
	for _, pkt := range rtpPkts {
		var newRtpPkt rtp.Packet
		err := newRtpPkt.Unmarshal(pkt.Raw)
		if err != nil {
			nazalog.Error(err)
			continue
		}

		pkts = append(pkts, &newRtpPkt)
	}

	if len(pkts) == 0 {
		return nil, fmt.Errorf("Packetize failed")
	}

	return pkts, nil
}

func (enc *HevcRtpEncoder) UpdateVideoCodec(vps, sps, pps []byte) {
	enc.vps = vps
	enc.sps = sps
	enc.pps = pps
}

type OpusRtpEncoder struct {
	IRtpEncoder
	rtpPacker *rtprtcp.RtpPacker
}

func NewOpusRtpEncoder(pt uint8) *OpusRtpEncoder {
	pp := rtprtcp.NewRtpPackerPayloadOpus()

	return &OpusRtpEncoder{
		rtpPacker: rtprtcp.NewRtpPacker(pp, 48000, 0),
	}
}

func (enc *OpusRtpEncoder) Encode(msg base.RtmpMsg) ([]*rtp.Packet, error) {
	avpacket := base.AvPacket{
		Timestamp: int64(msg.Dts()),
		Payload:   msg.Payload[1:],
	}

	var pkts []*rtp.Packet
	rtpPkts := enc.rtpPacker.Pack(avpacket)
	for _, pkt := range rtpPkts {
		var newRtpPkt rtp.Packet
		err := newRtpPkt.Unmarshal(pkt.Raw)
		if err != nil {
			nazalog.Error(err)
			continue
		}

		pkts = append(pkts, &newRtpPkt)
	}

	if len(pkts) == 0 {
		return nil, fmt.Errorf("Packetize failed")
	}

	return pkts, nil
}
