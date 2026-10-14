package voip

import (
	"fmt"

	coreauth "github.com/lalmax-pro/lalmax-nvr/internal/auth"
)

type digestNonceStore = coreauth.DigestNonceStore

func newDigestNonceStore() *digestNonceStore {
	return coreauth.NewDigestNonceStore(0, 0)
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
	var err error
	qop, err = coreauth.NormalizeDigestQop(qop)
	if err != nil {
		return "", fmt.Errorf("PBX does not support digest qop auth: %w", err)
	}
	return coreauth.BuildDigestAuthorization(method, uri, username, password, realm, nonce, qop)
}
