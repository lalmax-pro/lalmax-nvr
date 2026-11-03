// SPDX-License-Identifier: MIT
//
// Coverage tests for the Xiaomi cloud client: the full login/2FA/captcha state
// machine, token re-authentication, device discovery and MISS URL resolution.
// Everything is served by local httptest servers; no real Xiaomi endpoint is
// contacted (the hard-coded URLs are redirected through a RoundTripper).

package xiaomi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var covCloudSsecurity = []byte("0123456789abcdef0123456789abcdef")

// covCloudTransport rewrites every outgoing request to a local test server.
type covCloudTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t *covCloudTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.Host = t.target.Host
	return t.base.RoundTrip(clone)
}

// covNewCloudServer starts an httptest server plus a client addressed at it.
func covNewCloudServer(t *testing.T, h http.Handler) (*httptest.Server, *http.Client) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	target, err := url.Parse(ts.URL)
	require.NoError(t, err)
	rt := &covCloudTransport{base: http.DefaultTransport, target: target}
	return ts, &http.Client{Transport: rt}
}

// covInstallDefaultTransport redirects http.DefaultTransport (used by SignIn,
// SignInWithCaptcha and SignInWithToken, which construct their own clients) to
// the local server.
func covInstallDefaultTransport(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	ts, _ := covNewCloudServer(t, h)
	target, err := url.Parse(ts.URL)
	require.NoError(t, err)
	prev := http.DefaultTransport
	http.DefaultTransport = &covCloudTransport{base: prev, target: target}
	t.Cleanup(func() { http.DefaultTransport = prev })
	return ts
}

type covFailRT struct{ err error }

func (rt covFailRT) RoundTrip(*http.Request) (*http.Response, error) { return nil, rt.err }

func covFailingCloudClient(err error) *http.Client {
	return &http.Client{Transport: covFailRT{err: err}}
}

// covCloudServer emulates the Xiaomi account/API endpoints used by the client.
type covCloudServer struct {
	mu sync.Mutex

	ssecurity []byte
	auth2Mode string // ok | captcha | verify | none
	flag      int    // 4 = phone, 8 = email, 0 = unknown
	ticket    string // sendTicket behaviour: ok | captcha | error

	phoneLocation string
	apiHandler    func(w http.ResponseWriter, r *http.Request) bool
	requests      []string
}

func (s *covCloudServer) setAuth2Mode(mode string) {
	s.mu.Lock()
	s.auth2Mode = mode
	s.mu.Unlock()
}

func (s *covCloudServer) record(r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	s.mu.Unlock()
}

func (s *covCloudServer) auth2Body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.auth2Mode {
	case "captcha":
		return `{"captchaURL":"/pass/getCode?icodeType=login"}`
	case "verify":
		return `{"notificationUrl":"https://account.xiaomi.com/fe/service/identity/authStart?sid=xiaomiio&_json=true"}`
	case "none":
		return `{}`
	default:
		b64 := base64.StdEncoding.EncodeToString(s.ssecurity)
		return fmt.Sprintf(`{"ssecurity":%q,"passToken":"pt-1","location":"https://account.xiaomi.com/pass/callback/first"}`, b64)
	}
}

func (s *covCloudServer) sendTicketBody() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.ticket {
	case "captcha":
		return `{"captchaURL":"/pass/getCode?icodeType=verify"}`
	case "error":
		return `{"code":7001,"message":"send failed"}`
	default:
		return `{"code":0}`
	}
}

func (s *covCloudServer) verifyBody() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phoneLocation == "" {
		return `{"location":"https://account.xiaomi.com/pass/callback/first"}`
	}
	return fmt.Sprintf(`{"location":%q}`, s.phoneLocation)
}

