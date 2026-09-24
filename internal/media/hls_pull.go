package media

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gohlslib"
	"github.com/bluenviron/gohlslib/pkg/codecs"
	"github.com/q191201771/lal/pkg/base"
)

const (
	hlsPullRetryInterval   = 2 * time.Second
	hlsPullMaxRetryWait    = 30 * time.Second
	defaultMaxHLSPullTasks = 32
)

var (
	// ErrHLSPullEmbeddedOnly is returned when native HLS pull is used outside embedded mode.
	ErrHLSPullEmbeddedOnly = errors.New("native HLS pull requires media.mode=embedded")
	// ErrHLSPullUnsupportedCodec is returned when the playlist has no H.264/H.265+AAC tracks.
	ErrHLSPullUnsupportedCodec = errors.New("hls pull requires h264/h265 video and optional aac audio")
	// ErrHLSPullBusy is returned when the concurrent pull budget is exhausted.
	ErrHLSPullBusy = errors.New("hls pull concurrency limit reached")
)

// HLSPullManager pulls standard HLS playlists and injects frames into lal.
type HLSPullManager struct {
	engine     Engine
	httpClient *http.Client
	maxTasks   int
	online     func(streamID string, online bool, at time.Time)

	mu    sync.Mutex
	tasks map[string]*hlsPullTask
}

type hlsPullTask struct {
	streamID string
	req      StartHLSPullRequest
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewHLSPullManager creates a native HLS puller bound to an embedded media engine.
func NewHLSPullManager(engine Engine) *HLSPullManager {
	return &HLSPullManager{
		engine: engine,
		httpClient: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          32,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 15 * time.Second,
			},
		},
		maxTasks: defaultMaxHLSPullTasks,
		tasks:    make(map[string]*hlsPullTask),
	}
}

// SetMaxTasks caps how many HLS pulls can run at once. 0 or negative keeps the default.
func (m *HLSPullManager) SetMaxTasks(n int) {
	if m == nil || n <= 0 {
		return
	}
	m.mu.Lock()
	m.maxTasks = n
	m.mu.Unlock()
}

// SetOnlineCallback reports when a pull starts delivering video frames and when
// that frame session ends. A nil callback disables lifecycle notifications.
func (m *HLSPullManager) SetOnlineCallback(callback func(streamID string, online bool, at time.Time)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.online = callback
	m.mu.Unlock()
}

func (m *HLSPullManager) notifyOnline(streamID string, online bool) {
	m.mu.Lock()
	callback := m.online
	m.mu.Unlock()
	if callback != nil {
		callback(streamID, online, time.Now())
	}
}

func (m *HLSPullManager) PullingStreamIDs() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.tasks))
	for id := range m.tasks {
		ids = append(ids, id)
	}
	return ids
}

