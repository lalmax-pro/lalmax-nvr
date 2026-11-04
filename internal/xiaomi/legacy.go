// SPDX-License-Identifier: MIT

package xiaomi

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi/tutk"
)

const (
	legacyAqara    = "lumi.camera.gwagl01"
	legacyImilab   = "chuangmi.camera.ipc019e"
	legacyXiaobai  = "chuangmi.camera.xiaobai"
	legacyXiaofang = "isa.camera.isc5"
	legacyMijia    = "chuangmi.camera.v2"
)

func legacyModel(model string) bool {
	switch model {
	case legacyAqara, legacyImilab, legacyXiaobai, legacyXiaofang:
		return true
	}
	return false
}

func newLegacyClient(ctx context.Context, u *url.URL, idleTimeout time.Duration) (*MISSClient, error) {
	q := u.Query()
	model := q.Get("model")
	if !legacyModel(model) && model != legacyMijia && model != ModelDafang {
		return nil, fmt.Errorf("xiaomi: unsupported legacy model %q", model)
	}
	if q.Get("uid") == "" {
		return nil, errors.New("xiaomi: legacy device UID is required")
	}
	var key []byte
	username, password := "admin", ""
	if q.Get("sign") != "" {
		if len(q.Get("device_public")) != 64 || len(q.Get("client_private")) != 64 || len(q.Get("client_public")) != 64 {
			return nil, errors.New("xiaomi: invalid legacy encryption keys")
		}
		var err error
		key, err = CalcSharedKey(q.Get("device_public"), q.Get("client_private"))
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(map[string]string{"public_key": q.Get("client_public"), "sign": q.Get("sign"), "account": "admin"})
		if err != nil {
			return nil, err
		}
		username = string(b)
	} else if model == legacyMijia || model == legacyXiaobai {
		password = q.Get("password")
	} else if model != legacyXiaofang && model != ModelDafang {
		return nil, errors.New("xiaomi: legacy device signature is required")
	}
	conn, err := tutk.DialContext(ctx, u.Host, q.Get("uid"), username, password)
	if err != nil {
		return nil, err
	}
	if idleTimeout <= 0 {
		idleTimeout = defaultIdleTimeout
	}
	conn.SetIdleTimeout(idleTimeout)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if model == legacyXiaofang || model == ModelDafang {
		if err = legacyChallengeLogin(conn, q.Get("password")); err != nil {
			_ = conn.Close()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
	}
	if err = ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &MISSClient{Conn: conn, key: key, model: model, legacy: true}, nil
}

func legacyChallengeLogin(conn MISSConn, password string) error {
	if len(password) < 16 {
		return errors.New("xiaomi: legacy device password is too short")
	}
	if err := conn.WriteCommand(0x100, tutk.ICAM(0x0400be)); err != nil {
		return err
	}
	_, data, err := conn.ReadCommand()
	if err != nil {
		return err
	}
	if len(data) < 40 {
		return errors.New("xiaomi: truncated legacy login challenge")
	}
	challenge := append([]byte(nil), data[24:40]...)
	tutk.XXTEADecrypt(challenge, challenge, []byte(password))
	challenge = append(challenge, 0, 0, 0, 0, 1, 1, 1)
	if err = conn.WriteCommand(0x100, tutk.ICAM(0x0400c0, challenge...)); err != nil {
		return err
	}
	_, _, err = conn.ReadCommand()
	return err
}

func legacyQuality(quality string, model string) (string, error) {
	switch quality {
	case "", "hd":
		if model == legacyAqara {
			return "1", nil
		}
		if model == legacyXiaobai {
			return "2", nil
		}
		return "3", nil
	case "sd":
		if model == legacyAqara {
			return "2", nil
		}
		if model == legacyXiaobai {
			return "4", nil
		}
		return "1", nil
	case "auto":
		if model == legacyXiaobai {
			return "255", nil
		}
		return "0", nil
	}
	return "", fmt.Errorf("xiaomi: unsupported legacy quality %q", quality)
}

func (c *MISSClient) startLegacyMedia(quality string, audio bool) error {
	q, err := legacyQuality(quality, c.model)
	if err != nil {
		return err
	}
	send := func(cmd uint32, data string) error { return c.Conn.WriteCommand(cmd, []byte(data)) }
	switch c.model {
	case legacyAqara:
		if err := send(0x1ff, `{}`); err != nil {
			return err
		}
		if err := send(0x605, fmt.Sprintf(`{"channel":%s}`, q)); err != nil {
			return err
		}
		return send(0x704, `{}`)
	case legacyImilab, legacyMijia:
		if audio {
			if err := send(0x300, `{}`); err != nil {
				return err
			}
		}
		if err := send(0x1ff, `{}`); err != nil {
			return err
		}
		return send(0x320, fmt.Sprintf(`{"videoquality":%s}`, q))
	case legacyXiaobai:
		if audio {
			if err := send(0x300, `{}`); err != nil {
				return err
			}
		}
		var value byte
		switch q {
		case "2":
			value = 2
		case "4":
			value = 4
		case "255":
			value = 255
		default:
			return errors.New("xiaomi: invalid legacy quality")
		}
		if err := c.Conn.WriteCommand(0x320, []byte{0, 0, 0, 0, value, 0, 0, 0}); err != nil {
			return err
		}
		return send(0x1ff, `{}`)
	case legacyXiaofang, ModelDafang:
		return nil
	}
	return errors.New("xiaomi: unsupported legacy model")
}

func (c *MISSClient) readLegacyPacket(hdr, payload []byte) (*MISSPacket, error) {
	if len(hdr) < 20 {
		return nil, errors.New("xiaomi: truncated legacy media header")
	}
	if c.key != nil {
		var err error
		if c.model == legacyAqara && hdr[0] == tutk.CodecH265 {
			payload, err = decodeLegacyVideo(payload, c.key)
		} else {
			if len(payload) < 8 {
				return nil, errors.New("xiaomi: truncated encrypted legacy media")
			}
			payload, err = Decode(payload, c.key)
		}
		if err != nil {
			return nil, err
		}
	}
	var codec uint32
	switch hdr[0] {
	case tutk.CodecH264:
		codec = missCodecH264
	case tutk.CodecH265:
		codec = missCodecH265
	case tutk.CodecPCMA, 1:
		codec = missCodecPCMA
	case tutk.CodecPCMU:
		codec = missCodecPCMU
	case tutk.CodecPCML:
		codec = missCodecPCM
	default:
		codec = 1033
	}
	return &MISSPacket{CodecID: codec, Payload: payload, Timestamp: uint64(time.Now().UnixMilli())}, nil
}

func decodeLegacyVideo(payload, key []byte) ([]byte, error) {
	if len(payload) >= 4 && string(payload[:4]) == "\x00\x00\x00\x01" {
		return payload, nil
	}
	if len(payload) < 9 {
		return nil, errors.New("xiaomi: truncated legacy video")
	}
	if payload[8] == 0 {
		return payload, nil
	}
	if payload[8] != 1 || len(payload) < 17 {
		return nil, errors.New("xiaomi: unsupported legacy video encryption")
	}
	offset, length := uint64(binary.LittleEndian.Uint32(payload[9:])), uint64(binary.LittleEndian.Uint32(payload[13:]))
	nonce, data := payload[:8], append([]byte(nil), payload[17:]...)
	if offset+length > uint64(len(data)) {
		return nil, errors.New("xiaomi: invalid legacy video encryption range")
	}
	src := data[offset : offset+length]
	for i := 32; i+16 < len(src); i += 160 {
		dst, err := DecodeNonce(src[i:i+16], nonce, key)
		if err != nil {
			return nil, err
		}
		copy(src[i:], dst)
	}
	return data, nil
}

func resolveLegacyWithCloud(cloud *Cloud, host, did, model string, public, private []byte) (string, error) {
	params, err := json.Marshal(map[string]string{"did": did, "toSignAppData": fmt.Sprintf("%x", public)})
	if err != nil {
		return "", err
	}
	result, err := cloud.Request(getAPIBaseURL(cloud.region), "/device/devicepass", string(params), nil)
	if err != nil {
		return "", fmt.Errorf("devicepass API: %w", err)
	}
	var response struct {
		UID       string `json:"p2p_id"`
		Password  string `json:"password"`
		PublicKey string `json:"p2p_dev_public_key"`
		Sign      string `json:"signForAppData"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return "", fmt.Errorf("parse devicepass response: %w", err)
	}
	if response.UID == "" {
		return "", errors.New("xiaomi: missing legacy device UID")
	}
	u := &url.URL{Scheme: "miss", Host: host}
	q := u.Query()
	q.Set("vendor", "legacy")
	q.Set("model", model)
	q.Set("uid", response.UID)
	if response.Sign != "" {
		if response.PublicKey == "" {
			return "", errors.New("xiaomi: missing legacy public key")
		}
		q.Set("device_public", response.PublicKey)
		q.Set("client_public", fmt.Sprintf("%x", public))
		q.Set("client_private", fmt.Sprintf("%x", private))
		q.Set("sign", response.Sign)
	} else {
		q.Set("password", response.Password)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
