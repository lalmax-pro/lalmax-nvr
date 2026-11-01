package media

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bluenviron/gohlslib/v2"
	gcodecs "github.com/bluenviron/gohlslib/v2/pkg/codecs"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/q191201771/lal/pkg/base"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test doubles -----------------------------------------------------------

// hlsPullTestSession records everything fed through a customize-pub session.
type hlsPullTestSession struct {
	mu      sync.Mutex
	option  base.AvPacketStreamOption
	asc     []byte
	packets []base.AvPacket
	ascErr  error
}

func (s *hlsPullTestSession) WithOption(f func(*base.AvPacketStreamOption)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.option)
}

func (s *hlsPullTestSession) FeedAudioSpecificConfig(asc []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asc = append([]byte(nil), asc...)
	return s.ascErr
}

func (s *hlsPullTestSession) FeedAvPacket(packet base.AvPacket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.packets = append(s.packets, packet)
	return nil
}

func (s *hlsPullTestSession) FeedRtmpMsg(base.RtmpMsg) error { return nil }

func (s *hlsPullTestSession) snapshot() ([]base.AvPacket, base.AvPacketStreamOption, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	packets := append([]base.AvPacket(nil), s.packets...)
	return packets, s.option, append([]byte(nil), s.asc...)
}

// hlsPullTestEngine implements just the customize-pub slice of Engine.
type hlsPullTestEngine struct {
	Engine

	mu       sync.Mutex
	session  *hlsPullTestSession
	addErr   error
	addCalls int
	delCalls int
}

func (e *hlsPullTestEngine) AddCustomizePubSession(context.Context, string) (CustomizePubSession, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.addCalls++
	if e.addErr != nil {
		return nil, e.addErr
	}
	if e.session == nil {
		e.session = &hlsPullTestSession{}
	}
	return e.session, nil
}

func (e *hlsPullTestEngine) DelCustomizePubSession(context.Context, CustomizePubSession) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.delCalls++
	return nil
}

func (e *hlsPullTestEngine) counts() (add, del int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.addCalls, e.delCalls
}

func (e *hlsPullTestEngine) pubSession() *hlsPullTestSession {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.session
}

type hlsPullTestEvent struct {
	streamID string
	online   bool
	reason   string
	detail   string
}

// hlsPullWaitFor polls cond with a short interval and fails the test on timeout.
func hlsPullWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// --- pure helper tests ------------------------------------------------------

func TestClassifyHLSTracksVariants(t *testing.T) {
	t.Parallel()

	aacTrack := &gohlslib.Track{
		Codec: &gcodecs.MPEG4Audio{Config: mpeg4audio.AudioSpecificConfig{
			Type: 2, SampleRate: 44100, ChannelConfig: 2, ChannelCount: 2,
		}},
		ClockRate: 44100,
	}
	videoTrack := &gohlslib.Track{
		Codec:     &gcodecs.H264{SPS: []byte{0x67, 0x42}, PPS: []byte{0x68, 0xce}},
		ClockRate: 90000,
	}

	t.Run("h264 plus aac", func(t *testing.T) {
		info, err := ClassifyHLSTracks([]*gohlslib.Track{nil, videoTrack, aacTrack})
		require.NoError(t, err)
		assert.Equal(t, "h264", info.VideoCodec)
		assert.Equal(t, "aac", info.AudioCodec)
		assert.Equal(t, base.AvPacketPtAvc, info.VideoPT)
		assert.True(t, info.Playable)
		assert.True(t, info.Recordable)
		assert.Equal(t, []byte{0, 0, 0, 1, 0x67, 0x42, 0, 0, 0, 1, 0x68, 0xce}, info.VideoParamSets)
		require.NotEmpty(t, info.AudioASC)
	})

	t.Run("h265", func(t *testing.T) {
		info, err := ClassifyHLSTracks([]*gohlslib.Track{{
			Codec:     &gcodecs.H265{VPS: []byte{0x40}, SPS: []byte{0x42}, PPS: []byte{0x44}},
			ClockRate: 90000,
		}})
		require.NoError(t, err)
		assert.Equal(t, "h265", info.VideoCodec)
		assert.Equal(t, base.AvPacketPtHevc, info.VideoPT)
		assert.Equal(t, 3, strings.Count(string(info.VideoParamSets), "\x00\x00\x00\x01"))
	})

	t.Run("unsupported codecs keep a hint", func(t *testing.T) {
		tests := []struct {
			name  string
			codec gcodecs.Codec
			hint  string
		}{
			{name: "opus", codec: &gcodecs.Opus{}, hint: "opus audio is not supported"},
			{name: "av1", codec: &gcodecs.AV1{}, hint: "av1 video is not supported"},
			{name: "vp9", codec: &gcodecs.VP9{}, hint: "vp9 video is not supported"},
		}
		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				info, err := ClassifyHLSTracks([]*gohlslib.Track{{Codec: tt.codec}})
				require.ErrorIs(t, err, ErrHLSPullUnsupportedCodec)
				assert.Equal(t, tt.hint, info.UnsupportedHint)
				assert.False(t, info.Playable)
				assert.Contains(t, err.Error(), tt.hint)
			})
		}
	})

	t.Run("empty playlist uses the generic hint", func(t *testing.T) {
		info, err := ClassifyHLSTracks(nil)
		require.ErrorIs(t, err, ErrHLSPullUnsupportedCodec)
		assert.Equal(t, ErrHLSPullUnsupportedCodec.Error(), info.UnsupportedHint)

		info, err = ClassifyHLSTracks([]*gohlslib.Track{nil})
		require.ErrorIs(t, err, ErrHLSPullUnsupportedCodec)
		assert.Equal(t, ErrHLSPullUnsupportedCodec.Error(), info.UnsupportedHint)
	})

	t.Run("video with unsupported audio still plays", func(t *testing.T) {
		info, err := ClassifyHLSTracks([]*gohlslib.Track{videoTrack, {Codec: &gcodecs.Opus{}}})
		require.NoError(t, err)
		assert.Equal(t, "h264", info.VideoCodec)
		assert.Equal(t, "opus audio is not supported", info.UnsupportedHint)
		assert.Empty(t, info.AudioASC)
	})
}

