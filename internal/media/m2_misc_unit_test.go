package media

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/q191201771/lal/pkg/base"
	maxvoip "github.com/q191201771/lalmax/voip"
	rtpmedia "github.com/q191201771/lalmax/voip/media"
	"github.com/q191201771/lalmax/voip/sdp"
	"github.com/stretchr/testify/require"

	"github.com/lalmax-pro/lalmax-nvr/internal/wsstream"
)

func TestM2RtmpFramesBranches(t *testing.T) {
	// Too short and unknown message types are dropped.
	require.Nil(t, rtmpMsgToFrames(base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: []byte{1}}))
	require.Nil(t, rtmpMsgToFrames(base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: 99}, Payload: []byte{1, 2}}))

	// AVC NALU without room for the payload returns nil.
	require.Nil(t, rtmpMsgToFrames(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo},
		Payload: []byte{0x27, 0x01, 0x00, 0x00, 0x00},
	}))

	// Malformed AVCC length fails the split.
	require.Nil(t, rtmpMsgToFrames(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo},
		Payload: []byte{0x17, 0x01, 0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0xff, 0x65},
	}))

	// A zero-length NAL is skipped while the following NAL is kept.
	zeroNal := []byte{0x17, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x65}
	frames := rtmpMsgToFrames(base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: zeroNal})
	require.Len(t, frames, 1)
	require.True(t, frames[0].IsKey)

	// Malformed AVC sequence header.
	require.Nil(t, videoSeqHeaderFrames(base.RtmpMsg{
		Payload: []byte{0x17, 0x00, 0x00, 0x00, 0x00, 0x01, 0x42},
	}, 0))

	// Malformed HEVC sequence headers (plain and enhanced fourcc).
	require.Nil(t, videoSeqHeaderFrames(base.RtmpMsg{Payload: []byte{0x1c, 0x00, 0x00, 0x00, 0x00}}, 0))
	badRecord := make([]byte, 34)
	badRecord[0] = 0x1c
	badRecord[27] = 2
	require.Nil(t, videoSeqHeaderFrames(base.RtmpMsg{Payload: badRecord}, 0))
	enhancedBad := make([]byte, 34)
	enhancedBad[0] = 0x90
	enhancedBad[27] = 2
	require.Nil(t, videoSeqHeaderFrames(base.RtmpMsg{Payload: enhancedBad}, 0))

	// A valid HEVC record yields VPS/SPS/PPS in order.
	record := make([]byte, 28)
	record[0] = 0x1c
	record[27] = 3
	add := func(nalType byte, body []byte) {
		record = append(record, nalType, 0x00, 0x01, byte(len(body)>>8), byte(len(body)))
		record = append(record, body...)
	}
	add(32, []byte{0xaa, 0xbb})
	add(33, []byte{0xcc})
	add(34, []byte{0xdd, 0xee, 0xff})
	hevcFrames := videoSeqHeaderFrames(base.RtmpMsg{Payload: record}, 7*time.Millisecond)
	require.Len(t, hevcFrames, 3)
	require.Equal(t, "h265", hevcFrames[0].Codec)
	require.Equal(t, []byte{0, 0, 0, 1, 0xaa, 0xbb}, hevcFrames[0].Data)
	require.Equal(t, []byte{0, 0, 0, 1, 0xcc}, hevcFrames[1].Data)
	require.Equal(t, []byte{0, 0, 0, 1, 0xdd, 0xee, 0xff}, hevcFrames[2].Data)

	// Enhanced HEVC sequence header hits the fourcc parser.
	enhanced := append([]byte{0x90}, record[1:]...)
	require.Len(t, videoSeqHeaderFrames(base.RtmpMsg{Payload: enhanced}, 0), 3)

	// Empty parameter sets produce no frames.
	empty := make([]byte, 28)
	empty[0] = 0x1c
	empty[27] = 3
	empty = append(empty, 32, 0, 1, 0, 0, 33, 0, 1, 0, 0, 34, 0, 1, 0, 0)
	require.Empty(t, videoSeqHeaderFrames(base.RtmpMsg{Payload: empty}, 0))

	// Audio branches.
	require.Nil(t, rtmpMsgToFrames(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdAudio},
		Payload: []byte{0xaf, 0x01},
	}))
	require.Nil(t, rtmpAudioToFrames(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdAudio},
		Payload: []byte{0x70},
	}))
	g711a := rtmpMsgToFrames(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdAudio, TimestampAbs: 5},
		Payload: []byte{byte(base.RtmpSoundFormatG711A) << 4, 0x11, 0x22},
	})
	require.Len(t, g711a, 1)
	require.Equal(t, "g711a", g711a[0].Codec)
	require.Equal(t, []byte{0x11, 0x22}, g711a[0].Data)
	g711u := rtmpMsgToFrames(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdAudio},
		Payload: []byte{byte(base.RtmpSoundFormatG711U) << 4, 0x33},
	})
	require.Len(t, g711u, 1)
	require.Equal(t, "g711u", g711u[0].Codec)
	require.Nil(t, rtmpMsgToFrames(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdAudio},
		Payload: []byte{0x20, 0x01, 0x02},
	}))
	require.False(t, isVideoKeyframeNALU(nil, false))
}

