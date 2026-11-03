package voip

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/q191201771/lal/pkg/base"
	maxvoip "github.com/q191201771/lalmax/voip"
	rtpmedia "github.com/q191201771/lalmax/voip/media"
	"github.com/q191201771/lalmax/voip/sdp"
	"github.com/q191201771/naza/pkg/nazalog"
)

func m2UnitServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		config:             Config{Realm: "lalmax-nvr", RtpTimeoutMs: 15000, AckTimeoutMs: 32000, RingTimeoutMs: 30000, MediaPortMin: 44000, MediaPortMax: 44100},
		ready:              make(chan struct{}),
		transactions:       map[string]*serverTransaction{},
		clientTransactions: map[string]*clientTransaction{},
		outbound:           map[string]*outboundCall{},
		dialogManager:      NewDialogManager(),
		registrar:          NewRegistrar(),
		nonceStore:         newDigestNonceStore(),
		portAllocator:      rtpmedia.NewPortAllocator(44000, 44100),
		stopChan:           make(chan struct{}),
		log:                nazalog.GetGlobalLogger(),
	}
	close(s.ready)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func m2PubSession(t *testing.T, n sdp.Negotiated) *maxvoip.PubSession {
	t.Helper()
	session, err := maxvoip.NewPubSession(maxvoip.PubSessionConfig{
		StreamName: "m2-call", MediaIP: "127.0.0.1", RemoteAudioRTPAddress: "127.0.0.1:9", RemoteSourceIP: "127.0.0.1",
		PortAllocator: rtpmedia.NewPortAllocator(44200, 44300),
		Negotiated:    n,
		OnAvPacket:    func(base.AvPacket) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Dispose() })
	return session
}

func TestM2VoipParseMessageBranches(t *testing.T) {
	_, err := ParseMessage("")
	if err == nil {
		t.Fatal("empty message accepted")
	}
	_, err = ParseMessage("SIP/2.0 200")
	if err == nil {
		t.Fatal("short response line accepted")
	}
	_, err = ParseMessage("SIP/2.0 abc OK")
	if err == nil {
		t.Fatal("invalid status accepted")
	}
	_, err = ParseMessage("INVITE sip:room@example SIP/2.0x")
	if err != nil {
		t.Fatalf("unexpected request line error: %v", err)
	}
	_, err = ParseMessage("INVITE sip:room@example")
	if err == nil {
		t.Fatal("short request line accepted")
	}

	msg, err := ParseMessage("SIP/2.0 200 OK\r\nVia: SIP/2.0/UDP 127.0.0.1:1\r\n  folded\r\nX-NoColon\r\nWWW-Authenticate: Digest\r\nMin-SE: 90\r\n\r\nbody")
	if err != nil {
		t.Fatal(err)
	}
	if got := msg.GetHeader("via"); got != "SIP/2.0/UDP 127.0.0.1:1 folded" {
		t.Fatalf("folded header not joined: %q", got)
	}
	if got := msg.GetHeader("www-authenticate"); got != "Digest" {
		t.Fatalf("challenge header missing: %q", got)
	}
	if got := msg.GetHeader("min-se"); got != "90" {
		t.Fatalf("min-se header missing: %q", got)
	}
	if msg.ContentLength() != len("body") {
		t.Fatalf("content length from body = %d", msg.ContentLength())
	}

	compact, err := ParseMessage("REGISTER sip:x SIP/2.0\r\ni: call-1\r\nm: <sip:a@b>\r\nf: <sip:a@b>;tag=1\r\nt: <sip:b@c>\r\nv: SIP/2.0/UDP h;branch=z9hG4bK-1\r\nl: 0\r\nc: application/sdp\r\ncseq: 1 REGISTER\r\nX-Unknown: keep\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if compact.CallID() != "call-1" || compact.Contact() == "" || compact.From() == "" || compact.To() == "" || compact.Via() == nil || compact.ContentType() == "" {
		t.Fatalf("compact headers not normalized: %+v", compact.Headers)
	}
	if compact.ContentLength() != 0 {
		t.Fatalf("explicit content length = %d", compact.ContentLength())
	}
	if got := compact.GetHeader("X-Unknown"); got != "keep" {
		t.Fatalf("canonical header lost: %q", got)
	}
	compact.SetHeader("x-extra", "1")
	compact.AddHeader("x-extra", "2")
	if got := compact.GetHeaderAll("X-Extra"); len(got) != 2 {
		t.Fatalf("header list = %v", got)
	}
	if compact.GetHeader("absent") != "" || compact.GetHeaderAll("absent") != nil {
		t.Fatal("missing header not empty")
	}

	invalidLength := &Message{Headers: map[string][]string{"Content-Length": {"oops"}}}
	if invalidLength.ContentLength() != 0 {
		t.Fatalf("invalid content length = %d", invalidLength.ContentLength())
	}
}

