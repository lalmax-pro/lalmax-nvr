package voip

import (
	"sort"
	"time"
)

type EndpointStatus struct {
	User       string        `json:"user"`
	Contact    string        `json:"contact"`
	UserAgent  string        `json:"user_agent"`
	ExpiresAt  time.Time     `json:"expires_at"`
	RemoteAddr string        `json:"remote_addr"`
	Transport  TransportType `json:"transport"`
}
type CallStatus struct {
	Direction            string        `json:"direction"`
	FailureReason        string        `json:"failure_reason,omitempty"`
	AudioReceivedPackets uint32        `json:"audio_received_packets"`
	AudioSentPackets     uint32        `json:"audio_sent_packets"`
	BrowserReady         bool          `json:"browser_ready"`
	TalkAvailable        bool          `json:"talk_available"`
	IncomingPending      bool          `json:"incoming_pending"`
	DTMFAvailable        bool          `json:"dtmf_available"`
	CallID               string        `json:"call_id"`
	StreamID             string        `json:"stream_id"`
	FromUser             string        `json:"from_user"`
	ToUser               string        `json:"to_user"`
	State                DialogState   `json:"state"`
	Held                 bool          `json:"held"`
	StartedAt            time.Time     `json:"started_at"`
	DurationSeconds      int64         `json:"duration_seconds"`
	RemoteAddr           string        `json:"remote_addr"`
	Transport            TransportType `json:"transport"`
	AudioCodec           string        `json:"audio_codec,omitempty"`
	VideoCodec           string        `json:"video_codec,omitempty"`
	AudioPort            int           `json:"audio_port,omitempty"`
	VideoPort            int           `json:"video_port,omitempty"`
}
type Status struct {
	Enabled                 bool                       `json:"enabled"`
	IncomingCallsNeedAnswer bool                       `json:"incoming_calls_need_answer"`
	UpstreamRegistration    UpstreamRegistrationStatus `json:"upstream_registration"`
	Endpoints               []EndpointStatus           `json:"endpoints"`
	Calls                   []CallStatus               `json:"calls"`
}

type UpstreamRegistrationStatus struct {
	Configured bool       `json:"configured"`
	Server     string     `json:"server,omitempty"`
	Transport  string     `json:"transport,omitempty"`
	Username   string     `json:"username,omitempty"`
	State      string     `json:"state"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
}

func (s *Server) Status() Status {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	out := Status{Enabled: true, IncomingCallsNeedAnswer: s.config.ManualAnswer, Endpoints: []EndpointStatus{}, Calls: []CallStatus{}}
	select {
	case <-s.stopChan:
		out.Enabled = false
	default:
	}
	now := time.Now()
	out.UpstreamRegistration = s.upstreamStatus(now)
	s.registrar.mutex.RLock()
	for _, r := range s.registrar.registrations {
		if now.Before(r.Expires) {
			out.Endpoints = append(out.Endpoints, EndpointStatus{r.User, r.Contact, r.UserAgent, r.Expires, r.RemoteAddr, r.Transport})
		}
	}
	s.registrar.mutex.RUnlock()
	for _, d := range s.dialogManager.List() {
		c := CallStatus{Direction: "inbound", IncomingPending: d.State == DialogStateEarly && d.PubSession == nil, CallID: d.CallID, FromUser: d.FromUser, ToUser: d.ToUser, State: d.State, Held: d.Held, StartedAt: d.CreatedAt, DurationSeconds: int64(now.Sub(d.CreatedAt).Seconds()), RemoteAddr: d.RemoteAddr, Transport: d.Peer.Type}
		if d.PubSession != nil {
			c.StreamID = d.PubSession.StreamName()
			n := d.PubSession.Negotiated()
			if n.Audio != nil {
				c.AudioCodec = n.Audio.CodecName
				c.DTMFAvailable = n.DTMF != nil
			}
			if n.Video != nil {
				c.VideoCodec = n.Video.CodecName
			}
			c.AudioPort = d.PubSession.AudioPort()
			c.VideoPort = d.PubSession.VideoPort()
			c.TalkAvailable = d.State == DialogStateEstablished && n.Audio != nil
			if d.talk != nil {
				c.BrowserReady = d.talk.attached
			}
		}
		out.Calls = append(out.Calls, c)
	}
	for _, c := range s.outbound {
		out.Calls = append(out.Calls, c.status(now))
	}
	sort.Slice(out.Endpoints, func(i, j int) bool { return out.Endpoints[i].User < out.Endpoints[j].User })
	sort.Slice(out.Calls, func(i, j int) bool { return out.Calls[i].CallID < out.Calls[j].CallID })
	return out
}
func (s *Server) Hangup(callID string) bool {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	select {
	case <-s.stopChan:
		return false
	default:
	}
	if c := s.outbound[callID]; c != nil {
		s.cancelOutbound(c, "ended by operator")
		return true
	}
	d := s.dialogManager.Get(callID)
	if d == nil {
		return false
	}
	s.notifyBye(d)
	outcome := "completed"
	if d.State == DialogStateEarly {
		outcome = "cancelled"
	}
	s.terminate(callID, outcome, "ended by operator")
	return true
}
