package iptv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

const (
	probeConcurrency = 4
	maxImportItems   = 2000
)

var logger = slog.Default().With("component", "iptv")

type Service struct {
	db         *storage.DB
	puller     media.HLSPuller
	httpClient *http.Client

	mu       sync.Mutex
	probing  map[string]context.CancelFunc
	starting map[string]struct{}
}

// PlaybackDetails contains the same-origin HLS proxy URL for browser playback.
type PlaybackDetails struct {
	URL string `json:"url"`
}

func NewService(db *storage.DB, puller media.HLSPuller) *Service {
	return &Service{
		db:     db,
		puller: puller,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   8 * time.Second,
				ResponseHeaderTimeout: 10 * time.Second,
				MaxIdleConns:          32,
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				if err := validatePublicURL(req.URL.String()); err != nil {
					return err
				}
				return nil
			},
		},
		probing:  make(map[string]context.CancelFunc),
		starting: make(map[string]struct{}),
	}
}

type ImportRequest struct {
	Name        string            `json:"name"`
	PlaylistURL string            `json:"playlist_url"`
	Headers     map[string]string `json:"headers"`
}

func (s *Service) ImportFromURL(ctx context.Context, req ImportRequest) (*storage.IPTVImportJob, error) {
	playlistURL := strings.TrimSpace(req.PlaylistURL)
	if err := validatePublicURL(playlistURL); err != nil {
		return nil, err
	}
	headers := sanitizeHeaders(req.Headers)
	body, err := s.downloadPlaylist(ctx, playlistURL, headers)
	if err != nil {
		return nil, err
	}
	return s.importPlaylist(ctx, req.Name, playlistURL, headers, body)
}

func (s *Service) ImportFromReader(ctx context.Context, name string, r io.Reader) (*storage.IPTVImportJob, error) {
	return s.importPlaylist(ctx, name, "", nil, r)
}

func (s *Service) importPlaylist(ctx context.Context, name, playlistURL string, headers map[string]string, r io.Reader) (*storage.IPTVImportJob, error) {
	channels, err := ParseM3U(r)
	if err != nil {
		return nil, fmt.Errorf("parse m3u: %w", err)
	}
	if len(channels) == 0 {
		return nil, errors.New("playlist contains no channels")
	}
	if len(channels) > maxImportItems {
		channels = channels[:maxImportItems]
	}
	if strings.TrimSpace(name) == "" {
		name = "IPTV import"
	}
	job := storage.IPTVImportJob{
		ID:             uuid.NewString(),
		Name:           name,
		PlaylistURL:    playlistURL,
		RequestHeaders: sealHeaders(headers),
		Status:         JobProbing,
		TotalItems:     len(channels),
	}
	if err := s.db.InsertIPTVImportJob(ctx, job); err != nil {
		return nil, err
	}
	items := make([]storage.IPTVImportItem, 0, len(channels))
	for _, ch := range channels {
		items = append(items, storage.IPTVImportItem{
			ID:             uuid.NewString(),
			JobID:          job.ID,
			RowNo:          ch.RowNo,
			ExternalID:     ch.ExternalID,
			Name:           ch.Name,
			GroupName:      ch.GroupName,
			LogoURL:        ch.LogoURL,
			ChannelNo:      ch.ChannelNo,
			SourceURL:      sealSecret(ch.SourceURL),
			RequestHeaders: sealHeaders(ch.Headers),
			Status:         ItemPending,
		})
	}
	if err := s.db.InsertIPTVImportItems(ctx, items); err != nil {
		return nil, err
	}
	go s.probeJob(job.ID)
	return &job, nil
}

func (s *Service) GetImport(ctx context.Context, id string) (*storage.IPTVImportJob, error) {
	return s.db.GetIPTVImportJob(ctx, id)
}

func (s *Service) ListImportItems(ctx context.Context, jobID, status, q string) ([]storage.IPTVImportItem, error) {
	items, err := s.db.ListIPTVImportItems(ctx, jobID, status, q)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].SourceURL = redactURL(openSecret(items[i].SourceURL))
	}
	return items, nil
}

func (s *Service) ProbeItem(ctx context.Context, itemID string) (*storage.IPTVImportItem, error) {
	item, err := s.db.GetIPTVImportItem(ctx, itemID)
	if err != nil || item == nil {
		return item, err
	}
	job, err := s.db.GetIPTVImportJob(ctx, item.JobID)
	if err != nil || job == nil {
		return nil, fmt.Errorf("import job not found")
	}
	s.applyProbe(ctx, job, item)
	return item, s.db.UpdateIPTVImportItem(ctx, *item)
}

