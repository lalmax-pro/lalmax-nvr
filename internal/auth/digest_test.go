package auth

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseDigestAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		want         DigestParams
		ok           bool
	}{
		{"escaped and normalized", `  dIgEsT USERNAME="a\"b\\c", Realm = "camera, realm", nc=00000001, qop=auth  `, DigestParams{"username": `a"b\c`, "realm": "camera, realm", "nc": "00000001", "qop": "auth"}, true},
		{"unquoted", `Digest username= camera , realm=office`, DigestParams{"username": "camera", "realm": "office"}, true},
		{"empty value", `Digest username=""`, DigestParams{"username": ""}, true},
		{"wrong scheme", "Basic abc", nil, false},
		{"missing scheme separator", "Digest", nil, false},
		{"missing assignment", "Digest username", nil, false},
		{"empty key", `Digest ="user"`, nil, false},
		{"unterminated quote", `Digest username="user`, nil, false},
		{"dangling escape", "Digest username=\"user\\", nil, false},
		{"duplicate key", `Digest username="one", USERNAME="two"`, nil, false},
		{"missing comma", `Digest username="one" realm="two"`, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseDigestAuthorization(tc.header)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestDigestResponseRFC2617Vector(t *testing.T) {
	// Published RFC 2617 section 3.5 example: independent of the header builder.
	require.Equal(t, "6629fae49393a05397450978507c4ef1", DigestResponse("GET", "Mufasa", "testrealm@host.com", "Circle Of Life", "/dir/index.html", "dcd98b7102dd2f0e8b11d0f600bfb0c093", "auth", "00000001", "0a4f113b"))
	params := DigestParams{"username": "Mufasa", "realm": "testrealm@host.com", "uri": "/dir/index.html", "nonce": "dcd98b7102dd2f0e8b11d0f600bfb0c093", "qop": "auth", "nc": "00000001", "cnonce": "0a4f113b", "response": "6629fae49393a05397450978507c4ef1"}
	require.True(t, VerifyDigest("GET", "Mufasa", params["realm"], "Circle Of Life", params))
	require.False(t, VerifyDigest("POST", "Mufasa", params["realm"], "Circle Of Life", params))
}

func TestBuildDigestAuthorization(t *testing.T) {
	for _, qop := range []string{"", "auth", " auth-int, AUTH "} {
		t.Run(qop, func(t *testing.T) {
			user, realm := `camera"operator`, `office\realm`
			header, err := BuildDigestAuthorizationWithNonceCount("DESCRIBE", "rtsp://camera/live", user, "password", realm, "nonce", qop, 42)
			require.NoError(t, err)
			params, ok := ParseDigestAuthorization(header)
			require.True(t, ok)
			require.Equal(t, user, params["username"])
			require.Equal(t, realm, params["realm"])
			require.True(t, VerifyDigest("DESCRIBE", user, realm, "password", params))
			require.False(t, VerifyDigest("DESCRIBE", user, realm, "wrong", params))
			if qop != "" {
				require.Equal(t, "0000002a", params["nc"])
				require.Len(t, params["cnonce"], 32)
			} else {
				require.True(t, VerifyDigestAuthorization("DESCRIBE", user, realm, "password", header))
			}
		})
	}
	header, err := BuildDigestAuthorization("GET", "/", "user", "pass", "realm", "nonce", "auth")
	require.NoError(t, err)
	require.Contains(t, header, "nc=00000001")
	args := []string{"GET", "/", "user", "pass", "realm", "nonce", "auth"}
	for _, idx := range []int{0, 1, 2, 4, 5} {
		invalid := append([]string(nil), args...)
		invalid[idx] = ""
		_, err := BuildDigestAuthorization(invalid[0], invalid[1], invalid[2], invalid[3], invalid[4], invalid[5], invalid[6])
		require.ErrorContains(t, err, "incomplete")
	}
	_, err = BuildDigestAuthorization("GET", "/", "user", "pass", "realm", "nonce", "auth-int")
	require.ErrorContains(t, err, "unsupported")
	_, err = BuildDigestAuthorizationWithNonceCount("GET", "/", "user", "pass", "realm", "nonce", "auth", 0)
	require.ErrorContains(t, err, "positive")
}

func TestDigestVerificationRejectsInvalidParameters(t *testing.T) {
	header, err := BuildDigestAuthorization("GET", "/", "user", "pass", "realm", "nonce", "auth")
	require.NoError(t, err)
	valid, ok := ParseDigestAuthorization(header)
	require.True(t, ok)
	for _, tc := range []struct{ key, value string }{
		{"username", "other"}, {"realm", "other"}, {"uri", ""}, {"nonce", ""}, {"qop", "auth-int"}, {"nc", ""}, {"cnonce", ""}, {"nc", "00000000"}, {"nc", "zzzzzzzz"}, {"nc", "1"}, {"algorithm", "SHA-256"}, {"response", "zz"}, {"response", "ab"}, {"response", strings.Repeat("0", 32)},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			params := DigestParams{}
			for k, v := range valid {
				params[k] = v
			}
			params[tc.key] = tc.value
			require.False(t, VerifyDigest("GET", "user", "realm", "pass", params))
		})
	}
	for _, tc := range []struct {
		qop, nc string
		count   uint32
		ok      bool
	}{
		{"", "bad", 0, true}, {"auth", "00000001", 1, true}, {"AUTH", "FFFFFFFF", 0xffffffff, true}, {"auth", "00000000", 0, false}, {"auth", "bad", 0, false},
	} {
		count, ok := DigestNonceCount(DigestParams{"qop": tc.qop, "nc": tc.nc})
		require.Equal(t, tc.ok, ok)
		require.Equal(t, tc.count, count)
	}
	for _, qop := range []string{"", " ", "AUTH", "auth-int, auth"} {
		_, err := NormalizeDigestQop(qop)
		require.NoError(t, err)
	}
	_, err = NormalizeDigestQop("auth-int")
	require.Error(t, err)
}

