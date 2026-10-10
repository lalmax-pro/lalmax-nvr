package voip

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
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
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(header), "digest ") {
		return nil
	}
	header = strings.TrimSpace(header[len("Digest "):])

	params := make(map[string]string)
	for _, part := range splitDigestParams(header) {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"`)
		params[key] = value
	}
	return params
}

func splitDigestParams(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch r {
		case '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case ',':
			if inQuote {
				cur.WriteRune(r)
				continue
			}
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, strings.TrimSpace(cur.String()))
	}
	return parts
}

func digestMD5(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func expectedDigestResponse(method, realm, password string, params map[string]string) string {
	ha1 := digestMD5(params["username"] + ":" + realm + ":" + password)
	ha2 := digestMD5(method + ":" + params["uri"])
	if params["qop"] != "" {
		return digestMD5(ha1 + ":" + params["nonce"] + ":" + params["nc"] + ":" + params["cnonce"] + ":" + params["qop"] + ":" + ha2)
	}
	return digestMD5(ha1 + ":" + params["nonce"] + ":" + ha2)
}

func buildDigestAuthorization(method, uri, username, password, realm, nonce, qop string) (string, error) {
	if username == "" || realm == "" || nonce == "" || uri == "" {
		return "", fmt.Errorf("incomplete digest authentication challenge")
	}
	algorithm := digestMD5(username + ":" + realm + ":" + password)
	ha2 := digestMD5(method + ":" + uri)
	fields := []string{
		`username="` + escapeDigest(username) + `"`,
		`realm="` + escapeDigest(realm) + `"`,
		`nonce="` + escapeDigest(nonce) + `"`,
		`uri="` + escapeDigest(uri) + `"`,
	}
	supportsAuth := false
	for _, option := range strings.Split(qop, ",") {
		if strings.EqualFold(strings.TrimSpace(option), "auth") {
			supportsAuth = true
			break
		}
	}
	if qop != "" && !supportsAuth {
		return "", fmt.Errorf("PBX does not support digest qop auth")
	}
	if supportsAuth {
		cnonce := generateTag()
		response := digestMD5(algorithm + ":" + nonce + ":00000001:" + cnonce + ":auth:" + ha2)
		fields = append(fields, `qop=auth`, `nc=00000001`, `cnonce="`+cnonce+`"`, `response="`+response+`"`, `algorithm=MD5`)
	} else {
		response := digestMD5(algorithm + ":" + nonce + ":" + ha2)
		fields = append(fields, `response="`+response+`"`, `algorithm=MD5`)
	}
	return "Digest " + strings.Join(fields, ", "), nil
}

func escapeDigest(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
