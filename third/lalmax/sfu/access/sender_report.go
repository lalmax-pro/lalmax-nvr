package access

import "github.com/q191201771/lalmax/sfu"

// RecordIncomingSenderReport is for the session's trusted media transport.
// It must validate the report against the SSRC of the mapped incoming track.
func (s *Session) RecordIncomingSenderReport(publicationID string, report sfu.SenderReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	return s.room.RecordSenderReport(s.scope.ParticipantID, s.generation, publicationID, report)
}

// SenderReportSSRC returns the latest report for one source encoding of a
// current subscription. The caller must still validate freshness and codec.
func (s *Session) SenderReportSSRC(publicationID string, ssrc uint32) (sfu.SenderReport, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return sfu.SenderReport{}, false, ErrClosed
	}
	return s.room.SenderReportSSRCSession(s.scope.ParticipantID, s.generation, publicationID, ssrc)
}
