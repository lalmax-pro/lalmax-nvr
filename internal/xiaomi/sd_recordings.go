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
	xiaomiRDTFileCommand  = 1
	xiaomiRDTChannel      = 1
	xiaomiRDTMaxMessage   = 16 << 20
	xiaomiSDMaxFile       = 64 << 20
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

// ReadSDRecordingFile fetches one MP4 by its exact start timestamp and storage channel.
func (c *MISSClient) ReadSDRecordingFile(ctx context.Context, storageChannel uint32, start int64) ([]byte, error) {
	if c.legacy {
		return nil, fmt.Errorf("xiaomi: SD recording files are unavailable on legacy transport")
	}
	if start <= 0 || start > int64(^uint32(0)) {
		return nil, fmt.Errorf("xiaomi: invalid SD recording timestamp %d", start)
	}
	conn, ok := c.Conn.(interface {
		ReadChannel(context.Context, byte) ([]byte, error)
		WriteChannel(byte, []byte) error
	})
	if !ok {
		return nil, fmt.Errorf("xiaomi: transport does not support SD recording access")
	}
	c.rdtMu.Lock()
	defer c.rdtMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	request := make([]byte, 12)
	binary.LittleEndian.PutUint32(request, uint32(start))
	binary.LittleEndian.PutUint32(request[8:], storageChannel)
	message := make([]byte, 8+len(request))
	binary.LittleEndian.PutUint32(message, xiaomiRDTFileCommand)
	binary.LittleEndian.PutUint32(message[4:], uint32(len(request)))
	copy(message[8:], request)
	encrypted, err := Encode(message, c.key)
	if err != nil {
		return nil, fmt.Errorf("xiaomi: encrypt SD recording request: %w", err)
	}
	if err := conn.WriteChannel(xiaomiRDTChannel, encrypted); err != nil {
		return nil, fmt.Errorf("xiaomi: send SD recording request: %w", err)
	}
	return c.readRDTFile(ctx, conn)
}

func (c *MISSClient) readRDTFile(ctx context.Context, conn interface {
	ReadChannel(context.Context, byte) ([]byte, error)
}) ([]byte, error) {
	started := time.Now()
	var wire, file []byte
	headerRead := false
	want := 0
	command := uint32(0)
	for {
		readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		plain, err := c.readRDTPlain(readCtx, conn, &wire)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("xiaomi: waiting for SD recording file: %w", ctx.Err())
			}
			if time.Since(started) >= 8*time.Second && len(file) == 0 {
				return nil, fmt.Errorf("xiaomi: camera acknowledged no SD recording data")
			}
			if len(file) > 4096 && bytes.Contains(file, []byte("ftyp")) {
				return normalizeSDMP4(file)
			}
			continue
		}
		if !headerRead {
			if len(plain) < 8 {
				return nil, fmt.Errorf("xiaomi: short SD recording reply header (%d bytes)", len(plain))
			}
			command = binary.LittleEndian.Uint32(plain)
			want = int(binary.LittleEndian.Uint32(plain[4:]))
			if command != xiaomiRDTFileCommand {
				continue
			}
			if want < 0 || want > xiaomiSDMaxFile {
				return nil, fmt.Errorf("xiaomi: invalid SD recording size %d", want)
			}
			headerRead = true
			file = append(file, plain[8:]...)
		} else {
			file = append(file, plain...)
		}
		if len(file) > xiaomiSDMaxFile {
			return nil, fmt.Errorf("xiaomi: SD recording exceeds %d bytes", xiaomiSDMaxFile)
		}
		if want > 16 && len(file) >= want {
			return normalizeSDMP4(file[:want])
		}
	}
}

func (c *MISSClient) readRDTPlain(ctx context.Context, conn interface {
	ReadChannel(context.Context, byte) ([]byte, error)
}, wire *[]byte) ([]byte, error) {
	for {
		for len(*wire) < 4 {
			chunk, err := conn.ReadChannel(ctx, xiaomiRDTChannel)
			if err != nil {
				return nil, err
			}
			*wire = append(*wire, chunk...)
		}
		size := int(binary.BigEndian.Uint32(*wire))
		if size < 8 || size > xiaomiRDTMaxMessage {
			return nil, fmt.Errorf("xiaomi: invalid SD recording frame size %d", size)
		}
		for len(*wire) < size+4 {
			chunk, err := conn.ReadChannel(ctx, xiaomiRDTChannel)
			if err != nil {
				return nil, err
			}
			*wire = append(*wire, chunk...)
		}
		plain, err := Decode((*wire)[4:4+size], c.key)
		*wire = (*wire)[4+size:]
		if err != nil {
			return nil, fmt.Errorf("xiaomi: decrypt SD recording frame: %w", err)
		}
		return plain, nil
	}
}

func normalizeSDMP4(data []byte) ([]byte, error) {
	index := bytes.Index(data, []byte("ftyp"))
	if index < 0 {
		return nil, fmt.Errorf("xiaomi: SD recording response is not an MP4 file")
	}
	if index >= 4 {
		data = data[index-4:]
	} else {
		data = data[index:]
	}
	return bytes.Clone(data), nil
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
	if err := r.ensureSDClient(ctx); err != nil {
		return nil, err
	}
	clips, err := r.sdClient.ReadSDRecordings(ctx, storageChannel)
	if err != nil {
		_ = r.sdClient.Conn.Close()
		r.sdClient = nil
		return nil, err
	}
	return clips, nil
}

// ReadSDRecordingFile returns a clip from the card, reusing the dedicated SD session.
func (r *XiaomiRecorder) ReadSDRecordingFile(ctx context.Context, storageChannel uint32, start int64) ([]byte, error) {
	if r.cfg.DID == "" {
		return nil, fmt.Errorf("xiaomi: device ID is not configured")
	}
	key := fmt.Sprintf("%d:%d", storageChannel, start)
	r.sdMu.Lock()
	defer r.sdMu.Unlock()
	if r.sdFileKey == key && time.Since(r.sdFileAt) < 5*time.Minute && len(r.sdFile) > 0 {
		return bytes.Clone(r.sdFile), nil
	}
	if err := r.ensureSDClient(ctx); err != nil {
		return nil, err
	}
	file, err := r.sdClient.ReadSDRecordingFile(ctx, storageChannel, start)
	if err != nil {
		_ = r.sdClient.Conn.Close()
		r.sdClient = nil
		return nil, err
	}
	if len(file) <= xiaomiSDMaxFile {
		r.sdFileKey = key
		r.sdFile = bytes.Clone(file)
		r.sdFileAt = time.Now()
	}
	return file, nil
}

func (r *XiaomiRecorder) ensureSDClient(ctx context.Context) error {
	if r.sdClient != nil {
		return nil
	}
	missURL, err := r.cloudCache.resolve(ctx, r.currentCloudConfig(), r.cfg.DID, r.cfg.Model)
	if err != nil {
		return fmt.Errorf("xiaomi: resolve SD recording session: %w", err)
	}
	client, err := NewMISSClientContext(ctx, missURL, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("xiaomi: connect SD recording session: %w", err)
	}
	r.sdClient = client
	return nil
}

func (r *XiaomiRecorder) closeSDClient() {
	r.sdMu.Lock()
	defer r.sdMu.Unlock()
	if r.sdClient != nil {
		_ = r.sdClient.Conn.Close()
		r.sdClient = nil
	}
	r.sdFileKey, r.sdFile, r.sdFileAt = "", nil, time.Time{}
}
