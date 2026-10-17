package gb28181

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Streams represents an active GB28181 media stream.
type Streams struct {
	DeviceID  string
	ChannelID string
	SSRC      string
	SessionID string
	CallID    string
	FromTag   string
	ToTag     string
	RemoteURI string
	InviteCSeq uint32
	Transport string
	StartedAt time.Time
}

// streamsManager manages active streams.
type streamsManager struct {
	streams sync.Map // map[string]*Streams (key = "play:deviceID:channelID")
	ssrc    uint32
}

var globalStreams = &streamsManager{}

func (m *streamsManager) getSSRC(domain string) string {
	v := atomic.AddUint32(&m.ssrc, 1)
	ssrc := v % 9000
	key := fmt.Sprintf("0%s%04d", domain[3:8], ssrc)
	return key
}

func (m *streamsManager) loadStream(key string) (*Streams, bool) {
	v, ok := m.streams.Load(key)
	if !ok {
		return nil, false
	}
	return v.(*Streams), true
}

func (m *streamsManager) storeStream(key string, stream *Streams) {
	m.streams.Store(key, stream)
}

func (m *streamsManager) deleteStream(key string) (*Streams, bool) {
	v, loaded := m.streams.LoadAndDelete(key)
	if !loaded {
		return nil, false
	}
	return v.(*Streams), true
}

// StreamID returns the internal lalmax stream ID for a GB28181 channel.
func StreamID(deviceID, channelID string) string {
	return deviceID + ":" + channelID
}

func playStreamKey(deviceID, channelID string) string {
	return "play:" + deviceID + ":" + channelID
}

func (m *streamsManager) isPlaying(deviceID, channelID string) bool {
	_, ok := m.loadStream(playStreamKey(deviceID, channelID))
	return ok
}

// IsStreamPlaying reports whether a lalmax stream ID has an active GB28181 play session.
func (m *streamsManager) IsStreamPlaying(streamID string) bool {
	found := false
	m.streams.Range(func(_, value any) bool {
		stream := value.(*Streams)
		if StreamID(stream.DeviceID, stream.ChannelID) == streamID {
			found = true
			return false
		}
		return true
	})
	return found
}
