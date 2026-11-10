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
		r.senderReports = make(map[string]map[uint32]SenderReport)
	}
	bySSRC := r.senderReports[publicationID]
	if bySSRC == nil {
		bySSRC = make(map[uint32]SenderReport)
		r.senderReports[publicationID] = bySSRC
	}
	if old, ok := bySSRC[report.SSRC]; ok && !report.ReceivedAt.After(old.ReceivedAt) {
		return nil
	}
	bySSRC[report.SSRC] = report
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
	var report SenderReport
	var ok bool
	for _, candidate := range r.senderReports[publicationID] {
		if !ok || candidate.ReceivedAt.After(report.ReceivedAt) {
			report, ok = candidate, true
		}
	}
	return report, ok, nil
}

// SenderReportSSRCSession returns the latest report for one source encoding
// only to a current subscriber. This lets simulcast consumers match an RTP
// packet to the Sender Report from the same RID/SSRC.
func (r *Room) SenderReportSSRCSession(participantID, session, publicationID string, ssrc uint32) (SenderReport, bool, error) {
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
	report, ok := r.senderReports[publicationID][ssrc]
	return report, ok, nil
}