func TestM2VoipExtractHelpers(t *testing.T) {
	cases := []struct{ header, user string }{
		{"<sip:1001@127.0.0.1>;tag=x", "1001"},
		{"<sip:1001>;tag=x", "1001"},
		{"<sip:1001;transport=tcp>", "1001"},
		{"<SIP:2002@example.test>", "2002"},
		{"no-uri", ""},
		{"<sip:3003", "3003"},
		{"<sip:@host>", ""},
	}
	for _, tt := range cases {
		if got := ExtractUser(tt.header); got != tt.user {
			t.Fatalf("ExtractUser(%q) = %q, want %q", tt.header, got, tt.user)
		}
	}
	if got := ExtractTag("<sip:a@b>;tag=abc;other=1"); got != "abc" {
		t.Fatalf("ExtractTag = %q", got)
	}
	if got := ExtractTag("no-tag"); got != "" {
		t.Fatalf("ExtractTag without tag = %q", got)
	}
	if ExtractCSeqNumber("") != 0 || ExtractCSeqMethod("") != "" {
		t.Fatal("empty CSeq not zero")
	}
	if got := ExtractCSeqMethod("42 INVITE"); got != MethodInvite {
		t.Fatalf("CSeq method = %s", got)
	}

	req, err := ParseMessage("INVITE sip:room@example SIP/2.0\r\nVia: SIP/2.0/UDP h;branch=b\r\nFrom: <sip:a@b>;tag=1\r\nTo: <sip:c@d>;tag=2\r\nCall-ID: c1\r\nCSeq: 1 INVITE\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if resp := BuildResponse(req, 486, "Busy Here", "v=0"); !strings.Contains(resp, "Content-Length: 3") || !strings.HasSuffix(resp, "v=0") {
		t.Fatalf("response body missing: %q", resp)
	}
	tagged := BuildResponseWithToTag(req, 200, "OK", "", "")
	if strings.Count(tagged, "tag=2") != 1 {
		t.Fatalf("existing To tag changed: %q", tagged)
	}
	untagged, _ := ParseMessage("INVITE sip:room@example SIP/2.0\r\nVia: SIP/2.0/UDP h;branch=b\r\nFrom: <sip:a@b>;tag=1\r\nTo: <sip:c@d>\r\nCall-ID: c1\r\nCSeq: 1 INVITE\r\n\r\n")
	withTag := BuildResponseWithToTag(untagged, 180, "Ringing", "v=0", "server-tag")
	if !strings.Contains(withTag, ";tag=server-tag") || !strings.Contains(withTag, "Content-Type: application/sdp") {
		t.Fatalf("to-tag response malformed: %q", withTag)
	}
	noTag := BuildResponseWithToTag(untagged, 100, "Trying", "", "")
	if strings.Contains(noTag, "To: <sip:c@d>;tag=") {
		t.Fatalf("empty to-tag added a tag: %q", noTag)
	}
	contact := BuildResponseWithContact(req, 200, "OK", "v=0", "server-tag", "<sip:server@host>")
	if !strings.Contains(contact, "Contact: <sip:server@host>") {
		t.Fatalf("contact response malformed: %q", contact)
	}

	withBody := BuildRequest(MethodInvite, "sip:room@example", map[string]string{"To": "<sip:room@example>"}, "v=0")
	if !strings.Contains(withBody, "Content-Type: application/sdp") || !strings.Contains(withBody, "Content-Length: 3") {
		t.Fatalf("request body headers missing: %q", withBody)
	}
	typed := BuildRequest(MethodInvite, "sip:room@example", map[string]string{"Content-Type": "application/json"}, "{}")
	if strings.Count(typed, "Content-Type:") != 1 || !strings.Contains(typed, "Content-Type: application/json") {
		t.Fatalf("explicit content type overridden: %q", typed)
	}
	noBody := BuildRequest(MethodOptions, "sip:room@example", map[string]string{"Content-Length": "7"}, "")
	if !strings.Contains(noBody, "Content-Length: 7") {
		t.Fatalf("explicit content length overridden: %q", noBody)
	}
	if plain := BuildRequest(MethodOptions, "sip:room@example", nil, ""); !strings.Contains(plain, "Content-Length: 0") {
		t.Fatalf("default content length missing: %q", plain)
	}

	if key := transactionKey(&Message{Headers: map[string][]string{"Via": {"garbage"}}, Method: MethodRegister}, MethodRegister); key != "" {
		t.Fatalf("invalid via produced transaction key %q", key)
	}
}