func TestAnnexBFromAUAndClockTicks(t *testing.T) {
	t.Parallel()

	assert.Nil(t, AnnexBFromAU(nil))
	assert.Nil(t, AnnexBFromAU([][]byte{nil, {}}))
	assert.Equal(t, []byte{0, 0, 0, 1, 0x65, 0, 0, 0, 1, 0x41}, AnnexBFromAU([][]byte{nil, {0x65}, {}, {0x41}}))

	assert.Equal(t, int64(0), clockTicksToMilliseconds(9000, 0))
	assert.Equal(t, int64(0), clockTicksToMilliseconds(9000, -1))
	assert.Equal(t, int64(100), clockTicksToMilliseconds(9000, 90000))
	assert.Equal(t, int64(1000), clockTicksToMilliseconds(44100, 44100))
}

func TestSanitizeHLSPullErrorVariants(t *testing.T) {
	t.Parallel()

	// Errors without URLs pass through untouched.
	assert.Equal(t, "dial tcp 127.0.0.1:1: connection refused",
		sanitizeHLSPullError("dial tcp 127.0.0.1:1: connection refused"))

	// Trailing punctuation is preserved after the redacted path.
	got := sanitizeHLSPullError(`Get "https://example.test/live/index.m3u8):", unexpected status`)
	if !strings.Contains(got, "%5Bpath%20redacted%5D") || !strings.Contains(got, "example.test") {
		t.Fatalf("unexpected sanitized error: %s", got)
	}
	if strings.Contains(got, "index.m3u8") {
		t.Fatalf("path was not redacted: %s", got)
	}

	// A query string without a path is redacted too.
	got = sanitizeHLSPullError("fetch https://example.test?token=topsecret failed")
	assert.NotContains(t, got, "topsecret")
	assert.Contains(t, got, "redacted")

	// An unparsable URL is fully redacted.
	got = sanitizeHLSPullError("fetch http://%zz/broken failed")
	assert.Contains(t, got, "[URL redacted]")
	assert.Contains(t, got, "fetch ") // surrounding text is preserved

	// Multiple URLs are each redacted.
	got = sanitizeHLSPullError("a https://one.test/a.m3u8?x=1 b http://two.test/b.ts?y=2")
	assert.NotContains(t, got, "a.m3u8")
	assert.NotContains(t, got, "b.ts")
	assert.NotContains(t, got, "x=1")
	assert.Contains(t, got, "one.test")
	assert.Contains(t, got, "two.test")
}

func TestHeaderClientBehavior(t *testing.T) {
	t.Parallel()

	// No custom headers returns the base client untouched.
	assert.Same(t, http.DefaultClient, headerClient(nil, nil, "", false))
	custom := &http.Client{}
	assert.Same(t, custom, headerClient(custom, map[string]string{}, "https://example.test/x", true))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"X-Token", "X-Origin"} {
			w.Header().Set("Echo-"+name, r.Header.Get(name))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Headers are added, blank names and values are skipped.
	client := headerClient(nil, map[string]string{
		"X-Token": "abc",
		"":        "ignored",
		"X-Blank": "  ",
	}, server.URL+"/live/index.m3u8", false)
	require.NotNil(t, client)
	headers := echoHeadersWith(t, client, server.URL+"/probe")
	assert.Equal(t, "abc", headers.Get("Echo-X-Token"))

	// Restricting to the playlist origin keeps matching origins and blocks others.
	client = headerClient(nil, map[string]string{"X-Token": "abc"}, server.URL+"/live/index.m3u8", true)
	headers = echoHeadersWith(t, client, server.URL+"/probe")
	assert.Equal(t, "abc", headers.Get("Echo-X-Token"))

	otherServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Echo-X-Token", r.Header.Get("X-Token"))
		w.WriteHeader(http.StatusOK)
	}))
	defer otherServer.Close()
	headers = echoHeadersWith(t, client, otherServer.URL+"/probe")
	assert.Empty(t, headers.Get("Echo-X-Token"))

	// An unparsable playlist URL disables origin restriction.
	client = headerClient(nil, map[string]string{"X-Token": "abc"}, "://bad", true)
	headers = echoHeadersWith(t, client, otherServer.URL+"/probe")
	assert.Equal(t, "abc", headers.Get("Echo-X-Token"))
}

func echoHeadersWith(t *testing.T, client *http.Client, url string) http.Header {
	t.Helper()
	resp, err := client.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return resp.Header
}

