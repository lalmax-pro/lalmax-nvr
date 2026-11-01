package iptv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

type memoryIPTV struct {
	mu       sync.Mutex
	fail     map[string]error
	jobs     map[string]storage.IPTVImportJob
	items    map[string]storage.IPTVImportItem
	sources  map[string]storage.IPTVSource
	channels map[string]storage.IPTVChannel
	plans    map[string]error
}

func newMemoryIPTV() *memoryIPTV {
	return &memoryIPTV{
		fail:     map[string]error{},
		jobs:     map[string]storage.IPTVImportJob{},
		items:    map[string]storage.IPTVImportItem{},
		sources:  map[string]storage.IPTVSource{},
		channels: map[string]storage.IPTVChannel{},
		plans:    map[string]error{},
	}
}

func (r *memoryIPTV) gate(method string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fail[method]
}

func (r *memoryIPTV) InsertIPTVImportJob(_ context.Context, job storage.IPTVImportJob) error {
	if err := r.gate("InsertIPTVImportJob"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[job.ID] = job
	return nil
}

func (r *memoryIPTV) UpdateIPTVImportJob(_ context.Context, job storage.IPTVImportJob) error {
	if err := r.gate("UpdateIPTVImportJob"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[job.ID] = job
	return nil
}

func (r *memoryIPTV) GetIPTVImportJob(_ context.Context, id string) (*storage.IPTVImportJob, error) {
	if err := r.gate("GetIPTVImportJob"); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok {
		return nil, nil
	}
	return &job, nil
}

func (r *memoryIPTV) InsertIPTVImportItems(_ context.Context, items []storage.IPTVImportItem) error {
	if err := r.gate("InsertIPTVImportItems"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, item := range items {
		r.items[item.ID] = item
	}
	return nil
}

func (r *memoryIPTV) UpdateIPTVImportItem(_ context.Context, item storage.IPTVImportItem) error {
	if err := r.gate("UpdateIPTVImportItem"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[item.ID] = item
	return nil
}

func (r *memoryIPTV) ListIPTVImportItems(_ context.Context, jobID, status, q string) ([]storage.IPTVImportItem, error) {
	if err := r.gate("ListIPTVImportItems"); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []storage.IPTVImportItem
	for _, item := range r.items {
		if item.JobID != jobID {
			continue
		}
		if status != "" && item.Status != status {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(item.Name), strings.ToLower(q)) {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (r *memoryIPTV) GetIPTVImportItem(_ context.Context, id string) (*storage.IPTVImportItem, error) {
	if err := r.gate("GetIPTVImportItem"); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	return &item, nil
}

func (r *memoryIPTV) GetIPTVImportItemsByIDs(_ context.Context, jobID string, ids []string) ([]storage.IPTVImportItem, error) {
	if err := r.gate("GetIPTVImportItemsByIDs"); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	want := map[string]struct{}{}
	for _, id := range ids {
		want[id] = struct{}{}
	}
	var out []storage.IPTVImportItem
	for _, item := range r.items {
		if item.JobID != jobID {
			continue
		}
		if _, ok := want[item.ID]; ok {
			out = append(out, item)
		}
	}
	return out, nil
}

func (r *memoryIPTV) InsertIPTVSource(_ context.Context, src storage.IPTVSource) error {
	if err := r.gate("InsertIPTVSource"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources[src.ID] = src
	return nil
}

func (r *memoryIPTV) ListIPTVSources(context.Context) ([]storage.IPTVSource, error) {
	if err := r.gate("ListIPTVSources"); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]storage.IPTVSource, 0, len(r.sources))
	for _, src := range r.sources {
		out = append(out, src)
	}
	return out, nil
}

func (r *memoryIPTV) GetIPTVSource(_ context.Context, id string) (*storage.IPTVSource, error) {
	if err := r.gate("GetIPTVSource"); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	src, ok := r.sources[id]
	if !ok {
		return nil, nil
	}
	return &src, nil
}

func (r *memoryIPTV) GetIPTVSourceByImportJob(_ context.Context, jobID string) (*storage.IPTVSource, error) {
	if err := r.gate("GetIPTVSourceByImportJob"); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, src := range r.sources {
		if src.ImportJobID == jobID {
			copied := src
			return &copied, nil
		}
	}
	return nil, nil
}

func (r *memoryIPTV) UpdateIPTVSource(_ context.Context, src storage.IPTVSource) error {
	if err := r.gate("UpdateIPTVSource"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources[src.ID] = src
	return nil
}

func (r *memoryIPTV) UpdateIPTVSourceRefresh(_ context.Context, id, status string, refreshedAt *time.Time, lastError string, added, missing int) error {
	if err := r.gate("UpdateIPTVSourceRefresh"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	src, ok := r.sources[id]
	if !ok {
		return errors.New("missing source")
	}
	src.RefreshStatus = status
	src.LastRefreshedAt = refreshedAt
	src.LastError = lastError
	src.LastRefreshNew = added
	src.LastRefreshMissing = missing
	r.sources[id] = src
	return nil
}

func (r *memoryIPTV) DeleteIPTVSource(_ context.Context, id string) error {
	if err := r.gate("DeleteIPTVSource"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sources, id)
	return nil
}

func (r *memoryIPTV) UpsertIPTVChannel(_ context.Context, ch storage.IPTVChannel) error {
	if err := r.gate("UpsertIPTVChannel"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.channels[ch.ID] = ch
	return nil
}

func (r *memoryIPTV) ListIPTVChannels(_ context.Context, sourceID, group, q string, favorite *bool) ([]storage.IPTVChannel, error) {
	if err := r.gate("ListIPTVChannels"); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []storage.IPTVChannel
	for _, ch := range r.channels {
		if sourceID != "" && ch.SourceID != sourceID {
			continue
		}
		if group != "" && ch.GroupName != group {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(ch.Name), strings.ToLower(q)) {
			continue
		}
		if favorite != nil && ch.Favorite != *favorite {
			continue
		}
		out = append(out, ch)
	}
	return out, nil
}

func (r *memoryIPTV) GetIPTVChannel(_ context.Context, id string) (*storage.IPTVChannel, error) {
	if err := r.gate("GetIPTVChannel"); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.channels[id]
	if !ok {
		return nil, nil
	}
	return &ch, nil
}

func (r *memoryIPTV) UpdateIPTVChannelMeta(_ context.Context, id, name string, enabled, favorite, publishEnabled bool) error {
	if err := r.gate("UpdateIPTVChannelMeta"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.channels[id]
	if !ok {
		return errors.New("missing channel")
	}
	ch.Name = name
	ch.Enabled = enabled
	ch.Favorite = favorite
	ch.PublishEnabled = publishEnabled
	r.channels[id] = ch
	return nil
}

func (r *memoryIPTV) DeleteIPTVChannel(_ context.Context, id string) error {
	if err := r.gate("DeleteIPTVChannel"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.channels, id)
	return nil
}

func (r *memoryIPTV) ListIPTVGroups(context.Context) ([]string, error) {
	if err := r.gate("ListIPTVGroups"); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]struct{}{}
	var out []string
	for _, ch := range r.channels {
		if ch.GroupName == "" {
			continue
		}
		if _, ok := seen[ch.GroupName]; ok {
			continue
		}
		seen[ch.GroupName] = struct{}{}
		out = append(out, ch.GroupName)
	}
	return out, nil
}

func (r *memoryIPTV) DeleteRecordingPlanByStream(_ context.Context, streamID string) error {
	if err := r.gate("DeleteRecordingPlanByStream"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err, ok := r.plans[streamID]; ok {
		return err
	}
	return nil
}

type fakeHLSPuller struct {
	mu         sync.Mutex
	pulling    map[string]bool
	startErr   error
	stopErr    error
	starts     int
	stops      int
	stopAll    int
	lastHeader string
}

func (p *fakeHLSPuller) StartHLSPull(_ context.Context, req media.StartHLSPullRequest) (*media.StreamSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.starts++
	p.lastHeader = req.Headers["User-Agent"]
	if p.startErr != nil {
		return nil, p.startErr
	}
	if p.pulling == nil {
		p.pulling = map[string]bool{}
	}
	p.pulling[req.StreamID] = true
	return &media.StreamSession{StreamID: req.StreamID}, nil
}

func (p *fakeHLSPuller) StopHLSPull(_ context.Context, streamID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stops++
	if p.stopErr != nil {
		return p.stopErr
	}
	delete(p.pulling, streamID)
	return nil
}

func (p *fakeHLSPuller) StopAllHLSPulls() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopAll++
	p.pulling = map[string]bool{}
}

func (p *fakeHLSPuller) PullingStreamIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.pulling))
	for id := range p.pulling {
		out = append(out, id)
	}
	return out
}

func failTransport(err error) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, err
	}), Timeout: time.Second}
}

func staticTransport(status int, body string, header http.Header) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		h := header.Clone()
		if h == nil {
			h = http.Header{}
		}
		return &http.Response{
			StatusCode: status,
			Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
			Header:     h,
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}), Timeout: time.Second}
}

