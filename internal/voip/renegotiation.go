package voip

import "time"

// Renegotiation prepares replacement media before touching the established call.
// Invalid offers and resource exhaustion leave the previous media usable.
func (s *Server) handleRenegotiation(msg *Message, peer TransportAddr) string {
	d := s.dialogManager.Get(msg.CallID())
	if !matchingDialog(msg, d) {
		return BuildResponse(msg, 481, "Call/Transaction Does Not Exist", "")
	}
	if ok, response := s.authenticateRequest(msg, d.FromUser); !ok {
		return response
	}
	seq := ExtractCSeqNumber(msg.CSeq())
	if seq <= d.RemoteCSeq {
		return BuildResponse(msg, 500, "CSeq Out of Order", "")
	}
	s.dialogManager.SetRemoteCSeq(d.CallID, seq)
	if d.State != DialogStateEstablished {
		return BuildResponse(msg, 491, "Request Pending", "")
	}
	if msg.Body == "" {
		if msg.Method == MethodUpdate {
			s.dialogManager.RefreshTarget(d.CallID, msg, peer)
			return BuildResponseWithContact(msg, 200, "OK", "", d.ToTag, s.buildContactURI(peer))
		}
		return BuildResponse(msg, 488, "SDP Offer Required", "")
	}
	if s.onReplaceSession == nil {
		return BuildResponse(msg, 488, "Media Replacement Unavailable", "")
	}
	identity := *d
	identity.MediaCreatedAt = time.Now()
	answer, next, code := s.prepareMedia(msg, &identity)
	if code != 200 {
		return BuildResponse(msg, code, "Media Negotiation Failed", "")
	}
	if err := s.onReplaceSession(d.PubSession, next); err != nil {
		next.Dispose()
		return BuildResponse(msg, 500, "Media Replacement Failed", "")
	}
	if d.talk != nil {
		s.resetInboundTalk(d.talk)
	}
	s.dialogManager.SetPubSession(d.CallID, next)
	s.dialogManager.SetMediaState(d.CallID, identity.MediaCreatedAt, next.Negotiated().Held())
	// UPDATE is complete at its 200 response; re-INVITE waits for its own ACK.
	if msg.Method == MethodInvite {
		s.dialogManager.SetInvite(d.CallID, msg, peer)
		s.dialogManager.UpdateState(d.CallID, DialogStateConfirmed)
	} else {
		s.dialogManager.RefreshTarget(d.CallID, msg, peer)
		s.dialogManager.UpdateActivity(d.CallID)
		next.StartMediaTimeout(s.config.RtpTimeoutMs)
	}
	_ = d.PubSession.Dispose()
	return BuildResponseWithContact(msg, 200, "OK", answer, d.ToTag, s.buildContactURI(peer))
}
