package emby

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

type embyTransport func(*http.Request) (*http.Response, error)

func (f embyTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestRepositoryErrorsPropagateBeforeSideEffects(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("repository unavailable")
	repo := newMemoryEmbyRepo()
	service := NewService(repo, nil)
	repo.serverErr = sentinel
	_, err := service.SaveConfig(ctx, ServerConfig{BaseURL: "https://unused.invalid", UserID: "user"})
	require.ErrorIs(t, err, sentinel)
	_, err = service.BrowseItem(ctx, "movie")
	require.ErrorIs(t, err, sentinel)
	_, err = service.AddItem(ctx, "movie")
	require.ErrorIs(t, err, sentinel)
	repo.getErr = sentinel
	require.ErrorIs(t, service.SetPublish(ctx, "movie", true, false), sentinel)
	require.Empty(t, service.active)
}

func TestEmbyHTTPErrorsWithoutNetwork(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryEmbyRepo()
	repo.server = &storage.EmbyServer{BaseURL: "https://unused.invalid", UserID: "user", Token: "secret"}
	service := NewService(repo, nil)
	sentinel := errors.New("transport failure")
	calls := 0
	service.client.Transport = embyTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "secret", req.Header.Get("X-Emby-Token"))
		return nil, sentinel
	})
	_, err := service.SaveConfig(ctx, ServerConfig{BaseURL: repo.server.BaseURL, UserID: "user", Token: "secret"})
	require.ErrorIs(t, err, sentinel)
	_, _, err = service.Browse(ctx, "", 10, 0)
	require.ErrorIs(t, err, sentinel)
	_, err = service.BrowseItem(ctx, "movie")
	require.ErrorIs(t, err, sentinel)
	_, _, err = service.playlist(ctx, "movie")
	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 4, calls)

	service.client.Transport = embyTransport(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodPost, req.Method)
		require.Equal(t, "application/json", req.Header.Get("Content-Type"))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"MediaSources":[{"Id":""}]}`))}, nil
	})
	_, _, err = service.playlist(ctx, "movie")
	require.ErrorContains(t, err, "no playable media source")
}

func TestStaleCompletionCannotDisableRestartedPublication(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryEmbyRepo()
	item := storage.EmbyItem{ID: "item", EmbyItemID: "movie", StreamID: "emby_movie", PublishEnabled: true}
	repo.items[item.ID] = item
	puller := &scriptedPuller{}
	service := NewService(repo, puller)
	t.Cleanup(service.Stop)
	require.NoError(t, service.startItem(ctx, item, false))
	oldComplete := puller.last.OnComplete
	require.NoError(t, service.SetPublish(ctx, item.ID, false, false))
	require.NoError(t, service.SetPublish(ctx, item.ID, true, false))
	oldComplete(nil)
	require.Contains(t, service.active, item.StreamID, "old completion must not remove new stream")
	current, err := repo.GetEmbyItem(ctx, item.ID)
	require.NoError(t, err)
	require.True(t, current.PublishEnabled)
	puller.last.OnComplete(nil)
	require.NotContains(t, service.active, item.StreamID)
	current, err = repo.GetEmbyItem(ctx, item.ID)
	require.NoError(t, err)
	require.False(t, current.PublishEnabled, "current non-loop completion must disable publication")
}

func TestCompletionFallbackRejectsInvalidState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		active bool
		loop   bool
		epoch  uint64
	}{
		{name: "stale epoch", active: true, epoch: 2},
		{name: "inactive", epoch: 1},
		{name: "changed loop", active: true, loop: true, epoch: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMemoryEmbyRepo()
			item := storage.EmbyItem{ID: "item", StreamID: "emby_movie", PublishEnabled: true, RunTimeTicks: 100}
			repo.items[item.ID] = item
			puller := &scriptedPuller{}
			service := NewService(repo, puller)
			service.epochs[item.StreamID] = tc.epoch
			if tc.active {
				service.active[item.StreamID] = tc.loop
			}
			service.scheduleCompletionFallback(item, false, 1)
			require.Empty(t, service.timers)
			service.finishAfterDuration(item, false, 1)
			require.Equal(t, tc.epoch, service.epochs[item.StreamID])
			require.Empty(t, puller.stoppedIDs())
			require.Equal(t, 0, puller.startCount())
			current, err := repo.GetEmbyItem(context.Background(), item.ID)
			require.NoError(t, err)
			require.True(t, current.PublishEnabled)
		})
	}
}