func (s *covCloudServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.record(r)
	switch {
	case r.URL.Path == "/pass/serviceLogin":
		if strings.Contains(r.Header.Get("Cookie"), "passToken=") {
			b64 := base64.StdEncoding.EncodeToString(s.ssecurity)
			fmt.Fprintf(w, "&&&START&&&"+`{"ssecurity":%q,"passToken":"pt-from-token","location":"https://account.xiaomi.com/pass/callback/first"}`, b64)
			return
		}
		fmt.Fprint(w, `&&&START&&&{"qs":"%3Fsid%3Dxiaomiio","_sign":"sign-1","sid":"xiaomiio","callback":"https://account.xiaomi.com/pass/callback"}`)

	case r.URL.Path == "/pass/serviceLoginAuth2":
		fmt.Fprintf(w, "&&&START&&&%s", s.auth2Body())

	case r.URL.Path == "/pass/getCode":
		http.SetCookie(w, &http.Cookie{Name: "ick", Value: "ick-value"})
		fmt.Fprint(w, "CAPTCHA-IMAGE")

	case r.URL.Path == "/identity/list":
		http.SetCookie(w, &http.Cookie{Name: "identity_session", Value: "identity-1"})
		fmt.Fprintf(w, `&&&START&&&{"code":0,"flag":%d}`, s.flag)

	case strings.HasPrefix(r.URL.Path, "/identity/auth/verify") && r.Method == http.MethodPost:
		fmt.Fprintf(w, "&&&START&&&%s", s.verifyBody())

	case strings.HasPrefix(r.URL.Path, "/identity/auth/verify"):
		fmt.Fprint(w, `&&&START&&&{"code":0,"maskedPhone":"138****0000","maskedEmail":"a***@example.com"}`)

	case strings.HasPrefix(r.URL.Path, "/identity/auth/send"):
		fmt.Fprintf(w, "&&&START&&&%s", s.sendTicketBody())

	case r.URL.Path == "/pass/callback/first":
		http.SetCookie(w, &http.Cookie{Name: "userId", Value: "user-1"})
		http.SetCookie(w, &http.Cookie{Name: "serviceToken", Value: "svc-token"})
		pragma, _ := json.Marshal(map[string]string{
			"ssecurity": base64.StdEncoding.EncodeToString(s.ssecurity),
		})
		w.Header().Set("Extension-Pragma", string(pragma))
		http.Redirect(w, r, "https://account.xiaomi.com/pass/callback/second", http.StatusFound)

	case r.URL.Path == "/pass/callback/second":
		http.SetCookie(w, &http.Cookie{Name: "cUserId", Value: "c-user-1"})
		http.SetCookie(w, &http.Cookie{Name: "passToken", Value: "pt-cookie"})
		fmt.Fprint(w, `{"ok":true}`)

	case strings.HasPrefix(r.URL.Path, "/app/"):
		if s.apiHandler != nil && s.apiHandler(w, r) {
			return
		}
		http.NotFound(w, r)

	default:
		http.NotFound(w, r)
	}
}

// covCloudEncrypted writes an encrypted API response the way the Xiaomi cloud
// does: base64(RC4(signedNonce, plaintextJSON)).
func covCloudEncrypted(w http.ResponseWriter, r *http.Request, ssecurity []byte, payload string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	nonce, err := base64.StdEncoding.DecodeString(r.Form.Get("_nonce"))
	if err != nil {
		http.Error(w, "bad nonce", http.StatusBadRequest)
		return
	}
	signedNonce := genSignedNonce(ssecurity, nonce)
	ciphertext, err := crypt(signedNonce, []byte(payload))
	if err != nil {
		http.Error(w, "crypt", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(ciphertext)))
}

func covCloudSession(t *testing.T, client *http.Client) *CloudSession {
	t.Helper()
	return &CloudSession{
		UserID:       "user-1",
		PassToken:    "pass-1",
		ServiceToken: "svc-1",
		Region:       "cn",
		client:       client,
		ssecurity:    covCloudSsecurity,
		cookies:      "userId=user-1; serviceToken=svc-token",
	}
}

// --- SignIn --------------------------------------------------------------

func TestCovXiaomiSignInFullFlow(t *testing.T) {
	state := &covCloudServer{ssecurity: covCloudSsecurity, auth2Mode: "ok"}
	covInstallDefaultTransport(t, state)

	session, err := SignIn("user@example.com", "secret", "de")
	require.NoError(t, err)
	require.NotNil(t, session)
	require.Equal(t, "user-1", session.UserID)
	require.Equal(t, "pt-cookie", session.PassToken)
	require.Equal(t, "de", session.Region)
	require.Equal(t, covCloudSsecurity, session.ssecurity)
	require.Contains(t, session.cookies, "serviceToken=svc-token")
	require.Contains(t, session.cookies, "cUserId=c-user-1")

	// Both redirect hops were followed: cookies from the 302 and the 200.
	require.Contains(t, state.requests, "GET /pass/callback/first")
	require.Contains(t, state.requests, "GET /pass/callback/second")
}

