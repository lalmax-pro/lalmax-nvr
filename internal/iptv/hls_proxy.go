package iptv

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
)

const maxProxyURLLength = 8192

var hlsURIAttribute = regexp.MustCompile(`(?i)URI="([^"]+)"`)

// OpenPlaybackResource opens the channel's main HLS manifest or one of its
// referenced resources. Upstream URLs and credentials remain server-side.
func (s *Service) OpenPlaybackResource(ctx context.Context, channelID, resourceURL, rangeHeader string) (*http.Response, *url.URL, error) {
	ch, err := s.db.GetIPTVChannel(ctx, channelID)
	if err != nil {
		return nil, nil, err
	}
	if ch == nil {
		return nil, nil, errors.New("IPTV channel not found")
	}
	if !ch.Enabled {
		return nil, nil, errors.New("IPTV channel is disabled")
	}
	src, err := s.db.GetIPTVSource(ctx, ch.SourceID)
	if err != nil {
		return nil, nil, err
	}
	if src == nil || !src.Enabled {
		return nil, nil, errors.New("IPTV source is disabled or not found")
	}

	target := openSecret(ch.SourceURL)
	if resourceURL != "" {
		target = resourceURL
	}
	if len(target) > maxProxyURLLength {
		return nil, nil, errors.New("HLS resource URL is too long")
	}
	if err := validatePublicURL(target); err != nil {
		return nil, nil, err
	}
	sourceURL, err := url.Parse(openSecret(ch.SourceURL))
	if err != nil {
		return nil, nil, errors.New("invalid channel source URL")
	}

	headers := mergeRequestHeaders(openHeaders(src.RequestHeaders), openHeaders(ch.RequestHeaders))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "lalmax-nvr-iptv")
	req.Header.Set("Accept-Encoding", "identity")
	if targetURL, parseErr := url.Parse(target); parseErr == nil && sameURLOrigin(sourceURL, targetURL) {
		for name, value := range headers {
			req.Header.Set(name, value)
		}
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	client := *s.httpClient
	previousCheckRedirect := client.CheckRedirect
	client.CheckRedirect = func(redirectReq *http.Request, via []*http.Request) error {
		if previousCheckRedirect != nil {
			if err := previousCheckRedirect(redirectReq, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if !sameURLOrigin(sourceURL, redirectReq.URL) {
			for name := range headers {
				redirectReq.Header.Del(name)
			}
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	finalURL := resp.Request.URL
	if finalURL == nil {
		finalURL = req.URL
	}
	return resp, finalURL, nil
}

func sameURLOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	if !strings.EqualFold(a.Scheme, b.Scheme) || !strings.EqualFold(a.Hostname(), b.Hostname()) {
		return false
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return port(a) == port(b)
}

func IsHLSManifest(contentType string, resourceURL *url.URL) bool {
	if strings.Contains(strings.ToLower(contentType), "mpegurl") || strings.Contains(strings.ToLower(contentType), "m3u8") {
		return true
	}
	return resourceURL != nil && strings.EqualFold(path.Ext(resourceURL.Path), ".m3u8")
}

func RewriteHLSManifest(body []byte, baseURL *url.URL, channelID string) ([]byte, error) {
	if len(body) > maxPlaylistBytes {
		return nil, errors.New("HLS manifest too large")
	}
	if baseURL == nil {
		return nil, errors.New("HLS manifest URL is missing")
	}
	proxyPath := "/api/iptv/channels/" + url.PathEscape(channelID) + "/hls"
	rewriteURI := func(raw string) (string, error) {
		ref, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return "", fmt.Errorf("invalid HLS resource URI: %w", err)
		}
		resolved := baseURL.ResolveReference(ref)
		if resolved.Scheme != "http" && resolved.Scheme != "https" {
			return raw, nil
		}
		if err := validatePublicURL(resolved.String()); err != nil {
			return "", err
		}
		proxyURL := url.URL{Path: proxyPath}
		query := url.Values{}
		query.Set("url", resolved.String())
		proxyURL.RawQuery = query.Encode()
		return proxyURL.String(), nil
	}

	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			var rewriteErr error
			lines[i] = hlsURIAttribute.ReplaceAllStringFunc(line, func(match string) string {
				parts := hlsURIAttribute.FindStringSubmatch(match)
				if len(parts) != 2 {
					return match
				}
				rewritten, err := rewriteURI(parts[1])
				if err != nil {
					rewriteErr = err
					return match
				}
				return strings.Replace(match, parts[1], rewritten, 1)
			})
			if rewriteErr != nil {
				return nil, rewriteErr
			}
			continue
		}
		leading := line[:len(line)-len(strings.TrimLeft(line, " \t\r"))]
		trailing := line[len(strings.TrimRight(line, " \t\r")):]
		rewritten, err := rewriteURI(trimmed)
		if err != nil {
			return nil, err
		}
		lines[i] = leading + rewritten + trailing
	}
	return []byte(strings.Join(lines, "\n")), nil
}