func TestHeaderClientRedirectGuards(t *testing.T) {
	t.Parallel()

	t.Run("too many redirects", func(t *testing.T) {
		t.Parallel()
		var server *httptest.Server
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, server.URL+"/loop", http.StatusFound)
		}))
		defer server.Close()

		client := headerClient(nil, map[string]string{"X-Token": "abc"}, server.URL+"/index.m3u8", false)
		resp, err := client.Get(server.URL + "/loop")
		if resp != nil {
			resp.Body.Close()
		}
		require.Error(t, err)
		assert.Contains(t, err.Error(), "too many redirects")
	})

	t.Run("cross origin redirect is rejected", func(t *testing.T) {
		t.Parallel()
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer target.Close()
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/segment.ts", http.StatusFound)
		}))
		defer origin.Close()

		client := headerClient(nil, map[string]string{"X-Token": "abc"}, origin.URL+"/index.m3u8", true)
		resp, err := client.Get(origin.URL + "/segment.ts")
		if resp != nil {
			resp.Body.Close()
		}
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HLS redirect changed origin")
	})

	t.Run("same origin redirect is allowed", func(t *testing.T) {
		t.Parallel()
		var server *httptest.Server
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/start" {
				http.Redirect(w, r, server.URL+"/final", http.StatusFound)
				return
			}
			w.Header().Set("Echo-X-Token", r.Header.Get("X-Token"))
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		client := headerClient(nil, map[string]string{"X-Token": "abc"}, server.URL+"/index.m3u8", true)
		resp, err := client.Get(server.URL + "/start")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, "abc", resp.Header.Get("Echo-X-Token"))
	})
}

func TestEngineSupportsCustomizePub(t *testing.T) {
	t.Parallel()
	assert.False(t, engineSupportsCustomizePub(nil))
	assert.False(t, engineSupportsCustomizePub(new(LalmaxHTTP)))
	assert.True(t, engineSupportsCustomizePub(&hlsPullTestEngine{}))
}

// --- manager lifecycle tests ------------------------------------------------

func TestHLSPullManagerGuards(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	var nilManager *HLSPullManager
	_, err := nilManager.StartHLSPull(ctx, StartHLSPullRequest{StreamID: "cam", PlaylistURL: "http://127.0.0.1/x.m3u8"})
	require.ErrorIs(t, err, ErrHLSPullEmbeddedOnly)

	// No engine configured.
	manager := NewHLSPullManager(nil)
	_, err = manager.StartHLSPull(ctx, StartHLSPullRequest{StreamID: "cam", PlaylistURL: "http://127.0.0.1/x.m3u8"})
	require.ErrorIs(t, err, ErrHLSPullEmbeddedOnly)

	engine := &hlsPullTestEngine{}
	manager = NewHLSPullManager(engine)

	_, err = manager.StartHLSPull(ctx, StartHLSPullRequest{PlaylistURL: "http://127.0.0.1/x.m3u8"})
	require.ErrorContains(t, err, "stream ID is required")

	_, err = manager.StartHLSPull(ctx, StartHLSPullRequest{StreamID: "   "})
	require.ErrorContains(t, err, "stream ID is required")

	_, err = manager.StartHLSPull(ctx, StartHLSPullRequest{StreamID: "cam"})
	require.ErrorContains(t, err, "playlist URL is required")

	// HTTP-mode engines cannot provide customize-pub sessions.
	httpEngine := new(LalmaxHTTP)
	manager = NewHLSPullManager(httpEngine)
	_, err = manager.StartHLSPull(ctx, StartHLSPullRequest{StreamID: "cam", PlaylistURL: "http://127.0.0.1/x.m3u8"})
	require.ErrorIs(t, err, ErrHLSPullEmbeddedOnly)

	// Nil receivers must be safe.
	nilManager.SetMaxTasks(4)
	nilManager.SetSessionCallback(func(string, bool, time.Time, string, string) {})
	nilManager.SetPullErrorCallback(func(string, string) {})
	assert.Nil(t, nilManager.PullingStreamIDs())

	// Stop and listing on an empty manager.
	assert.NoError(t, manager.StopHLSPull(ctx, "missing"))
	manager.StopAllHLSPulls()
	assert.Empty(t, manager.PullingStreamIDs())
}

func TestHLSPullManagerDefaultTaskLimit(t *testing.T) {
	t.Parallel()
	engine := &hlsPullTestEngine{}
	manager := NewHLSPullManager(engine)
	assert.Equal(t, defaultMaxHLSPullTasks, manager.maxTasks)
	manager.SetMaxTasks(0)
	assert.Equal(t, defaultMaxHLSPullTasks, manager.maxTasks)
	manager.SetMaxTasks(-3)
	assert.Equal(t, defaultMaxHLSPullTasks, manager.maxTasks)
	manager.SetMaxTasks(2)
	assert.Equal(t, 2, manager.maxTasks)
	require.NotNil(t, manager.httpClient)
	assert.Zero(t, manager.httpClient.Timeout)
}

