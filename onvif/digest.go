package onvif

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"net/http"
	"regexp"
	"strings"
)

type digestChallenge struct {
	params    map[string]string
	algorithm string
	hash      func() hash.Hash
	qop       string
}

var authFields = regexp.MustCompile(`(?i)(?:^|,)[ \t]*([a-z0-9!#$%&'*+.^_` + "`" + `|~-]+)[ \t]*(?:=[ \t]*(?:"((?:\\.|[^"\\])*)"|([^, \t]+))|([ \t]+|$))`)
var quotedPair = regexp.MustCompile(`\\(.)`)

// preferredDigestChallenge parses the HTTP challenge list without splitting quoted realms or qop values.
func preferredDigestChallenge(headers []string) (digestChallenge, bool) {
	var best digestChallenge
	for _, header := range headers {
		var params map[string]string
		consider := func() {
			candidate := newDigestChallenge(params)
			if candidate.hash != nil && (best.hash == nil ||
				(strings.HasPrefix(candidate.algorithm, "SHA-256") && !strings.HasPrefix(best.algorithm, "SHA-256"))) {
				best = candidate
			}
		}
		for remaining := strings.TrimSpace(header); remaining != ""; {
			field := authFields.FindStringSubmatchIndex(remaining)
			if field == nil || field[0] != 0 {
				// A non-Digest scheme may carry token68 rather than parameters.
				comma := strings.IndexByte(remaining, ',')
				if comma < 0 {
					break
				}
				remaining = remaining[comma:]
				if comma == 0 {
					remaining = strings.TrimSpace(remaining[1:])
				}
				continue
			}
			key := strings.ToLower(remaining[field[2]:field[3]])
			if field[8] >= 0 {
				consider()
				params = nil
				if key == "digest" {
					params = make(map[string]string)
				}
			} else if params != nil {
				if field[4] >= 0 {
					params[key] = quotedPair.ReplaceAllString(remaining[field[4]:field[5]], "$1")
				} else {
					params[key] = remaining[field[6]:field[7]]
				}
			}
			remaining = remaining[field[1]:]
		}
		consider()
	}
	return best, best.hash != nil
}

func newDigestChallenge(params map[string]string) digestChallenge {
	if _, hasRealm := params["realm"]; !hasRealm || params["nonce"] == "" {
		return digestChallenge{}
	}
	challenge := digestChallenge{params: params, algorithm: strings.ToUpper(params["algorithm"])}
	switch challenge.algorithm {
	case "", "MD5", "MD5-SESS":
		challenge.hash = md5.New
	case "SHA-256", "SHA-256-SESS":
		challenge.hash = sha256.New
	default:
		return digestChallenge{}
	}
	if value := params["qop"]; value != "" {
		for _, qop := range strings.Split(value, ",") {
			switch strings.ToLower(strings.TrimSpace(qop)) {
			case "auth":
				challenge.qop = "auth"
			case "auth-int":
				if challenge.qop == "" {
					challenge.qop = "auth-int"
				}
			}
		}
		if challenge.qop == "" {
			return digestChallenge{}
		}
	}
	return challenge
}

func (s *SOAPClient) digestAuthorization(req *http.Request, body string, challenge digestChallenge) string {
	hashText := func(value string) string {
		h := challenge.hash()
		h.Write([]byte(value))
		return hex.EncodeToString(h.Sum(nil))
	}
	params := challenge.params
	nonce, realm := params["nonce"], params["realm"]
	uri, cnonce := req.URL.RequestURI(), randomHex(16)
	s.mu.Lock()
	if s.digestNonce != nonce {
		s.digestNonce, s.digestCount = nonce, 0
	}
	s.digestCount++
	nc := fmt.Sprintf("%08x", s.digestCount)
	s.mu.Unlock()

	ha1 := hashText(s.username + ":" + realm + ":" + s.password)
	sess := strings.HasSuffix(challenge.algorithm, "-SESS")
	if sess {
		ha1 = hashText(ha1 + ":" + nonce + ":" + cnonce)
	}
	a2 := req.Method + ":" + uri
	if challenge.qop == "auth-int" {
		a2 += ":" + hashText(body)
	}
	a3 := ha1 + ":" + nonce
	if challenge.qop != "" {
		a3 += ":" + nc + ":" + cnonce + ":" + challenge.qop
	}
	response := hashText(a3 + ":" + hashText(a2))

	quote := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace
	header := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
		quote(s.username), quote(realm), quote(nonce), quote(uri), response)
	if params["algorithm"] != "" {
		header += ", algorithm=" + params["algorithm"]
	}
	if _, ok := params["opaque"]; ok {
		header += `, opaque="` + quote(params["opaque"]) + `"`
	}
	if challenge.qop != "" {
		header += fmt.Sprintf(", qop=%s, nc=%s", challenge.qop, nc)
	}
	if sess || challenge.qop != "" {
		header += `, cnonce="` + cnonce + `"`
	}
	return header
}