func TestCovXiaomiSignInStep1UnexpectedBody(t *testing.T) {
	covInstallDefaultTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "not-a-xiaomi-response")
	}))
	_, err := SignIn("user@example.com", "secret", "cn")
	require.ErrorContains(t, err, "unexpected response")
}

func TestCovXiaomiSignInStep2InvalidJSON(t *testing.T) {
	covInstallDefaultTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pass/serviceLoginAuth2" {
			fmt.Fprint(w, "&&&START&&&{not json")
			return
		}
		fmt.Fprint(w, `&&&START&&&{"sid":"xiaomiio"}`)
	}))
	_, err := SignIn("user@example.com", "secret", "cn")
	require.Error(t, err)
}

func TestCovXiaomiSignInLocationMissing(t *testing.T) {
	state := &covCloudServer{ssecurity: covCloudSsecurity, auth2Mode: "none"}
	covInstallDefaultTransport(t, state)
	_, err := SignIn("user@example.com", "secret", "cn")
	require.ErrorContains(t, err, "login failed")
}

func TestCovXiaomiSignInHTTPFailure(t *testing.T) {
	covInstallDefaultTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "&&&START&&&{}")
	}))
	_, err := SignIn("user@example.com", "secret", "cn")
	require.Error(t, err)
}

// --- captcha flow --------------------------------------------------------

func TestCovXiaomiCaptchaFlow(t *testing.T) {
	state := &covCloudServer{ssecurity: covCloudSsecurity, auth2Mode: "captcha"}
	covInstallDefaultTransport(t, state)

	session, sid, err := SignInWithCaptcha("user@example.com", "secret", "cn")
	require.Nil(t, session)
	require.NotEmpty(t, sid)
	var loginErr *LoginError
	require.ErrorAs(t, err, &loginErr)
	require.Equal(t, "CAPTCHA-IMAGE", string(loginErr.Captcha))
	require.Equal(t, "ick-value", loadPendingCloud(sid).auth["ick"])

	// The retry succeeds once the server stops demanding a captcha.
	state.setAuth2Mode("ok")
	session, err = LoginWithCaptcha(sid, "1234")
	require.NoError(t, err)
	require.Equal(t, "user-1", session.UserID)
	require.Equal(t, "cn", session.Region)

	// The pending session is single-use.
	_, err = LoginWithCaptcha(sid, "1234")
	require.ErrorContains(t, err, "invalid or expired captcha session")
}

func TestCovXiaomiCaptchaRetryReturnsNewSession(t *testing.T) {
	state := &covCloudServer{ssecurity: covCloudSsecurity, auth2Mode: "captcha"}
	covInstallDefaultTransport(t, state)

	_, sid, err := SignInWithCaptcha("user@example.com", "secret", "cn")
	require.NotEmpty(t, sid)
	require.Error(t, err)

	_, err = LoginWithCaptcha(sid, "0000")
	var capErr *CaptchaSessionError
	require.ErrorAs(t, err, &capErr)
	require.NotEmpty(t, capErr.CaptchaSessionID)
	require.NotEqual(t, sid, capErr.CaptchaSessionID)
	require.NotNil(t, capErr.Unwrap())
	deletePendingCloud(capErr.CaptchaSessionID)
}

func TestCovXiaomiCaptchaFlowPlainError(t *testing.T) {
	c := &Cloud{
		client: covFailingCloudClient(errors.New("dial refused")),
		region: "cn",
		auth:   map[string]string{"ick": "x", "username": "u", "password": "p"},
	}
	sid := storePendingCloud(c)
	_, err := LoginWithCaptcha(sid, "1234")
	require.ErrorContains(t, err, "dial refused")
	require.Nil(t, loadPendingCloud(sid), "pending session must be consumed")
}

