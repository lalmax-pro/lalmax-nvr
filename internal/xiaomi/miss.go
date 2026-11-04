// SPDX-License-Identifier: MIT
//
// Xiaomi MISS protocol client adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package xiaomi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi/tutk"
)

// MISS protocol command constants.
const (
	missCmdAuthReq           = 0x100
	missCmdAuthRes           = 0x101
	missCmdVideoStart        = 0x102
	missCmdVideoStop         = 0x103
	missCmdAudioStart        = 0x104
	missCmdAudioStop         = 0x105
	missCmdSpeakerStartReq   = 0x106
	missCmdSpeakerStartRes   = 0x107
	missCmdSpeakerStop       = 0x108
	missCmdStreamCtrlReq     = 0x109
	missCmdStreamCtrlRes     = 0x10A
	missCmdGetAudioFormatReq = 0x10B
	missCmdGetAudioFormatRes = 0x10C
	missCmdPlaybackReq       = 0x10D
	missCmdPlaybackRes       = 0x10E
	missCmdDevInfoReq        = 0x110
	missCmdDevInfoRes        = 0x111
	missCmdMotorReq          = 0x112
	missCmdMotorRes          = 0x113

	missCmdEncoded = 0x1001
)

// MISS codec ID constants (from producer.go).
const (
	missCodecH264 = 4
	missCodecH265 = 5
	missCodecPCM  = 1024
	missCodecPCMU = 1026
	missCodecPCMA = 1027
	missCodecOPUS = 1032
	missCodecAAC  = 1033
)

// Known Xiaomi camera model identifiers.
const (
	ModelDafang    = "isa.camera.df3"
	ModelLoockV2   = "loock.cateye.v02"
	ModelC200      = "chuangmi.camera.046c04"
	ModelC300      = "chuangmi.camera.72ac1"
	ModelXiaofang  = "isa.camera.isc5c1"
	ModelHLC8      = "isa.camera.hlc8"
	ModelCW400     = "isa.camera.hlc8a"
	ModelCW500     = "isa.camera.500dh"
	ModelMJSXJ10CM = "chuangmi.camera.026c05"
	ModelMi3602K   = "chuangmi.camera.029a02"
	ModelC01A01    = "xiaomi.camera.c01a01"
)

// missHdrSize is the size of a MISS media packet header.
const missHdrSize = 32

// MISSConn is the interface for a MISS protocol transport connection.
// CS2Conn implements this interface.
type MISSConn interface {
	Protocol() string
	Version() string
	ReadCommand() (cmd uint32, data []byte, err error)
	WriteCommand(cmd uint32, data []byte) error
	ReadPacket() (hdr, payload []byte, err error)
	WritePacket(hdr, payload []byte) error
	RemoteAddr() net.Addr
	SetDeadline(t time.Time) error
	Close() error
}

// MISSClient wraps a MISSConn with encryption and protocol logic.
type MISSClient struct {
	Conn   MISSConn
	key    []byte
	model  string
	legacy bool
}

// MISSPacket is a decoded media packet from a Xiaomi camera.
type MISSPacket struct {
	CodecID   uint32
	Sequence  uint32
	Flags     uint32
	Timestamp uint64 // msec
	Payload   []byte
}

// SampleRate returns the audio sample rate derived from packet flags.
func (p *MISSPacket) SampleRate() uint32 {
	v := (p.Flags >> 3) & 0b1111
	if v != 0 {
		return 16000
	}
	return 8000
}

// NewMISSClient parses a MISS URL, establishes a CS2 connection, and performs login.
// URL format: miss://host?vendor=cs2&device_public=...&client_private=...&client_public=...&sign=...&model=...
func NewMISSClient(rawURL string, idleTimeout time.Duration) (*MISSClient, error) {
	return NewMISSClientContext(context.Background(), rawURL, idleTimeout)
}

