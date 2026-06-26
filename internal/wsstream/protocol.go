package wsstream

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ─── codec byte helpers ──────────────────────────────────────────────

func codecByte(codec string) (byte, error) {
	switch codec {
	case CodecH264:
		return 4, nil
	case CodecH265:
		return 5, nil
	default:
		return 0, fmt.Errorf("wsstream: unknown codec %q", codec)
	}
}

func codecFromByte(b byte) (string, error) {
	switch b {
	case 4:
		return CodecH264, nil
	case 5:
		return CodecH265, nil
	default:
		return "", fmt.Errorf("wsstream: unknown codec byte 0x%02x", b)
	}
}

// ─── CodecInfo encode/decode ─────────────────────────────────────────

// EncodeCodecInfo encodes a CodecInfo into binary wire format.
//
// Wire format:
//
//	{type:1}{codec:1}{profile:1}{level:1}{sps_len:2}{sps}{pps_len:2}{pps}[vps_len:2][vps]
//
// All multi-byte integers are big-endian.
// codec byte: 4 = H.264, 5 = H.265.
// vps fields are only present for H.265.
func EncodeCodecInfo(ci *CodecInfo) ([]byte, error) {
	if ci == nil {
		return nil, errors.New("wsstream: nil CodecInfo")
	}

	cb, err := codecByte(ci.Codec)
	if err != nil {
		return nil, err
	}

	// type + codec + profile + level + sps_len + sps + pps_len + pps
	size := 1 + 1 + 1 + 1 + 2 + len(ci.SPS) + 2 + len(ci.PPS)
	if ci.Codec == CodecH265 {
		size += 2 + len(ci.VPS) // vps_len + vps
	}

	buf := make([]byte, size)

	offset := 0
	buf[offset] = MsgTypeCodecInfo
	offset++

	buf[offset] = cb
	offset++

	buf[offset] = ci.Profile
	offset++

	buf[offset] = ci.Level
	offset++

	// SPS length + data
	binary.BigEndian.PutUint16(buf[offset:], uint16(len(ci.SPS)))
	offset += 2
	copy(buf[offset:], ci.SPS)
	offset += len(ci.SPS)

	// PPS length + data
	binary.BigEndian.PutUint16(buf[offset:], uint16(len(ci.PPS)))
	offset += 2
	copy(buf[offset:], ci.PPS)
	offset += len(ci.PPS)

	// VPS length + data (H.265 only)
	if ci.Codec == CodecH265 {
		binary.BigEndian.PutUint16(buf[offset:], uint16(len(ci.VPS)))
		offset += 2
		copy(buf[offset:], ci.VPS)
		offset += len(ci.VPS)
	}

	return buf, nil
}

// DecodeCodecInfo decodes binary wire format into a CodecInfo.
func DecodeCodecInfo(data []byte) (*CodecInfo, error) {
	if len(data) < 5 {
		return nil, fmt.Errorf("wsstream: codec info too short: %d bytes", len(data))
	}

	if data[0] != MsgTypeCodecInfo {
		return nil, fmt.Errorf("wsstream: expected message type 0x01, got 0x%02x", data[0])
	}

	codec, err := codecFromByte(data[1])
	if err != nil {
		return nil, err
	}

	ci := &CodecInfo{
		Codec:   codec,
		Profile: data[2],
		Level:   data[3],
	}

	offset := 4

	// SPS
	if offset+2 > len(data) {
		return nil, fmt.Errorf("wsstream: codec info truncated at SPS length")
	}
	spsLen := int(binary.BigEndian.Uint16(data[offset:]))
	offset += 2
	if offset+spsLen > len(data) {
		return nil, fmt.Errorf("wsstream: codec info truncated at SPS data")
	}
	ci.SPS = make([]byte, spsLen)
	copy(ci.SPS, data[offset:offset+spsLen])
	offset += spsLen

	// PPS
	if offset+2 > len(data) {
		return nil, fmt.Errorf("wsstream: codec info truncated at PPS length")
	}
	ppsLen := int(binary.BigEndian.Uint16(data[offset:]))
	offset += 2
	if offset+ppsLen > len(data) {
		return nil, fmt.Errorf("wsstream: codec info truncated at PPS data")
	}
	ci.PPS = make([]byte, ppsLen)
	copy(ci.PPS, data[offset:offset+ppsLen])
	offset += ppsLen

	// VPS (H.265 only)
	if codec == CodecH265 {
		if offset+2 > len(data) {
			return nil, fmt.Errorf("wsstream: codec info truncated at VPS length")
		}
		vpsLen := int(binary.BigEndian.Uint16(data[offset:]))
		offset += 2
		if offset+vpsLen > len(data) {
			return nil, fmt.Errorf("wsstream: codec info truncated at VPS data")
		}
		ci.VPS = make([]byte, vpsLen)
		copy(ci.VPS, data[offset:offset+vpsLen])
		offset += vpsLen
	}

	return ci, nil
}