func TestHLSPullManagerDuplicateAndBusy(t *testing.T) {
	engine := &hlsPullTestEngine{}
	manager := NewHLSPullManager(engine)
	manager.SetMaxTasks(1)

	blockingProvider := func(ctx context.Context) (string, map[string]string, error) {
		<-ctx.Done()
		return "", nil, ctx.Err()
	}

	_, err := manager.StartHLSPull(context.Background(), StartHLSPullRequest{
		StreamID:            "cam-a",
		PlaylistURLProvider: blockingProvider,
		PullRetryNum:        0,
	})
	require.NoError(t, err)
	hlsPullWaitFor(t, "first pull task", func() bool { return len(manager.PullingStreamIDs()) == 1 })

	// A duplicate stream ID returns the existing session instead of starting a task.
	session, err := manager.StartHLSPull(context.Background(), StartHLSPullRequest{
		StreamID:     "  cam-a  ",
		PlaylistURL:  "http://127.0.0.1/ignored.m3u8",
		PullRetryNum: 0,
	})
	require.NoError(t, err)
	require.NotNil(t, session)
	assert.Equal(t, "cam-a", session.StreamID)
	assert.Equal(t, "hls_pull", session.Protocol)
	assert.Len(t, manager.PullingStreamIDs(), 1)

	// The task budget is one, so a different stream is rejected.
	_, err = manager.StartHLSPull(context.Background(), StartHLSPullRequest{
		StreamID:            "cam-b",
		PlaylistURLProvider: blockingProvider,
		PullRetryNum:        0,
	})
	require.ErrorIs(t, err, ErrHLSPullBusy)

	// Stopping drops the task and the slot becomes free again.
	require.NoError(t, manager.StopHLSPull(context.Background(), "cam-a"))
	assert.Empty(t, manager.PullingStreamIDs())
	// Stopping an unknown stream is a no-op.
	require.NoError(t, manager.StopHLSPull(context.Background(), "cam-a"))
}

func TestHLSPullManagerStopAllCancelsTasks(t *testing.T) {
	engine := &hlsPullTestEngine{}
	manager := NewHLSPullManager(engine)
	blockingProvider := func(ctx context.Context) (string, map[string]string, error) {
		<-ctx.Done()
		return "", nil, ctx.Err()
	}
	for _, id := range []string{"cam-1", "cam-2"} {
		_, err := manager.StartHLSPull(context.Background(), StartHLSPullRequest{
			StreamID:            id,
			PlaylistURLProvider: blockingProvider,
			PullRetryNum:        0,
		})
		require.NoError(t, err)
	}
	hlsPullWaitFor(t, "both pull tasks", func() bool { return len(manager.PullingStreamIDs()) == 2 })
	manager.StopAllHLSPulls()
	assert.Empty(t, manager.PullingStreamIDs())
}

func TestHLSPullManagerReportsUpstreamErrors(t *testing.T) {
	engine := &hlsPullTestEngine{}
	manager := NewHLSPullManager(engine)

	var mu sync.Mutex
	var events []hlsPullTestEvent
	manager.SetSessionCallback(func(streamID string, online bool, _ time.Time, reason, detail string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, hlsPullTestEvent{streamID: streamID, online: online, reason: reason, detail: detail})
	})
	var pullErrors []string
	manager.SetPullErrorCallback(func(streamID, detail string) {
		mu.Lock()
		defer mu.Unlock()
		pullErrors = append(pullErrors, streamID+":"+detail)
	})

	done := make(chan error, 1)
	_, err := manager.StartHLSPull(context.Background(), StartHLSPullRequest{
		StreamID: "cam-err",
		PlaylistURLProvider: func(context.Context) (string, map[string]string, error) {
			return "", nil, fmt.Errorf("source unavailable at https://user:pass@example.test/live/index.m3u8?token=secret")
		},
		PullRetryNum: 0,
		OnComplete:   func(err error) { done <- err },
	})
	require.NoError(t, err)

	select {
	case completionErr := <-done:
		require.Error(t, completionErr)
	case <-time.After(10 * time.Second):
		t.Fatal("pull did not complete after a permanent upstream error")
	}
	hlsPullWaitFor(t, "offline session event", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, event := range events {
			if !event.online {
				return true
			}
		}
		return false
	})
	assert.Empty(t, manager.PullingStreamIDs())

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, events)
	assert.True(t, events[0].online)
	last := events[len(events)-1]
	assert.False(t, last.online)
	assert.Equal(t, "upstream_error", last.reason)
	assert.NotContains(t, last.detail, "secret")
	assert.NotContains(t, last.detail, "user")
	require.NotEmpty(t, pullErrors)
	assert.NotContains(t, pullErrors[0], "secret")
	assert.NotContains(t, pullErrors[0], "user")
	assert.Equal(t, "cam-err", strings.SplitN(pullErrors[0], ":", 2)[0])
}

func TestHLSPullManagerRetryWaitsBeforeGivingUp(t *testing.T) {
	engine := &hlsPullTestEngine{}
	manager := NewHLSPullManager(engine)

	attempts := 0
	var attemptMu sync.Mutex
	done := make(chan error, 1)
	start := time.Now()
	_, err := manager.StartHLSPull(context.Background(), StartHLSPullRequest{
		StreamID: "cam-retry",
		PlaylistURLProvider: func(context.Context) (string, map[string]string, error) {
			attemptMu.Lock()
			defer attemptMu.Unlock()
			attempts++
			return "", nil, fmt.Errorf("upstream still down")
		},
		PullRetryNum: 1,
		OnComplete:   func(err error) { done <- err },
	})
	require.NoError(t, err)

	select {
	case completionErr := <-done:
		require.Error(t, completionErr)
	case <-time.After(15 * time.Second):
		t.Fatal("pull did not finish after exhausting its retry budget")
	}
	attemptMu.Lock()
	assert.Equal(t, 2, attempts)
	attemptMu.Unlock()
	assert.GreaterOrEqual(t, time.Since(start), hlsPullRetryInterval-hlsPullRetryInterval/4)
	assert.Empty(t, manager.PullingStreamIDs())
}