func NewMISSClientContext(ctx context.Context, rawURL string, idleTimeout time.Duration) (*MISSClient, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	if u.Query().Get("vendor") == "legacy" {
		return newLegacyClient(ctx, u, idleTimeout)
	}
	// 1. Calculate shared encryption key.
	query := u.Query()
	key, err := CalcSharedKey(query.Get("device_public"), query.Get("client_private"))
	if err != nil {
		return nil, err
	}

	model := query.Get("model")

	// 2. Establish the transport connection.
	var conn MISSConn
	switch s := query.Get("vendor"); s {
	case "cs2":
		conn, err = CS2DialContext(ctx, u.Host, query.Get("transport"), idleTimeout)
	case "tutk":
		if query.Get("uid") == "" {
			return nil, errors.New("miss: TUTK UID is required")
		}
		var tutkConn *tutk.Conn
		tutkConn, err = tutk.DialContext(ctx, u.Host, query.Get("uid"), "Miss", "client")
		if err == nil {
			if idleTimeout == 0 {
				idleTimeout = defaultIdleTimeout
			}
			tutkConn.SetIdleTimeout(idleTimeout)
			conn = tutkConn
		}
	default:
		err = fmt.Errorf("miss: unsupported vendor %q", s)
	}

	if err != nil {
		return nil, err
	}

	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	// 3. Login with credentials.
	err = missLogin(conn, query.Get("client_public"), query.Get("sign"))
	if err != nil {
		_ = conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &MISSClient{Conn: conn, key: key, model: model}, nil
}

// missLogin sends the authentication request and verifies the response.
func missLogin(conn MISSConn, clientPublic, sign string) error {
	s := fmt.Sprintf(`{"public_key":"%s","sign":"%s","uuid":"","support_encrypt":0}`, clientPublic, sign)
	if err := conn.WriteCommand(missCmdAuthReq, []byte(s)); err != nil {
		return err
	}

	_, data, err := conn.ReadCommand()
	if err != nil {
		return err
	}

	if !bytes.Contains(data, []byte(`"result":"success"`)) {
		return fmt.Errorf("miss: auth: %s", data)
	}

	return nil
}

// Version returns a human-readable protocol + model version string.
func (c *MISSClient) Version() string {
	return fmt.Sprintf("%s (%s)", c.Conn.Version(), c.model)
}

// WriteCommand encrypts data and sends it as an encoded command.
func (c *MISSClient) WriteCommand(data []byte) error {
	data, err := Encode(data, c.key)
	if err != nil {
		return err
	}
	return c.Conn.WriteCommand(missCmdEncoded, data)
}

var ErrNotConnected = errors.New("xiaomi: camera control session is not connected")

// MotorControlContext sends a direction and native motor speed on the current
// session. A stop always uses speed zero.
func (c *MISSClient) MotorControlContext(ctx context.Context, direction string, speed int) error {
	if c.legacy {
		return errors.New("xiaomi: legacy PTZ is not supported")
	}
	switch direction {
	case "left", "right", "up", "down":
		if speed < 1 || speed > 100 {
			return fmt.Errorf("motor speed must be between 1 and 100")
		}
	case "stop":
		if speed != 0 {
			return fmt.Errorf("stop requires speed zero")
		}
	default:
		return fmt.Errorf("unsupported motor direction %q", direction)
	}
	if c.model == ModelDafang {
		var horizontal, vertical byte
		switch direction {
		case "left":
			horizontal = 1
		case "right":
			horizontal = 2
		case "up":
			vertical = 2
		case "down":
			vertical = 1
		}
		motorSpeed := byte(min(speed, 9))
		if direction == "stop" {
			motorSpeed = 5
		}
		payload := tutk.ICAM(0xff2404, horizontal, vertical, motorSpeed)
		command := make([]byte, 4+hex.EncodedLen(len(payload)))
		copy(command, []byte{0x7f, 0xff, 0xff, 0xff})
		hex.Encode(command[4:], payload)
		return c.writeEncodedCommandContext(ctx, command)
	}
	if fixedStepMotorModel(c.model) {
		// Each command is one step; these models stop on their own.
		if direction == "stop" {
			return ctx.Err()
		}
		operation := map[string]int{"left": 1, "right": 2, "up": 3, "down": 4}[direction]
		return c.writeMotorBody(ctx, struct {
			Operation int `json:"operation"`
		}{operation})
	}
	return c.writeMotorBody(ctx, struct {
		Direction string `json:"direction"`
		Speed     int    `json:"speed"`
	}{direction, speed})
}

func (c *MISSClient) writeMotorBody(ctx context.Context, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	command := binary.BigEndian.AppendUint32(nil, missCmdMotorReq)
	command = append(command, body...)
	return c.writeEncodedCommandContext(ctx, command)
}

func (c *MISSClient) writeEncodedCommandContext(ctx context.Context, command []byte) error {
	encoded, err := Encode(command, c.key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if conn, ok := c.Conn.(interface {
		WriteCommandContext(context.Context, uint32, []byte) error
	}); ok {
		return conn.WriteCommandContext(ctx, missCmdEncoded, encoded)
	}
	return c.Conn.WriteCommand(missCmdEncoded, encoded)
}

// StartMedia sends the video start command for the given channel and quality.
// Quality: "auto"=0, "sd"=1, "hd"=2 (or 3 for C200/C300), default="hd".
func (c *MISSClient) StartMedia(channel, quality string, audioEnabled ...bool) error {
	if c.legacy {
		if channel != "" && channel != "0" {
			return errors.New("xiaomi: legacy camera channel is not supported")
		}
		audio := len(audioEnabled) == 0 || audioEnabled[0]
		return c.startLegacyMedia(quality, audio)
	}
	if channel != "" && channel != "0" && channel != "1" && channel != "2" {
		return fmt.Errorf("unsupported camera channel %q", channel)
	}
	switch quality {
	case "", "auto", "sd", "hd", "0", "1", "2", "3":
	default:
		return fmt.Errorf("unsupported camera quality %q", quality)
	}
	audio := 1
	if len(audioEnabled) > 0 && !audioEnabled[0] {
		audio = 0
	}
	// 0 - auto, 1 - sd, 2 - hd, default - hd
	switch quality {
	case "", "hd":
		// Different models require different default quality settings.
		switch c.model {
		case ModelC200, ModelC300, ModelCW400, ModelCW500:
			quality = "3"
		default:
			quality = "2"
		}
	case "sd":
		quality = "1"
	case "auto":
		quality = "0"
	}

	data := binary.BigEndian.AppendUint32(nil, missCmdVideoStart)
	switch channel {
	case "", "0":
		data = fmt.Appendf(data, `{"videoquality":%s,"enableaudio":%d}`, quality, audio)
	default:
		data = fmt.Appendf(data, `{"videoquality":-1,"videoquality2":%s,"enableaudio":%d}`, quality, audio)
	}
	return c.WriteCommand(data)
}

// StopMedia sends the video stop command.
func (c *MISSClient) StopMedia() error {
	if c.legacy {
		return errors.Join(c.Conn.WriteCommand(0x2ff, []byte(`{}`)), c.Conn.WriteCommand(0x2ff, make([]byte, 8)))
	}
	data := binary.BigEndian.AppendUint32(nil, missCmdVideoStop)
	return c.WriteCommand(data)
}

// StartAudio sends the audio start command (receive audio from camera).
func (c *MISSClient) StartAudio() error {
	if c.legacy {
		return errors.New("xiaomi: legacy audio control is not supported")
	}
	data := binary.BigEndian.AppendUint32(nil, missCmdAudioStart)
	return c.WriteCommand(data)
}

// StopAudio sends the audio stop command.
func (c *MISSClient) StopAudio() error {
	if c.legacy {
		return errors.New("xiaomi: legacy audio control is not supported")
	}
	data := binary.BigEndian.AppendUint32(nil, missCmdAudioStop)
	return c.WriteCommand(data)
}

// StartSpeaker sends the speaker start command (send audio to camera).
func (c *MISSClient) StartSpeaker() error {
	if c.legacy {
		return errors.New("xiaomi: legacy audio control is not supported")
	}
	data := binary.BigEndian.AppendUint32(nil, missCmdSpeakerStartReq)
	return c.WriteCommand(data)
}

// StopSpeaker sends the speaker stop command.
func (c *MISSClient) StopSpeaker() error {
	if c.legacy {
		return errors.New("xiaomi: legacy audio control is not supported")
	}
	data := binary.BigEndian.AppendUint32(nil, missCmdSpeakerStop)
	return c.WriteCommand(data)
}

// SpeakerCodec returns the speaker codec ID for the camera model.
func (c *MISSClient) SpeakerCodec() uint32 {
	switch c.model {
	case ModelDafang, ModelXiaofang, "isa.camera.hlc6":
		return missCodecPCM
	case ModelC300:
		return missCodecOPUS
	}
	return missCodecPCMA
}

// WriteAudio encrypts and sends an audio packet to the camera via CS2 channel 3.
func (c *MISSClient) WriteAudio(codecID uint32, payload []byte) error {
	if c.legacy {
		return errors.New("xiaomi: legacy talk is not supported")
	}
	payload, err := Encode(payload, c.key)
	if err != nil {
		return err
	}

	n := uint32(len(payload))
	hdr := make([]byte, missHdrSize)
	binary.LittleEndian.PutUint32(hdr, n)
	binary.LittleEndian.PutUint32(hdr[4:], codecID)
	binary.LittleEndian.PutUint64(hdr[16:], uint64(time.Now().UnixMilli()))
	return c.Conn.WritePacket(hdr, payload)
}

// ReadPacket reads and decrypts a media packet from the connection.
func (c *MISSClient) ReadPacket() (*MISSPacket, error) {
	hdr, payload, err := c.Conn.ReadPacket()
	if err != nil {
		return nil, fmt.Errorf("miss: read media: %w", err)
	}

	if c.legacy {
		return c.readLegacyPacket(hdr, payload)
	}
	if len(hdr) < missHdrSize {
		return nil, fmt.Errorf("miss: packet header too small")
	}

	payload, err = Decode(payload, c.key)
	if err != nil {
		return nil, err
	}

	pkt := &MISSPacket{
		CodecID:  binary.LittleEndian.Uint32(hdr[4:]),
		Sequence: binary.LittleEndian.Uint32(hdr[8:]),
		Flags:    binary.LittleEndian.Uint32(hdr[12:]),
		Payload:  payload,
	}

	// Model-specific timestamp handling.
	switch c.model {
	case ModelDafang, ModelXiaofang, ModelLoockV2:
		// Dafang has ts in sec, LoockV2 has ts in msec for video but zero for audio.
		pkt.Timestamp = uint64(time.Now().UnixMilli())
	default:
		pkt.Timestamp = binary.LittleEndian.Uint64(hdr[16:])
	}

	return pkt, nil
}
