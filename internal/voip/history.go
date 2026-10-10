package voip

import "time"

// CallRecord is the durable summary written after a SIP call ends.
type CallRecord struct {
	CallID         string        `json:"call_id"`
	Direction      string        `json:"direction"`
	FromUser       string        `json:"from_user"`
	ToUser         string        `json:"to_user"`
	Outcome        string        `json:"outcome"`
	StartedAt      time.Time     `json:"started_at"`
	AnsweredAt     time.Time     `json:"answered_at,omitempty"`
	EndedAt        time.Time     `json:"ended_at"`
	DurationSecond int64         `json:"duration_seconds"`
	FailureReason  string        `json:"failure_reason,omitempty"`
	RemoteAddr     string        `json:"remote_addr,omitempty"`
	Transport      TransportType `json:"transport,omitempty"`
	AudioCodec     string        `json:"audio_codec,omitempty"`
	VideoCodec     string        `json:"video_codec,omitempty"`
	StreamID       string        `json:"stream_id,omitempty"`
}

func callRecordFromDialog(d *Dialog, outcome, reason string, ended time.Time) CallRecord {
	started := d.CreatedAt
	if started.IsZero() {
		started = ended
	}
	record := CallRecord{CallID: d.CallID, Direction: "inbound", FromUser: d.FromUser, ToUser: d.ToUser,
		Outcome: outcome, StartedAt: started, AnsweredAt: d.AnsweredAt, EndedAt: ended, FailureReason: reason,
		RemoteAddr: d.RemoteAddr, Transport: d.Peer.Type}
	if !record.AnsweredAt.IsZero() && ended.After(record.AnsweredAt) {
		record.DurationSecond = int64(ended.Sub(record.AnsweredAt).Seconds())
	}
	if d.PubSession != nil {
		record.StreamID = d.PubSession.StreamName()
		negotiated := d.PubSession.Negotiated()
		if negotiated.Audio != nil {
			record.AudioCodec = negotiated.Audio.CodecName
		}
		if negotiated.Video != nil {
			record.VideoCodec = negotiated.Video.CodecName
		}
	}
	return record
}