func (s *Service) Commit(ctx context.Context, jobID string, itemIDs []string) (*storage.IPTVSource, int, error) {
	job, err := s.db.GetIPTVImportJob(ctx, jobID)
	if err != nil {
		return nil, 0, err
	}
	if job == nil {
		return nil, 0, errors.New("import job not found")
	}
	if job.Status == JobCommitted {
		return nil, 0, errors.New("import already committed")
	}
	items, err := s.db.GetIPTVImportItemsByIDs(ctx, jobID, itemIDs)
	if err != nil {
		return nil, 0, err
	}
	if len(items) == 0 {
		return nil, 0, errors.New("no channels selected")
	}
	var accepted []storage.IPTVImportItem
	for _, item := range items {
		if item.DRM || item.Status == ItemUnsupported || item.Status == ItemFailed {
			continue
		}
		if !item.Playable && item.Status != ItemWarning && item.Status != ItemPlayable {
			continue
		}
		accepted = append(accepted, item)
	}
	if len(accepted) == 0 {
		return nil, 0, errors.New("none of the selected channels passed validation")
	}
	now := time.Now()
	src := storage.IPTVSource{
		ID:                 uuid.NewString(),
		Name:               job.Name,
		PlaylistURL:        job.PlaylistURL,
		RequestHeaders:     job.RequestHeaders,
		Enabled:            true,
		RefreshIntervalSec: 21600,
		LastRefreshedAt:    &now,
	}
	if err := s.db.InsertIPTVSource(ctx, src); err != nil {
		return nil, 0, err
	}
	for _, item := range accepted {
		ch := storage.IPTVChannel{
			ID:             uuid.NewString(),
			SourceID:       src.ID,
			ExternalID:     item.ExternalID,
			StreamID:       channelStreamID(src.ID, item.ExternalID),
			ChannelNo:      item.ChannelNo,
			Name:           item.Name,
			GroupName:      item.GroupName,
			LogoURL:        item.LogoURL,
			SourceURL:      item.SourceURL,
			RequestHeaders: item.RequestHeaders,
			Enabled:        true,
			ProbeStatus:    item.Status,
			ProbeError:     item.Error,
			VideoCodec:     item.VideoCodec,
			AudioCodec:     item.AudioCodec,
			Playable:       item.Playable,
			Recordable:     item.Recordable,
			LastCheckedAt:  item.CheckedAt,
		}
		if err := s.db.UpsertIPTVChannel(ctx, ch); err != nil {
			return nil, 0, err
		}
	}
	job.Status = JobCommitted
	if err := s.db.UpdateIPTVImportJob(ctx, *job); err != nil {
		return nil, 0, err
	}
	src.PlaylistURL = redactURL(src.PlaylistURL)
	return &src, len(accepted), nil
}

func (s *Service) ListSources(ctx context.Context) ([]storage.IPTVSource, error) {
	sources, err := s.db.ListIPTVSources(ctx)
	if err != nil {
		return nil, err
	}
	for i := range sources {
		sources[i].PlaylistURL = redactURL(sources[i].PlaylistURL)
		sources[i].RequestHeaders = ""
	}
	return sources, nil
}

func (s *Service) DeleteSource(ctx context.Context, id string) error {
	channels, err := s.db.ListIPTVChannels(ctx, id, "", "", nil)
	if err != nil {
		return err
	}
	for _, ch := range channels {
		_ = s.stopSourcePull(ctx, ch.ID)
		if err := s.db.DeleteRecordingPlanByStream(ctx, ch.StreamID); err != nil {
			logger.Warn("iptv delete recording plan failed", "stream_id", ch.StreamID, "error", err)
		}
	}
	return s.db.DeleteIPTVSource(ctx, id)
}

func (s *Service) ListChannels(ctx context.Context, sourceID, group, q string, favorite *bool) ([]storage.IPTVChannel, error) {
	channels, err := s.db.ListIPTVChannels(ctx, sourceID, group, q, favorite)
	if err != nil {
		return nil, err
	}
	for i := range channels {
		channels[i].SourceURL = ""
	}
	return channels, nil
}

func (s *Service) ListGroups(ctx context.Context) ([]string, error) {
	return s.db.ListIPTVGroups(ctx)
}

