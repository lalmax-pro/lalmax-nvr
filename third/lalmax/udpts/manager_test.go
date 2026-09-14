package udpts

import (
	"testing"

	"github.com/asticode/go-astits"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
)

type fakePub struct {
	uk         string
	streamName string
	packets    []base.AvPacket
}

func (p *fakePub) WithOption(func(option *base.AvPacketStreamOption)) {}
func (p *fakePub) FeedAudioSpecificConfig([]byte) error               { return nil }
func (p *fakePub) FeedAvPacket(packet base.AvPacket) error {
	p.packets = append(p.packets, packet)
	return nil
}
func (p *fakePub) FeedRtmpMsg(base.RtmpMsg) error { return nil }
func (p *fakePub) UniqueKey() string              { return p.uk }
func (p *fakePub) StreamName() string             { return p.streamName }

type fakeHost struct {
	pubs map[string]*fakePub
}

func newFakeHost() *fakeHost {
	return &fakeHost{pubs: make(map[string]*fakePub)}
}

func (h *fakeHost) AddCustomizePubSession(streamName string) (logic.ICustomizePubSessionContext, error) {
	if _, ok := h.pubs[streamName]; ok {
		return nil, base.ErrDupInStream
	}
	pub := &fakePub{uk: "CUSTOMIZEPUB0", streamName: streamName}
	h.pubs[streamName] = pub
	return pub, nil
}

func (h *fakeHost) DelCustomizePubSession(ctx logic.ICustomizePubSessionContext) {
	if ctx == nil {
		return
	}
	delete(h.pubs, ctx.StreamName())
}

func TestIsUDPURL(t *testing.T) {
	if !IsUDPURL("udp://239.1.1.1:5004") {
		t.Fatal("expected udp url")
	}
	if !IsUDPURL("UDP://239.1.1.1:5004?interface=en0") {
		t.Fatal("expected UDP url")
	}
	if IsUDPURL("rtsp://127.0.0.1/live/s") {
		t.Fatal("did not expect rtsp url")
	}
	if IsUDPURL("not a url") {
		t.Fatal("did not expect invalid url")
	}
}

func TestResolveStreamName(t *testing.T) {
	if got := resolveStreamName(base.ApiCtrlStartRelayPullReq{Url: "udp://239.1.1.1:5004", StreamName: "cam1"}); got != "cam1" {
		t.Fatalf("got %q", got)
	}
	if got := resolveStreamName(base.ApiCtrlStartRelayPullReq{Url: "udp://239.1.1.1:5004"}); got != "239.1.1.1_5004" {
		t.Fatalf("got %q", got)
	}
}

func TestParseProgramID(t *testing.T) {
	if got := ParseProgramID("udp://239.1.1.1:5004"); got != 0 {
		t.Fatalf("got %d", got)
	}
	if got := ParseProgramID("udp://239.1.1.1:5004?program=3"); got != 3 {
		t.Fatalf("got %d", got)
	}
	if got := ParseProgramID("udp://239.1.1.1:5004?interface=en0&program_id=7"); got != 7 {
		t.Fatalf("got %d", got)
	}
	if got := ParseProgramID("udp://239.1.1.1:5004?pn=2"); got != 2 {
		t.Fatalf("got %d", got)
	}
	if got := ParseProgramID(ApplyProgramID("udp://239.1.1.1:5004?interface=en0", 9)); got != 9 {
		t.Fatalf("got %d", got)
	}
}