// --- token sign-in -------------------------------------------------------

func TestCovXiaomiSignInWithToken(t *testing.T) {
	state := &covCloudServer{ssecurity: covCloudSsecurity}
	covInstallDefaultTransport(t, state)

	session, err := SignInWithToken("12345", "pass-token", "sg")
	require.NoError(t, err)
	require.Equal(t, "user-1", session.UserID)
	require.Equal(t, "pt-cookie", session.PassToken)
	require.Equal(t, "sg", session.Region)
	require.Equal(t, covCloudSsecurity, session.ssecurity)
	require.Contains(t, state.requests, "GET /pass/serviceLogin")
}

func TestCovXiaomiSignInWithTokenErrors(t *testing.T) {
	covInstallDefaultTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "garbage")
	}))
	_, err := SignInWithToken("12345", "pass-token", "cn")
	require.Error(t, err)

	c := &Cloud{client: covFailingCloudClient(errors.New("no route")), sid: "xiaomiio", region: "cn"}
	require.ErrorContains(t, c.LoginWithToken("1", "2"), "no route")
}

// --- device list + MISS URL ---------------------------------------------

func covCloudAPIFixture(t *testing.T, state *covCloudServer, vendor int, deviceIP string) *httptest.Server {
	t.Helper()
	state.apiHandler = func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/app/v2/home/device_list_page":
			payload := fmt.Sprintf(`{"code":0,"message":"ok","result":{"list":[{"did":"655448418","name":"C200","model":"chuangmi.camera.046c04","localip":%q,"mac":"AA:BB","isOnline":true}]}}`, deviceIP)
			covCloudEncrypted(w, r, state.ssecurity, payload)
			return true
		case "/app/v2/device/miss_get_vendor":
			payload := fmt.Sprintf(`{"code":0,"message":"ok","result":{"vendor":{"vendor":%d,"vendor_params":{"p2p_id":"p2p-1"}},"public_key":"deadbeef","sign":"sig-1"}}`, vendor)
			covCloudEncrypted(w, r, state.ssecurity, payload)
			return true
		}
		return false
	}
	ts := covInstallDefaultTransport(t, state)
	return ts
}

func TestCovXiaomiGetDeviceListAndRequest(t *testing.T) {
	state := &covCloudServer{ssecurity: covCloudSsecurity}
	ts := covCloudAPIFixture(t, state, 4, "127.0.0.1:32108")
	_ = ts

	session := covCloudSession(t, nil)
	// nil client is not valid for a real request; use the rewriting client.
	_, client := covNewCloudServer(t, state)
	session.client = client

	devices, err := GetDeviceList(session)
	require.NoError(t, err)
	require.Len(t, devices, 1)
	require.Equal(t, "655448418", devices[0].DID)
	require.Equal(t, "chuangmi.camera.046c04", devices[0].Model)
	require.Equal(t, "127.0.0.1:32108", devices[0].IP)
	require.True(t, devices[0].IsOnline)
}