func TestIPTVImportCommitAndCatalog(t *testing.T) {
	repo := newMemoryIPTV()
	puller := &fakeHLSPuller{}
	svc := NewService(repo, puller)
	svc.httpClient = failTransport(errors.New("offline"))
	t.Cleanup(svc.Stop)
	ctx := context.Background()

	_, err := svc.ImportFromReader(ctx, "", strings.NewReader(""))
	require.ErrorContains(t, err, "no channels")
	_, err = svc.ImportFromReader(ctx, "bad", errReader{})
	require.ErrorContains(t, err, "parse m3u")
	repo.fail["InsertIPTVImportJob"] = errors.New("job")
	_, err = svc.ImportFromReader(ctx, "x", strings.NewReader("#EXTM3U\nhttp://203.0.113.1/a.m3u8\n"))
	require.Error(t, err)
	delete(repo.fail, "InsertIPTVImportJob")
	repo.fail["InsertIPTVImportItems"] = errors.New("items")
	_, err = svc.ImportFromReaderWithHeaders(ctx, "x", map[string]string{"User-Agent": "ua", "Host": "nope"}, strings.NewReader("#EXTM3U\n#EXTINF:-1 tvg-id=\"a\" http-user-agent=\"item\",A\nhttp://203.0.113.1/a.m3u8\n"))
	require.Error(t, err)
	delete(repo.fail, "InsertIPTVImportItems")

	job, err := svc.ImportFromReaderWithHeaders(ctx, "", map[string]string{"Authorization": "Bearer t"}, strings.NewReader("#EXTM3U\n#EXTINF:-1 tvg-id=\"a\" http-user-agent=\"item\",A\nhttp://203.0.113.1/a.m3u8\n"))
	require.NoError(t, err)
	require.Equal(t, "IPTV import", job.Name)
	require.Equal(t, 1, job.TotalItems)
	require.Eventually(t, func() bool {
		got, err := svc.GetImport(ctx, job.ID)
		return err == nil && got != nil && got.Status == JobReady
	}, 3*time.Second, 20*time.Millisecond)
	items, err := svc.ListImportItems(ctx, job.ID, "", "a")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NotContains(t, items[0].SourceURL, "203.0.113.1/a.m3u8")
	repo.fail["ListIPTVImportItems"] = errors.New("list")
	_, err = svc.ListImportItems(ctx, job.ID, "", "")
	require.Error(t, err)
	delete(repo.fail, "ListIPTVImportItems")

	_, err = svc.ProbeItem(ctx, "missing")
	require.NoError(t, err)
	repo.fail["GetIPTVImportItem"] = errors.New("item")
	_, err = svc.ProbeItem(ctx, items[0].ID)
	require.Error(t, err)
	delete(repo.fail, "GetIPTVImportItem")
	repo.fail["GetIPTVImportJob"] = errors.New("job")
	_, err = svc.ProbeItem(ctx, items[0].ID)
	require.ErrorContains(t, err, "import job not found")
	delete(repo.fail, "GetIPTVImportJob")
	probed, err := svc.ProbeItem(ctx, items[0].ID)
	require.NoError(t, err)
	require.Equal(t, ItemFailed, probed.Status)

	_, _, err = svc.Commit(ctx, "missing", nil)
	require.ErrorContains(t, err, "not found")
	repo.fail["GetIPTVImportJob"] = errors.New("job")
	_, _, err = svc.Commit(ctx, job.ID, []string{items[0].ID})
	require.Error(t, err)
	delete(repo.fail, "GetIPTVImportJob")
	repo.fail["GetIPTVImportItemsByIDs"] = errors.New("ids")
	_, _, err = svc.Commit(ctx, job.ID, []string{items[0].ID})
	require.Error(t, err)
	delete(repo.fail, "GetIPTVImportItemsByIDs")
	_, _, err = svc.Commit(ctx, job.ID, nil)
	require.ErrorContains(t, err, "no channels selected")

	repo.mu.Lock()
	failed := repo.items[items[0].ID]
	failed.Status = ItemFailed
	repo.items[items[0].ID] = failed
	repo.items["drm"] = storage.IPTVImportItem{ID: "drm", JobID: job.ID, ExternalID: "drm", Status: ItemPlayable, DRM: true, Playable: true}
	repo.items["bad"] = storage.IPTVImportItem{ID: "bad", JobID: job.ID, ExternalID: "bad", Status: ItemUnsupported, Playable: true}
	repo.items["pend"] = storage.IPTVImportItem{ID: "pend", JobID: job.ID, ExternalID: "pend", Status: ItemPending}
	repo.mu.Unlock()
	_, _, err = svc.Commit(ctx, job.ID, []string{items[0].ID, "drm", "bad", "pend"})
	require.ErrorContains(t, err, "none of the selected")

	repo.mu.Lock()
	okItem := repo.items[items[0].ID]
	okItem.Status = ItemPlayable
	okItem.Playable = true
	okItem.Recordable = true
	okItem.ExternalID = "news"
	repo.items[items[0].ID] = okItem
	repo.items["warn"] = storage.IPTVImportItem{ID: "warn", JobID: job.ID, ExternalID: "warn", Name: "Warn", Status: ItemWarning, SourceURL: sealSecret("http://203.0.113.1/w.m3u8")}
	repo.items["dup"] = storage.IPTVImportItem{ID: "dup", JobID: job.ID, ExternalID: "news", Name: "Dup", Status: ItemPlayable, Playable: true}
	repo.jobs[job.ID] = func() storage.IPTVImportJob {
		jobCopy := repo.jobs[job.ID]
		jobCopy.Status = JobCommitted
		return jobCopy
	}()
	repo.mu.Unlock()
	_, _, err = svc.Commit(ctx, job.ID, []string{items[0].ID})
	require.ErrorContains(t, err, "no linked source")

	repo.mu.Lock()
	committed := repo.jobs[job.ID]
	committed.Status = JobReady
	committed.PlaylistURL = "http://203.0.113.9/list.m3u"
	repo.jobs[job.ID] = committed
	repo.mu.Unlock()
	repo.fail["InsertIPTVSource"] = errors.New("src")
	_, _, err = svc.Commit(ctx, job.ID, []string{items[0].ID, "warn", "dup"})
	require.Error(t, err)
	delete(repo.fail, "InsertIPTVSource")
	repo.fail["ListIPTVChannels"] = errors.New("channels")
	_, _, err = svc.Commit(ctx, job.ID, []string{items[0].ID})
	require.Error(t, err)
	delete(repo.fail, "ListIPTVChannels")
	repo.fail["UpsertIPTVChannel"] = errors.New("upsert")
	_, _, err = svc.Commit(ctx, job.ID, []string{items[0].ID})
	require.Error(t, err)
	delete(repo.fail, "UpsertIPTVChannel")
	repo.fail["GetIPTVSourceByImportJob"] = errors.New("link")
	_, _, err = svc.Commit(ctx, job.ID, []string{items[0].ID})
	require.Error(t, err)
	delete(repo.fail, "GetIPTVSourceByImportJob")

	src, added, err := svc.Commit(ctx, job.ID, []string{items[0].ID, "warn", "dup"})
	require.NoError(t, err)
	require.Equal(t, 2, added)
	require.Contains(t, src.PlaylistURL, "…")
	repo.fail["UpdateIPTVImportJob"] = errors.New("status")
	repo.mu.Lock()
	again := repo.jobs[job.ID]
	again.Status = JobReady
	repo.jobs[job.ID] = again
	repo.mu.Unlock()
	_, _, err = svc.Commit(ctx, job.ID, []string{"warn"})
	require.Error(t, err)
	delete(repo.fail, "UpdateIPTVImportJob")
	src, added, err = svc.Commit(ctx, job.ID, []string{items[0].ID, "warn"})
	require.NoError(t, err)
	require.Zero(t, added)

	sources, err := svc.ListSources(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, sources)
	require.Empty(t, sources[0].RequestHeaders)
	require.Equal(t, "idle", sources[0].RefreshStatus)
	repo.fail["ListIPTVSources"] = errors.New("list")
	_, err = svc.ListSources(ctx)
	require.Error(t, err)
	delete(repo.fail, "ListIPTVSources")

	_, err = svc.UpdateSource(ctx, "missing", "n", true, 3600)
	require.NoError(t, err)
	_, err = svc.UpdateSource(ctx, src.ID, "Renamed", true, 10)
	require.ErrorContains(t, err, "refresh interval")
	_, err = svc.UpdateSource(ctx, src.ID, "  ", false, 7200)
	require.NoError(t, err)
	updated, err := svc.GetSource(ctx, src.ID)
	require.NoError(t, err)
	require.Equal(t, src.Name, updated.Name)
	require.False(t, updated.Enabled)
	require.Equal(t, 7200, updated.RefreshIntervalSec)
	repo.fail["UpdateIPTVSource"] = errors.New("upd")
	_, err = svc.UpdateSource(ctx, src.ID, "Next", true, 3600)
	require.Error(t, err)
	delete(repo.fail, "UpdateIPTVSource")
	_, err = svc.GetSource(ctx, "missing")
	require.NoError(t, err)
	repo.fail["GetIPTVSource"] = errors.New("get")
	_, err = svc.GetSource(ctx, src.ID)
	require.Error(t, err)
	delete(repo.fail, "GetIPTVSource")

	channels, err := svc.ListChannels(ctx, src.ID, "", "", nil)
	require.NoError(t, err)
	require.NotEmpty(t, channels)
	require.Empty(t, channels[0].SourceURL)
	repo.fail["ListIPTVChannels"] = errors.New("list")
	_, err = svc.ListChannels(ctx, "", "", "", nil)
	require.Error(t, err)
	delete(repo.fail, "ListIPTVChannels")
	repo.mu.Lock()
	grouped := repo.channels[channels[0].ID]
	grouped.GroupName = "News"
	repo.channels[channels[0].ID] = grouped
	repo.mu.Unlock()
	groups, err := svc.ListGroups(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"News"}, groups)
	ch, err := svc.GetChannel(ctx, channels[0].ID)
	require.NoError(t, err)
	require.Empty(t, ch.SourceURL)
	_, err = svc.GetChannel(ctx, "missing")
	require.NoError(t, err)

	_, err = svc.CheckChannel(ctx, "missing")
	require.NoError(t, err)
	repo.mu.Lock()
	stored := repo.channels[channels[0].ID]
	stored.SourceID = "gone"
	repo.channels[channels[0].ID] = stored
	repo.mu.Unlock()
	_, err = svc.CheckChannel(ctx, channels[0].ID)
	require.ErrorContains(t, err, "source not found")
	repo.mu.Lock()
	stored.SourceID = src.ID
	stored.SourceURL = sealSecret("http://203.0.113.1/a.m3u8")
	stored.RequestHeaders = sealHeaders(map[string]string{"Referer": "https://example.test"})
	repo.channels[channels[0].ID] = stored
	repo.sources[src.ID] = func() storage.IPTVSource {
		current := repo.sources[src.ID]
		current.RequestHeaders = sealHeaders(map[string]string{"User-Agent": "src"})
		current.Enabled = true
		return current
	}()
	repo.mu.Unlock()
	checked, err := svc.CheckChannel(ctx, channels[0].ID)
	require.NoError(t, err)
	require.Equal(t, ItemFailed, checked.ProbeStatus)
	repo.fail["UpsertIPTVChannel"] = errors.New("upsert")
	_, err = svc.CheckChannel(ctx, channels[0].ID)
	require.Error(t, err)
	delete(repo.fail, "UpsertIPTVChannel")

	_, err = svc.GetPlaybackDetails(ctx, "missing")
	require.NoError(t, err)
	repo.mu.Lock()
	disabled := repo.channels[channels[0].ID]
	disabled.Enabled = false
	repo.channels[channels[0].ID] = disabled
	repo.mu.Unlock()
	_, err = svc.GetPlaybackDetails(ctx, channels[0].ID)
	require.ErrorContains(t, err, "disabled")
	repo.mu.Lock()
	disabled.Enabled = true
	repo.channels[channels[0].ID] = disabled
	off := repo.sources[src.ID]
	off.Enabled = false
	repo.sources[src.ID] = off
	repo.mu.Unlock()
	_, err = svc.GetPlaybackDetails(ctx, channels[0].ID)
	require.ErrorContains(t, err, "disabled")
	repo.mu.Lock()
	off.Enabled = true
	repo.sources[src.ID] = off
	repo.mu.Unlock()
	details, err := svc.GetPlaybackDetails(ctx, channels[0].ID)
	require.NoError(t, err)
	require.Contains(t, details.URL, channels[0].ID)

	_, err = svc.UpdateChannel(ctx, channels[0].ID, "News", false, true, true)
	require.ErrorContains(t, err, "cannot publish")
	repo.mu.Lock()
	current := repo.channels[channels[0].ID]
	current.Enabled = true
	current.Recordable = false
	current.PublishEnabled = false
	repo.channels[channels[0].ID] = current
	repo.mu.Unlock()
	_, err = svc.UpdateChannel(ctx, channels[0].ID, " News ", true, true, true)
	require.ErrorContains(t, err, "not supported")
	repo.mu.Lock()
	current.Recordable = true
	repo.channels[channels[0].ID] = current
	repo.mu.Unlock()
	bare := NewService(repo, nil)
	_, err = bare.UpdateChannel(ctx, channels[0].ID, "", true, false, true)
	require.ErrorIs(t, err, media.ErrHLSPullEmbeddedOnly)
	renamed, err := svc.UpdateChannel(ctx, channels[0].ID, " News ", true, true, true)
	require.NoError(t, err)
	require.Equal(t, "News", renamed.Name)
	require.True(t, renamed.PublishEnabled)
	repo.fail["UpdateIPTVChannelMeta"] = errors.New("meta")
	_, err = svc.UpdateChannel(ctx, channels[0].ID, "News", true, true, true)
	require.Error(t, err)
	delete(repo.fail, "UpdateIPTVChannelMeta")

	repo.plans[renamed.StreamID] = errors.New("plan")
	require.NoError(t, svc.DeleteChannel(ctx, renamed.ID))
	_, err = svc.GetChannel(ctx, renamed.ID)
	require.NoError(t, err)
	repo.fail["DeleteIPTVChannel"] = errors.New("del")
	require.Error(t, svc.DeleteChannel(ctx, "missing"))
	delete(repo.fail, "DeleteIPTVChannel")
	repo.fail["GetIPTVChannel"] = errors.New("get")
	require.Error(t, svc.DeleteChannel(ctx, renamed.ID))
	delete(repo.fail, "GetIPTVChannel")
}