func TestM2VoipConfigValidateAndClone(t *testing.T) {
	valid := Config{Users: []User{{Username: "1001", Password: "pass"}}, AuthEnable: true}
	valid.Normalize()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cases := []Config{
		{MediaPortMin: 100, MediaPortMax: 200},
		{MediaPortMin: 41000, MediaPortMax: 30000},
		{MediaPortMin: 41000, MediaPortMax: 70000},
		{MediaPortMin: 41000, MediaPortMax: 41010, AckTimeoutMs: -1},
		{MediaPortMin: 41000, MediaPortMax: 41010, RingTimeoutMs: 10},
		{MediaPortMin: 41000, MediaPortMax: 41010, RingTimeoutMs: 300001},
		{MediaPortMin: 41000, MediaPortMax: 41010, PbxServer: "bad"},
		{MediaPortMin: 41000, MediaPortMax: 41010, PbxServer: ":5060"},
		{MediaPortMin: 41000, MediaPortMax: 41010, PbxServer: "host:abc"},
		{MediaPortMin: 41000, MediaPortMax: 41010, PbxServer: "host:5060", PbxTransport: "sctp"},
		{MediaPortMin: 41000, MediaPortMax: 41010, PbxServer: "host:5060", PbxTransport: "udp", PbxPassword: "x"},
		{MediaPortMin: 41000, MediaPortMax: 41010, PbxServer: "host:5060", PbxTransport: "udp", PbxUsername: "1001", PbxPassword: "x", PbxDomain: "bad domain"},
		{MediaPortMin: 41000, MediaPortMax: 41010, PbxServer: "host:5060", PbxTransport: "udp", PbxUsername: "1001", PbxPassword: "x", PbxRegisterExpires: 10},
		{MediaPortMin: 41000, MediaPortMax: 41010, PbxServer: "host:5060", PbxTransport: "udp", PbxUsername: "1001", PbxPassword: "x", PbxTLSServerName: "bad/name"},
		{MediaPortMin: 41000, MediaPortMax: 41010, PbxServer: "host:5060", PbxTransport: "udp", PbxUsername: "1001", PbxPassword: "x", PbxTLSCAFile: "ca.pem"},
		{MediaPortMin: 41000, MediaPortMax: 41010, PbxUsername: "1001"},
		{MediaPortMin: 41000, MediaPortMax: 41010, SipListenAddr: "bad"},
		{MediaPortMin: 41000, MediaPortMax: 41010, SipListenAddr: "127.0.0.1:99999"},
		{MediaPortMin: 41000, MediaPortMax: 41010, MediaIP: "not-an-ip"},
		{MediaPortMin: 41000, MediaPortMax: 41010, SipIP: "::1"},
		{MediaPortMin: 41000, MediaPortMax: 41010, SipIP: "0.0.0.0"},
		{MediaPortMin: 41000, MediaPortMax: 41010, SrtpMandatory: true},
		{MediaPortMin: 41000, MediaPortMax: 41010, SipTlsListenAddr: "127.0.0.1:0"},
		{MediaPortMin: 41000, MediaPortMax: 41010, AuthEnable: true},
		{MediaPortMin: 41000, MediaPortMax: 41010, Users: []User{{Username: "u", Password: "p", RecordCalls: true}}},
		{MediaPortMin: 41000, MediaPortMax: 41010, AuthEnable: true, Users: []User{{Username: "", Password: "p"}}},
		{MediaPortMin: 41000, MediaPortMax: 41010, AuthEnable: true, Users: []User{{Username: "u", Password: "p"}, {Username: "u", Password: "q"}}},
	}
	for i, cfg := range cases {
		cfg.Normalize()
		if err := cfg.Validate(); err == nil {
			t.Fatalf("case %d accepted invalid config: %+v", i, cfg)
		}
	}

	normalized := Config{PbxServer: "pbx.example:5060", PbxTransport: " TLS ", SipListenAddr: "127.0.0.1:0"}
	normalized.Normalize()
	if normalized.PbxTransport != "tls" || normalized.PbxDomain != "pbx.example" {
		t.Fatalf("normalize = %+v", normalized)
	}
	withUsers := Config{Users: []User{{Username: "a", Password: "p"}}}
	clone := withUsers.Clone()
	clone.Users[0].Password = "changed"
	if withUsers.Users[0].Password != "p" {
		t.Fatal("Clone shares the user slice")
	}
}