func TestHLSPullManagerCancelDuringRetryWait(t *testing.T) {
	engine := &hlsPullTestEngine{}
	manager := NewHLSPullManager(engine)

	firstAttempt := make(chan struct{}, 1)
	completed := make(chan error, 1)
	_, err := manager.StartHLSPull(context.Background(), StartHLSPullRequest{
		StreamID: "cam-cancel",
		PlaylistURLProvider: func(context.Context) (string, map[string]string, error) {
			select {
			case firstAttempt <- struct{}{}:
			default:
			}
			return "", nil, fmt.Errorf("still failing")
		},
		PullRetryNum: 5,
		OnComplete:   func(err error) { completed <- err },
	})
	require.NoError(t, err)

	select {
	case <-firstAttempt:
	case <-time.After(10 * time.Second):
		t.Fatal("first attempt never ran")
	}
	// Stop while the task is waiting to retry; cancellation must win.
	require.NoError(t, manager.StopHLSPull(context.Background(), "cam-cancel"))
	assert.Empty(t, manager.PullingStreamIDs())
	select {
	case <-completed:
		// The completion callback is skipped for cancelled tasks, so this must not happen.
		t.Fatal("OnComplete must not run for a cancelled pull")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHLSPullManagerSessionOpenFailure(t *testing.T) {
	engine := &hlsPullTestEngine{addErr: fmt.Errorf("customize pub unavailable")}
	manager := NewHLSPullManager(engine)

	done := make(chan error, 1)
	_, err := manager.StartHLSPull(context.Background(), StartHLSPullRequest{
		StreamID:     "cam-open",
		PlaylistURL:  "http://127.0.0.1:1/index.m3u8",
		PullRetryNum: 0,
		OnComplete:   func(err error) { done <- err },
	})
	require.NoError(t, err)

	select {
	case completionErr := <-done:
		require.ErrorContains(t, completionErr, "customize pub unavailable")
	case <-time.After(10 * time.Second):
		t.Fatal("pull did not complete after the session open failure")
	}
	add, del := engine.counts()
	assert.Equal(t, 1, add)
	assert.Zero(t, del, "no session was opened so none is closed")
}

func TestHLSPullManagerInvalidPlaylistURLErrors(t *testing.T) {
	engine := &hlsPullTestEngine{}
	manager := NewHLSPullManager(engine)

	done := make(chan error, 1)
	_, err := manager.StartHLSPull(context.Background(), StartHLSPullRequest{
		StreamID:     "cam-bad-url",
		PlaylistURL:  "://bad",
		PullRetryNum: 0,
		OnComplete:   func(err error) { done <- err },
	})
	require.NoError(t, err)

	select {
	case completionErr := <-done:
		require.Error(t, completionErr)
	case <-time.After(10 * time.Second):
		t.Fatal("pull did not complete after an invalid playlist URL")
	}
	// The opened customize-pub session must be released.
	add, del := engine.counts()
	assert.Equal(t, 1, add)
	assert.Equal(t, 1, del)
}

// --- end-to-end tests over a local HLS server -------------------------------

var hlsPullTestSPS = []byte{
	0x67, 0x42, 0xc0, 0x28, 0xd9, 0x00, 0x78, 0x02,
	0x27, 0xe5, 0x84, 0x00, 0x00, 0x03, 0x00, 0x04,
	0x00, 0x00, 0x03, 0x00, 0xf0, 0x3c, 0x60, 0xc9,
	0x20,
}

var hlsPullTestPPS = []byte{0x08}

// hlsPullGenerateServer writes a small H.264 HLS stream with the gohlslib muxer,
// copies it to a stable directory and serves it over httptest.
//
// The muxer deletes segments from disk when it is closed, so the copy happens
// first. With withEndList the media playlist is turned into a VOD playlist.
func hlsPullGenerateServer(t *testing.T, withEndList, withAudio bool, segmentDuration time.Duration) *httptest.Server {
	t.Helper()
	sourceDir := t.TempDir()
	servedDir := t.TempDir()

	video := &gohlslib.Track{
		Codec:     &gcodecs.H264{SPS: hlsPullTestSPS, PPS: hlsPullTestPPS},
		ClockRate: 90000,
	}
	audio := &gohlslib.Track{
		Codec: &gcodecs.MPEG4Audio{Config: mpeg4audio.AudioSpecificConfig{
			Type: 2, SampleRate: 44100, ChannelConfig: 2, ChannelCount: 2,
		}},
		ClockRate: 44100,
	}
	tracks := []*gohlslib.Track{video}
	if withAudio {
		tracks = append(tracks, audio)
	}
	muxer := &gohlslib.Muxer{
		Variant:            gohlslib.MuxerVariantMPEGTS,
		SegmentCount:       7,
		SegmentMinDuration: segmentDuration,
		Tracks:             tracks,
		Directory:          sourceDir,
	}
	require.NoError(t, muxer.Start())

	// Five access units produce four segments, which is enough for a gohlslib
	// client (it needs three before playback starts).
	ticksPerSegment := int64(segmentDuration * 90000 / time.Second)
	audioUnitsPerSegment := int(segmentDuration / (23 * time.Millisecond))
	if audioUnitsPerSegment < 1 {
		audioUnitsPerSegment = 1
	}
	audioTicksPerSegment := int64(segmentDuration * 44100 / time.Second)
	baseNTP := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		err := muxer.WriteH264(video,
			baseNTP.Add(time.Duration(i)*segmentDuration),
			int64(i)*ticksPerSegment,
			[][]byte{hlsPullTestSPS, {0x05}, {byte(i + 1)}})
		require.NoError(t, err)
		if !withAudio {
			continue
		}
		for j := 0; j < audioUnitsPerSegment; j++ {
			err = muxer.WriteMPEG4Audio(audio,
				baseNTP.Add(time.Duration(i)*segmentDuration+time.Duration(j)*23*time.Millisecond),
				int64(i)*audioTicksPerSegment+int64(j)*1024,
				[][]byte{{0x21, 0x10, 0x05, byte(i), byte(j), 0x00, 0x00, 0x00}})
			require.NoError(t, err)
		}
	}

	mediaPlaylistBytes := func() (map[string][]byte, int) {
		entries, err := os.ReadDir(sourceDir)
		require.NoError(t, err)
		playlists := map[string][]byte{}
		total := 0
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".m3u8") || entry.Name() == "index.m3u8" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(sourceDir, entry.Name()))
			require.NoError(t, err)
			playlists[entry.Name()] = data
			total = strings.Count(string(data), "#EXTINF")
		}
		return playlists, total
	}
	// Wait until the muxer flushed at least three segments, which is the number
	// a gohlslib client needs before it starts playback.
	hlsPullWaitFor(t, "muxer to flush three segments", func() bool {
		_, total := mediaPlaylistBytes()
		return total >= 3
	})

	// Copy playlists and segments while the muxer still has them on disk.
	entries, err := os.ReadDir(sourceDir)
	require.NoError(t, err)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(sourceDir, entry.Name()))
		require.NoError(t, err)
		if strings.HasSuffix(entry.Name(), ".m3u8") && entry.Name() != "index.m3u8" {
			// Sub-second segments round down to a target duration of zero,
			// which playlist parsers reject; clamp it to one second.
			data = bytes.Replace(data, []byte("#EXT-X-TARGETDURATION:0\n"), []byte("#EXT-X-TARGETDURATION:1\n"), 1)
			if withEndList {
				data = append(data, []byte("#EXT-X-ENDLIST\n")...)
			}
		}
		require.NoError(t, os.WriteFile(filepath.Join(servedDir, entry.Name()), data, 0o644))
	}
	muxer.Close()

	server := httptest.NewServer(http.FileServer(http.Dir(servedDir)))
	t.Cleanup(server.Close)
	return server
}