func TestCovXiaomiRequestErrorPaths(t *testing.T) {
	_, client := covNewCloudServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/badb64":
			fmt.Fprint(w, "!!!not-base64!!!")
		case "/app/badjson":
			nonce, _ := base64.StdEncoding.DecodeString(r.FormValue("_nonce"))
			ct, _ := crypt(genSignedNonce(covCloudSsecurity, nonce), []byte("not json"))
			fmt.Fprint(w, base64.StdEncoding.EncodeToString(ct))
		case "/app/codeerr":
			nonce, _ := base64.StdEncoding.DecodeString(r.FormValue("_nonce"))
			ct, _ := crypt(genSignedNonce(covCloudSsecurity, nonce), []byte(`{"code":-1,"message":"boom"}`))
			fmt.Fprint(w, base64.StdEncoding.EncodeToString(ct))
		case "/app/ok":
			require.Equal(t, "yes", r.Header.Get("X-Cov"))
			nonce, _ := base64.StdEncoding.DecodeString(r.FormValue("_nonce"))
			ct, _ := crypt(genSignedNonce(covCloudSsecurity, nonce), []byte(`{"code":0,"message":"","result":{"ok":true}}`))
			fmt.Fprint(w, base64.StdEncoding.EncodeToString(ct))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "boom")
		}
	}))

	c := &Cloud{client: client, ssecurity: covCloudSsecurity, cookies: "userId=u; serviceToken=t"}

	result, err := c.Request("https://api.io.mi.com/app", "/ok", "{}", map[string]string{"X-Cov": "yes"})
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(result))

	_, err = c.Request("https://api.io.mi.com/app", "/status500", "{}", nil)
	require.Error(t, err)

	_, err = c.Request("https://api.io.mi.com/app", "/badb64", "{}", nil)
	require.Error(t, err)

	_, err = c.Request("https://api.io.mi.com/app", "/badjson", "{}", nil)
	require.Error(t, err)

	_, err = c.Request("https://api.io.mi.com/app", "/codeerr", "{}", nil)
	require.ErrorContains(t, err, "boom")

	// Invalid base URL surfaces the http.NewRequest error.
	_, err = c.Request("://bad", "/x", "{}", nil)
	require.Error(t, err)

	// Transport failures surface as errors.
	bad := &Cloud{client: covFailingCloudClient(errors.New("dial")), ssecurity: covCloudSsecurity}
	_, err = bad.Request("https://api.io.mi.com/app", "/x", "{}", nil)
	require.ErrorContains(t, err, "dial")
}

func TestCovXiaomiResolveMISSURL(t *testing.T) {
	state := &covCloudServer{ssecurity: covCloudSsecurity}
	covCloudAPIFixture(t, state, 4, "127.0.0.1:32108")

	missURL, err := ResolveMISSURL(XiaomiCloudConfig{UserID: "12345", Token: "pass-token", Region: "cn"}, "655448418", "")
	require.NoError(t, err)
	require.Contains(t, missURL, "miss://127.0.0.1:32108")
	require.Contains(t, missURL, "vendor=cs2")
	require.Contains(t, missURL, "transport=udp")
	require.Contains(t, missURL, "device_public=deadbeef")
	require.Contains(t, missURL, "sign=sig-1")
	require.Contains(t, missURL, "model=chuangmi.camera.046c04")
}

func TestCovXiaomiResolveMISSURLTUTKVendor(t *testing.T) {
	state := &covCloudServer{ssecurity: covCloudSsecurity}
	covCloudAPIFixture(t, state, 1, "127.0.0.1:32108")

	missURL, err := ResolveMISSURL(XiaomiCloudConfig{UserID: "12345", Token: "pass-token", Region: "cn"}, "655448418", "isa.camera.df3")
	require.NoError(t, err)
	require.Contains(t, missURL, "vendor=tutk")
	require.Contains(t, missURL, "uid=p2p-1")
	require.Contains(t, missURL, "model=isa.camera.df3")
}

func TestCovXiaomiResolveMISSURLNoLANIP(t *testing.T) {
	state := &covCloudServer{ssecurity: covCloudSsecurity}
	covCloudAPIFixture(t, state, 4, "")

	_, err := ResolveMISSURL(XiaomiCloudConfig{UserID: "12345", Token: "pass-token", Region: "cn"}, "655448418", "")
	require.ErrorContains(t, err, "has no LAN IP")
}

func TestCovXiaomiResolveMISSURLUnknownDevice(t *testing.T) {
	state := &covCloudServer{ssecurity: covCloudSsecurity}
	covCloudAPIFixture(t, state, 4, "127.0.0.1:1")

	_, err := ResolveMISSURL(XiaomiCloudConfig{UserID: "12345", Token: "pass-token", Region: "cn"}, "unknown-did", "")
	require.ErrorContains(t, err, "no LAN IP")
}

func TestCovXiaomiResolveMISSURLVendorAPIError(t *testing.T) {
	state := &covCloudServer{ssecurity: covCloudSsecurity}
	state.apiHandler = func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/app/v2/home/device_list_page":
			covCloudEncrypted(w, r, state.ssecurity, `{"code":0,"message":"ok","result":{"list":[{"did":"655448418","localip":"127.0.0.1:32108"}]}}`)
			return true
		case "/app/v2/device/miss_get_vendor":
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		return false
	}
	covInstallDefaultTransport(t, state)

	_, err := ResolveMISSURL(XiaomiCloudConfig{UserID: "12345", Token: "pass-token", Region: "cn"}, "655448418", "")
	require.ErrorContains(t, err, "miss_get_vendor API")
}

