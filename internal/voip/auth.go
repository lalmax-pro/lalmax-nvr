package voip

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	coreauth "github.com/lalmax-pro/lalmax-nvr/internal/auth"
)

const digestNonceTTL = 5 * time.Minute

type digestNonceStore struct {
	nonces sync.Map
}

type digestNonce struct {
	expires time.Time
}

func newDigestNonceStore() *digestNonceStore {
	return &digestNonceStore{}
}

func (s *digestNonceStore) New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	nonce := hex.EncodeToString(b[:])
	s.nonces.Store(nonce, digestNonce{expires: time.Now().Add(digestNonceTTL)})
	return nonce
}

func (s *digestNonceStore) Valid(nonce string) bool {
	val, ok := s.nonces.Load(nonce)
	if !ok {
		return false
	}
	n := val.(digestNonce)
	if time.Now().After(n.expires) {
		s.nonces.Delete(nonce)
		return false
	}
	return true
}

func buildDigestChallenge(realm, nonce string) string {
	return fmt.Sprintf(`Digest realm="%s", nonce="%s", algorithm=MD5, qop="auth"`, realm, nonce)
}

func parseDigestAuthorization(header string) map[string]string {
	params, ok := coreauth.ParseDigestAuthorization(header)
	if !ok {
		return nil
	}
	return params
}

func expectedDigestResponse(method, realm, password string, params map[string]string) string {
	return coreauth.DigestResponse(method, params["username"], realm, password, params["uri"], params["nonce"], params["qop"], params["nc"], params["cnonce"])
}

func verifyDigestResponse(method, username, realm, password string, params map[string]string) bool {
	return coreauth.VerifyDigest(method, username, realm, password, params)
}

func buildDigestAuthorization(method, uri, username, password, realm, nonce, qop string) (string, error) {
	if strings.Contains(qop, ",") {
		supportsAuth := false
		for _, option := range strings.Split(qop, ",") {
			if strings.EqualFold(strings.TrimSpace(option), "auth") {
				supportsAuth = true
				break
			}
		}
		if !supportsAuth {
			return "", fmt.Errorf("PBX does not support digest qop auth")
		}
		qop = "auth"
	}
	return coreauth.BuildDigestAuthorization(method, uri, username, password, realm, nonce, qop)
}