func TestManagerStartStopUnicast(t *testing.T) {
	host := newFakeHost()
	mgr := NewManager(host)

	resp := mgr.Start(base.ApiCtrlStartRelayPullReq{
		Url:           "udp://127.0.0.1:0",
		StreamName:    "cam-udp",
		PullTimeoutMs: 1000,
		PullRetryNum:  base.PullRetryNumNever,
	})
	if resp.ErrorCode != base.ErrorCodeSucc {
		t.Fatalf("start failed: %+v", resp)
	}
	if resp.Data.StreamName != "cam-udp" || resp.Data.SessionId == "" {
		t.Fatalf("unexpected resp data: %+v", resp.Data)
	}

	dup := mgr.Start(base.ApiCtrlStartRelayPullReq{
		Url:        "udp://127.0.0.1:0",
		StreamName: "cam-udp",
	})
	if dup.ErrorCode != base.ErrorCodeStartRelayPullFail {
		t.Fatalf("expected duplicate fail, got %+v", dup)
	}

	sessionID, err := mgr.Stop("cam-udp")
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != resp.Data.SessionId {
		t.Fatalf("session id mismatch: %s vs %s", sessionID, resp.Data.SessionId)
	}

	if _, err = mgr.Stop("cam-udp"); err != ErrSessionNotFound {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestTsFrameFeederVideo(t *testing.T) {
	pub := &fakePub{uk: "p", streamName: "s"}
	f := &tsFrameFeeder{ss: pub}
	f.OnFrame(astits.StreamTypeH264Video, []byte{0x00, 0x00, 0x00, 0x01, 0x65}, 1000, 900)
	if len(pub.packets) != 1 {
		t.Fatalf("got %d packets", len(pub.packets))
	}
	if pub.packets[0].PayloadType != base.AvPacketPtAvc {
		t.Fatalf("payload type %v", pub.packets[0].PayloadType)
	}
	if pub.packets[0].Pts != 1000 || pub.packets[0].Timestamp != 900 {
		t.Fatalf("pts/dts mismatch: %+v", pub.packets[0])
	}
}

func TestProgramSelectorSpecified(t *testing.T) {
	sel := newProgramSelector(2, "t")
	sel.onPAT(&astits.PATData{Programs: []*astits.PATProgram{
		{ProgramNumber: 1, ProgramMapID: 0x100},
		{ProgramNumber: 2, ProgramMapID: 0x200},
	}})
	sel.onPMT(&astits.PMTData{
		ProgramNumber: 1,
		ElementaryStreams: []*astits.PMTElementaryStream{
			{ElementaryPID: 0x101, StreamType: astits.StreamTypeH264Video},
		},
	})
	if _, ok := sel.streamType(0x101); ok {
		t.Fatal("program 1 should be ignored")
	}
	sel.onPMT(&astits.PMTData{
		ProgramNumber: 2,
		ElementaryStreams: []*astits.PMTElementaryStream{
			{ElementaryPID: 0x201, StreamType: astits.StreamTypeH265Video},
			{ElementaryPID: 0x202, StreamType: astits.StreamTypeAACAudio},
		},
	})
	if st, ok := sel.streamType(0x201); !ok || st != astits.StreamTypeH265Video {
		t.Fatalf("expected hevc pid, got %v %v", st, ok)
	}
	if !sel.skipPID(0x101) {
		t.Fatal("other program es should be skipped")
	}
	if sel.skipPID(0x201) || sel.skipPID(0) || sel.skipPID(0x200) {
		t.Fatal("selected pids should not be skipped")
	}
}

func TestProgramSelectorAutoPrefersVideo(t *testing.T) {
	sel := newProgramSelector(0, "t")
	sel.onPAT(&astits.PATData{Programs: []*astits.PATProgram{
		{ProgramNumber: 1, ProgramMapID: 0x100},
		{ProgramNumber: 2, ProgramMapID: 0x200},
	}})
	sel.onPMT(&astits.PMTData{
		ProgramNumber: 1,
		ElementaryStreams: []*astits.PMTElementaryStream{
			{ElementaryPID: 0x101, StreamType: astits.StreamTypeAACAudio},
		},
	})
	if sel.selected != 0 {
		t.Fatal("should wait for a video program")
	}
	sel.onPMT(&astits.PMTData{
		ProgramNumber: 2,
		ElementaryStreams: []*astits.PMTElementaryStream{
			{ElementaryPID: 0x201, StreamType: astits.StreamTypeH264Video},
		},
	})
	if sel.selected != 2 {
		t.Fatalf("expected program 2, got %d", sel.selected)
	}
}

func TestPesTimeMs(t *testing.T) {
	pts, dts := pesTimeMs(&astits.PESData{Header: &astits.PESHeader{
		OptionalHeader: &astits.PESOptionalHeader{
			PTS: &astits.ClockReference{Base: 90000},
			DTS: &astits.ClockReference{Base: 81000},
		},
	}})
	if pts != 1000 || dts != 900 {
		t.Fatalf("got pts=%d dts=%d", pts, dts)
	}
}