func TestCovXiaomiResolveMISSURLAuthError(t *testing.T) {
	// No device list: the lookup is logged and resolution fails on the missing IP.
	state := &covCloudServer{ssecurity: covCloudSsecurity}
	covInstallDefaultTransport(t, state)

	_, err := ResolveMISSURL(XiaomiCloudConfig{UserID: "12345", Token: "pass-token", Region: "cn"}, "655448418", "")
	require.ErrorContains(t, err, "no LAN IP")
}

// --- 2FA verification flow ----------------------------------------------

func TestCovXiaomiVerifyPhoneFlow(t *testing.T) {
	state := &covCloudServer{ssecurity: covCloudSsecurity, auth2Mode: "verify", flag: 4, ticket: "ok"}
	covInstallDefaultTransport(t, state)

	session, sid, err := SignInWithCaptcha("user@example.com", "secret", "cn")
	require.Nil(t, session)
	require.NotEmpty(t, sid)
	var loginErr *LoginError
	require.ErrorAs(t, err, &loginErr)
	require.Equal(t, "138****0000", loginErr.VerifyPhone)
	require.Equal(t, "a***@example.com", loginErr.VerifyEmail)
	require.Contains(t, state.requests, "POST /identity/auth/sendPhoneTicket")

	session, err = LoginWithVerify(sid, "ticket-123")
	require.NoError(t, err)
	require.Equal(t, "user-1", session.UserID)
	require.Equal(t, "cn", session.Region)
	require.Contains(t, state.requests, "POST /identity/auth/verifyPhone")

	_, err = LoginWithVerify(sid, "ticket-123")
	require.ErrorContains(t, err, "invalid or expired verification session")
}

func TestCovXiaomiVerifyEmailFlow(t *testing.T) {
	state := &covCloudServer{ssecurity: covCloudSsecurity, auth2Mode: "verify", flag: 8, ticket: "ok"}
	covInstallDefaultTransport(t, state)

	_, sid, err := SignInWithCaptcha("user@example.com", "secret", "cn")
	require.NotEmpty(t, sid)
	var loginErr *LoginError
	require.ErrorAs(t, err, &loginErr)

	_, err = LoginWithVerify(sid, "ticket-123")
	require.NoError(t, err)
	require.Contains(t, state.requests, "POST /identity/auth/sendEmailTicket")
	require.Contains(t, state.requests, "POST /identity/auth/verifyEmail")
}

func TestCovXiaomiVerifyUnknownFlag(t *testing.T) {
	state := &covCloudServer{ssecurity: covCloudSsecurity, auth2Mode: "verify", flag: 0, ticket: "ok"}
	covInstallDefaultTransport(t, state)

	_, sid, err := SignInWithCaptcha("user@example.com", "secret", "cn")
	require.NotEmpty(t, sid)
	require.Error(t, err)
	require.Contains(t, state.requests, "POST /identity/auth/sendTicket")
	deletePendingCloud(sid)
}

func TestCovXiaomiSendTicketCaptcha(t *testing.T) {
	state := &covCloudServer{ssecurity: covCloudSsecurity, auth2Mode: "verify", flag: 4, ticket: "captcha"}
	covInstallDefaultTransport(t, state)

	_, _, err := SignInWithCaptcha("user@example.com", "secret", "cn")
	var loginErr *LoginError
	require.ErrorAs(t, err, &loginErr)
	require.Equal(t, "CAPTCHA-IMAGE", string(loginErr.Captcha))
}

func TestCovXiaomiSendTicketCodeError(t *testing.T) {
	state := &covCloudServer{ssecurity: covCloudSsecurity, auth2Mode: "verify", flag: 4, ticket: "error"}
	covInstallDefaultTransport(t, state)

	_, _, err := SignInWithCaptcha("user@example.com", "secret", "cn")
	require.ErrorContains(t, err, "send failed")
}