func (m *HLSPullManager) StartHLSPull(_ context.Context, req StartHLSPullRequest) (*StreamSession, error) {
	if m == nil || m.engine == nil {
		return nil, ErrHLSPullEmbeddedOnly
	}
	streamID := strings.TrimSpace(req.StreamID)
	playlist := strings.TrimSpace(req.PlaylistURL)
	if streamID == "" {
		return nil, errors.New("stream ID is required")
	}
	if playlist == "" {
		return nil, errors.New("playlist URL is required")
	}
	if !engineSupportsCustomizePub(m.engine) {
		return nil, ErrHLSPullEmbeddedOnly
	}

	m.mu.Lock()
	if existing, ok := m.tasks[streamID]; ok {
		m.mu.Unlock()
		return &StreamSession{StreamID: existing.streamID, Protocol: "hls_pull", StartedAt: time.Now()}, nil
	}
	if m.maxTasks > 0 && len(m.tasks) >= m.maxTasks {
		m.mu.Unlock()
		return nil, ErrHLSPullBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	task := &hlsPullTask{
		streamID: streamID,
		req:      req,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	task.req.StreamID = streamID
	task.req.PlaylistURL = playlist
	m.tasks[streamID] = task
	m.mu.Unlock()

	go m.runTask(ctx, task)
	return &StreamSession{StreamID: streamID, Protocol: "hls_pull", StartedAt: time.Now()}, nil
}

func (m *HLSPullManager) StopHLSPull(_ context.Context, streamID string) error {
	streamID = strings.TrimSpace(streamID)
	m.mu.Lock()
	task, ok := m.tasks[streamID]
	if ok {
		delete(m.tasks, streamID)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	task.cancel()
	select {
	case <-task.done:
	case <-time.After(8 * time.Second):
	}
	return nil
}

func (m *HLSPullManager) StopAllHLSPulls() {
	m.mu.Lock()
	tasks := make([]*hlsPullTask, 0, len(m.tasks))
	for id, task := range m.tasks {
		delete(m.tasks, id)
		tasks = append(tasks, task)
	}
	m.mu.Unlock()
	for _, task := range tasks {
		task.cancel()
	}
	for _, task := range tasks {
		select {
		case <-task.done:
		case <-time.After(3 * time.Second):
		}
	}
}

func (m *HLSPullManager) runTask(ctx context.Context, task *hlsPullTask) {
	defer close(task.done)
	defer func() {
		m.mu.Lock()
		if cur, ok := m.tasks[task.streamID]; ok && cur == task {
			delete(m.tasks, task.streamID)
		}
		m.mu.Unlock()
	}()

	attempts := 0
	wait := hlsPullRetryInterval
	for {
		if ctx.Err() != nil {
			return
		}
		attempts++
		err := m.pullOnce(ctx, task)
		if ctx.Err() != nil {
			return
		}
		if task.req.PullRetryNum >= 0 && attempts > task.req.PullRetryNum {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait < hlsPullMaxRetryWait {
			wait *= 2
			if wait > hlsPullMaxRetryWait {
				wait = hlsPullMaxRetryWait
			}
		}
		_ = err
	}
}

func (m *HLSPullManager) pullOnce(ctx context.Context, task *hlsPullTask) error {
	session, err := m.engine.AddCustomizePubSession(ctx, task.streamID)
	if err != nil {
		return err
	}
	session.WithOption(func(option *base.AvPacketStreamOption) {
		option.VideoFormat = base.AvPacketStreamVideoFormatAnnexb
		option.AudioFormat = base.AvPacketStreamAudioFormatRawAac
	})
	defer func() {
		_ = m.engine.DelCustomizePubSession(context.Background(), session)
	}()

	client := &gohlslib.Client{
		URI:                       task.req.PlaylistURL,
		HTTPClient:                headerClient(m.httpClient, task.req.Headers),
		OnDownloadPrimaryPlaylist: func(string) {},
		OnDownloadStreamPlaylist:  func(string) {},
		OnDownloadSegment:         func(string) {},
		OnDownloadPart:            func(string) {},
		OnDecodeError:             func(error) {},
	}
	var onlineOnce sync.Once
	var onlineMu sync.Mutex
	wasOnline := false
	markOnline := func() {
		onlineOnce.Do(func() {
			onlineMu.Lock()
			wasOnline = true
			onlineMu.Unlock()
			m.notifyOnline(task.streamID, true)
		})
	}
	defer func() {
		onlineMu.Lock()
		wasOnline := wasOnline
		onlineMu.Unlock()
		if wasOnline {
			m.notifyOnline(task.streamID, false)
		}
	}()
	client.OnTracks = func(tracks []*gohlslib.Track) error {
		return attachHLSTracks(client, session, tracks, markOnline)
	}
	if err := client.Start(); err != nil {
		return err
	}
	defer client.Close()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-client.Wait():
		return err
	}
}

func attachHLSTracks(client *gohlslib.Client, session CustomizePubSession, tracks []*gohlslib.Track, onVideoFrame ...func()) error {
	info, err := ClassifyHLSTracks(tracks)
	if err != nil {
		return err
	}
	if info.VideoParamSets != nil {
		_ = session.FeedAvPacket(base.AvPacket{
			Payload:     info.VideoParamSets,
			PayloadType: info.VideoPT,
			Pts:         0,
			Timestamp:   0,
		})
	}
	if len(info.AudioASC) > 0 {
		if err := session.FeedAudioSpecificConfig(info.AudioASC); err != nil {
			return err
		}
	}
	for _, track := range tracks {
		switch codec := track.Codec.(type) {
		case *codecs.H264, *codecs.H265:
			pt := info.VideoPT
			client.OnDataH26x(track, func(pts, dts time.Duration, au [][]byte) {
				payload := AnnexBFromAU(au)
				if len(payload) == 0 {
					return
				}
				if len(onVideoFrame) > 0 && onVideoFrame[0] != nil {
					onVideoFrame[0]()
				}
				_ = session.FeedAvPacket(base.AvPacket{
					Payload:     payload,
					PayloadType: pt,
					Pts:         pts.Milliseconds(),
					Timestamp:   dts.Milliseconds(),
				})
			})
		case *codecs.MPEG4Audio:
			sampleRate := codec.SampleRate
			if sampleRate <= 0 {
				sampleRate = 44100
			}
			client.OnDataMPEG4Audio(track, func(pts time.Duration, aus [][]byte) {
				ts := pts.Milliseconds()
				step := int64(0)
				if sampleRate > 0 {
					step = int64(1024 * 1000 / sampleRate)
				}
				for i, au := range aus {
					_ = session.FeedAvPacket(base.AvPacket{
						Payload:     au,
						PayloadType: base.AvPacketPtAac,
						Pts:         ts + int64(i)*step,
						Timestamp:   ts + int64(i)*step,
					})
				}
			})
			_ = codec
		}
	}
	return nil
}

// HLSTrackInfo is the codec summary extracted from a gohlslib track list.
type HLSTrackInfo struct {
	VideoCodec      string
	AudioCodec      string
	VideoPT         base.AvPacketPt
	VideoParamSets  []byte
	AudioASC        []byte
	Playable        bool
	Recordable      bool
	UnsupportedHint string
}

// ClassifyHLSTracks maps gohlslib tracks onto lal-friendly codec metadata.
func ClassifyHLSTracks(tracks []*gohlslib.Track) (HLSTrackInfo, error) {
	var info HLSTrackInfo
	for _, track := range tracks {
		if track == nil {
			continue
		}
		switch codec := track.Codec.(type) {
		case *codecs.H264:
			info.VideoCodec = "h264"
			info.VideoPT = base.AvPacketPtAvc
			info.VideoParamSets = AnnexBFromAU([][]byte{codec.SPS, codec.PPS})
		case *codecs.H265:
			info.VideoCodec = "h265"
			info.VideoPT = base.AvPacketPtHevc
			info.VideoParamSets = AnnexBFromAU([][]byte{codec.VPS, codec.SPS, codec.PPS})
		case *codecs.MPEG4Audio:
			info.AudioCodec = "aac"
			if asc, err := codec.Marshal(); err == nil {
				info.AudioASC = asc
			}
		case *codecs.Opus:
			info.UnsupportedHint = "opus audio is not supported"
		case *codecs.AV1:
			info.UnsupportedHint = "av1 video is not supported"
		case *codecs.VP9:
			info.UnsupportedHint = "vp9 video is not supported"
		}
	}
	switch info.VideoCodec {
	case "h264", "h265":
		info.Playable = true
		info.Recordable = true
		return info, nil
	default:
		if info.UnsupportedHint == "" {
			info.UnsupportedHint = ErrHLSPullUnsupportedCodec.Error()
		}
		return info, fmt.Errorf("%w: %s", ErrHLSPullUnsupportedCodec, info.UnsupportedHint)
	}
}

// AnnexBFromAU concatenates NAL units with 4-byte start codes.
func AnnexBFromAU(au [][]byte) []byte {
	total := 0
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		total += 4 + len(nalu)
	}
	if total == 0 {
		return nil
	}
	out := make([]byte, 0, total)
	start := []byte{0, 0, 0, 1}
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		out = append(out, start...)
		out = append(out, nalu...)
	}
	return out
}

func headerClient(baseClient *http.Client, headers map[string]string) *http.Client {
	if baseClient == nil {
		baseClient = http.DefaultClient
	}
	if len(headers) == 0 {
		return baseClient
	}
	transport := baseClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &http.Client{
		Timeout:   baseClient.Timeout,
		Transport: &headerRoundTripper{base: transport, headers: headers},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

func engineSupportsCustomizePub(engine Engine) bool {
	if engine == nil {
		return false
	}
	if _, ok := engine.(*LalmaxHTTP); ok {
		return false
	}
	return true
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			continue
		}
		req.Header.Set(k, v)
	}
	return h.base.RoundTrip(req)
}