func TestHLSPullPlaysGeneratedPlaylistWithPlaybackDuration(t *testing.T) {
	server := hlsPullGenerateServer(t, false, false, time.Second)

	engine := &hlsPullTestEngine{}
	manager := NewHLSPullManager(engine)
	var mu sync.Mutex
	var events []hlsPullTestEvent
	manager.SetSessionCallback(func(streamID string, online bool, _ time.Time, reason, detail string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, hlsPullTestEvent{streamID: streamID, online: online, reason: reason, detail: detail})
	})

	done := make(chan error, 1)
	session, err := manager.StartHLSPull(context.Background(), StartHLSPullRequest{
		StreamID:         "cam-playback",
		PlaylistURL:      server.URL + "/index.m3u8",
		Headers:          map[string]string{"X-Token": "abc"},
		PlaybackDuration: 300 * time.Millisecond,
		OnComplete:       func(err error) { done <- err },
	})
	require.NoError(t, err)
	require.NotNil(t, session)
	assert.Equal(t, "cam-playback", session.StreamID)
	assert.Equal(t, "hls_pull", session.Protocol)
	assert.False(t, session.StartedAt.IsZero())

	select {
	case completionErr := <-done:
		require.NoError(t, completionErr, "bounded playback should complete without an error")
	case <-time.After(20 * time.Second):
		t.Fatal("bounded HLS playback did not complete")
	}
	hlsPullWaitFor(t, "offline session event", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, event := range events {
			if !event.online {
				return true
			}
		}
		return false
	})
	assert.Empty(t, manager.PullingStreamIDs())

	pub := engine.pubSession()
	require.NotNil(t, pub)
	packets, option, asc := pub.snapshot()
	assert.Equal(t, base.AvPacketStreamVideoFormatAnnexb, option.VideoFormat)
	assert.Equal(t, base.AvPacketStreamAudioFormatRawAac, option.AudioFormat)
	require.NotEmpty(t, packets)
	var fedVideo bool
	for _, packet := range packets {
		if packet.PayloadType != base.AvPacketPtAvc {
			continue
		}
		assert.True(t, bytes.HasPrefix(packet.Payload, []byte{0, 0, 0, 1}),
			"Annex-B start code is required")
		if bytes.Contains(packet.Payload, []byte{0, 0, 0, 1, 0x05}) {
			fedVideo = true
		}
		assert.GreaterOrEqual(t, packet.Pts, int64(0))
		assert.GreaterOrEqual(t, packet.Timestamp, int64(0))
	}
	assert.True(t, fedVideo, "the H.264 IDR access unit should be fed to the session")
	assert.Empty(t, asc, "a video-only stream must not configure audio")

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, events)
	assert.True(t, events[0].online)
	last := events[len(events)-1]
	assert.False(t, last.online)
	assert.Equal(t, "playback_completed", last.reason)
	add, del := engine.counts()
	assert.Equal(t, 1, add)
	assert.Equal(t, 1, del)
}

