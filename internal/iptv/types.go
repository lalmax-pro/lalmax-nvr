package iptv

import "time"

const (
	JobPending   = "pending"
	JobProbing   = "probing"
	JobReady     = "ready"
	JobCommitted = "committed"
	JobFailed    = "failed"
	JobCancelled = "cancelled"

	ItemPending     = "pending"
	ItemProbing     = "probing"
	ItemPlayable    = "playable"
	ItemWarning     = "warning"
	ItemUnsupported = "unsupported"
	ItemFailed      = "failed"
)

// PlaylistChannel is one EXTINF entry from an M3U/M3U8 channel list.
type PlaylistChannel struct {
	RowNo      int
	ExternalID string
	Name       string
	GroupName  string
	LogoURL    string
	ChannelNo  string
	SourceURL  string
	Headers    map[string]string
}

// ProbeResult is the outcome of checking one channel URL.
type ProbeResult struct {
	Status      string
	Error       string
	HTTPStatus  int
	ContentType string
	VideoCodec  string
	AudioCodec  string
	Encrypted   bool
	DRM         bool
	Playable    bool
	Recordable  bool
	CheckedAt   time.Time
}
