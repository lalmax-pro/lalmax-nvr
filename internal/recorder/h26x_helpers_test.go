package recorder

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtplpcm"
	"github.com/stretchr/testify/require"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
)

// hxStore is a file-based SegmentStore for the MP4 (H264/H265) recorders.
// Unlike the MJPEG stores a segment is a single file: the muxer writes the temp
// path and CloseSegment atomically renames it to the final path.
type hxStore struct {
	mu sync.Mutex

	dir    string
	seq    int
	closed int

	createErr error
	closeErr  error
	tempAsDir bool // return an existing directory as temp path to break muxer writes
}

func newHXStore(t *testing.T) *hxStore {
	t.Helper()
	return &hxStore{dir: t.TempDir()}
}

func (s *hxStore) CreateSegment(cameraID string, format string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return "", "", s.createErr
	}
	s.seq++
	if s.tempAsDir {
		return s.dir, filepath.Join(s.dir, fmt.Sprintf("%s-final-%03d.mp4", cameraID, s.seq)), nil
	}
	base := filepath.Join(s.dir, fmt.Sprintf("%s-%s-%03d", cameraID, format, s.seq))
	return base + ".tmp.mp4", base + ".mp4", nil
}

func (s *hxStore) WriteFrame(_ string, data []byte) (int, error) {
	return len(data), nil
}

func (s *hxStore) CloseSegment(tempPath, finalPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeErr != nil {
		return s.closeErr
	}
	s.closed++
	return os.Rename(tempPath, finalPath)
}

func (s *hxStore) stats() (segments, closed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq, s.closed
}

// hxFeedH265 pushes annexB-wrapped NALUs through the H265 writer synchronously
// and returns once every NALU was processed.
func hxFeedH265(rec *H265Recorder, nals ...[]byte) {
	rec.frameCh = make(chan []byte, len(nals))
	for _, nal := range nals {
		rec.frameCh <- annexB(nal)
	}
	close(rec.frameCh)
	rec.writeFrames(make(chan struct{}))
}

// hxFeedH264 pushes annexB-wrapped NALUs through the H264 writer synchronously.
func hxFeedH264(rec *H264Recorder, nals ...[]byte) {
	rec.frameCh = make(chan []byte, len(nals))
	for _, nal := range nals {
		rec.frameCh <- annexB(nal)
	}
	close(rec.frameCh)
	rec.writeFrames(make(chan struct{}))
}

// hxAudioFrame builds an audio MediaFrame for the frame-source consumers.
func hxAudioFrame(codec string, data []byte) media.MediaFrame {
	return media.MediaFrame{Kind: media.FrameAudio, Codec: codec, Data: data}
}

// hxSub is a controllable FrameSubscription whose Close never touches the
// channels, so tests can close Frames()/Err() themselves.
type hxSub struct {
	ch   chan media.MediaFrame
	errc chan error
}

func (s *hxSub) Frames() <-chan media.MediaFrame { return s.ch }
func (s *hxSub) Err() <-chan error               { return s.errc }
func (s *hxSub) Close() error                    { return nil }

func newHXSub() *hxSub {
	return &hxSub{ch: make(chan media.MediaFrame, 8), errc: make(chan error, 4)}
}

// hxClosableSub closes its Frames channel on Close, emulating a real
// subscription torn down by the frame watchdog.
type hxClosableSub struct {
	*hxSub
	once sync.Once
}

func newHXClosableSub() *hxClosableSub {
	return &hxClosableSub{hxSub: &hxSub{ch: make(chan media.MediaFrame), errc: make(chan error)}}
}

func (s *hxClosableSub) Close() error {
	s.once.Do(func() { close(s.ch) })
	return nil
}

// hxG711Server is an in-process RTSP server exposing a video format plus a
// G.711 (μ-law) audio track. It is used to exercise the G.711 setup and RTP
// handling paths of the H264/H265 recorders.
type hxG711Server struct {
	server     *gortsplib.Server
	stream     *gortsplib.ServerStream
	audioMedia *description.Media
	audioEnc   *rtplpcm.Encoder
	playCh     chan struct{}
	rtspURL    string
}

func newHXG711Server(t *testing.T, videoFormat format.Format, muLaw bool) *hxG711Server {
	t.Helper()
	port := findPort(t)
	time.Sleep(5 * time.Millisecond)

	// Payload type 0 means μ-law (PCMU), 8 means A-law (PCMA) on the wire.
	payloadTyp := uint8(8)
	if muLaw {
		payloadTyp = 0
	}
	audioForma := &format.G711{PayloadTyp: payloadTyp, MULaw: muLaw, SampleRate: 8000, ChannelCount: 1}
	desc := &description.Session{
		Medias: []*description.Media{
			{Type: description.MediaTypeVideo, Formats: []format.Format{videoFormat}},
			{Type: description.MediaTypeAudio, Formats: []format.Format{audioForma}},
		},
	}

	s := &hxG711Server{playCh: make(chan struct{})}
	s.audioMedia = desc.Medias[1]
	enc, err := audioForma.CreateEncoder()
	require.NoError(t, err)
	s.audioEnc = enc

	s.server = &gortsplib.Server{Handler: s, RTSPAddress: fmt.Sprintf("127.0.0.1:%d", port)}
	require.NoError(t, s.server.Start())

	s.stream = &gortsplib.ServerStream{Server: s.server, Desc: desc}
	require.NoError(t, s.stream.Initialize())
	s.rtspURL = fmt.Sprintf("rtsp://127.0.0.1:%d/live", port)
	t.Cleanup(s.close)
	return s
}

func (s *hxG711Server) close() {
	s.stream.Close()
	s.server.Close()
}

func (s *hxG711Server) OnDescribe(*gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *hxG711Server) OnSetup(*gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *hxG711Server) OnPlay(*gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	select {
	case <-s.playCh:
	default:
		close(s.playCh)
	}
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func (s *hxG711Server) waitPlay(t *testing.T) {
	t.Helper()
	select {
	case <-s.playCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for PLAY")
	}
}

func (s *hxG711Server) sendAudio(t *testing.T, data []byte) {
	t.Helper()
	pkts, err := s.audioEnc.Encode(data)
	require.NoError(t, err)
	for _, pkt := range pkts {
		s.stream.WritePacketRTP(s.audioMedia, pkt)
	}
}