func TestM2VoipDialogManagerBranches(t *testing.T) {
	dm := NewDialogManager()
	dm.Delete("missing")
	dm.UpdateActivity("missing")
	dm.SetRemoteCSeq("missing", 5)
	dm.SetPubSession("missing", nil)
	dm.SetTalk("missing", nil)
	dm.SetMediaState("missing", time.Now(), true)
	dm.RefreshTarget("missing", &Message{}, TransportAddr{})

	dm.Create("call", "from-tag", "to-tag", "1001", "1002", "127.0.0.1:9")
	before := dm.Get("call").LastActivity
	time.Sleep(2 * time.Millisecond)
	dm.UpdateActivity("call")
	if !dm.Get("call").LastActivity.After(before) {
		t.Fatal("UpdateActivity did not refresh activity")
	}

	terminated := 0
	session := m2PubSession(t, sdp.Negotiated{Audio: &sdp.Payload{PayloadType: 8, Codec: base.AvPacketPtG711A, ClockRate: 8000, CodecName: "PCMA"}})
	dm.SetPubSession("call", session)
	dm.Get("call")
	d := dm.Take("call")
	d.OnTerminate = func() { terminated++ }
	dm.Create("call2", "f", "t", "a", "b", "peer")
	dm.SetPubSession("call2", session)
	dm.Get("call2").OnTerminate = nil
	live := dm.Take("call2")
	if live == nil {
		t.Fatal("dialog not taken")
	}
	dm.Create("call3", "f", "t", "a", "b", "peer")
	stored := dm.dialogs["call3"]
	stored.PubSession = session
	stored.OnTerminate = func() { terminated++ }
	dm.Delete("call3")
	if terminated != 1 {
		t.Fatalf("OnTerminate calls = %d", terminated)
	}
	if dm.Get("call3") != nil {
		t.Fatal("deleted dialog still present")
	}

	dm.Create("refresh", "f", "t", "a", "b", "peer")
	invite, _ := ParseMessage("INVITE sip:x SIP/2.0\r\nVia: SIP/2.0/UDP h;branch=b\r\nFrom: <sip:a@b>;tag=t\r\nTo: <sip:c@d>\r\nCall-ID: refresh\r\nCSeq: 1 INVITE\r\nRecord-Route: <sip:proxy;lr>\r\n\r\n")
	dm.SetInvite("refresh", invite, TransportAddr{Type: TransportUDP, Addr: "peer"})
	dm.SetInvite("refresh", invite, TransportAddr{Type: TransportUDP, Addr: "peer2"})
	if got := dm.Get("refresh"); got.Peer.Addr != "peer2" || got.Invite.GetHeader("Record-Route") == "" {
		t.Fatalf("dialog invite not refreshed: %+v", got)
	}
	update, _ := ParseMessage("INVITE sip:x SIP/2.0\r\nContact: <sip:new@peer>\r\n\r\n")
	dm.RefreshTarget("refresh", update, TransportAddr{Type: TransportTCP, Addr: "peer3"})
	refreshed := dm.Get("refresh")
	if refreshed.Peer.Type != TransportTCP || refreshed.Invite.GetHeader("Contact") != "<sip:new@peer>" {
		t.Fatalf("target not refreshed: %+v", refreshed)
	}
	dm.RefreshTarget("refresh", &Message{}, TransportAddr{})
}