func TestM2InProcessTransportBranches(t *testing.T) {
	// Nil fallback uses the default transport for non-play paths.
	rt := NewInProcessPlayTransport(nil, nil)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:1/live/cam.flv", nil)
	require.NoError(t, err)
	_, err = rt.RoundTrip(req)
	require.Error(t, err)

	// Nil requests and nil URLs are delegated to the fallback.
	fb := &fallbackTripper{}
	rt = NewInProcessPlayTransport(staticPlayProvider{h: http.NotFoundHandler()}, fb)
	_, err = rt.RoundTrip(nil)
	require.NoError(t, err)
	require.True(t, fb.called)
	fb.called = false
	_, err = rt.RoundTrip(&http.Request{})
	require.NoError(t, err)
	require.True(t, fb.called)

	// A panic inside the handler becomes a 500 response with a failing body.
	panicking := NewInProcessPlayTransport(staticPlayProvider{h: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})}, fb)
	req, err = http.NewRequest(http.MethodGet, "http://127.0.0.1/live/hls/cam/index.m3u8", nil)
	require.NoError(t, err)
	resp, err := panicking.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	_, err = io.ReadAll(resp.Body)
	require.Error(t, err)
	require.NoError(t, resp.Body.Close())

	// Cancelling the request before headers are written returns the context error.
	ctx, cancel := context.WithCancel(context.Background())
	blocking := NewInProcessPlayTransport(staticPlayProvider{h: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})}, fb)
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1/live/hls/cam/index.m3u8", nil)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := blocking.RoundTrip(req)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("RoundTrip did not observe cancellation")
	}
}

func TestM2PipeResponseWriterStates(t *testing.T) {
	pr, pw := io.Pipe()
	w := &pipeResponseWriter{header: make(http.Header), pw: pw, headerSent: make(chan struct{})}
	w.Header().Set("X-Test", "1")
	require.Equal(t, http.StatusOK, w.statusCode())
	require.Equal(t, "1", w.headerSnapshot().Get("X-Test"))

	// A zero code means OK and later write headers are ignored.
	w.WriteHeader(0)
	w.WriteHeader(http.StatusTeapot)
	require.Equal(t, http.StatusOK, w.statusCode())
	<-w.headerSent

	go func() { _, _ = io.Copy(io.Discard, pr) }()
	_, err := w.Write([]byte("body"))
	require.NoError(t, err)
	w.Flush()
	w.finish()
	require.NoError(t, pr.Close())

	// Header snapshots before WriteHeader clone the live header map.
	pr2, pw2 := io.Pipe()
	w2 := &pipeResponseWriter{header: make(http.Header), pw: pw2, headerSent: make(chan struct{})}
	w2.Header().Set("X-Late", "yes")
	require.Equal(t, "yes", w2.headerSnapshot().Get("X-Late"))
	w2.WriteHeader(http.StatusAccepted)
	w2.Header().Set("X-Late", "changed")
	require.Equal(t, "yes", w2.headerSnapshot().Get("X-Late"))
	w2.abort(errors.New("aborted"))
	_, err = io.ReadAll(pr2)
	require.Error(t, err)
	require.Equal(t, http.StatusAccepted, w2.statusCode())

	// Aborting before headers yields 500, and finishing without headers yields 200.
	pr3, pw3 := io.Pipe()
	w3 := &pipeResponseWriter{header: make(http.Header), pw: pw3, headerSent: make(chan struct{})}
	w3.abort(errors.New("early abort"))
	require.Equal(t, http.StatusInternalServerError, w3.statusCode())
	_, _ = io.Copy(io.Discard, pr3)

	pr4, pw4 := io.Pipe()
	w4 := &pipeResponseWriter{header: make(http.Header), pw: pw4, headerSent: make(chan struct{})}
	w4.finish()
	require.Equal(t, http.StatusOK, w4.statusCode())
	require.NoError(t, pr4.Close())
}

