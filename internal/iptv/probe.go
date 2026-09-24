package iptv

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bluenviron/gohlslib"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
)

const (
	defaultProbeTimeout = 8 * time.Second
	maxPlaylistBytes    = 2 << 20
)

func (s *Service) probeURL(ctx context.Context, rawURL string, headers map[string]string) ProbeResult {
	res := ProbeResult{Status: ItemFailed, CheckedAt: time.Now()}
	if err := validatePublicURL(rawURL); err != nil {
		res.Error = err.Error()
		return res
	}

	timeout := defaultProbeTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
		if timeout <= 0 {
			res.Error = "probe timeout"
			return res
		}
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("User-Agent", "lalmax-nvr-iptv")
	for k, v := range sanitizeHeaders(headers) {
		req.Header.Set(k, v)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()
	res.HTTPStatus = resp.StatusCode
	res.ContentType = resp.Header.Get("Content-Type")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		res.Error = resp.Status
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			res.Status = ItemUnsupported
		}
		return res
	}
	limited := io.LimitReader(resp.Body, maxPlaylistBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if len(body) > maxPlaylistBytes {
		res.Error = "playlist too large"
		return res
	}
	text := string(body)
	if !strings.Contains(text, "#EXTM3U") {
		res.Error = "response is not an HLS playlist"
		return res
	}
	if strings.Contains(text, "SAMPLE-AES") || strings.Contains(text, "SAMPLE-AES-CTR") || strings.Contains(text, "com.apple.streamingkeydelivery") {
		res.DRM = true
		res.Encrypted = true
		res.Status = ItemUnsupported
		res.Error = "DRM / SAMPLE-AES is not supported"
		return res
	}
	if strings.Contains(text, "#EXT-X-KEY") {
		res.Encrypted = true
		res.Status = ItemWarning
		res.Error = "AES-128 encryption is not supported in MVP"
		return res
	}

	mediaRes := s.probeMedia(reqCtx, rawURL, headers)
	if mediaRes.VideoCodec != "" || mediaRes.Error != "" {
		mediaRes.HTTPStatus = res.HTTPStatus
		mediaRes.ContentType = res.ContentType
		mediaRes.Encrypted = res.Encrypted
		mediaRes.DRM = res.DRM
		mediaRes.CheckedAt = time.Now()
		return mediaRes
	}

	res.Status = ItemWarning
	res.Playable = true
	res.Error = "playlist reachable; codec not confirmed"
	return res
}

func (s *Service) probeMedia(ctx context.Context, rawURL string, headers map[string]string) ProbeResult {
	res := ProbeResult{Status: ItemWarning, CheckedAt: time.Now()}
	client := &gohlslib.Client{
		URI:                       rawURL,
		HTTPClient:                s.httpClient,
		OnDownloadPrimaryPlaylist: func(string) {},
		OnDownloadStreamPlaylist:  func(string) {},
		OnDownloadSegment:         func(string) {},
		OnDownloadPart:            func(string) {},
		OnDecodeError:             func(error) {},
	}
	if len(headers) > 0 {
		client.HTTPClient = &http.Client{
			Timeout: defaultProbeTimeout,
			Transport: &headerProbeTransport{
				base:    http.DefaultTransport,
				headers: sanitizeHeaders(headers),
			},
		}
	}
	done := make(chan ProbeResult, 1)
	client.OnTracks = func(tracks []*gohlslib.Track) error {
		info, err := media.ClassifyHLSTracks(tracks)
		out := ProbeResult{
			VideoCodec: info.VideoCodec,
			AudioCodec: info.AudioCodec,
			Playable:   info.Playable,
			Recordable: info.Recordable,
			CheckedAt:  time.Now(),
		}
		if err != nil {
			out.Status = ItemUnsupported
			out.Error = err.Error()
			out.Playable = false
			out.Recordable = false
		} else {
			out.Status = ItemPlayable
			if info.VideoCodec == "h265" {
				out.Status = ItemWarning
				out.Error = "hevc playback depends on the client"
			}
		}
		select {
		case done <- out:
		default:
		}
		client.Close()
		return nil
	}
	if err := client.Start(); err != nil {
		res.Status = ItemFailed
		res.Error = err.Error()
		return res
	}
	defer client.Close()

	select {
	case <-ctx.Done():
		res.Status = ItemFailed
		res.Error = "probe timeout"
		return res
	case out := <-done:
		return out
	case err := <-client.Wait():
		if err != nil && res.Error == "" {
			res.Status = ItemFailed
			res.Error = err.Error()
		}
		return res
	}
}

type headerProbeTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerProbeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.base.RoundTrip(req)
}
