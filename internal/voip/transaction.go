package voip

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const sipT1 = 500 * time.Millisecond
const sipT2 = 4 * time.Second
const transactionLifetime = 64 * sipT1

// Accessed under Server.requestMu. Retain final responses after media cleanup
// so a lost BYE/REGISTER response can be replayed without repeating side effects.
type serverTransaction struct {
	response string
	request  *Message
	address  TransportAddr
	expires  time.Time
	next     time.Time
	interval time.Duration
	invite   bool
	accepted bool
	acked    bool
}

func transactionKey(m *Message, method Method) string {
	via := strings.Split(m.GetHeader("Via"), ",")[0]
	// received/rport are response routing hints, not transaction identity.
	fields := strings.Split(via, ";")
	branch := ""
	for _, p := range fields[1:] {
		name, value, found := strings.Cut(strings.TrimSpace(p), "=")
		if found && strings.EqualFold(name, "branch") {
			branch = value
		}
	}
	viaFields := strings.Fields(fields[0])
	if len(viaFields) != 2 || branch == "" {
		return ""
	}
	sentBy := strings.ToUpper(viaFields[0]) + " " + strings.ToLower(viaFields[1])
	return fmt.Sprintf("%q|%q|%q|%d|%s|%q|%q", sentBy, branch,
		m.CallID(), ExtractCSeqNumber(m.CSeq()), method, ExtractTag(m.From()), m.RequestURI)
}

func sameTransactionRequest(a, b *Message) bool {
	return a.CallID() == b.CallID() && a.CSeq() == b.CSeq() && a.From() == b.From() && a.To() == b.To() &&
		a.RequestURI == b.RequestURI && a.ContentType() == b.ContentType() && a.Body == b.Body
}

func validRequestHeaders(m *Message) bool {
	f := strings.Fields(m.CSeq())
	if len(f) != 2 || Method(f[1]) != m.Method || m.CallID() == "" || m.From() == "" || m.To() == "" || transactionKey(m, m.Method) == "" {
		return false
	}
	_, err := strconv.ParseUint(f[0], 10, 31)
	return err == nil
}

func matchingDialog(m *Message, d *Dialog) bool {
	return d != nil && ExtractTag(m.From()) == d.FromTag && ExtractTag(m.To()) == d.ToTag
}

func (s *Server) rememberResponse(m *Message, response string, addr TransportAddr) {
	if len(s.transactions) >= 4096 {
		// Bound state from unauthenticated challenges. Never evict a live
		// INVITE awaiting ACK in favor of a completed transaction.
		for key, tx := range s.transactions {
			if !tx.accepted || tx.acked {
				delete(s.transactions, key)
				break
			}
		}
		if len(s.transactions) >= 4096 {
			return
		}
	}
	answer, err := ParseMessage(response)
	if err != nil {
		return
	}
	now := time.Now()
	accepted := m.Method == MethodInvite && answer.StatusCode >= 200 && answer.StatusCode < 300
	invite := m.Method == MethodInvite
	tx := &serverTransaction{response: response, request: m, address: addr, expires: now.Add(transactionLifetime),
		invite: invite, accepted: accepted, interval: sipT1}
	// 2xx INVITE responses are retransmitted end-to-end even over a reliable
	// transport (RFC 3261 13.3.1.4). Other final INVITE responses use timer G.
	if invite && answer.StatusCode >= 200 && (accepted || addr.Type == TransportUDP || addr.Type == TransportDTLS) {
		tx.next = now.Add(sipT1)
	}
	s.transactions[transactionKey(m, m.Method)] = tx
}

func (s *Server) updateInviteResponse(d *Dialog, response string) {
	if d == nil || d.Invite == nil {
		return
	}
	tx := s.transactions[transactionKey(d.Invite, MethodInvite)]
	if tx == nil {
		return
	}
	answer, err := ParseMessage(response)
	if err != nil {
		return
	}
	tx.response = response
	tx.accepted = answer.StatusCode >= 200 && answer.StatusCode < 300
	tx.acked = false
	tx.interval = sipT1
	tx.expires = time.Now().Add(transactionLifetime)
	tx.next = time.Time{}
	if answer.StatusCode >= 200 && (tx.accepted || tx.address.Type == TransportUDP || tx.address.Type == TransportDTLS) {
		tx.next = time.Now().Add(sipT1)
	}
}

func (s *Server) runTransactions(now time.Time) {
	s.runClientTransactions(now)
	for key, tx := range s.transactions {
		if !now.Before(tx.expires) {
			delete(s.transactions, key)
			continue
		}
		if tx.acked || tx.next.IsZero() || now.Before(tx.next) {
			continue
		}
		s.sendResponse(tx.response, tx.address)
		tx.interval *= 2
		if tx.interval > sipT2 {
			tx.interval = sipT2
		}
		tx.next = now.Add(tx.interval)
	}
}

func (s *Server) acknowledgeTransaction(m *Message) {
	// A non-2xx ACK belongs to its INVITE transaction; a 2xx ACK has a
	// different branch and is matched using the complete dialog identity.
	if tx := s.transactions[transactionKey(m, MethodInvite)]; tx != nil && !tx.accepted {
		answer, _ := ParseMessage(tx.response)
		if answer != nil && ExtractTag(m.To()) == ExtractTag(answer.To()) {
			tx.acked = true
		}
		return
	}
	d := s.dialogManager.Get(m.CallID())
	if !matchingDialog(m, d) || ExtractCSeqNumber(m.CSeq()) != d.InviteCSeq {
		return
	}
	if d.State == DialogStateEstablished {
		return
	}
	for _, tx := range s.transactions {
		if tx.accepted && tx.request.CallID() == d.CallID && ExtractTag(tx.request.From()) == d.FromTag && ExtractCSeqNumber(tx.request.CSeq()) == d.InviteCSeq {
			tx.acked = true
		}
	}
	s.dialogManager.UpdateState(d.CallID, DialogStateEstablished)
	if d.PubSession != nil {
		d.PubSession.StartMediaTimeout(s.config.RtpTimeoutMs)
	}
}
