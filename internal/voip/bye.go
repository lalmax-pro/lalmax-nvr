package voip

import (
	"fmt"
	"strings"
	"time"
)

type clientTransaction struct {
	request       *Message
	raw           string
	peer          TransportAddr
	next, expires time.Time
	interval      time.Duration
}

func headerURI(header string) string {
	if a := strings.IndexByte(header, '<'); a >= 0 {
		if b := strings.IndexByte(header[a+1:], '>'); b >= 0 {
			return header[a+1 : a+1+b]
		}
	}
	return strings.TrimSpace(strings.Split(header, ";")[0])
}

// Send through the established signaling flow, preserving the dialog route set.
// This also reaches clients whose Contact contains a private NAT address.
func (s *Server) notifyBye(d *Dialog) {
	if d.Invite == nil || d.Peer.Addr == "" {
		return
	}
	target := headerURI(d.Invite.GetHeader("Contact"))
	if target == "" {
		target = headerURI(d.Invite.From())
	}
	routes := []string{}
	for _, line := range d.Invite.GetHeaderAll("Record-Route") {
		for _, route := range strings.Split(line, ",") {
			if uri := headerURI(route); uri != "" {
				routes = append(routes, "<"+uri+">")
			}
		}
	}
	if len(routes) > 0 && !strings.Contains(strings.ToLower(routes[0]), ";lr") {
		first := headerURI(routes[0])
		routes = append(routes[1:], "<"+target+">")
		target = first
	}
	from := d.Invite.To()
	if ExtractTag(from) == "" {
		from += ";tag=" + d.ToTag
	}
	contact := s.buildContactURI(d.Peer)
	localAddress := strings.Split(strings.TrimPrefix(headerURI(contact), "sip:"), ";")[0]
	request := &Message{IsRequest: true, Method: MethodBye, RequestURI: target, Headers: map[string][]string{
		"Via":  {fmt.Sprintf("SIP/2.0/%s %s;branch=z9hG4bK-%s;rport", d.Peer.Type, localAddress, generateTag())},
		"From": {from}, "To": {d.Invite.From()}, "Call-ID": {d.CallID}, "CSeq": {"1 BYE"}, "Max-Forwards": {"70"}, "Route": routes,
	}}
	var raw strings.Builder
	fmt.Fprintf(&raw, "BYE %s SIP/2.0\r\n", target)
	for _, name := range []string{"Via", "From", "To", "Call-ID", "CSeq", "Max-Forwards", "Route"} {
		for _, value := range request.GetHeaderAll(name) {
			fmt.Fprintf(&raw, "%s: %s\r\n", name, value)
		}
	}
	raw.WriteString("Content-Length: 0\r\n\r\n")
	tx := &clientTransaction{request: request, raw: raw.String(), peer: d.Peer, expires: time.Now().Add(transactionLifetime), interval: sipT1}
	if d.Peer.Type == TransportUDP || d.Peer.Type == TransportDTLS {
		tx.next = time.Now().Add(sipT1)
	}
	if len(s.clientTransactions) < 256 {
		s.clientTransactions[d.CallID] = tx
	}
	s.sendResponse(tx.raw, d.Peer)
}

func (s *Server) handleClientResponse(m *Message, addr TransportAddr) {
	tx := s.clientTransactions[m.CallID()]
	if tx == nil || addr != tx.peer || m.StatusCode < 200 {
		return
	}
	copy := *m
	copy.RequestURI = tx.request.RequestURI
	method := ExtractCSeqMethod(tx.request.CSeq())
	toMatches := m.To() == tx.request.To()
	if method == MethodCancel {
		toMatches = headerURI(m.To()) == headerURI(tx.request.To())
	}
	if transactionKey(&copy, method) != transactionKey(tx.request, method) || m.From() != tx.request.From() || !toMatches || ExtractCSeqMethod(m.CSeq()) != method {
		return
	}
	delete(s.clientTransactions, m.CallID())
}

func (s *Server) runClientTransactions(now time.Time) {
	for id, tx := range s.clientTransactions {
		if !now.Before(tx.expires) {
			delete(s.clientTransactions, id)
			continue
		}
		if tx.next.IsZero() || now.Before(tx.next) {
			continue
		}
		s.sendResponse(tx.raw, tx.peer)
		tx.interval = min(tx.interval*2, sipT2)
		tx.next = now.Add(tx.interval)
	}
}
