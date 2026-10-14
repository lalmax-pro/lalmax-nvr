// Package auth provides shared authentication policies used by HTTP and media
// protocol adapters. Adapters are responsible only for extracting credentials
// and resource identifiers from their protocol's request format.
package auth

import (
	"crypto/subtle"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// MediaAuthorizer checks per-stream publish and playback credentials.
// Credential maps are replaced atomically when configuration is reloaded.
type MediaAuthorizer struct {
	mu     sync.RWMutex
	tokens map[string]map[string]map[string]string
}

// NewMediaAuthorizer creates an empty media authorizer. Streams without a
// configured token are denied by default.
func NewMediaAuthorizer() *MediaAuthorizer {
	return &MediaAuthorizer{tokens: make(map[string]map[string]map[string]string)}
}

// SetPublishTokens configures the required publish token for each stream on a
// protocol, replacing that protocol's current publish token set.
func (a *MediaAuthorizer) SetPublishTokens(protocol string, tokens map[string]string) {
	a.setTokens("publish", protocol, tokens)
}

// SetPlaybackTokens configures the required playback token for each stream on
// a protocol, replacing that protocol's current playback token set.
func (a *MediaAuthorizer) SetPlaybackTokens(protocol string, tokens map[string]string) {
	a.setTokens("playback", protocol, tokens)
}

func (a *MediaAuthorizer) setTokens(direction, protocol string, tokens map[string]string) {
	if a == nil {
		return
	}
	protocol = normalizeProtocol(protocol)
	cloned := make(map[string]string, len(tokens))
	for streamID, token := range tokens {
		streamID = strings.TrimSpace(streamID)
		if streamID != "" && token != "" {
			cloned[streamID] = token
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tokens == nil {
		a.tokens = make(map[string]map[string]map[string]string)
	}
	if a.tokens[direction] == nil {
		a.tokens[direction] = make(map[string]map[string]string)
	}
	a.tokens[direction][protocol] = cloned
}

// AuthorizePublish verifies the configured token for a publishing stream.
func (a *MediaAuthorizer) AuthorizePublish(protocol, streamID, token string) error {
	return a.authorize("publish", protocol, streamID, token)
}

// AuthorizePlayback verifies the configured token for a playback stream.
func (a *MediaAuthorizer) AuthorizePlayback(protocol, streamID, token string) error {
	return a.authorize("playback", protocol, streamID, token)
}

func (a *MediaAuthorizer) authorize(direction, protocol, streamID, token string) error {
	protocol = normalizeProtocol(protocol)
	streamID = strings.TrimSpace(streamID)
	if a == nil {
		return fmt.Errorf("%s %s is not configured for stream %q", protocol, direction, streamID)
	}
	a.mu.RLock()
	want := a.tokens[direction][protocol][streamID]
	a.mu.RUnlock()
	if want == "" {
		return fmt.Errorf("%s %s is not configured for stream %q", protocol, direction, streamID)
	}
	if token == "" || len(token) != len(want) || subtle.ConstantTimeCompare([]byte(token), []byte(want)) != 1 {
		return fmt.Errorf("invalid %s %s token for stream %q", protocol, direction, streamID)
	}
	return nil
}

// QueryToken extracts a credential from protocol parameters, returning an
// error for malformed query strings rather than silently accepting a partial
// parse.
func QueryToken(rawQuery, parameter string) (string, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", err
	}
	return values.Get(parameter), nil
}

func normalizeProtocol(protocol string) string {
	return strings.ToLower(strings.TrimSpace(protocol))
}