func TestIPTVRefreshReconcileAndPull(t *testing.T) {
	repo := newMemoryIPTV()
	puller := &fakeHLSPuller{pulling: map[string]bool{}}
	svc := NewService(repo, puller)
	t.Cleanup(svc.Stop)
	ctx := context.Background()

	release := make(chan struct{})
	var releaseOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "playlist") {
			<-release
			_, _ = io.WriteString(w, "#EXTM3U\n#EXTINF:-1 tvg-id=\"news\" group-title=\"News\",News\nhttp://203.0.113.10/news.m3u8\n#EXTINF:-1 tvg-id=\"news\",Dup\nhttp://203.0.113.10/dup.m3u8\n")
			return
		}
		_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n")
	}))
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		srv.Close()
	})
	svc.httpClient.Transport = &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, srv.Listener.Addr().String())
	}}

	require.ErrorContains(t, svc.RefreshSource(ctx, "missing"), "not found")
	repo.fail["GetIPTVSource"] = errors.New("get")
	require.Error(t, svc.RefreshSource(ctx, "src"))
	delete(repo.fail, "GetIPTVSource")
	require.NoError(t, repo.InsertIPTVSource(ctx, storage.IPTVSource{ID: "src", Name: "Src", Enabled: true}))
	require.ErrorContains(t, svc.RefreshSource(ctx, "src"), "no playlist")
	require.NoError(t, repo.UpdateIPTVSource(ctx, storage.IPTVSource{ID: "src", Name: "Src", Enabled: true, PlaylistURL: "http://203.0.113.10/playlist.m3u", RequestHeaders: sealHeaders(map[string]string{"User-Agent": "src"})}))
	repo.fail["UpdateIPTVSourceRefresh"] = errors.New("status")
	require.Error(t, svc.RefreshSource(ctx, "src"))
	delete(repo.fail, "UpdateIPTVSourceRefresh")

	now := time.Now()
	require.NoError(t, repo.UpsertIPTVChannel(ctx, storage.IPTVChannel{ID: "news", SourceID: "src", ExternalID: "news", StreamID: "iptv_news", Name: "Old", Enabled: true, SourceURL: sealSecret("http://203.0.113.10/old.m3u8")}))
	require.NoError(t, repo.UpsertIPTVChannel(ctx, storage.IPTVChannel{ID: "gone", SourceID: "src", ExternalID: "gone", StreamID: "iptv_gone", Name: "Gone", Enabled: false}))
	require.NoError(t, svc.RefreshSource(ctx, "src"))
	require.NoError(t, svc.RefreshSource(ctx, "src"))
	releaseOnce.Do(func() { close(release) })
	require.Eventually(t, func() bool {
		src, err := repo.GetIPTVSource(ctx, "src")
		return err == nil && src != nil && src.RefreshStatus == "ready"
	}, 5*time.Second, 20*time.Millisecond)
	refreshed, err := repo.GetIPTVChannel(ctx, "news")
	require.NoError(t, err)
	require.Equal(t, "News", refreshed.GroupName)

	require.NoError(t, repo.UpdateIPTVSource(ctx, storage.IPTVSource{ID: "bad", Name: "Bad", Enabled: true, PlaylistURL: "http://203.0.113.10/missing.m3u"}))
	svc.httpClient = failTransport(errors.New("down"))
	require.NoError(t, svc.RefreshSource(ctx, "bad"))
	require.Eventually(t, func() bool {
		src, err := repo.GetIPTVSource(ctx, "bad")
		return err == nil && src != nil && src.RefreshStatus == "failed"
	}, 3*time.Second, 20*time.Millisecond)
	svc.Stop()

	plain := NewService(repo, puller)
	plain.httpClient = staticTransport(http.StatusOK, "", nil)
	_, _, err = plain.refreshSource(context.Background(), "missing")
	require.ErrorContains(t, err, "not found")
	require.NoError(t, repo.UpdateIPTVSource(ctx, storage.IPTVSource{ID: "empty", PlaylistURL: "http://203.0.113.10/empty.m3u", RequestHeaders: "not-json"}))
	plain.httpClient = staticTransport(http.StatusOK, "#EXTM3U\n", nil)
	_, _, err = plain.refreshSource(ctx, "empty")
	require.ErrorContains(t, err, "no channels")
	plain.httpClient = staticTransport(http.StatusBadGateway, "", nil)
	_, _, err = plain.refreshSource(ctx, "src")
	require.ErrorContains(t, err, "download failed")
	repo.fail["ListIPTVChannels"] = errors.New("list")
	plain.httpClient = staticTransport(http.StatusOK, "#EXTM3U\n#EXTINF:-1 tvg-id=\"news\",News\nhttp://203.0.113.10/a.m3u8\n", nil)
	_, _, err = plain.refreshSource(ctx, "src")
	require.Error(t, err)
	delete(repo.fail, "ListIPTVChannels")
	repo.fail["UpsertIPTVChannel"] = errors.New("upsert")
	_, _, err = plain.refreshSource(ctx, "src")
	require.Error(t, err)
	delete(repo.fail, "UpsertIPTVChannel")

	cancelled, cancel := context.WithCancel(context.Background())
	plain.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "playlist") {
			return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader("#EXTM3U\n#EXTINF:-1 tvg-id=\"news\",News\nhttp://203.0.113.10/news.m3u8\n#EXTINF:-1 tvg-id=\"other\",Other\nhttp://203.0.113.10/other.m3u8\n")), Request: req}, nil
		}
		cancel()
		return nil, context.Canceled
	}), Timeout: time.Second}
	require.NoError(t, repo.UpsertIPTVChannel(ctx, storage.IPTVChannel{ID: "other", SourceID: "src", ExternalID: "other", Name: "Other", Enabled: true, SourceURL: sealSecret("http://203.0.113.10/other.m3u8")}))
	_, _, err = plain.refreshSource(cancelled, "src")
	require.Error(t, err)

	due := NewService(repo, puller)
	due.httpClient = failTransport(errors.New("down"))
	t.Cleanup(due.Stop)
	old := now.Add(-48 * time.Hour)
	require.NoError(t, repo.UpdateIPTVSource(ctx, storage.IPTVSource{ID: "due", Name: "Due", Enabled: true, PlaylistURL: "http://203.0.113.10/playlist.m3u", RefreshIntervalSec: 3600, LastRefreshedAt: &old}))
	require.NoError(t, repo.UpdateIPTVSource(ctx, storage.IPTVSource{ID: "fresh", Name: "Fresh", Enabled: true, PlaylistURL: "http://203.0.113.10/playlist.m3u", RefreshIntervalSec: 0, LastRefreshedAt: &now}))
	require.NoError(t, repo.UpdateIPTVSource(ctx, storage.IPTVSource{ID: "off", Name: "Off", Enabled: false, PlaylistURL: "http://203.0.113.10/playlist.m3u"}))
	require.NoError(t, repo.UpdateIPTVSource(ctx, storage.IPTVSource{ID: "busy", Name: "Busy", Enabled: true, PlaylistURL: "http://203.0.113.10/playlist.m3u", RefreshStatus: "running"}))
	require.NoError(t, repo.UpdateIPTVSource(ctx, storage.IPTVSource{ID: "blank", Name: "Blank", Enabled: true}))
	repo.fail["UpdateIPTVSourceRefresh"] = errors.New("queue")
	due.refreshDueSources(ctx)
	delete(repo.fail, "UpdateIPTVSourceRefresh")
	repo.fail["ListIPTVSources"] = errors.New("list")
	due.refreshDueSources(ctx)
	delete(repo.fail, "ListIPTVSources")
	loopCtx, loopCancel := context.WithCancel(context.Background())
	due.StartRefreshLoop(loopCtx)
	loopCancel()
	time.Sleep(20 * time.Millisecond)

	repo.plans["iptv_news"] = errors.New("plan")
	require.NoError(t, due.DeleteSource(ctx, "src"))
	repo.fail["ListIPTVChannels"] = errors.New("list")
	require.Error(t, due.DeleteSource(ctx, "src"))
	delete(repo.fail, "ListIPTVChannels")

	require.NoError(t, repo.UpsertIPTVChannel(ctx, storage.IPTVChannel{
		ID: "pub", SourceID: "src", ExternalID: "pub", StreamID: "iptv_pub", Name: "Pub",
		Enabled: true, Recordable: true, PublishEnabled: true, SourceURL: sealSecret("http://203.0.113.10/pub.m3u8"),
		RequestHeaders: sealHeaders(map[string]string{"User-Agent": "ch"}),
	}))
	require.NoError(t, repo.UpdateIPTVSource(ctx, storage.IPTVSource{ID: "src", Enabled: true, RequestHeaders: sealHeaders(map[string]string{"Referer": "https://example.test"})}))
	require.True(t, due.ReconcileSources(ctx, map[string]bool{}, nil))
	require.False(t, due.ReconcileSources(ctx, nil, func(string) bool { return false }))
	puller.mu.Lock()
	puller.pulling["iptv_pub"] = true
	puller.mu.Unlock()
	repo.mu.Lock()
	ch := repo.channels["pub"]
	ch.PublishEnabled = false
	ch.Enabled = false
	repo.channels["pub"] = ch
	repo.mu.Unlock()
	require.False(t, due.ReconcileSources(ctx, nil, nil))
	puller.mu.Lock()
	puller.pulling["iptv_stop"] = true
	puller.stopErr = errors.New("stop")
	puller.startErr = errors.New("start")
	puller.mu.Unlock()
	require.NoError(t, repo.UpsertIPTVChannel(ctx, storage.IPTVChannel{ID: "stop", SourceID: "src", ExternalID: "stop", StreamID: "iptv_stop", Name: "Stop", Enabled: true}))
	require.NoError(t, repo.UpsertIPTVChannel(ctx, storage.IPTVChannel{ID: "want", SourceID: "src", ExternalID: "want", StreamID: "iptv_want", Name: "Want", Enabled: true, Recordable: true}))
	require.False(t, due.ReconcileSources(ctx, map[string]bool{"iptv_want": true}, func(id string) bool { return id == "iptv_want" }))
	repo.fail["ListIPTVChannels"] = errors.New("list")
	require.False(t, due.ReconcileSources(ctx, nil, nil))
	delete(repo.fail, "ListIPTVChannels")
	require.False(t, (*Service)(nil).ReconcileSources(ctx, nil, nil))
	require.False(t, NewService(repo, nil).ReconcileSources(ctx, nil, nil))

	puller.mu.Lock()
	puller.startErr = nil
	puller.mu.Unlock()
	require.ErrorIs(t, NewService(repo, nil).startSourcePull(ctx, "pub"), media.ErrHLSPullEmbeddedOnly)
	require.NoError(t, due.startSourcePull(ctx, "missing"))
	due.mu.Lock()
	due.starting["pub"] = struct{}{}
	due.mu.Unlock()
	require.NoError(t, due.startSourcePull(ctx, "pub"))
	due.mu.Lock()
	delete(due.starting, "pub")
	due.mu.Unlock()
	repo.mu.Lock()
	ch = repo.channels["pub"]
	ch.SourceID = "missing"
	repo.channels["pub"] = ch
	repo.mu.Unlock()
	require.ErrorContains(t, due.startSourcePull(ctx, "pub"), "source not found")
	repo.mu.Lock()
	ch.SourceID = "src"
	repo.channels["pub"] = ch
	repo.mu.Unlock()
	require.NoError(t, due.startSourcePull(ctx, "pub"))
	require.Positive(t, puller.starts)
	require.NoError(t, NewService(repo, nil).stopSourcePull(ctx, "pub"))
	require.NoError(t, due.stopSourcePull(ctx, "missing"))
	puller.mu.Lock()
	puller.stopErr = errors.New("stop")
	puller.mu.Unlock()
	require.Error(t, due.stopSourcePull(ctx, "pub"))

	merged := mergeRequestHeaders(map[string]string{"user-agent": "a"}, map[string]string{"Referer": "b"})
	require.Equal(t, "a", merged["User-Agent"])
	require.Equal(t, "b", merged["Referer"])
	require.Nil(t, mergeRequestHeaders(nil, nil))
	require.Contains(t, channelStreamID("src", "news"), "iptv_")
}

