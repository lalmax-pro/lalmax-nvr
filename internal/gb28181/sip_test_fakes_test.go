package gb28181

import (
	"context"
	"sync"
	"testing"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// This file contains in-process SIP doubles used by the signaling coverage
// tests. Nothing here opens a socket unless a test explicitly needs one: the
// client side is driven through sipgo.Client.TxRequester and the server side
// through a recording sip.ServerTransaction, so registration, INVITE, BYE and
// catalog exchanges stay fully deterministic.

// fakeTxRequester implements sipgo.ClientTransactionRequester.
type fakeTxRequester struct {
	mu       sync.Mutex
	requests []*sip.Request
	handler  func(req *sip.Request) []*sip.Response
	override func(req *sip.Request) (sip.ClientTransaction, error)
	err      error
}

func (f *fakeTxRequester) Request(_ context.Context, req *sip.Request) (sip.ClientTransaction, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	handler := f.handler
	override := f.override
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if override != nil {
		return override(req)
	}
	if handler == nil {
		return newFakeClientTx([]*sip.Response{okResponse(req)}), nil
	}
	return newFakeClientTx(handler(req)), nil
}

func (f *fakeTxRequester) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeTxRequester) Requests() []*sip.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*sip.Request, len(f.requests))
	copy(out, f.requests)
	return out
}

func (f *fakeTxRequester) LastRequest() *sip.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return nil
	}
	return f.requests[len(f.requests)-1]
}

// fakeClientTx implements sip.ClientTransaction with pre-recorded responses.
type fakeClientTx struct {
	responses chan *sip.Response
	done      chan struct{}
	once      sync.Once
}

func newFakeClientTx(responses []*sip.Response) *fakeClientTx {
	tx := &fakeClientTx{
		responses: make(chan *sip.Response, len(responses)),
		done:      make(chan struct{}),
	}
	for _, res := range responses {
		tx.responses <- res
	}
	if len(responses) == 0 {
		// A closed response channel mirrors a transaction that terminates
		// without producing any response (nil on receive).
		close(tx.responses)
	}
	return tx
}

func (t *fakeClientTx) Responses() <-chan *sip.Response        { return t.responses }
func (t *fakeClientTx) Terminate()                             {}
func (t *fakeClientTx) OnTerminate(sip.FnTxTerminate) bool     { return false }
func (t *fakeClientTx) Done() <-chan struct{}                  { return t.done }
func (t *fakeClientTx) Err() error                             { return nil }
func (t *fakeClientTx) OnRetransmission(sip.FnTxResponse) bool { return false }

// fakeServerTx implements sip.ServerTransaction and records responses.
type fakeServerTx struct {
	mu         sync.Mutex
	responses  []*sip.Response
	done       chan struct{}
	once       sync.Once
	respondErr error
}

func newFakeServerTx() *fakeServerTx {
	return &fakeServerTx{done: make(chan struct{})}
}

func (t *fakeServerTx) Respond(res *sip.Response) error {
	t.mu.Lock()
	t.responses = append(t.responses, res)
	err := t.respondErr
	t.mu.Unlock()
	return err
}

func (t *fakeServerTx) Acks() <-chan *sip.Request          { return nil }
func (t *fakeServerTx) OnCancel(sip.FnTxCancel) bool       { return false }
func (t *fakeServerTx) Terminate()                         { t.once.Do(func() { close(t.done) }) }
func (t *fakeServerTx) OnTerminate(sip.FnTxTerminate) bool { return false }
func (t *fakeServerTx) Done() <-chan struct{}              { return t.done }
func (t *fakeServerTx) Err() error                         { return nil }

func (t *fakeServerTx) StatusCodes() []int {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]int, 0, len(t.responses))
	for _, res := range t.responses {
		out = append(out, res.StatusCode)
	}
	return out
}

func (t *fakeServerTx) LastResponse() *sip.Response {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.responses) == 0 {
		return nil
	}
	return t.responses[len(t.responses)-1]
}

func okResponse(req *sip.Request) *sip.Response {
	return sip.NewResponseFromRequest(req, 200, "OK", nil)
}

// newTestSIPClient returns a sipgo client whose transactions are answered by
// handler (nil means "always 200 OK"). The user agent is closed on cleanup.
func newTestSIPClient(t *testing.T, handler func(req *sip.Request) []*sip.Response) (*sipgo.Client, *fakeTxRequester) {
	t.Helper()
	ua, err := sipgo.NewUA(
		sipgo.WithUserAgent("lalmax-nvr-test"),
		sipgo.WithUserAgentHostname("127.0.0.1"),
	)
	if err != nil {
		t.Fatalf("create test SIP UA: %v", err)
	}
	client, err := sipgo.NewClient(ua, sipgo.WithClientHostname("127.0.0.1"))
	if err != nil {
		ua.Close()
		t.Fatalf("create test SIP client: %v", err)
	}
	req := &fakeTxRequester{handler: handler}
	client.TxRequester = req
	t.Cleanup(func() { ua.Close() })
	return client, req
}

// newTestSIPClientFailing returns a client whose every transaction fails with
// err, which exercises the transport failure branches without any timeout.
func newTestSIPClientFailing(t *testing.T, err error) (*sipgo.Client, *fakeTxRequester) {
	t.Helper()
	client, requester := newTestSIPClient(t, nil)
	requester.err = err
	return client, requester
}

// newTestInboundRequest builds a request as it would arrive from a device,
// including a Via header so req.Source() reports a stable 192.0.2.20:5060.
func newTestInboundRequest(method sip.RequestMethod, fromUser, toUser, body string) *sip.Request {
	req := sip.NewRequest(method, sip.Uri{
		Scheme: "sip",
		User:   toUser,
		Host:   "192.0.2.10",
		Port:   5060,
	})
	req.AppendHeader(&sip.ViaHeader{
		ProtocolName:    "SIP",
		ProtocolVersion: "2.0",
		Transport:       "UDP",
		Host:            "192.0.2.20",
		Port:            5060,
		Params:          sip.HeaderParams(sip.NewParams()),
	})
	req.AppendHeader(&sip.FromHeader{
		Address: sip.Uri{Scheme: "sip", User: fromUser, Host: "3402000000"},
		Params:  sip.HeaderParams(sip.NewParams()),
	})
	req.AppendHeader(&sip.ToHeader{
		Address: sip.Uri{Scheme: "sip", User: toUser, Host: "3402000000"},
		Params:  sip.HeaderParams(sip.NewParams()),
	})
	callID := sip.CallIDHeader("test-call-id")
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: method})
	req.SetTransport("UDP")
	req.SetBody([]byte(body))
	return req
}