type m2WS struct {
	WS
	stopped int
}

func (m *m2WS) StopAll() { m.stopped++ }

func TestM2RuntimeStartStop(t *testing.T) {
	require.NoError(t, (&Runtime{}).Start(context.Background()))
	require.NoError(t, (&Runtime{}).Stop())
	ws := &m2WS{}
	r := &Runtime{ws: ws}
	require.NoError(t, r.Stop())
	require.Equal(t, 1, ws.stopped)
}

func TestM2FramesHelpers(t *testing.T) {
	require.Equal(t, "live", streamAppName(SubscribeFramesRequest{}))
	require.Equal(t, "app", streamAppName(SubscribeFramesRequest{AppName: "app"}))

	ctx, cancel := waitContext(nil)
	require.NoError(t, ctx.Err())
	cancel()

	parent, parentCancel := context.WithTimeout(context.Background(), time.Minute)
	defer parentCancel()
	ctx, cancel = waitContext(parent)
	require.NoError(t, ctx.Err())
	cancel()

	out := make(chan Event, 1)
	poller := NewStreamPoller(listEngine{err: errors.New("poll failed")}, time.Hour)
	poller.poll(context.Background(), map[string]bool{"live/cam": true}, out)
	require.Empty(t, out)
	_ = wsstream.AudioCodecInfo{}
}

type m2VoipPubEngine struct {
	Engine
	pub     CustomizePubSession
	addErr  error
	removed int
}

func (e *m2VoipPubEngine) AddCustomizePubSession(context.Context, string) (CustomizePubSession, error) {
	if e.addErr != nil {
		return nil, e.addErr
	}
	return e.pub, nil
}

func (e *m2VoipPubEngine) DelCustomizePubSession(context.Context, CustomizePubSession) error {
	e.removed++
	return nil
}

type m2VoipPub struct {
	option    base.AvPacketStreamOption
	ascErr    error
	feedErr   error
	resetErr  error
	ascCalls  int
	feedCalls int
}

func (p *m2VoipPub) WithOption(f func(*base.AvPacketStreamOption)) { f(&p.option) }
func (p *m2VoipPub) FeedAudioSpecificConfig(asc []byte) error {
	p.ascCalls++
	return p.ascErr
}
func (p *m2VoipPub) FeedAvPacket(base.AvPacket) error { p.feedCalls++; return p.feedErr }
func (p *m2VoipPub) FeedRtmpMsg(base.RtmpMsg) error   { return nil }
func (p *m2VoipPub) ResetMedia() error                { return p.resetErr }

func m2VoipSession(t *testing.T, n sdp.Negotiated) *maxvoip.PubSession {
	t.Helper()
	session, err := maxvoip.NewPubSession(maxvoip.PubSessionConfig{
		StreamName: "m2-voip", MediaIP: "127.0.0.1",
		PortAllocator: rtpmedia.NewPortAllocator(43200, 43300),
		Negotiated:    n,
		OnAvPacket:    func(base.AvPacket) {},
	})
	require.NoError(t, err)
	t.Cleanup(func() { session.Dispose() })
	return session
}