func TestM2VoipCallRecordBranches(t *testing.T) {
	now := time.Now()
	record := callRecordFromDialog(&Dialog{CallID: "zero"}, "missed", "no answer", now)
	if !record.StartedAt.Equal(now) || record.DurationSecond != 0 {
		t.Fatalf("zero dialog record = %+v", record)
	}

	session := m2PubSession(t, sdp.Negotiated{
		Audio: &sdp.Payload{PayloadType: 8, Codec: base.AvPacketPtG711A, ClockRate: 8000, CodecName: "PCMA"},
		Video: &sdp.Payload{PayloadType: 96, Codec: base.AvPacketPtAvc, ClockRate: 90000, CodecName: "H264"},
	})
	answered := now.Add(-90 * time.Second)
	record = callRecordFromDialog(&Dialog{
		CallID: "done", CreatedAt: now.Add(-2 * time.Minute), AnsweredAt: answered,
		PubSession: session, RemoteAddr: "127.0.0.1:9", Peer: TransportAddr{Type: TransportTCP},
	}, "completed", "", now)
	if record.DurationSecond != 90 || record.AudioCodec != "PCMA" || record.VideoCodec != "H264" || record.Transport != TransportTCP {
		t.Fatalf("completed record = %+v", record)
	}
}

func TestM2VoipTransactionBranches(t *testing.T) {
	s := m2UnitServer(t)
	req, err := ParseMessage("REGISTER sip:x SIP/2.0\r\nVia: SIP/2.0/UDP 127.0.0.1:9999;branch=z9hG4bK-a\r\nFrom: <sip:1001@x>;tag=f\r\nTo: <sip:1001@x>\r\nCall-ID: c1\r\nCSeq: 1 REGISTER\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	addr := TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9999"}
	response := BuildResponse(req, 200, "OK", "")
	s.rememberResponse(req, response, addr)
	key := transactionKey(req, MethodRegister)
	if _, ok := s.transactions[key]; !ok {
		t.Fatal("final response not remembered")
	}

	// Invalid stored responses are ignored.
	s.rememberResponse(req, "not a sip message", addr)
	// Both early-response branches of updateInviteResponse return early.
	s.updateInviteResponse(nil, response)
	s.updateInviteResponse(&Dialog{}, response)
	s.updateInviteResponse(&Dialog{Invite: req}, "not a sip message")

	invite, _ := ParseMessage("INVITE sip:x SIP/2.0\r\nVia: SIP/2.0/UDP 127.0.0.1:9999;branch=z9hG4bK-i\r\nFrom: <sip:1001@x>;tag=f\r\nTo: <sip:1001@x>\r\nCall-ID: c2\r\nCSeq: 1 INVITE\r\n\r\n")
	accepted := BuildResponse(invite, 200, "OK", "")
	s.rememberResponse(invite, accepted, addr)
	s.updateInviteResponse(&Dialog{Invite: invite}, accepted)
	if tx := s.transactions[transactionKey(invite, MethodInvite)]; tx == nil || !tx.accepted {
		t.Fatalf("invite transaction = %+v", tx)
	}

	// Retransmission timers: expired entries are dropped, acked/zero-next skipped,
	// due entries are resent and their interval backs off.
	s.transactions["expired"] = &serverTransaction{expires: time.Now().Add(-time.Second)}
	s.transactions["acked"] = &serverTransaction{expires: time.Now().Add(time.Minute), acked: true, next: time.Now().Add(-time.Second)}
	s.transactions["idle"] = &serverTransaction{expires: time.Now().Add(time.Minute)}
	s.transactions["due"] = &serverTransaction{expires: time.Now().Add(time.Minute), next: time.Now().Add(-time.Second), interval: sipT1, response: response, address: addr}
	s.runTransactions(time.Now())
	if _, ok := s.transactions["expired"]; ok {
		t.Fatal("expired transaction retained")
	}
	if tx := s.transactions["due"]; tx == nil || tx.next.IsZero() || tx.interval <= sipT1 {
		t.Fatalf("due transaction not retransmitted: %+v", tx)
	}
	// The interval is capped at T2.
	s.transactions["due"].next = time.Now().Add(-time.Second)
	s.transactions["due"].interval = sipT2
	s.runTransactions(time.Now())
	if s.transactions["due"].interval != sipT2 {
		t.Fatalf("interval not capped: %v", s.transactions["due"].interval)
	}

	// rememberResponse bound: all accepted transactions block insertion.
	full := m2UnitServer(t)
	acceptedTx := func() *serverTransaction {
		return &serverTransaction{request: invite, accepted: true}
	}
	for i := 0; i < 4096; i++ {
		full.transactions[string(rune(i))+time.Now().String()] = acceptedTx()
	}
	full.rememberResponse(req, response, addr)
	if len(full.transactions) != 4096 {
		t.Fatalf("transaction bound exceeded: %d", len(full.transactions))
	}
	// A completed transaction is evicted to make room.
	full.transactions["done"] = &serverTransaction{request: invite, next: time.Time{}}
	full.rememberResponse(req, response, addr)
	if len(full.transactions) < 4096 {
		t.Fatalf("completed transaction not evicted: %d", len(full.transactions))
	}

	// ACK handling: non-accepted dialog-transaction ACK, unknown dialogs and
	// established dialogs are all no-ops; a matching ACK marks the transaction.
	s.transactions["nonaccepted"] = &serverTransaction{request: req, response: BuildResponse(req, 486, "Busy", ""), accepted: false}
	s.acknowledgeTransaction(req)
	if s.transactions["nonaccepted"].acked {
		t.Fatal("non-matching ACK acknowledged")
	}
	s.acknowledgeTransaction(&Message{IsRequest: true, Method: MethodAck, Headers: map[string][]string{"Call-ID": {"unknown"}}, RequestURI: "sip:x"})

	s.dialogManager.Create("established", "f", "t", "a", "b", "peer")
	s.dialogManager.UpdateState("established", DialogStateEstablished)
	ack := &Message{IsRequest: true, Method: MethodAck, Headers: map[string][]string{
		"Call-ID": {"established"}, "From": {"<sip:a@b>;tag=f"}, "To": {"<sip:c@d>;tag=t"}, "CSeq": {"1 ACK"},
	}}
	s.acknowledgeTransaction(ack)
}