func TestIPTVProbeDownloadAndProxyEdges(t *testing.T) {
	repo := newMemoryIPTV()
	svc := NewService(repo, &fakeHLSPuller{})
	ctx := context.Background()

	redirectReq, err := http.NewRequest(http.MethodGet, "http://203.0.113.4/next", nil)
	require.NoError(t, err)
	require.ErrorContains(t, svc.httpClient.CheckRedirect(redirectReq, make([]*http.Request, 5)), "too many redirects")
	blocked, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/secret", nil)
	require.NoError(t, err)
	require.Error(t, svc.httpClient.CheckRedirect(blocked, nil))
	allowed, err := http.NewRequest(http.MethodGet, "http://203.0.113.4/ok", nil)
	require.NoError(t, err)
	require.NoError(t, svc.httpClient.CheckRedirect(allowed, nil))

	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	cancel()
	timed := svc.probeURL(expired, "http://203.0.113.4/a.m3u8", nil)
	require.Equal(t, "probe timeout", timed.Error)
	blockedRes := svc.probeURL(ctx, "http://127.0.0.1/a.m3u8", nil)
	require.Contains(t, blockedRes.Error, "not allowed")
	svc.httpClient = failTransport(errors.New("dial"))
	failed := svc.probeURL(ctx, "http://203.0.113.4/a.m3u8", map[string]string{"User-Agent": "ua", "X-Skip": "1"})
	require.Contains(t, failed.Error, "dial")

	svc.httpClient = staticTransport(http.StatusForbidden, "", nil)
	denied := svc.probeURL(ctx, "http://203.0.113.4/a.m3u8", nil)
	require.Equal(t, ItemUnsupported, denied.Status)
	svc.httpClient = staticTransport(http.StatusBadGateway, "", nil)
	bad := svc.probeURL(ctx, "http://203.0.113.4/a.m3u8", nil)
	require.Equal(t, ItemFailed, bad.Status)
	svc.httpClient = staticTransport(http.StatusOK, "not a playlist", nil)
	plain := svc.probeURL(ctx, "http://203.0.113.4/a.m3u8", nil)
	require.Contains(t, plain.Error, "not an HLS")
	svc.httpClient = staticTransport(http.StatusOK, "#EXTM3U\nSAMPLE-AES\n", nil)
	drm := svc.probeURL(ctx, "http://203.0.113.4/a.m3u8", nil)
	require.True(t, drm.DRM)
	svc.httpClient = staticTransport(http.StatusOK, "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128\n", nil)
	encrypted := svc.probeURL(ctx, "http://203.0.113.4/a.m3u8", nil)
	require.Equal(t, ItemWarning, encrypted.Status)
	svc.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(io.MultiReader(strings.NewReader("#EXTM3U"), io.LimitReader(neverEnding('a'), maxPlaylistBytes+8))), Request: req}, nil
	})}
	large := svc.probeURL(ctx, "http://203.0.113.4/a.m3u8", nil)
	require.Contains(t, large.Error, "too large")
	svc.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: errBody{}, Request: req}, nil
	})}
	unreadable := svc.probeURL(ctx, "http://203.0.113.4/a.m3u8", nil)
	require.Contains(t, unreadable.Error, "read")

	probeCtx, probeCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer probeCancel()
	svc.httpClient = staticTransport(http.StatusOK, "#EXTM3U\n#EXT-X-VERSION:3\n", nil)
	mediaRes := svc.probeURL(probeCtx, "http://203.0.113.4/live.m3u8", nil)
	require.NotEmpty(t, mediaRes.Error)

	req, err := http.NewRequest(http.MethodGet, "http://203.0.113.4/seg", nil)
	require.NoError(t, err)
	transport := &headerProbeTransport{base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "ua", r.Header.Get("User-Agent"))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
	}), headers: map[string]string{"User-Agent": "ua"}}
	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()

	_, err = svc.downloadPlaylist(ctx, "http://\x7f", nil)
	require.Error(t, err)
	svc.httpClient = failTransport(errors.New("down"))
	_, err = svc.downloadPlaylist(ctx, "http://203.0.113.4/list.m3u", map[string]string{"User-Agent": "ua"})
	require.Error(t, err)
	svc.httpClient = staticTransport(http.StatusNotFound, "", nil)
	_, err = svc.downloadPlaylist(ctx, "http://203.0.113.4/list.m3u", nil)
	require.ErrorContains(t, err, "download failed")
	svc.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: errBody{}, Request: req}, nil
	})}
	_, err = svc.downloadPlaylist(ctx, "http://203.0.113.4/list.m3u", nil)
	require.Error(t, err)
	svc.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(io.LimitReader(neverEnding('b'), maxPlaylistBytes+8)), Request: req}, nil
	})}
	_, err = svc.downloadPlaylist(ctx, "http://203.0.113.4/list.m3u", nil)
	require.ErrorContains(t, err, "too large")

	hops := 0
	redirectCheck := NewService(repo, nil).httpClient.CheckRedirect
	svc.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		hops++
		if hops < 6 {
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"http://203.0.113.4/next"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("#EXTM3U\n")), Request: req}, nil
	}), CheckRedirect: redirectCheck}
	_, err = svc.ImportFromURL(ctx, ImportRequest{PlaylistURL: "http://127.0.0.1/list.m3u"})
	require.Error(t, err)
	_, err = svc.ImportFromURL(ctx, ImportRequest{Name: "Remote", PlaylistURL: "http://203.0.113.4/list.m3u", Headers: map[string]string{"User-Agent": "ua"}})
	require.ErrorContains(t, err, "too many redirects")

	require.NoError(t, repo.InsertIPTVSource(ctx, storage.IPTVSource{ID: "src", Enabled: true, RequestHeaders: sealHeaders(map[string]string{"Authorization": "secret"})}))
	require.NoError(t, repo.UpsertIPTVChannel(ctx, storage.IPTVChannel{ID: "ch", SourceID: "src", Enabled: true, SourceURL: sealSecret("http://203.0.113.4/live.m3u8"), RequestHeaders: sealHeaders(map[string]string{"Referer": "https://example.test"})}))
	longURL := "http://203.0.113.4/" + strings.Repeat("a", maxProxyURLLength)
	_, _, err = svc.OpenPlaybackResource(ctx, "ch", longURL, "")
	require.ErrorContains(t, err, "too long")
	_, _, err = svc.OpenPlaybackResource(ctx, "missing", "", "")
	require.ErrorContains(t, err, "not found")
	repo.mu.Lock()
	ch := repo.channels["ch"]
	ch.Enabled = false
	repo.channels["ch"] = ch
	repo.mu.Unlock()
	_, _, err = svc.OpenPlaybackResource(ctx, "ch", "", "")
	require.ErrorContains(t, err, "disabled")
	repo.mu.Lock()
	ch.Enabled = true
	repo.channels["ch"] = ch
	src := repo.sources["src"]
	src.Enabled = false
	repo.sources["src"] = src
	repo.mu.Unlock()
	_, _, err = svc.OpenPlaybackResource(ctx, "ch", "", "")
	require.ErrorContains(t, err, "disabled")
	repo.mu.Lock()
	src.Enabled = true
	repo.sources["src"] = src
	ch.SourceURL = "http://%"
	repo.channels["ch"] = ch
	repo.mu.Unlock()
	_, _, err = svc.OpenPlaybackResource(ctx, "ch", "http://203.0.113.4/ok.ts", "")
	require.ErrorContains(t, err, "invalid channel")
	repo.mu.Lock()
	ch.SourceURL = sealSecret("http://203.0.113.4/live.m3u8")
	repo.channels["ch"] = ch
	repo.mu.Unlock()
	svc.httpClient = failTransport(errors.New("proxy"))
	_, _, err = svc.OpenPlaybackResource(ctx, "ch", "", "")
	require.Error(t, err)

	seen := 0
	svc.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen++
		if seen == 1 {
			require.Equal(t, "secret", req.Header.Get("Authorization"))
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"http://203.0.113.9/other.ts"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		}
		require.Empty(t, req.Header.Get("Authorization"))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("seg")), Request: &http.Request{}}, nil
	}), CheckRedirect: NewService(repo, nil).httpClient.CheckRedirect}
	resp, finalURL, err := svc.OpenPlaybackResource(ctx, "ch", "", "")
	require.NoError(t, err)
	require.NotNil(t, finalURL)
	resp.Body.Close()

	base, err := url.Parse("https://cdn.example.test/live/index.m3u8")
	require.NoError(t, err)
	_, err = RewriteHLSManifest(bytesRepeat(maxPlaylistBytes+1), base, "ch")
	require.ErrorContains(t, err, "too large")
	_, err = RewriteHLSManifest([]byte("http://203.0.113.4/a.ts"), nil, "ch")
	require.ErrorContains(t, err, "missing")
	rewritten, err := RewriteHLSManifest([]byte("#EXTM3U\n#EXT-X-KEY:URI=\"data:text/plain,abc\"\n"), base, "ch")
	require.NoError(t, err)
	require.Contains(t, string(rewritten), "data:text/plain")
	_, err = RewriteHLSManifest([]byte("http://[::1\n"), base, "ch")
	require.Error(t, err)
	require.False(t, sameURLOrigin(nil, base))
	httpsURL, err := url.Parse("https://cdn.example.test/a")
	require.NoError(t, err)
	httpURL, err := url.Parse("http://cdn.example.test/a")
	require.NoError(t, err)
	require.False(t, sameURLOrigin(httpsURL, httpURL))
	require.True(t, IsHLSManifest("application/vnd.apple.mpegurl", nil))
	require.True(t, IsHLSManifest("", base))
	require.False(t, IsHLSManifest("video/mp2t", httpURL))

	require.Error(t, validatePublicURL("http://localhost/a"))
	require.Error(t, validatePublicURL("http://169.254.1.1/a"))
	require.Error(t, validatePublicURL("http://0.0.0.0/a"))
	require.Error(t, validatePublicURL("http://[::1]/a"))
	require.NoError(t, validatePublicURL("http://203.0.113.8/a"))
	require.True(t, isBlockedIP(nil))
	require.Nil(t, sanitizeHeaders(map[string]string{"Host": "x", "User-Agent": "  "}))
	require.Nil(t, sanitizeHeaders(nil))
	require.Equal(t, "", redactURL(""))
	require.Contains(t, redactURL("short"), "…")
	require.Contains(t, redactURL("https://example.test/very/long/playlist.m3u"), "…")
	require.Equal(t, "", sealSecret(""))
	require.Equal(t, "plain", openSecret("plain"))
	require.Equal(t, "", sealHeaders(nil))
	require.Nil(t, openHeaders("not-json"))
	require.Nil(t, openHeaders(""))
	sealed := sealHeaders(map[string]string{"Cookie": "a=b"})
	require.Equal(t, "a=b", openHeaders(sealed)["Cookie"])
}

