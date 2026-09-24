package iptv

import (
	"bufio"
	"io"
	"net/url"
	"strings"
	"unicode"
)

// ParseM3U parses an IPTV channel list. It accepts #EXTM3U playlists with
// EXTINF attributes (tvg-id, tvg-name, tvg-logo, tvg-chno, group-title).
func ParseM3U(r io.Reader) ([]PlaylistChannel, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var out []PlaylistChannel
	var pending *PlaylistChannel
	row := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#EXTM3U") {
			continue
		}
		if strings.HasPrefix(line, "#EXTINF:") {
			ch := parseExtInf(line)
			pending = &ch
			continue
		}
		if strings.HasPrefix(line, "#EXTGRP:") && pending != nil {
			if pending.GroupName == "" {
				pending.GroupName = strings.TrimSpace(strings.TrimPrefix(line, "#EXTGRP:"))
			}
			continue
		}
		if strings.HasPrefix(line, "#EXTVLCOPT:") && pending != nil {
			opt := strings.TrimSpace(strings.TrimPrefix(line, "#EXTVLCOPT:"))
			key, val, ok := strings.Cut(opt, "=")
			if ok && strings.EqualFold(strings.TrimSpace(key), "http-user-agent") {
				if pending.Headers == nil {
					pending.Headers = map[string]string{}
				}
				pending.Headers["User-Agent"] = strings.TrimSpace(val)
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		row++
		u := line
		if pending == nil {
			pending = &PlaylistChannel{Name: fallbackName(u)}
		}
		pending.RowNo = row
		pending.SourceURL = u
		if pending.Name == "" {
			pending.Name = fallbackName(u)
		}
		if pending.ExternalID == "" {
			pending.ExternalID = normalizeExternalID(pending.Name, u)
		}
		out = append(out, *pending)
		pending = nil
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func parseExtInf(line string) PlaylistChannel {
	body := strings.TrimSpace(strings.TrimPrefix(line, "#EXTINF:"))
	comma := strings.LastIndex(body, ",")
	attrs, name := body, ""
	if comma >= 0 {
		attrs = strings.TrimSpace(body[:comma])
		name = strings.TrimSpace(body[comma+1:])
	}
	if sp := strings.IndexByte(attrs, ' '); sp >= 0 {
		attrs = strings.TrimSpace(attrs[sp+1:])
	} else {
		attrs = ""
	}
	ch := PlaylistChannel{Name: name}
	if fields := parseAttributes(attrs); len(fields) > 0 {
		ch.ExternalID = firstNonEmpty(fields["tvg-id"], fields["channel-id"], fields["tvg_id"])
		if ch.Name == "" {
			ch.Name = firstNonEmpty(fields["tvg-name"], fields["tvg_name"])
		}
		ch.LogoURL = firstNonEmpty(fields["tvg-logo"], fields["tvg_logo"])
		ch.GroupName = firstNonEmpty(fields["group-title"], fields["group_title"])
		ch.ChannelNo = firstNonEmpty(fields["tvg-chno"], fields["channel-number"], fields["tvg_chno"])
		if ua := firstNonEmpty(fields["http-user-agent"], fields["user-agent"]); ua != "" {
			ch.Headers = map[string]string{"User-Agent": ua}
		}
	}
	return ch
}

func parseAttributes(s string) map[string]string {
	out := map[string]string{}
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 {
			break
		}
		key := strings.ToLower(strings.TrimSpace(s[i : i+eq]))
		i += eq + 1
		if i >= len(s) {
			break
		}
		var val string
		if s[i] == '"' {
			i++
			end := strings.IndexByte(s[i:], '"')
			if end < 0 {
				val = s[i:]
				i = len(s)
			} else {
				val = s[i : i+end]
				i += end + 1
			}
		} else {
			end := i
			for end < len(s) && s[end] != ' ' && s[end] != '\t' {
				end++
			}
			val = s[i:end]
			i = end
		}
		if key != "" && !strings.Contains(key, " ") {
			out[key] = val
		}
	}
	return out
}

func fallbackName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Path == "" {
		return rawURL
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return rawURL
	}
	name := parts[len(parts)-1]
	name = strings.TrimSuffix(name, ".m3u8")
	name = strings.TrimSuffix(name, ".m3u")
	if name == "" || name == "index" {
		if len(parts) >= 2 {
			return parts[len(parts)-2]
		}
	}
	return name
}

func normalizeExternalID(name, sourceURL string) string {
	base := strings.ToLower(strings.TrimSpace(name))
	if base == "" {
		base = strings.ToLower(sourceURL)
	}
	var b strings.Builder
	for _, r := range base {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else if r == '-' || r == '_' {
			b.WriteByte(byte(r))
		} else if unicode.IsSpace(r) {
			b.WriteByte('_')
		}
	}
	id := strings.Trim(b.String(), "_")
	if id == "" {
		id = "channel"
	}
	if len(id) > 64 {
		id = id[:64]
	}
	return id
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