func (s *Service) GetChannel(ctx context.Context, id string) (*storage.IPTVChannel, error) {
	ch, err := s.db.GetIPTVChannel(ctx, id)
	if err != nil || ch == nil {
		return ch, err
	}
	ch.SourceURL = ""
	return ch, nil
}

func (s *Service) GetPlaybackDetails(ctx context.Context, id string) (*PlaybackDetails, error) {
	ch, err := s.db.GetIPTVChannel(ctx, id)
	if err != nil || ch == nil {
		return nil, err
	}
	if !ch.Enabled {
		return nil, errors.New("channel is disabled")
	}
	src, err := s.db.GetIPTVSource(ctx, ch.SourceID)
	if err != nil || src == nil {
		return nil, errors.New("iptv source not found")
	}
	if !src.Enabled {
		return nil, errors.New("IPTV source is disabled")
	}
	return &PlaybackDetails{URL: "/api/iptv/channels/" + url.PathEscape(ch.ID) + "/hls"}, nil
}

func (s *Service) UpdateChannel(ctx context.Context, id, name string, enabled, favorite, publishEnabled bool) (*storage.IPTVChannel, error) {
	ch, err := s.db.GetIPTVChannel(ctx, id)
	if err != nil || ch == nil {
		return ch, err
	}
	if publishEnabled && enabled && !ch.PublishEnabled {
		if !ch.Recordable {
			return nil, errors.New("IPTV channel codec is not supported by lal publishing")
		}
		if s.puller == nil {
			return nil, media.ErrHLSPullEmbeddedOnly
		}
	} else if publishEnabled && !ch.PublishEnabled {
		return nil, errors.New("cannot publish a disabled IPTV channel")
	}
	if strings.TrimSpace(name) != "" {
		ch.Name = strings.TrimSpace(name)
	}
	if err := s.db.UpdateIPTVChannelMeta(ctx, id, ch.Name, enabled, favorite, publishEnabled); err != nil {
		return nil, err
	}
	return s.GetChannel(ctx, id)
}

func (s *Service) DeleteChannel(ctx context.Context, id string) error {
	ch, err := s.db.GetIPTVChannel(ctx, id)
	if err != nil {
		return err
	}
	_ = s.stopSourcePull(ctx, id)
	if ch != nil && ch.StreamID != "" {
		if err := s.db.DeleteRecordingPlanByStream(ctx, ch.StreamID); err != nil {
			logger.Warn("iptv delete recording plan failed", "stream_id", ch.StreamID, "error", err)
		}
	}
	return s.db.DeleteIPTVChannel(ctx, id)
}

func (s *Service) startSourcePull(ctx context.Context, id string) error {
	if s.puller == nil {
		return media.ErrHLSPullEmbeddedOnly
	}
	ch, err := s.db.GetIPTVChannel(ctx, id)
	if err != nil || ch == nil {
		return err
	}
	src, err := s.db.GetIPTVSource(ctx, ch.SourceID)
	if err != nil || src == nil {
		return errors.New("iptv source not found")
	}
	s.mu.Lock()
	if _, busy := s.starting[ch.ID]; busy {
		s.mu.Unlock()
		return nil
	}
	s.starting[ch.ID] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.starting, ch.ID)
		s.mu.Unlock()
	}()

	headers := mergeRequestHeaders(openHeaders(src.RequestHeaders), openHeaders(ch.RequestHeaders))
	_, err = s.puller.StartHLSPull(ctx, media.StartHLSPullRequest{
		StreamID:     ch.StreamID,
		PlaylistURL:  openSecret(ch.SourceURL),
		Headers:      headers,
		PullTimeout:  10 * time.Second,
		PullRetryNum: -1,
	})
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) stopSourcePull(ctx context.Context, id string) error {
	if s.puller == nil {
		return nil
	}
	ch, err := s.db.GetIPTVChannel(ctx, id)
	if err != nil || ch == nil {
		return err
	}
	return s.puller.StopHLSPull(ctx, ch.StreamID)
}

