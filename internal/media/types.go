package media

import (
	"context"
	"time"

	"github.com/q191201771/lal/pkg/base"
)

// CustomizePubSession is a handle for feeding frames directly into lal.
type CustomizePubSession interface {
	FeedAvPacket(packet base.AvPacket) error
	FeedRtmpMsg(msg base.RtmpMsg) error
}

type Engine interface {
	Start(ctx context.Context) error
	Shutdown(ctx context.Context) error
	Ready(ctx context.Context) error

	StartPull(ctx context.Context, req StartPullRequest) (*StreamSession, error)
	StopPull(ctx context.Context, streamID string) error
	StartRTPReceive(ctx context.Context, req StartRTPReceiveRequest) (*StreamSession, error)
	StopRTPReceive(ctx context.Context, sessionID string) error
	KickSession(ctx context.Context, sessionID string) error

	GetStream(ctx context.Context, streamID string) (*StreamInfo, error)
	ListStreams(ctx context.Context) ([]StreamInfo, error)
	BuildPlayURL(ctx context.Context, req PlayURLRequest) (*PlayURL, error)

	// AddCustomizePubSession registers a custom publish session for feeding frames directly into lal.
	AddCustomizePubSession(ctx context.Context, streamName string) (CustomizePubSession, error)
	// DelCustomizePubSession removes a custom publish session.
	DelCustomizePubSession(ctx context.Context, session CustomizePubSession) error

	SubscribeEvents(ctx context.Context, filter EventFilter) (<-chan Event, error)

	// SubscribeRTMPEvents subscribes to RTMP-related stream events.
	// The returned channel emits events for RTMP publish start/stop and stream active/stopped.
	SubscribeRTMPEvents(ctx context.Context) (<-chan RTMPEvent, error)

	// SubscribeSRTEvents subscribes to SRT-related stream events.
	// The returned channel emits events for SRT publish start/stop and stream active/stopped.
	SubscribeSRTEvents(ctx context.Context) (<-chan SRTEvent, error)
}

// Restarter is implemented by engines that support dynamic protocol reconfiguration.
type Restarter interface {
	Restart(ctx context.Context, rtmpPort, srtPort int, rtmpEnabled, srtEnabled bool) error
}

// RTMPEvent represents an RTMP stream event for the ingest handler.
type RTMPEvent struct {
	StreamID string
	AppName  string
	Protocol string
	Type     string // "pub_start", "pub_stop", "stream_active", "stream_stopped"
}

// SRTEvent represents an SRT stream event for the ingest handler.
type SRTEvent struct {
	StreamID string
	AppName  string
	Protocol string
	Type     string // "pub_start", "pub_stop", "stream_active", "stream_stopped"
}

type StartPullRequest struct {
	StreamID       string
	AppName        string
	SourceURL      string
	Transport      string
	PullTimeout    time.Duration
	RetryForever   bool
	PullRetryNum   int // -1=forever, 0=never, >0=limited. Overrides RetryForever if set.
	AutoStopNoView time.Duration
}

type StartRTPReceiveRequest struct {
	StreamID string
	AppName  string
	SSRC     string
	Protocol string
	Port     int
	Timeout  time.Duration
}

type StreamSession struct {
	SessionID string
	StreamID  string
	AppName   string
	Protocol  string
	Port      int
	StartedAt time.Time
}

type StreamInfo struct {
	StreamID      string
	AppName       string
	Active        bool
	Publisher     *SessionInfo
	Subscribers   []SessionInfo
	InFPS         float64
	AudioCodec    string
	VideoCodec    string
	LastFrameTime time.Time
}

type SessionInfo struct {
	SessionID         string
	Protocol          string
	Remote            string
	StartedAt         time.Time
	BitrateKbits      int
	ReadBitrateKbits  int
	WriteBitrateKbits int
	ReadBytesSum      uint64
	WroteBytesSum     uint64
}

type PlayURLRequest struct {
	StreamID string
	AppName  string
	Protocol string
	Token    string
	TTL      time.Duration
}

type PlayURL struct {
	URL       string
	Protocol  string
	ExpiresAt time.Time
}

type EventType string

const (
	EventStreamStarted     EventType = "media.stream.started"
	EventStreamActive      EventType = "media.stream.active"
	EventStreamStopped     EventType = "media.stream.stopped"
	EventPublisherStarted  EventType = "media.publisher.started"
	EventPublisherStopped  EventType = "media.publisher.stopped"
	EventSubscriberStarted EventType = "media.subscriber.started"
	EventSubscriberStopped EventType = "media.subscriber.stopped"
	EventRelayPullStarted  EventType = "media.relay_pull.started"
	EventRelayPullStopped  EventType = "media.relay_pull.stopped"
)

type Event struct {
	ID        int64
	Type      EventType
	StreamID  string
	AppName   string
	SessionID string
	Protocol  string
	At        time.Time
	Raw       []byte
}

type EventFilter struct {
	StreamID string
	AppName  string
	Types    []EventType
}