func TestHLSPullReportsSourceEndedForVODPlaylist(t *testing.T) {
	server := hlsPullGenerateServer(t, true, false, 200*time.Millisecond)

	engine := &hlsPullTestEngine{}
	manager := NewHLSPullManager(engine)
	var mu sync.Mutex
	var offline hlsPullTestEvent
	manager.SetSessionCallback(func(streamID string, online bool, _ time.Time, reason, detail string) {
		if online {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		offline = hlsPullTestEvent{streamID: streamID, online: online, reason: reason, detail: detail}
	})

	done := make(chan error, 1)
	_, err := manager.StartHLSPull(context.Background(), StartHLSPullRequest{
		StreamID:    "cam-vod",
		PlaylistURL: server.URL + "/index.m3u8",
		OnComplete:  func(err error) { done <- err },
	})
	require.NoError(t, err)

	select {
	case completionErr := <-done:
		require.NoError(t, completionErr, "a VOD playlist ends without an error")
	case <-time.After(20 * time.Second):
		t.Fatal("VOD HLS pull did not reach the end of its playlist")
	}
	hlsPullWaitFor(t, "offline session event", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return offline.streamID != ""
	})
	mu.Lock()
	defer mu.Unlock()
	assert.False(t, offline.online)
	assert.Equal(t, "source_ended", offline.reason)
	assert.Empty(t, offline.detail)
	assert.Empty(t, manager.PullingStreamIDs())
}

func TestHLSPullRepeatOnEndReopensSource(t *testing.T) {
	server := hlsPullGenerateServer(t, true, false, 200*time.Millisecond)

	engine := &hlsPullTestEngine{}
	manager := NewHLSPullManager(engine)

	_, err := manager.StartHLSPull(context.Background(), StartHLSPullRequest{
		StreamID:    "cam-repeat",
		PlaylistURL: server.URL + "/index.m3u8",
		RepeatOnEnd: true,
	})
	require.NoError(t, err)

	// A second customize-pub session proves the task looped after the replay ended.
	hlsPullWaitFor(t, "the pull task to reopen the source", func() bool {
		add, _ := engine.counts()
		return add >= 2
	})
	manager.StopAllHLSPulls()
	assert.Empty(t, manager.PullingStreamIDs())
}

func TestAttachHLSTracksRejectsUnsupportedCodecs(t *testing.T) {
	t.Parallel()
	// Classification fails before the client is touched, so a nil client is safe.
	session := &hlsPullTestSession{}
	err := attachHLSTracks(nil, session, []*gohlslib.Track{{Codec: &gcodecs.Opus{}}})
	require.ErrorIs(t, err, ErrHLSPullUnsupportedCodec)
	assert.Empty(t, session.asc)
	assert.Empty(t, session.packets)
}

func TestHLSPullNotifyPullErrorIgnoresNil(t *testing.T) {
	t.Parallel()
	manager := NewHLSPullManager(&hlsPullTestEngine{})
	called := false
	manager.SetPullErrorCallback(func(string, string) { called = true })
	manager.notifyPullError("cam", nil)
	assert.False(t, called)
}

func TestHLSPullSanitizeErrorTrailingPunctuation(t *testing.T) {
	t.Parallel()
	got := sanitizeHLSPullError("fetch (https://example.test/live/x.m3u8).")
	assert.NotContains(t, got, "x.m3u8")
	assert.True(t, strings.HasSuffix(got, ")."), "trailing punctuation should be preserved: %s", got)
	assert.Contains(t, got, "example.test")
}

func TestHLSPullManagerProviderSuccessUpdatesRequest(t *testing.T) {
	server := hlsPullGenerateServer(t, false, false, time.Second)

	engine := &hlsPullTestEngine{}
	manager := NewHLSPullManager(engine)
	providerCalls := 0
	done := make(chan error, 1)
	_, err := manager.StartHLSPull(context.Background(), StartHLSPullRequest{
		StreamID: "cam-provider",
		PlaylistURLProvider: func(context.Context) (string, map[string]string, error) {
			providerCalls++
			return server.URL + "/index.m3u8", map[string]string{"X-Token": "abc"}, nil
		},
		PlaylistURL:      "http://127.0.0.1:1/placeholder.m3u8",
		PlaybackDuration: 300 * time.Millisecond,
		OnComplete:       func(err error) { done <- err },
	})
	require.NoError(t, err)

	select {
	case completionErr := <-done:
		require.NoError(t, completionErr)
	case <-time.After(20 * time.Second):
		t.Fatal("provider-based pull did not complete")
	}
	assert.Equal(t, 1, providerCalls)
	pub := engine.pubSession()
	require.NotNil(t, pub)
	packets, _, _ := pub.snapshot()
	assert.NotEmpty(t, packets)
	assert.Empty(t, manager.PullingStreamIDs())
}