func TestM2VoIPPublisherErrorBranches(t *testing.T) {
	aac := func(fmtp string) sdp.Negotiated {
		return sdp.Negotiated{Audio: &sdp.Payload{PayloadType: 97, Codec: base.AvPacketPtAac, ClockRate: 48000, CodecName: "MPEG4-GENERIC", Fmtp: fmtp}}
	}
	aacSession := m2VoipSession(t, aac("config=1190"))
	g711Session := m2VoipSession(t, sdp.Negotiated{Audio: &sdp.Payload{PayloadType: 8, Codec: base.AvPacketPtG711A, ClockRate: 8000, CodecName: "PCMA"}})

	// Invalid hexadecimal ASC.
	badHex := m2VoipSession(t, aac("config=zz"))
	_, err := voipASC(badHex)
	require.Error(t, err)
	require.Error(t, NewVoIPPublisher(&m2VoipPubEngine{pub: &m2VoipPub{}}).Add(badHex))

	// Missing ASC.
	short := m2VoipSession(t, aac("config=11"))
	_, err = voipASC(short)
	require.Error(t, err)

	// Add failures: engine registration and audio config feed.
	require.Error(t, NewVoIPPublisher(&m2VoipPubEngine{addErr: errors.New("no engine")}).Add(g711Session))
	engine := &m2VoipPubEngine{pub: &m2VoipPub{ascErr: errors.New("asc rejected")}}
	require.Error(t, NewVoIPPublisher(engine).Add(aacSession))
	require.Equal(t, 1, engine.removed)

	// Replace failures: unknown publisher, missing ASC, reset failure and feed failure.
	publisher := NewVoIPPublisher(&m2VoipPubEngine{pub: &m2VoipPub{}})
	require.Error(t, publisher.Replace(g711Session, aacSession))
	unknown := m2VoipSession(t, sdp.Negotiated{Audio: &sdp.Payload{PayloadType: 8, Codec: base.AvPacketPtG711A, ClockRate: 8000, CodecName: "PCMA"}})
	require.Error(t, publisher.Replace(unknown, badHex))

	resetPub := &m2VoipPub{resetErr: errors.New("reset failed")}
	resetEngine := &m2VoipPubEngine{pub: resetPub}
	resetPublisher := NewVoIPPublisher(resetEngine)
	require.NoError(t, resetPublisher.Add(g711Session))
	videoSession := m2VoipSession(t, sdp.Negotiated{
		Audio: &sdp.Payload{PayloadType: 8, Codec: base.AvPacketPtG711A, ClockRate: 8000, CodecName: "PCMA"},
		Video: &sdp.Payload{PayloadType: 96, Codec: base.AvPacketPtAvc, ClockRate: 90000, CodecName: "H264"},
	})
	require.Error(t, resetPublisher.Replace(g711Session, videoSession))

	feedPub := &m2VoipPub{ascErr: errors.New("asc failed")}
	feedPublisher := NewVoIPPublisher(&m2VoipPubEngine{pub: feedPub})
	require.NoError(t, feedPublisher.Add(g711Session))
	require.Error(t, feedPublisher.Replace(g711Session, aacSession))

	// Remove of an unknown session is a no-op.
	require.NoError(t, publisher.Remove(unknown))
}

func TestM2LalmaxHTTPEdgeBranches(t *testing.T) {
	// nil contexts surface request construction errors.
	plain, err := NewLalmaxHTTP(LalmaxHTTPConfig{BaseURL: "http://127.0.0.1:1", PublicURL: "https://example.test"})
	require.NoError(t, err)
	//nolint:staticcheck // nil context is the behavior under test
	_, err = plain.SubscribeEvents(nil, EventFilter{})
	require.Error(t, err)
	//nolint:staticcheck
	require.Error(t, plain.getJSON(nil, "/api/x", nil, nil))
	//nolint:staticcheck
	require.Error(t, plain.postJSON(nil, "/api/x", nil, nil, nil))
	require.Error(t, plain.postJSON(context.Background(), "/api/x", nil, func() {}, nil))

	// A non-2xx hook stream response is rejected.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer bad.Close()
	badEngine, err := NewLalmaxHTTP(LalmaxHTTPConfig{BaseURL: bad.URL})
	require.NoError(t, err)
	_, err = badEngine.SubscribeEvents(context.Background(), EventFilter{Types: []EventType{EventStreamStopped}})
	require.Error(t, err)

	// Transport failures are returned.
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closed.Close()
	closedEngine, err := NewLalmaxHTTP(LalmaxHTTPConfig{BaseURL: closed.URL})
	require.NoError(t, err)
	_, err = closedEngine.SubscribeEvents(context.Background(), EventFilter{})
	require.Error(t, err)

	// WS-FLV over an HTTPS public URL upgrades the scheme.
	req, err := plain.BuildPlayURL(context.Background(), PlayURLRequest{StreamID: "cam", AppName: "live", Protocol: "ws-flv"})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(req.URL, "wss://example.test:18080/live/cam.flv"), req.URL)

	require.Equal(t, 0.0, firstNonZero(0, 0))
	require.Equal(t, 2.0, firstNonZero(0, 2, 3))
	require.Equal(t, "custom", mediaEventToHook(EventType("custom")))

	// HTTP-mode engines do not support custom publish sessions.
	_, err = plain.AddCustomizePubSession(context.Background(), "cam")
	require.Error(t, err)
	require.Error(t, plain.DelCustomizePubSession(context.Background(), nil))
}