// ─── VideoFrame encode/decode ────────────────────────────────────────

// EncodeVideoFrame encodes a VideoFrame into binary wire format.
//
// Wire format:
//
//	{type:2}{pts:8bytes_BE}{is_keyframe:1byte}{nalu_count:2bytes}{nalu1_len:4bytes}{nalu1}...
//
// All multi-byte integers are big-endian.
// PTS is in 90kHz clock (matching StreamHub convention).
// NALUs do NOT include start codes.
func EncodeVideoFrame(vf *VideoFrame) ([]byte, error) {
	if vf == nil {
		return nil, errors.New("wsstream: nil VideoFrame")
	}

	if len(vf.NALUs) > 65535 {
		return nil, fmt.Errorf("wsstream: too many NALUs: %d", len(vf.NALUs))
	}

	// type(1) + pts(8) + isKeyframe(1) + naluCount(2)
	size := 1 + 8 + 1 + 2
	for _, nalu := range vf.NALUs {
		size += 4 + len(nalu) // naluLen(4) + nalu
	}

	buf := make([]byte, size)
	offset := 0

	buf[offset] = MsgTypeVideoFrame
	offset++

	binary.BigEndian.PutUint64(buf[offset:], uint64(vf.PTS))
	offset += 8

	if vf.IsKeyframe {
		buf[offset] = 1
	} else {
		buf[offset] = 0
	}
	offset++

	binary.BigEndian.PutUint16(buf[offset:], uint16(len(vf.NALUs)))
	offset += 2

	for _, nalu := range vf.NALUs {
		binary.BigEndian.PutUint32(buf[offset:], uint32(len(nalu)))
		offset += 4
		copy(buf[offset:], nalu)
		offset += len(nalu)
	}

	return buf, nil
}

// ─── AudioCodecInfo encode/decode ────────────────────────────────────

// AudioCodecInfo describes the audio track, sent once per viewer right after
// the video CodecInfo. For AAC, Config carries the AudioSpecificConfig (ASC)
// required to construct a WebCodecs AudioDecoder. For G.711 there is no config.
type AudioCodecInfo struct {
	Codec      byte   // AudioCodecAAC / AudioCodecG711A / AudioCodecG711U
	SampleRate uint32 // samples per second (e.g. 8000, 44100, 48000)
	Channels   byte   // channel count (1 = mono, 2 = stereo)
	Config     []byte // AAC AudioSpecificConfig; nil for G.711
}

// EncodeAudioCodecInfo encodes an AudioCodecInfo into binary wire format.
//
// Wire format:
//
//	{type:1}{codec:1}{sampleRate:4}{channels:1}{config_len:2}{config...}
func EncodeAudioCodecInfo(aci *AudioCodecInfo) ([]byte, error) {
	if aci == nil {
		return nil, errors.New("wsstream: nil AudioCodecInfo")
	}
	if len(aci.Config) > 65535 {
		return nil, fmt.Errorf("wsstream: audio config too long: %d bytes", len(aci.Config))
	}

	// type(1) + codec(1) + sampleRate(4) + channels(1) + configLen(2) + config
	size := 1 + 1 + 4 + 1 + 2 + len(aci.Config)
	buf := make([]byte, size)
	offset := 0

	buf[offset] = MsgTypeAudioCodecInfo
	offset++
	buf[offset] = aci.Codec
	offset++
	binary.BigEndian.PutUint32(buf[offset:], aci.SampleRate)
	offset += 4
	buf[offset] = aci.Channels
	offset++
	binary.BigEndian.PutUint16(buf[offset:], uint16(len(aci.Config)))
	offset += 2
	copy(buf[offset:], aci.Config)

	return buf, nil
}