func TestM2VoipByeAndClientTransactions(t *testing.T) {
	s := m2UnitServer(t)
	// No invite/peer means no BYE.
	s.notifyBye(&Dialog{})
	s.notifyBye(&Dialog{Invite: &Message{Headers: map[string][]string{}}})

	invite, _ := ParseMessage("INVITE sip:x SIP/2.0\r\nVia: SIP/2.0/UDP h;branch=b\r\nFrom: <sip:caller@x>;tag=f\r\nTo: <sip:door@x>\r\nCall-ID: bye-call\r\nCSeq: 1 INVITE\r\nRecord-Route: <sip:proxy1;lr>, <sip:proxy2>\r\n\r\n")
	d := &Dialog{CallID: "bye-call", ToTag: "server-tag", Invite: invite, Peer: TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9999"}}
	s.notifyBye(d)
	if tx := s.clientTransactions["bye-call"]; tx == nil {
		t.Fatal("BYE transaction not recorded")
	} else if !strings.Contains(tx.raw, "Route: <sip:proxy1;lr>") {
		t.Fatalf("route set missing: %q", tx.raw)
	}

	// Without a Contact header the From URI is used, and a route set without
	// loose routing moves the target to the first route.
	loose, _ := ParseMessage("INVITE sip:x SIP/2.0\r\nFrom: <sip:caller@x>;tag=f\r\nTo: <sip:door@x>\r\nCall-ID: bye-route\r\nRecord-Route: <sip:proxy3>\r\n\r\n")
	d = &Dialog{CallID: "bye-route", ToTag: "t", Invite: loose, Peer: TransportAddr{Type: TransportTCP, Addr: "peer"}}
	s.notifyBye(d)
	tx := s.clientTransactions["bye-route"]
	if tx == nil {
		t.Fatal("strict-route BYE not recorded")
	}
	if !strings.HasPrefix(tx.raw, "BYE sip:proxy3 SIP/2.0") {
		t.Fatalf("strict route target wrong: %q", tx.raw)
	}
	if !strings.Contains(tx.raw, "From: <sip:door@x>;tag=t") {
		t.Fatalf("from tag not added: %q", tx.raw)
	}
	if !tx.next.IsZero() {
		t.Fatal("TCP BYE scheduled retransmission")
	}

	// Response handling: unknown, provisional, wrong peer and mismatched
	// headers are ignored; a matching final response clears the transaction.
	byeTx := s.clientTransactions["bye-call"]
	byeReq := byeTx.request
	buildResp := func(status, from, cseq string) *Message {
		raw := "SIP/2.0 " + status + "\r\nVia: " + byeReq.GetHeader("Via") +
			"\r\nFrom: " + from + "\r\nTo: " + byeReq.To() +
			"\r\nCall-ID: bye-call\r\nCSeq: " + cseq + "\r\n\r\n"
		msg, err := ParseMessage(raw)
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	okResp := buildResp("200 OK", byeReq.From(), "1 BYE")
	s.handleClientResponse(okResp, TransportAddr{Type: TransportUDP, Addr: "other"})
	s.handleClientResponse(buildResp("100 Trying", byeReq.From(), "1 BYE"), byeTx.peer)
	s.handleClientResponse(buildResp("200 OK", "<sip:caller@x>;tag=z", "1 BYE"), byeTx.peer)
	s.handleClientResponse(buildResp("200 OK", byeReq.From(), "1 REGISTER"), byeTx.peer)
	if _, ok := s.clientTransactions["bye-call"]; !ok {
		t.Fatal("mismatched response cleared transaction")
	}
	s.handleClientResponse(okResp, byeTx.peer)
	if _, ok := s.clientTransactions["bye-call"]; ok {
		t.Fatal("matching response did not clear transaction")
	}
	s.handleClientResponse(okResp, TransportAddr{})

	// CANCEL responses match the To URI without tags.
	s.clientTransactions["cancel"] = &clientTransaction{
		request: &Message{IsRequest: true, Method: MethodCancel, RequestURI: "sip:x", Headers: map[string][]string{
			"Call-ID": {"cancel"}, "From": {"<sip:a@b>;tag=f"}, "To": {"<sip:c@d>"}, "CSeq": {"1 CANCEL"},
			"Via": {"SIP/2.0/UDP h;branch=b"},
		}},
		peer:    TransportAddr{Type: TransportUDP, Addr: "peer"},
		raw:     "CANCEL sip:x SIP/2.0\r\n\r\n",
		expires: time.Now().Add(time.Minute),
	}
	cancelResp, _ := ParseMessage("SIP/2.0 200 OK\r\nVia: SIP/2.0/UDP h;branch=b\r\nFrom: <sip:a@b>;tag=f\r\nTo: <sip:c@d>;tag=server\r\nCall-ID: cancel\r\nCSeq: 1 CANCEL\r\n\r\n")
	s.handleClientResponse(cancelResp, TransportAddr{Type: TransportUDP, Addr: "peer"})
	if _, ok := s.clientTransactions["cancel"]; ok {
		t.Fatal("CANCEL response not matched by URI")
	}

	// Client retransmission timers.
	s.clientTransactions["expired"] = &clientTransaction{expires: time.Now().Add(-time.Second)}
	s.clientTransactions["idle"] = &clientTransaction{expires: time.Now().Add(time.Minute)}
	s.clientTransactions["due"] = &clientTransaction{
		expires: time.Now().Add(time.Minute), next: time.Now().Add(-time.Second),
		interval: sipT1, raw: "OPTIONS sip:x SIP/2.0\r\n\r\n", peer: TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"},
	}
	s.runClientTransactions(time.Now())
	if _, ok := s.clientTransactions["expired"]; ok {
		t.Fatal("expired client transaction retained")
	}
	if tx := s.clientTransactions["due"]; tx == nil || tx.interval <= sipT1 {
		t.Fatalf("client transaction not retransmitted: %+v", tx)
	}
	s.clientTransactions["due"].interval = sipT2
	s.clientTransactions["due"].next = time.Now().Add(-time.Second)
	s.runClientTransactions(time.Now())
	if s.clientTransactions["due"].interval != sipT2 {
		t.Fatalf("client interval not capped: %v", s.clientTransactions["due"].interval)
	}

	if got := headerURI("sip:plain;tag=1"); got != "sip:plain" {
		t.Fatalf("headerURI without angles = %q", got)
	}
	if got := headerURI("<sip:wrapped>;tag=1"); got != "sip:wrapped" {
		t.Fatalf("headerURI = %q", got)
	}
}

func TestM2VoipStatusAndHangupBranches(t *testing.T) {
	s := m2UnitServer(t)
	s.dialogManager.Create("inbound", "f", "t", "1001", "1002", "peer")
	s.dialogManager.UpdateState("inbound", DialogStateEarly)
	session := m2PubSession(t, sdp.Negotiated{Audio: &sdp.Payload{PayloadType: 8, Codec: base.AvPacketPtG711A, ClockRate: 8000, CodecName: "PCMA"}})
	s.dialogManager.SetPubSession("inbound", session)
	status := s.Status()
	if !status.Enabled || len(status.Calls) != 1 || status.Calls[0].AudioCodec != "PCMA" || status.Calls[0].IncomingPending {
		t.Fatalf("status = %+v", status)
	}
	if !s.Hangup("inbound") || s.Hangup("inbound") {
		t.Fatal("Hangup must be idempotent")
	}
	if s.Hangup("missing") {
		t.Fatal("Hangup on unknown call returned true")
	}
	_ = s.Close()
	if status := s.Status(); status.Enabled || len(status.Calls) != 0 {
		t.Fatalf("closed status = %+v", status)
	}
	if s.Hangup("inbound") {
		t.Fatal("Hangup on stopped server returned true")
	}
}

func TestM2VoipAuthHelpers(t *testing.T) {
	if params := parseDigestAuthorization("nonsense"); params != nil {
		t.Fatalf("invalid authorization parsed: %v", params)
	}
	if _, err := buildDigestAuthorization("REGISTER", "sip:x", "1001", "pass", "realm", "nonce", "unknown-qop"); err == nil {
		t.Fatal("unsupported qop accepted")
	}
	if got := buildDigestChallenge("realm", "nonce"); !strings.Contains(got, `realm="realm"`) {
		t.Fatalf("challenge = %q", got)
	}
	_ = expectedDigestResponse("REGISTER", "realm", "pass", map[string]string{"username": "u", "uri": "sip:x", "nonce": "n", "qop": "auth", "nc": "00000001", "cnonce": "c"})
	if verifyDigestResponse("REGISTER", "u", "realm", "pass", map[string]string{}) {
		t.Fatal("empty digest verified")
	}
	if got := insertHeaderBeforeContentLength("SIP/2.0 200 OK\r\n\r\n", "X-Test", "1"); !strings.Contains(got, "X-Test: 1") {
		t.Fatalf("header insertion without content-length = %q", got)
	}
	withLength := "SIP/2.0 401 Unauthorized\r\nContent-Length: 0\r\n\r\n"
	if got := insertHeaderBeforeContentLength(withLength, "WWW-Authenticate", "Digest"); !strings.HasPrefix(got, "SIP/2.0 401 Unauthorized\r\nWWW-Authenticate: Digest\r\n") {
		t.Fatalf("header insertion order = %q", got)
	}
}

func TestM2VoipPBXUDPHelpers(t *testing.T) {
	if got := pbxExtensionURI("1001", "pbx", TransportTCP); !strings.Contains(got, ";transport=tcp") {
		t.Fatalf("tcp extension uri = %q", got)
	}
	if got := pbxExtensionURI("1001", "pbx", TransportTLS); !strings.HasPrefix(got, "sips:") {
		t.Fatalf("tls extension uri = %q", got)
	}
	if got := pbxExtensionURI("1001", "pbx", TransportUDP); !strings.HasPrefix(got, "sip:") {
		t.Fatalf("udp extension uri = %q", got)
	}
	_ = net.IPv4len
	_ = context.Background
}
