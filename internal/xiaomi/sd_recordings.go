// SPDX-License-Identifier: MIT

package xiaomi

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"time"
)

const (
	xiaomiRDTIndexCommand = 6
	xiaomiRDTChannel      = 1
	xiaomiRDTMaxMessage   = 16 << 20
)

// SDRecording describes one clip indexed on a Xiaomi camera's SD card.
type SDRecording struct {
	Start    int64 `json:"start"`
	Duration int32 `json:"duration"`
	Event    bool  `json:"event,omitempty"`
}

// ReadSDRecordings requests the recording index over the file-transfer channel.
// The channel is a storage channel and may differ from the live video channel.
func (c *MISSClient) ReadSDRecordings(ctx context.Context, storageChannel uint32) ([]SDRecording, error) {
	if c.legacy {
		return nil, fmt.Errorf("xiaomi: SD recording index is unavailable on legacy transport")
	}
	conn, ok := c.Conn.(interface {
		ReadChannel(context.Context, byte) ([]byte, error)
		WriteChannel(byte, []byte) error
	})
	if !ok {
		return nil, fmt.Errorf("xiaomi: transport does not support SD recording access")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.rdtMu.Lock()
	defer c.rdtMu.Unlock()

	request := make([]byte, 24)
	binary.LittleEndian.PutUint32(request[8:], storageChannel)
	message := make([]byte, 8+len(request))
	binary.LittleEndian.PutUint32(message, xiaomiRDTIndexCommand)
	binary.LittleEndian.PutUint32(message[4:], uint32(len(request)))
	copy(message[8:], request)
	encrypted, err := Encode(message, c.key)
	if err != nil {
		return nil, fmt.Errorf("xiaomi: encrypt SD index request: %w", err)
	}
	if err := conn.WriteChannel(xiaomiRDTChannel, encrypted); err != nil {
		return nil, fmt.Errorf("xiaomi: send SD index request: %w", err)
	}

	body, err := c.readRDTReply(ctx, conn, xiaomiRDTIndexCommand)
	if err != nil {
		return nil, err
	}
	return parseSDRecordingIndex(body)
}

func (c *MISSClient) readRDTReply(ctx context.Context, conn interface {
	ReadChannel(context.Context, byte) ([]byte, error)
}, wantCommand uint32) ([]byte, error) {
	var (
		wire       []byte
		command    uint32
		want       int
		payload    []byte
		headerRead bool
	)
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("xiaomi: waiting for SD index reply: %w", err)
		}
		chunk, err := conn.ReadChannel(ctx, xiaomiRDTChannel)
		if err != nil {
			return nil, fmt.Errorf("xiaomi: read SD index channel: %w", err)
		}
		wire = append(wire, chunk...)
		for len(wire) >= 4 {
			size := int(binary.BigEndian.Uint32(wire))
			if size < 8 || size > xiaomiRDTMaxMessage {
				return nil, fmt.Errorf("xiaomi: invalid SD index frame size %d", size)
			}
			if len(wire) < size+4 {
				break
			}
			plain, decodeErr := Decode(wire[4:4+size], c.key)
			wire = wire[4+size:]
			if decodeErr != nil {
				return nil, fmt.Errorf("xiaomi: decrypt SD index frame: %w", decodeErr)
			}
			if !headerRead {
				if len(plain) < 8 {
					return nil, fmt.Errorf("xiaomi: short SD index reply header (%d bytes)", len(plain))
				}
				command = binary.LittleEndian.Uint32(plain)
				want = int(binary.LittleEndian.Uint32(plain[4:]))
				if want < 0 || want > xiaomiRDTMaxMessage {
					return nil, fmt.Errorf("xiaomi: invalid SD index reply length %d", want)
				}
				payload = append(payload, plain[8:]...)
				headerRead = true
			} else {
				payload = append(payload, plain...)
			}
			if len(payload) > xiaomiRDTMaxMessage {
				return nil, fmt.Errorf("xiaomi: SD index reply exceeds size limit")
			}
			if headerRead && len(payload) >= want {
				if command != wantCommand {
					command, want, payload, headerRead = 0, 0, nil, false
					continue
				}
				return bytes.Clone(payload[:want]), nil
			}
		}
	}
}

func parseSDRecordingIndex(payload []byte) ([]SDRecording, error) {
	const (
		headerSize = 4
		recordSize = 8
		minStart   = int64(1_577_836_800)
		maxStart   = int64(2_524_608_000)
	)
	if len(payload) == headerSize {
		return []SDRecording{}, nil
	}
	if len(payload) < headerSize+recordSize {
		return nil, fmt.Errorf("xiaomi: SD index is too short (%d bytes)", len(payload))
	}
	clips := make([]SDRecording, 0, (len(payload)-headerSize)/recordSize)
	for off := headerSize; off+recordSize <= len(payload); off += recordSize {
		start := int64(binary.LittleEndian.Uint32(payload[off:]))
		flags := binary.LittleEndian.Uint32(payload[off+4:])
		if flags&^uint32(0x1ff) != 0 || start < minStart || start > maxStart {
			continue
		}
		duration := int32(flags & 0xff)
		if duration <= 0 || duration > 3600 {
			continue
		}
		clips = append(clips, SDRecording{Start: start, Duration: duration, Event: flags&0x100 != 0})
	}
	if len(clips) == 0 {
		return []SDRecording{}, nil
	}
	sort.Slice(clips, func(i, j int) bool { return clips[i].Start < clips[j].Start })
	unique := clips[:1]
	for _, clip := range clips[1:] {
		if clip.Start != unique[len(unique)-1].Start {
			unique = append(unique, clip)
		}
	}
	return unique, nil
}

// ReadSDRecordings uses a dedicated MISS connection that is reused between lookups.
func (r *XiaomiRecorder) ReadSDRecordings(ctx context.Context, storageChannel uint32) ([]SDRecording, error) {
	if r.cfg.DID == "" {
		return nil, fmt.Errorf("xiaomi: device ID is not configured")
	}
	r.sdMu.Lock()
	defer r.sdMu.Unlock()
	if r.sdClient == nil {
		missURL, err := r.cloudCache.resolve(ctx, r.currentCloudConfig(), r.cfg.DID, r.cfg.Model)
		if err != nil {
			return nil, fmt.Errorf("xiaomi: resolve SD recording session: %w", err)
		}
		client, err := NewMISSClientContext(ctx, missURL, 5*time.Minute)
		if err != nil {
			return nil, fmt.Errorf("xiaomi: connect SD recording session: %w", err)
		}
		r.sdClient = client
	}
	clips, err := r.sdClient.ReadSDRecordings(ctx, storageChannel)
	if err != nil {
		_ = r.sdClient.Conn.Close()
		r.sdClient = nil
		return nil, err
	}
	return clips, nil
}

func (r *XiaomiRecorder) closeSDClient() {
	r.sdMu.Lock()
	defer r.sdMu.Unlock()
	if r.sdClient != nil {
		_ = r.sdClient.Conn.Close()
		r.sdClient = nil
	}
}