// DecodeAudioCodecInfo decodes binary wire format into an AudioCodecInfo.
func DecodeAudioCodecInfo(data []byte) (*AudioCodecInfo, error) {
	if len(data) < 9 {
		return nil, fmt.Errorf("wsstream: audio codec info too short: %d bytes", len(data))
	}
	if data[0] != MsgTypeAudioCodecInfo {
		return nil, fmt.Errorf("wsstream: expected message type 0x05, got 0x%02x", data[0])
	}

	aci := &AudioCodecInfo{
		Codec:      data[1],
		SampleRate: binary.BigEndian.Uint32(data[2:6]),
		Channels:   data[6],
	}
	configLen := int(binary.BigEndian.Uint16(data[7:9]))
	if 9+configLen > len(data) {
		return nil, fmt.Errorf("wsstream: audio codec info truncated at config")
	}
	if configLen > 0 {
		aci.Config = make([]byte, configLen)
		copy(aci.Config, data[9:9+configLen])
	}
	return aci, nil
}

// ─── AudioFrame encode/decode ────────────────────────────────────────

// AudioFrame carries a single encoded audio frame (one AAC AU or one G.711
// packet). PTS is the source RTP timestamp in the audio clock (SampleRate).
type AudioFrame struct {
	PTS  int64
	Data []byte
}

// EncodeAudioFrame encodes an AudioFrame into binary wire format.
//
// Wire format:
//
//	{type:1}{pts:8bytes_BE}{data...}
//
// The payload occupies the remainder of the message, so no length prefix is
// needed.
func EncodeAudioFrame(af *AudioFrame) ([]byte, error) {
	if af == nil {
		return nil, errors.New("wsstream: nil AudioFrame")
	}
	buf := make([]byte, 1+8+len(af.Data))
	buf[0] = MsgTypeAudioFrame
	binary.BigEndian.PutUint64(buf[1:], uint64(af.PTS))
	copy(buf[9:], af.Data)
	return buf, nil
}

// DecodeAudioFrame decodes binary wire format into an AudioFrame.
func DecodeAudioFrame(data []byte) (*AudioFrame, error) {
	if len(data) < 9 {
		return nil, fmt.Errorf("wsstream: audio frame too short: %d bytes", len(data))
	}
	if data[0] != MsgTypeAudioFrame {
		return nil, fmt.Errorf("wsstream: expected message type 0x03, got 0x%02x", data[0])
	}
	af := &AudioFrame{PTS: int64(binary.BigEndian.Uint64(data[1:9]))}
	if len(data) > 9 {
		af.Data = make([]byte, len(data)-9)
		copy(af.Data, data[9:])
	}
	return af, nil
}

// DecodeVideoFrame decodes binary wire format into a VideoFrame.
func DecodeVideoFrame(data []byte) (*VideoFrame, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("wsstream: video frame too short: %d bytes", len(data))
	}

	if data[0] != MsgTypeVideoFrame {
		return nil, fmt.Errorf("wsstream: expected message type 0x02, got 0x%02x", data[0])
	}

	vf := &VideoFrame{
		PTS:        int64(binary.BigEndian.Uint64(data[1:9])),
		IsKeyframe: data[9] != 0,
	}

	naluCount := int(binary.BigEndian.Uint16(data[10:12]))
	offset := 12

	vf.NALUs = make([][]byte, 0, naluCount)
	for i := 0; i < naluCount; i++ {
		if offset+4 > len(data) {
			return nil, fmt.Errorf("wsstream: video frame truncated at NALU %d length", i)
		}
		naluLen := int(binary.BigEndian.Uint32(data[offset:]))
		offset += 4
		if offset+naluLen > len(data) {
			return nil, fmt.Errorf("wsstream: video frame truncated at NALU %d data", i)
		}
		nalu := make([]byte, naluLen)
		copy(nalu, data[offset:offset+naluLen])
		vf.NALUs = append(vf.NALUs, nalu)
		offset += naluLen
	}

	return vf, nil
}
