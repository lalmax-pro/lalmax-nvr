package sfu

import "time"

// SenderReport holds a source's RTP/NTP mapping and local receipt time.
// It is meaningful only within the publication's lifetime.
type SenderReport struct {
	SSRC       uint32
	NTP        uint64
	RTP        uint32
	ReceivedAt time.Time
}

// RecordSenderReport is for trusted transports. The transport must validate
// that the RTCP report's SSRC belongs to the mapped inbound track.
func (r *Room) RecordSenderReport(participantID, session, publicationID string, report SenderReport) error {
	if report.NTP == 0 || report.ReceivedAt.IsZero() {
		return ErrInvalidID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	if err := r.checkSessionLocked(participantID, session); err != nil {
		return err
	}
	p, ok := r.publications[publicationID]
	if !ok || p.ParticipantID != participantID {
		return ErrPublicationNotFound
	}
	if !p.RTPReady {
		return ErrMediaNotEnabled
	}
	if r.senderReports == nil {
		r.senderReports = make(map[string]SenderReport)
	}
	if old, ok := r.senderReports[publicationID]; ok && !report.ReceivedAt.After(old.ReceivedAt) {
		return nil
	}
	r.senderReports[publicationID] = report
	return nil
}

// SenderReportSession returns the latest report only to a current subscribed
// participant. The caller must separately authorize the operation.
func (r *Room) SenderReportSession(participantID, session, publicationID string) (SenderReport, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return SenderReport{}, false, ErrRoomClosed
	}
	if err := r.checkSessionLocked(participantID, session); err != nil {
		return SenderReport{}, false, err
	}
	if _, ok := r.subscriptions[subscriptionKey{participantID, publicationID}]; !ok {
		return SenderReport{}, false, ErrSubscriptionNotFound
	}
	report, ok := r.senderReports[publicationID]
	return report, ok, nil
}
