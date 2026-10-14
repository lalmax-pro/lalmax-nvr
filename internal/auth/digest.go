package auth

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// DigestParams is a parsed HTTP/SIP Digest authorization parameter set.
type DigestParams map[string]string

// ParseDigestAuthorization parses a Digest Authorization header. Parameter
// names are normalized to lowercase and quoted values are unescaped.
func ParseDigestAuthorization(header string) (DigestParams, bool) {
	header = strings.TrimSpace(header)
	scheme, rest, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Digest") {
		return nil, false
	}
	params := make(DigestParams)
	for rest = strings.TrimSpace(rest); rest != ""; {
		key, value, found := strings.Cut(rest, "=")
		if !found {
			return nil, false
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			return nil, false
		}
		rest = strings.TrimLeft(value, " \t")
		var parsed string
		if strings.HasPrefix(rest, `"`) {
			rest = rest[1:]
			var b strings.Builder
			closed := false
			for i := 0; i < len(rest); i++ {
				switch rest[i] {
				case '\\':
					i++
					if i >= len(rest) {
						return nil, false
					}
					b.WriteByte(rest[i])
				case '"':
					parsed = b.String()
					rest = rest[i+1:]
					closed = true
					i = len(rest)
				default:
					b.WriteByte(rest[i])
				}
			}
			if !closed {
				return nil, false
			}
		} else {
			if parsed, rest, _ = strings.Cut(rest, ","); rest != "" {
				rest = "," + rest
			}
			parsed = strings.TrimSpace(parsed)
		}
		if _, duplicate := params[key]; duplicate {
			return nil, false
		}
		params[key] = parsed
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			break
		}
		if rest[0] != ',' {
			return nil, false
		}
		rest = strings.TrimSpace(rest[1:])
	}
	return params, true
}

// DigestResponse calculates an MD5 Digest response, supporting legacy
// no-qop and qop=auth handshakes used by the media protocols in this project.
func DigestResponse(method, username, realm, password, uri, nonce, qop, nc, cnonce string) string {
	ha1 := md5Hex(username + ":" + realm + ":" + password)
	ha2 := md5Hex(method + ":" + uri)
	if qop != "" {
		return md5Hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	}
	return md5Hex(ha1 + ":" + nonce + ":" + ha2)
}

// VerifyDigestResponse validates a legacy no-qop Digest response in constant
// time after checking that the supplied response is a valid MD5 hex digest.
func VerifyDigestResponse(method, username, realm, password, uri, nonce, response string) bool {
	if method == "" || username == "" || realm == "" || uri == "" || nonce == "" {
		return false
	}
	got, err := hex.DecodeString(response)
	if err != nil || len(got) != md5.Size {
		return false
	}
	want, _ := hex.DecodeString(DigestResponse(method, username, realm, password, uri, nonce, "", "", ""))
	return len(want) == len(got) && ConstantTimeEqual(string(got), string(want))
}

// VerifyDigestAuthorization parses and verifies an RTSP-style legacy Digest
// Authorization header against the expected identity and realm.
func VerifyDigestAuthorization(method, username, realm, password, header string) bool {
	params, ok := ParseDigestAuthorization(header)
	if !ok || params["username"] != username || params["realm"] != realm || params["qop"] != "" {
		return false
	}
	if algorithm := params["algorithm"]; algorithm != "" && !strings.EqualFold(algorithm, "MD5") {
		return false
	}
	return VerifyDigestResponse(method, username, realm, password, params["uri"], params["nonce"], params["response"])
}

// VerifyDigestAuthorizationAndConsumeNonce verifies an RTSP-style Digest
// header and consumes its nonce only after the response is valid.
func VerifyDigestAuthorizationAndConsumeNonce(method, username, realm, password, header string, nonces *DigestNonceStore) bool {
	if nonces == nil {
		return false
	}
	params, ok := ParseDigestAuthorization(header)
	if !ok || !VerifyDigestAuthorization(method, username, realm, password, header) {
		return false
	}
	return nonces.Consume(params["nonce"])
}

// DigestNonceCount extracts and validates the eight-hex-digit nonce count used
// by qop=auth. Requests without qop return a zero count.
func DigestNonceCount(params DigestParams) (uint32, bool) {
	if strings.ToLower(params["qop"]) == "" {
		return 0, true
	}
	nc := params["nc"]
	if len(nc) != 8 {
		return 0, false
	}
	count, err := strconv.ParseUint(nc, 16, 32)
	return uint32(count), err == nil && count > 0
}

// VerifyDigest validates a Digest parameter set, including qop=auth requests.
// Replay prevention and nonce expiry remain the protocol adapter's job.
func VerifyDigest(method, username, realm, password string, params DigestParams) bool {
	if params["username"] != username || params["realm"] != realm || params["uri"] == "" || params["nonce"] == "" {
		return false
	}
	qop := strings.ToLower(params["qop"])
	if qop != "" && qop != "auth" {
		return false
	}
	if qop == "auth" && (params["nc"] == "" || params["cnonce"] == "") {
		return false
	}
	if qop == "auth" {
		if _, ok := DigestNonceCount(params); !ok {
			return false
		}
	}
	if algorithm := params["algorithm"]; algorithm != "" && !strings.EqualFold(algorithm, "MD5") {
		return false
	}
	response := params["response"]
	got, err := hex.DecodeString(response)
	if err != nil || len(got) != md5.Size {
		return false
	}
	want, _ := hex.DecodeString(DigestResponse(method, username, realm, password, params["uri"], params["nonce"], qop, params["nc"], params["cnonce"]))
	return len(want) == len(got) && ConstantTimeEqual(string(got), string(want))
}

// BuildDigestAuthorization creates a Digest Authorization header for qop=auth
// or legacy no-qop challenges.
func BuildDigestAuthorization(method, uri, username, password, realm, nonce, qop string) (string, error) {
	if method == "" || uri == "" || username == "" || realm == "" || nonce == "" {
		return "", fmt.Errorf("incomplete digest authentication challenge")
	}
	qop = strings.ToLower(strings.TrimSpace(qop))
	if qop != "" && qop != "auth" {
		return "", fmt.Errorf("unsupported digest qop %q", qop)
	}
	fields := []string{
		`username="` + escapeDigest(username) + `"`,
		`realm="` + escapeDigest(realm) + `"`,
		`nonce="` + escapeDigest(nonce) + `"`,
		`uri="` + escapeDigest(uri) + `"`,
	}
	response := ""
	if qop == "auth" {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", fmt.Errorf("create digest cnonce: %w", err)
		}
		cnonce := hex.EncodeToString(random[:])
		nc := "00000001"
		response = DigestResponse(method, username, realm, password, uri, nonce, qop, nc, cnonce)
		fields = append(fields, "qop=auth", "nc="+nc, `cnonce="`+cnonce+`"`)
	} else {
		response = DigestResponse(method, username, realm, password, uri, nonce, "", "", "")
	}
	fields = append(fields, `response="`+response+`"`, "algorithm=MD5")
	return "Digest " + strings.Join(fields, ", "), nil
}

func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func escapeDigest(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