func TestHLSPullRepeatOnPlaybackDurationReopensSource(t *testing.T) {
	server := hlsPullGenerateServer(t, false, false, time.Second)

	engine := &hlsPullTestEngine{}
	manager := NewHLSPullManager(engine)
	_, err := manager.StartHLSPull(context.Background(), StartHLSPullRequest{
		StreamID:         "cam-repeat-duration",
		PlaylistURL:      server.URL + "/index.m3u8",
		PlaybackDuration: 200 * time.Millisecond,
		RepeatOnEnd:      true,
	})
	require.NoError(t, err)

	hlsPullWaitFor(t, "the bounded pull to restart", func() bool {
		add, _ := engine.counts()
		return add >= 2
	})
	manager.StopAllHLSPulls()
	assert.Empty(t, manager.PullingStreamIDs())
}

func TestHLSPullPullOnceHonorsCancelledContext(t *testing.T) {
	server := hlsPullGenerateServer(t, false, false, time.Second)

	engine := &hlsPullTestEngine{}
	manager := NewHLSPullManager(engine)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	task := &hlsPullTask{
		streamID: "cam-cancelled",
		req: StartHLSPullRequest{
			StreamID:         "cam-cancelled",
			PlaylistURL:      server.URL + "/index.m3u8",
			PlaybackDuration: time.Minute,
		},
	}
	err := manager.pullOnce(ctx, task)
	require.Error(t, err)
	add, del := engine.counts()
	assert.Equal(t, 1, add)
	assert.Equal(t, 1, del)
}

func TestHLSPullCancelDuringPlaybackTimer(t *testing.T) {
	server := hlsPullGenerateServer(t, false, false, time.Second)

	engine := &hlsPullTestEngine{}
	manager := NewHLSPullManager(engine)
	_, err := manager.StartHLSPull(context.Background(), StartHLSPullRequest{
		StreamID:         "cam-stop-playback",
		PlaylistURL:      server.URL + "/index.m3u8",
		PlaybackDuration: time.Minute,
	})
	require.NoError(t, err)

	// Wait until frames flow, which means the playback timer is armed, then stop.
	hlsPullWaitFor(t, "the first frame", func() bool {
		pub := engine.pubSession()
		if pub == nil {
			return false
		}
		packets, _, _ := pub.snapshot()
		return len(packets) > 0
	})
	require.NoError(t, manager.StopHLSPull(context.Background(), "cam-stop-playback"))
	assert.Empty(t, manager.PullingStreamIDs())
}

func TestHLSPullAudioConfigFailureAbortsPull(t *testing.T) {
	server := hlsPullGenerateServer(t, true, true, 200*time.Millisecond)

	engine := &hlsPullTestEngine{session: &hlsPullTestSession{ascErr: fmt.Errorf("asc rejected")}}
	manager := NewHLSPullManager(engine)
	done := make(chan error, 1)
	_, err := manager.StartHLSPull(context.Background(), StartHLSPullRequest{
		StreamID:     "cam-asc",
		PlaylistURL:  server.URL + "/index.m3u8",
		PullRetryNum: 0,
		OnComplete:   func(err error) { done <- err },
	})
	require.NoError(t, err)

	select {
	case completionErr := <-done:
		require.ErrorContains(t, completionErr, "asc rejected")
	case <-time.After(20 * time.Second):
		t.Fatal("audio config failure did not abort the pull")
	}
	assert.Empty(t, manager.PullingStreamIDs())
}

func TestHLSPullFeedsAudioSpecificConfigAndFrames(t *testing.T) {
	server := hlsPullGenerateServer(t, false, true, 200*time.Millisecond)

	engine := &hlsPullTestEngine{}
	manager := NewHLSPullManager(engine)
	_, err := manager.StartHLSPull(context.Background(), StartHLSPullRequest{
		StreamID:         "cam-audio",
		PlaylistURL:      server.URL + "/index.m3u8",
		PlaybackDuration: time.Minute,
	})
	require.NoError(t, err)
	defer func() {
		_ = manager.StopHLSPull(context.Background(), "cam-audio")
	}()

	// Wait until an AAC access unit reaches the session; this also proves the
	// AudioSpecificConfig was accepted before the frames were fed.
	var audioPackets []base.AvPacket
	hlsPullWaitFor(t, "the first AAC access unit", func() bool {
		pub := engine.pubSession()
		if pub == nil {
			return false
		}
		packets, _, _ := pub.snapshot()
		audioPackets = audioPackets[:0]
		for _, packet := range packets {
			if packet.PayloadType == base.AvPacketPtAac {
				audioPackets = append(audioPackets, packet)
			}
		}
		return len(audioPackets) > 0
	})

	pub := engine.pubSession()
	require.NotNil(t, pub)
	_, _, asc := pub.snapshot()
	assert.NotEmpty(t, asc, "the AudioSpecificConfig should be forwarded to the session")
	for _, packet := range audioPackets {
		assert.NotEmpty(t, packet.Payload)
		assert.Equal(t, packet.Pts, packet.Timestamp)
		assert.GreaterOrEqual(t, packet.Pts, int64(0))
	}
}