func TestIPTVParserAndProbeJobEdges(t *testing.T) {
	got, err := ParseM3U(strings.NewReader("#EXTM3U\n#EXTINF:-1,Name\n#EXTGRP:Docs\n#EXTVLCOPT:http-referrer=skip\nhttp://203.0.113.1/live/index.m3u8\nhttp://203.0.113.1/raw.ts\n"))
	require.NoError(t, err)
	require.Equal(t, "Docs", got[0].GroupName)
	require.Equal(t, "raw.ts", got[1].Name)
	require.Equal(t, "live", fallbackName("http://203.0.113.1/live/index.m3u8"))
	require.Equal(t, "clip", fallbackName("http://203.0.113.1/clip.m3u"))
	require.NotEmpty(t, fallbackName(":::"))
	long := strings.Repeat("a b", 40)
	require.LessOrEqual(t, len(normalizeExternalID(long, "")), 64)
	require.Equal(t, "channel", normalizeExternalID("***", ""))
	attrs := parseAttributes(`broken=unterminated group-title="News" bad key=skip empty=`)
	require.Equal(t, "News", attrs["group-title"])
	require.Equal(t, "keep", firstNonEmpty(" ", "keep"))
	_, err = ParseM3U(errReader{})
	require.Error(t, err)

	repo := newMemoryIPTV()
	svc := NewService(repo, nil)
	svc.httpClient = failTransport(errors.New("offline"))
	t.Cleanup(svc.Stop)
	ctx := context.Background()
	var body strings.Builder
	body.WriteString("#EXTM3U\n")
	for i := 0; i < maxImportItems+1; i++ {
		fmt.Fprintf(&body, "#EXTINF:-1 tvg-id=\"c%d\",C%d\nhttp://203.0.113.1/%d.m3u8\n", i, i, i)
	}
	job, err := svc.ImportFromReader(ctx, "bulk", strings.NewReader(body.String()))
	require.NoError(t, err)
	require.Equal(t, maxImportItems, job.TotalItems)
	require.Eventually(t, func() bool {
		got, err := repo.GetIPTVImportJob(ctx, job.ID)
		return err == nil && got != nil && got.Status == JobReady
	}, 8*time.Second, 20*time.Millisecond)

	repo.fail["ListIPTVImportItems"] = errors.New("list")
	require.NoError(t, repo.InsertIPTVImportJob(ctx, storage.IPTVImportJob{ID: "job-list", Status: JobProbing}))
	svc.probeJob("job-list")
	failed, err := repo.GetIPTVImportJob(ctx, "job-list")
	require.NoError(t, err)
	require.Equal(t, JobFailed, failed.Status)
	delete(repo.fail, "ListIPTVImportItems")
	svc.probeJob("absent")
	repo.fail["UpdateIPTVImportItem"] = errors.New("item")
	require.NoError(t, repo.InsertIPTVImportJob(ctx, storage.IPTVImportJob{ID: "job-upd", Status: JobProbing, RequestHeaders: sealHeaders(map[string]string{"User-Agent": "job"})}))
	require.NoError(t, repo.InsertIPTVImportItems(ctx, []storage.IPTVImportItem{{
		ID: "item-upd", JobID: "job-upd", Name: "U", SourceURL: sealSecret("http://203.0.113.1/u.m3u8"),
		RequestHeaders: sealHeaders(map[string]string{"Referer": "https://example.test"}),
	}}))
	svc.probeJob("job-upd")
	delete(repo.fail, "UpdateIPTVImportItem")
	ready, err := repo.GetIPTVImportJob(ctx, "job-upd")
	require.NoError(t, err)
	require.Equal(t, JobReady, ready.Status)

	hold := make(chan struct{})
	svc.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		<-hold
		return nil, errors.New("cancelled")
	})}
	require.NoError(t, repo.InsertIPTVImportJob(ctx, storage.IPTVImportJob{ID: "job-stop", Status: JobProbing}))
	require.NoError(t, repo.InsertIPTVImportItems(ctx, []storage.IPTVImportItem{{ID: "item-stop", JobID: "job-stop", SourceURL: "http://203.0.113.1/s.m3u8"}}))
	go svc.probeJob("job-stop")
	require.Eventually(t, func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		_, ok := svc.probing["job-stop"]
		return ok
	}, time.Second, 10*time.Millisecond)
	svc.Stop()
	close(hold)
	require.Eventually(t, func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		_, ok := svc.probing["job-stop"]
		return !ok
	}, time.Second, 10*time.Millisecond)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, errors.New("read") }
func (errBody) Close() error             { return nil }

type neverEnding byte

func (b neverEnding) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

func bytesRepeat(n int) []byte {
	return []byte(strings.Repeat("a", n))
}
