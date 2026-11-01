package emby

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bluenviron/gohlslib/v2"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

type memoryEmbyRepo struct {
	mu            sync.Mutex
	server        *storage.EmbyServer
	serverErr     error
	saveErr       error
	items         map[string]storage.EmbyItem
	listErr       error
	getErr        error
	saveItemErr   error
	publishErr    error
	deleteErr     error
	streamErr     error
	streamNames   map[string]string
	deletedStream string
}

func newMemoryEmbyRepo() *memoryEmbyRepo {
	return &memoryEmbyRepo{items: map[string]storage.EmbyItem{}, streamNames: map[string]string{}}
}

func (r *memoryEmbyRepo) GetEmbyServer(context.Context) (*storage.EmbyServer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.serverErr != nil {
		return nil, r.serverErr
	}
	if r.server == nil {
		return nil, nil
	}
	copyServer := *r.server
	return &copyServer, nil
}

func (r *memoryEmbyRepo) SaveEmbyServer(_ context.Context, server storage.EmbyServer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return r.saveErr
	}
	r.server = &server
	return nil
}

func (r *memoryEmbyRepo) ListEmbyItems(context.Context) ([]storage.EmbyItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	out := make([]storage.EmbyItem, 0, len(r.items))
	for _, item := range r.items {
		out = append(out, item)
	}
	return out, nil
}

func (r *memoryEmbyRepo) GetEmbyItem(_ context.Context, id string) (*storage.EmbyItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	for _, item := range r.items {
		if item.ID == id || item.EmbyItemID == id {
			copyItem := item
			return &copyItem, nil
		}
	}
	return nil, nil
}

func (r *memoryEmbyRepo) SaveEmbyItem(_ context.Context, item storage.EmbyItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveItemErr != nil {
		return r.saveItemErr
	}
	r.items[item.ID] = item
	return nil
}

func (r *memoryEmbyRepo) SetEmbyItemPublish(_ context.Context, id string, enabled, loop bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.publishErr != nil {
		return r.publishErr
	}
	for key, item := range r.items {
		if item.ID == id || item.EmbyItemID == id {
			item.PublishEnabled = enabled
			item.Loop = loop
			r.items[key] = item
			return nil
		}
	}
	return nil
}

func (r *memoryEmbyRepo) DeleteEmbyItem(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleteErr != nil {
		return r.deleteErr
	}
	for key, item := range r.items {
		if item.ID == id || item.EmbyItemID == id {
			delete(r.items, key)
		}
	}
	return nil
}

func (r *memoryEmbyRepo) SetCreatedStreamName(_ context.Context, streamID, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.streamErr != nil {
		return r.streamErr
	}
	r.streamNames[streamID] = name
	return nil
}

func (r *memoryEmbyRepo) DeleteCreatedStream(_ context.Context, streamID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletedStream = streamID
	delete(r.streamNames, streamID)
	return true, nil
}

type scriptedPuller struct {
	mu       sync.Mutex
	startErr error
	stopErr  error
	ids      []string
	stopped  []string
	last     media.StartHLSPullRequest
	complete bool
	starts   int
}

func (p *scriptedPuller) StartHLSPull(_ context.Context, req media.StartHLSPullRequest) (*media.StreamSession, error) {
	p.mu.Lock()
	p.starts++
	p.last = req
	err := p.startErr
	complete := p.complete
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if complete && req.OnComplete != nil {
		req.OnComplete(nil)
	}
	return &media.StreamSession{}, nil
}

func (p *scriptedPuller) StopHLSPull(_ context.Context, streamID string) error {
	p.mu.Lock()
	p.stopped = append(p.stopped, streamID)
	err := p.stopErr
	p.mu.Unlock()
	return err
}

func (p *scriptedPuller) startCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.starts
}

func (p *scriptedPuller) stoppedIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.stopped))
	copy(out, p.stopped)
	return out
}

func (p *scriptedPuller) StopAllHLSPulls() {}

func (p *scriptedPuller) PullingStreamIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.ids...)
}

func embyServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") == "" && r.URL.Query().Get("api_key") == "" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "/Items/") && strings.HasSuffix(r.URL.Path, "/PlaybackInfo") && r.Method == http.MethodPost:
			_, _ = io.WriteString(w, `{"MediaSources":[{"Id":"media-1"}],"PlaySessionId":"play-1"}`)
		case strings.HasSuffix(r.URL.Path, "/Items/movie-1"):
			_, _ = io.WriteString(w, `{"Id":"movie-1","Name":"Film","Type":"Movie","Overview":"plot","RunTimeTicks":100,"ImageTags":{"Primary":"tag"}}`)
		case strings.HasSuffix(r.URL.Path, "/Items/song-1"):
			_, _ = io.WriteString(w, `{"Id":"song-1","Name":"Song","Type":"Audio"}`)
		case strings.Contains(r.URL.Path, "/Items"):
			if r.URL.Query().Get("Limit") == "1" {
				_, _ = io.WriteString(w, `{"Items":[],"TotalRecordCount":0}`)
				return
			}
			_, _ = io.WriteString(w, `{"Items":[{"Id":"movie-1","Name":"Film","Type":"Movie","Overview":"plot","RunTimeTicks":100,"ImageTags":{"Primary":"tag"}},{"Id":"","Name":"skip","Type":"Movie"},{"Id":"song","Name":"Song","Type":"Audio"}],"TotalRecordCount":3}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestEmbyConfigBrowseAndPublish(t *testing.T) {
	server := embyServer(t)
	defer server.Close()
	repo := newMemoryEmbyRepo()
	puller := &scriptedPuller{}
	service := NewService(repo, puller)
	ctx := context.Background()

	cfg, err := service.Config(ctx)
	require.NoError(t, err)
	require.False(t, cfg.Configured)
	_, _, err = service.Browse(ctx, "", 0, -1)
	require.ErrorIs(t, err, ErrNotConfigured)

	_, err = service.SaveConfig(ctx, ServerConfig{BaseURL: "ftp://files"})
	require.Error(t, err)
	_, err = service.SaveConfig(ctx, ServerConfig{BaseURL: "http://user:pass@example"})
	require.Error(t, err)
	_, err = service.SaveConfig(ctx, ServerConfig{BaseURL: server.URL, Name: " Emby ", UserID: " user "})
	require.ErrorContains(t, err, "access token")

	saved, err := service.SaveConfig(ctx, ServerConfig{BaseURL: server.URL + "/", Name: " Emby ", UserID: " user ", Token: " secret "})
	require.NoError(t, err)
	require.True(t, saved.Configured)
	require.Equal(t, server.URL, saved.BaseURL)
	require.Equal(t, "user", saved.UserID)

	kept, err := service.SaveConfig(ctx, ServerConfig{BaseURL: server.URL, Name: "Emby", UserID: "user"})
	require.NoError(t, err)
	require.True(t, kept.HasToken)

	loaded, err := service.Config(ctx)
	require.NoError(t, err)
	require.True(t, loaded.HasToken)
	require.Empty(t, loaded.Token)

	items, total, err := service.Browse(ctx, "  film  ", 0, -1)
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Len(t, items, 1)
	require.Equal(t, "movie-1", items[0].ID)
	require.Equal(t, "tag", items[0].ImageTag)

	longQuery := strings.Repeat("q", 300)
	_, _, err = service.Browse(ctx, longQuery, 1000, 2)
	require.NoError(t, err)

	item, err := service.BrowseItem(ctx, "movie-1")
	require.NoError(t, err)
	require.Equal(t, "Film", item.Name)
	_, err = service.BrowseItem(ctx, "song-1")
	require.ErrorIs(t, err, ErrItemNotFound)

	added, err := service.AddItem(ctx, "movie-1")
	require.NoError(t, err)
	require.Equal(t, "movie-1", added.EmbyItemID)
	require.Contains(t, added.StreamID, "emby_")
	listed, err := service.ListItems(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	got, err := service.GetItem(ctx, added.ID)
	require.NoError(t, err)
	require.Equal(t, added.Name, got.Name)

	require.ErrorIs(t, service.SetPublish(ctx, "missing", true, false), ErrItemNotFound)
	repo.publishErr = errors.New("publish")
	require.Error(t, service.SetPublish(ctx, added.ID, true, false))
	repo.publishErr = nil

	noPuller := NewService(repo, nil)
	require.ErrorIs(t, noPuller.SetPublish(ctx, added.ID, true, false), media.ErrHLSPullEmbeddedOnly)
	require.NoError(t, noPuller.SetPublish(ctx, added.ID, false, false))

	puller.startErr = errors.New("start")
	require.Error(t, service.SetPublish(ctx, added.ID, true, false))
	puller.startErr = nil
	require.NoError(t, service.SetPublish(ctx, added.ID, true, false))
	require.Equal(t, 2, puller.startCount())
	require.NoError(t, service.SetPublish(ctx, added.ID, true, false))
	require.Equal(t, 2, puller.startCount())
	require.NoError(t, service.SetPublish(ctx, added.ID, true, true))
	require.GreaterOrEqual(t, puller.startCount(), 2)
	require.NoError(t, service.SetPublish(ctx, added.ID, false, false))
	require.Contains(t, puller.stoppedIDs(), added.StreamID)

	playlist, headers, err := service.playlist(ctx, "movie-1")
	require.NoError(t, err)
	require.Contains(t, playlist, "master.m3u8")
	require.Equal(t, "secret", headers["X-Emby-Token"])

	require.NoError(t, service.RemoveItem(ctx, added.ID))
	require.Equal(t, added.StreamID, repo.deletedStream)
	require.ErrorIs(t, service.RemoveItem(ctx, added.ID), ErrItemNotFound)
}

func TestEmbyRecoveryStopAndHelpers(t *testing.T) {
	server := embyServer(t)
	defer server.Close()
	repo := newMemoryEmbyRepo()
	repo.server = &storage.EmbyServer{Name: "Emby", BaseURL: server.URL, UserID: "user", Token: "secret"}
	item := storage.EmbyItem{ID: "local", EmbyItemID: "movie-1", Name: "Film", StreamID: "emby_film", PublishEnabled: true, Loop: true, RunTimeTicks: 1}
	repo.items[item.ID] = item
	puller := &scriptedPuller{ids: []string{"emby_film", "live_other"}, complete: true}
	service := NewService(repo, puller)

	require.NoError(t, service.Recover(context.Background()))
	require.Equal(t, 1, puller.startCount())
	service.Stop()
	require.Contains(t, puller.stoppedIDs(), "emby_film")

	repo.listErr = errors.New("list")
	require.Error(t, service.Recover(context.Background()))
	repo.listErr = nil
	puller.startErr = errors.New("start")
	require.Error(t, service.Recover(context.Background()))
	puller.startErr = nil

	service.scheduleCompletionFallback(item, true, 99)
	service.finishAfterDuration(item, true, 1)
	service.mu.Lock()
	service.active[item.StreamID] = true
	service.epochs[item.StreamID] = 3
	service.timers[item.StreamID] = time.AfterFunc(time.Hour, func() {})
	service.mu.Unlock()
	service.finishAfterDuration(item, true, 3)
	require.Eventually(t, func() bool { return puller.startCount() >= 2 }, time.Second, 5*time.Millisecond)

	repo.serverErr = errors.New("db")
	_, err := service.Config(context.Background())
	require.Error(t, err)
	_, _, err = service.playlist(context.Background(), "movie-1")
	require.Error(t, err)
	repo.serverErr = nil

	badPlay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "PlaybackInfo") {
			_, _ = io.WriteString(w, `{"MediaSources":[]}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer badPlay.Close()
	repo.server.BaseURL = badPlay.URL
	_, _, err = service.playlist(context.Background(), "movie-1")
	require.ErrorContains(t, err, "no playable")

	statusServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/empty") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer statusServer.Close()
	err = service.doJSON(context.Background(), storage.EmbyServer{BaseURL: statusServer.URL, Token: "secret"}, http.MethodGet, statusServer.URL+"/bad", nil, &map[string]any{})
	require.ErrorContains(t, err, "HTTP 502")
	require.NoError(t, service.doJSON(context.Background(), storage.EmbyServer{Token: "secret"}, http.MethodGet, statusServer.URL+"/empty", nil, nil))

	err = service.doJSON(context.Background(), storage.EmbyServer{Token: "secret"}, http.MethodGet, "http://[::1", nil, nil)
	require.Error(t, err)
	err = service.doJSON(context.Background(), storage.EmbyServer{Token: "secret"}, http.MethodGet, "http://127.0.0.1:1/down", nil, &map[string]any{})
	require.Error(t, err)

	badJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{`)
	}))
	defer badJSON.Close()
	require.Error(t, service.doJSON(context.Background(), storage.EmbyServer{Token: "t"}, http.MethodPost, badJSON.URL, []byte(`{}`), &map[string]any{}))

	_, err = normalizeBaseURL("://")
	require.Error(t, err)
	_, err = normalizeBaseURL("http://example/path/?q=1")
	require.Error(t, err)
	normalized, err := normalizeBaseURL(" https://Example.test/library/ ")
	require.NoError(t, err)
	require.Equal(t, "https://Example.test/library", normalized)
	left, _ := url.Parse("http://Example.test")
	right, _ := url.Parse("HTTP://example.test")
	require.True(t, sameOrigin(left, right))
	other, _ := url.Parse("https://example.test")
	require.False(t, sameOrigin(left, other))

	require.Equal(t, "plain", sealSecret("plain"))
	require.Equal(t, "plain", openSecret("plain"))
	require.True(t, IsEndOfStream(gohlslib.ErrClientEOS))
	require.False(t, IsEndOfStream(errors.New("other")))

	repo.saveErr = errors.New("save")
	repo.server.BaseURL = server.URL
	repo.server.Token = "secret"
	repo.serverErr = nil
	_, err = service.SaveConfig(context.Background(), ServerConfig{BaseURL: server.URL, UserID: "user", Token: "secret"})
	require.Error(t, err)

	repo.saveErr = nil
	repo.saveItemErr = errors.New("item")
	_, err = service.AddItem(context.Background(), "movie-1")
	require.Error(t, err)
	repo.saveItemErr = nil
	repo.streamErr = errors.New("stream")
	_, err = service.AddItem(context.Background(), "movie-1")
	require.Error(t, err)

	repo.deleteErr = errors.New("delete")
	require.Error(t, service.RemoveItem(context.Background(), "local"))
	repo.deleteErr = nil
	repo.getErr = errors.New("get")
	require.Error(t, service.RemoveItem(context.Background(), "local"))
}

func TestEmbyRedirectPolicy(t *testing.T) {
	var cross, loop *httptest.Server
	cross = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, loop.URL+"/elsewhere", http.StatusFound)
	}))
	defer cross.Close()
	loop = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, cross.URL+"/again", http.StatusFound)
	}))
	defer loop.Close()

	service := NewService(newMemoryEmbyRepo(), nil)
	err := service.doJSON(context.Background(), storage.EmbyServer{Token: "t"}, http.MethodGet, cross.URL, nil, &map[string]any{})
	require.Error(t, err)

	same := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("n") == "5" {
			_, _ = io.WriteString(w, `{}`)
			return
		}
		next := 1
		if raw := r.URL.Query().Get("n"); raw != "" {
			if parsed, convErr := strconv.Atoi(raw); convErr == nil {
				next = parsed + 1
			}
		}
		http.Redirect(w, r, "/?n="+strconv.Itoa(next), http.StatusFound)
	}))
	defer same.Close()
	err = service.doJSON(context.Background(), storage.EmbyServer{Token: "t"}, http.MethodGet, same.URL, nil, &map[string]any{})
	require.ErrorContains(t, err, "too many")
}