func TestLegacyDigestVerification(t *testing.T) {
	const response = "670fd8c2df070c60b045671b8b24ff02" // RFC 2069 example.
	const nonce = "dcd98b7102dd2f0e8b11d0f600bfb0c093"
	require.True(t, VerifyDigestResponse("GET", "Mufasa", "testrealm@host.com", "Circle Of Life", "/dir/index.html", nonce, response))
	args := []string{"GET", "Mufasa", "testrealm@host.com", "Circle Of Life", "/dir/index.html", nonce, response}
	for _, idx := range []int{0, 1, 2, 4, 5, 6} {
		invalid := append([]string(nil), args...)
		invalid[idx] = ""
		require.False(t, VerifyDigestResponse(invalid[0], invalid[1], invalid[2], invalid[3], invalid[4], invalid[5], invalid[6]))
	}
	require.False(t, VerifyDigestResponse("GET", "user", "realm", "pass", "/", "nonce", "zz"))
	require.False(t, VerifyDigestResponse("GET", "user", "realm", "pass", "/", "nonce", "ab"))
	header, err := BuildDigestAuthorization("GET", "/", "user", "pass", "realm", "nonce", "")
	require.NoError(t, err)
	for _, bad := range []string{"Basic abc", strings.Replace(header, `username="user"`, `username="other"`, 1), strings.Replace(header, `realm="realm"`, `realm="other"`, 1), header + ", qop=auth", strings.Replace(header, "algorithm=MD5", "algorithm=SHA-256", 1)} {
		require.False(t, VerifyDigestAuthorization("GET", "user", "realm", "pass", bad), fmt.Sprint(bad))
	}
	require.True(t, VerifyDigestAuthorization("GET", "user", "realm", "pass", strings.Replace(header, "MD5", "md5", 1)))
}