// ReconcileSources shares a single HLS pull between manual publication and
// recording plans. The pull remains active while either use case needs it.
func (s *Service) ReconcileSources(ctx context.Context, desired map[string]bool, eventActive func(string) bool) bool {
	if s == nil || s.puller == nil {
		return false
	}
	channels, err := s.db.ListIPTVChannels(ctx, "", "", "", nil)
	if err != nil {
		logger.Warn("iptv list recording sources failed", "error", err)
		return false
	}
	pulling := make(map[string]bool)
	for _, id := range s.puller.PullingStreamIDs() {
		pulling[id] = true
	}
	started := false
	for _, ch := range channels {
		recordingWanted := desired[ch.StreamID] || (eventActive != nil && eventActive(ch.StreamID))
		want := ch.Enabled && ch.Recordable && (ch.PublishEnabled || recordingWanted)
		if want && !pulling[ch.StreamID] {
			if err := s.startSourcePull(ctx, ch.ID); err != nil {
				logger.Warn("iptv source pull failed", "channel_id", ch.ID, "name", ch.Name, "error", err)
				continue
			}
			started = true
			logger.Info("iptv source pull started", "channel_id", ch.ID, "name", ch.Name, "stream_id", ch.StreamID)
		} else if !want && pulling[ch.StreamID] {
			if err := s.stopSourcePull(ctx, ch.ID); err != nil {
				logger.Warn("iptv source pull stop failed", "channel_id", ch.ID, "error", err)
			}
		}
	}
	return started
}

func (s *Service) Stop() {
	s.mu.Lock()
	for _, cancel := range s.probing {
		cancel()
	}
	s.mu.Unlock()
	if s.puller != nil {
		s.puller.StopAllHLSPulls()
	}
}

func mergeRequestHeaders(base, override map[string]string) map[string]string {
	if len(base)+len(override) == 0 {
		return nil
	}
	merged := make(map[string]string, len(base)+len(override))
	for name, value := range base {
		merged[http.CanonicalHeaderKey(name)] = value
	}
	for name, value := range override {
		merged[http.CanonicalHeaderKey(name)] = value
	}
	return merged
}

func (s *Service) probeJob(jobID string) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if prev, ok := s.probing[jobID]; ok {
		prev()
	}
	s.probing[jobID] = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.probing, jobID)
		s.mu.Unlock()
	}()

	job, err := s.db.GetIPTVImportJob(ctx, jobID)
	if err != nil || job == nil {
		return
	}
	items, err := s.db.ListIPTVImportItems(ctx, jobID, "", "")
	if err != nil {
		job.Status = JobFailed
		job.Error = err.Error()
		_ = s.db.UpdateIPTVImportJob(ctx, *job)
		return
	}
	sem := make(chan struct{}, probeConcurrency)
	var wg sync.WaitGroup
	for i := range items {
		item := items[i]
		wg.Add(1)
		sem <- struct{}{}
		go func(item storage.IPTVImportItem) {
			defer wg.Done()
			defer func() { <-sem }()
			s.applyProbe(ctx, job, &item)
			if err := s.db.UpdateIPTVImportItem(ctx, item); err != nil {
				logger.Warn("update import item failed", "item_id", item.ID, "error", err)
			}
		}(item)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	job.Status = JobReady
	_ = s.db.UpdateIPTVImportJob(context.Background(), *job)
}

func (s *Service) applyProbe(ctx context.Context, job *storage.IPTVImportJob, item *storage.IPTVImportItem) {
	item.Status = ItemProbing
	_ = s.db.UpdateIPTVImportItem(ctx, *item)
	headers := openHeaders(job.RequestHeaders)
	if itemHeaders := openHeaders(item.RequestHeaders); len(itemHeaders) > 0 {
		if headers == nil {
			headers = itemHeaders
		} else {
			for k, v := range itemHeaders {
				headers[k] = v
			}
		}
	}
	res := s.probeURL(ctx, openSecret(item.SourceURL), headers)
	item.Status = res.Status
	item.Error = res.Error
	item.HTTPStatus = res.HTTPStatus
	item.ContentType = res.ContentType
	item.VideoCodec = res.VideoCodec
	item.AudioCodec = res.AudioCodec
	item.Encrypted = res.Encrypted
	item.DRM = res.DRM
	item.Playable = res.Playable
	item.Recordable = res.Recordable
	now := res.CheckedAt
	item.CheckedAt = &now
}

func (s *Service) downloadPlaylist(ctx context.Context, playlistURL string, headers map[string]string) (io.Reader, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, playlistURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "lalmax-nvr-iptv")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("playlist download failed: %s", resp.Status)
	}
	limited := io.LimitReader(resp.Body, maxPlaylistBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > maxPlaylistBytes {
		return nil, errors.New("playlist too large")
	}
	return strings.NewReader(string(body)), nil
}

func channelStreamID(sourceID, externalID string) string {
	sum := sha256.Sum256([]byte(sourceID + ":" + externalID))
	return "iptv_" + hex.EncodeToString(sum[:12])
}