func TestCovXiaomiLoginWithVerifyNoLocation(t *testing.T) {
	c := &Cloud{
		client: covFailingCloudClient(errors.New("unused")),
		region: "cn",
		auth:   map[string]string{"flag": "4"},
	}
	sid := storePendingCloud(c)
	_, err := LoginWithVerify(sid, "ticket")
	require.ErrorContains(t, err, "unused")
}

func TestCovXiaomiLoginWithVerifyMissingLocation(t *testing.T) {
	_, client := covNewCloudServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `&&&START&&&{"code":0}`)
	}))
	c := &Cloud{client: client, region: "cn", auth: map[string]string{"flag": "4"}}
	sid := storePendingCloud(c)
	_, err := LoginWithVerify(sid, "ticket")
	require.ErrorContains(t, err, "verification failed")
}

// --- direct error branches ----------------------------------------------

func TestCovXiaomiCloudClientFailures(t *testing.T) {
	failing := func() *Cloud {
		return &Cloud{
			client: covFailingCloudClient(errors.New("dial")),
			sid:    "xiaomiio",
			region: "cn",
			auth:   map[string]string{"flag": "4", "ick": "x", "identity_session": "s"},
		}
	}

	require.ErrorContains(t, failing().Login("u", "p"), "login step 1")
	require.ErrorContains(t, failing().LoginWithToken("u", "t"), "dial")
	require.ErrorContains(t, failing().getCaptcha("/pass/getCode"), "dial")
	require.ErrorContains(t, failing().authStart("https://account.xiaomi.com/fe/service/identity/authStart"), "dial")
	require.ErrorContains(t, failing().sendTicket(), "dial")
	require.ErrorContains(t, failing().loginWithVerify("ticket"), "dial")
	require.ErrorContains(t, failing().finishAuth("https://account.xiaomi.com/pass/callback"), "dial")

	// authStart with a malformed response body.
	_, client := covNewCloudServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "broken")
	}))
	bad := &Cloud{client: client, auth: map[string]string{}}
	require.ErrorContains(t, bad.authStart("https://account.xiaomi.com/fe/service/identity/authStart"), "unexpected response")

	// finishAuth with an unparseable Extension-Pragma header.
	_, client = covNewCloudServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Extension-Pragma", "{not-json")
	}))
	require.Error(t, (&Cloud{client: client}).finishAuth("https://account.xiaomi.com/pass/callback"))
}

func TestCovXiaomiLoginWithCaptchaFlagGoesToSendTicket(t *testing.T) {
	// A pending session that already carries a verify flag uses sendTicket
	// (the 2FA captcha path) instead of restarting Login.
	state := &covCloudServer{ssecurity: covCloudSsecurity, flag: 4, ticket: "ok"}
	_, client := covNewCloudServer(t, state)

	c := &Cloud{
		client: client,
		region: "cn",
		auth: map[string]string{
			"flag":             "4",
			"ick":              strconvItoaClient(state.flag),
			"identity_session": "identity-1",
			"username":         "u",
			"password":         "p",
		},
	}
	var loginErr *LoginError
	require.ErrorAs(t, c.loginWithCaptcha("1234"), &loginErr)
	require.Contains(t, state.requests, "GET /identity/auth/verifyPhone")
}

func TestCovXiaomiLoginStep2ThenFinishAuth(t *testing.T) {
	// Direct Cloud.Login call with an injected client (no DefaultTransport swap).
	state := &covCloudServer{ssecurity: covCloudSsecurity, auth2Mode: "ok"}
	_, client := covNewCloudServer(t, state)
	c := &Cloud{client: client, sid: "xiaomiio", region: "cn"}
	require.NoError(t, c.Login("user@example.com", "secret"))
	userID, passToken := c.UserToken()
	require.Equal(t, "user-1", userID)
	require.Equal(t, "pt-cookie", passToken)
	require.Nil(t, c.auth)
	require.Equal(t, covCloudSsecurity, c.ssecurity)
}

// strconvItoaClient formats an int without importing strconv in this file's
// assertion helper.
func strconvItoaClient(v int) string {
	return fmt.Sprintf("%d", v)
}

var _ = io.Discard
var _ = time.Second
