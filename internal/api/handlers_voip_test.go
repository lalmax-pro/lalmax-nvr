package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/lalmax-pro/lalmax-nvr/internal/voip"
	"github.com/stretchr/testify/require"
)

type voipTestRestarter struct {
	calls []voip.Config
	err   error
}

func (f *voipTestRestarter) RestartVoIP(_ context.Context, c *voip.Config) error {
	f.calls = append(f.calls, c.Clone())
	return f.err
}
func newVoIPSettingsHandler(t *testing.T) (*Handler, *voipTestRestarter) {
	t.Helper()
	cfg := &config.Config{}
	cfg.ApplyDefaults()
	cfg.VoIP.Users = []voip.User{{Username: "door", Password: "secret-door"}}
	path := filepath.Join(t.TempDir(), "nvr.yaml")
	require.NoError(t, config.Save(path, cfg))
	h := NewHandler(nil, nil, func(next http.Handler) http.Handler { return next }, cfg, nil, path, nil, nil)
	fake := &voipTestRestarter{}
	h.SetVoIPRestarter(fake)
	return h, fake
}
func putVoIP(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, httptest.NewRequest("PUT", "/api/settings/voip", strings.NewReader(body)))
	return rr
}
func TestVoIPSettingsHideAndPreservePasswords(t *testing.T) {
	h, fake := newVoIPSettingsHandler(t)
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, httptest.NewRequest("GET", "/api/settings/voip", nil))
	require.Equal(t, 200, rr.Code)
	require.NotContains(t, rr.Body.String(), "secret-door")
	require.Contains(t, rr.Body.String(), `"has_password":true`)
	require.Contains(t, rr.Body.String(), `"enabled":false`)
	rr = putVoIP(t, h, `{"enabled":true,"auth_enable":true,"users":[{"username":"door","password":""}]}`)
	require.Equal(t, 200, rr.Code, rr.Body.String())
	require.Len(t, fake.calls, 1)
	require.Equal(t, "secret-door", fake.calls[0].Users[0].Password)
	saved, err := config.Load(h.configPath)
	require.NoError(t, err)
	require.True(t, saved.VoIP.Enable)
	require.Equal(t, "secret-door", saved.VoIP.Users[0].Password)
	// Renaming cannot copy the previous array element's secret.
	rr = putVoIP(t, h, `{"users":[{"username":"another"}]}`)
	require.Equal(t, 400, rr.Code)
	require.Equal(t, "door", h.config.VoIP.Users[0].Username)
	rr = putVoIP(t, h, `{"enabled":false,"auth_enable":false,"users":[]}`)
	require.Equal(t, 200, rr.Code, rr.Body.String())
	require.Empty(t, h.config.VoIP.Users)
}

func TestVoIPSettingsHideAndPreservePBXPassword(t *testing.T) {
	h, fake := newVoIPSettingsHandler(t)
	h.config.VoIP.PbxServer = "pbx.example.test:5060"
	h.config.VoIP.PbxDomain = "pbx.example.test"
	h.config.VoIP.PbxUsername = "nvr-account"
	h.config.VoIP.PbxPassword = "pbx-secret"
	rr := httptest.NewRecorder()
	h.handleGetVoIPSettings(rr, httptest.NewRequest("GET", "/api/settings/voip", nil))
	require.Equal(t, 200, rr.Code)
	require.NotContains(t, rr.Body.String(), "pbx-secret")
	require.Contains(t, rr.Body.String(), `"pbx_has_password":true`)
	rr = putVoIP(t, h, `{"manual_answer":true,"ring_timeout_ms":45000}`)
	require.Equal(t, 200, rr.Code, rr.Body.String())
	require.Len(t, fake.calls, 1)
	require.True(t, fake.calls[0].ManualAnswer)
	require.Equal(t, "pbx-secret", fake.calls[0].PbxPassword)
}
func TestVoIPSettingsValidationAndRuntimeFailure(t *testing.T) {
	h, fake := newVoIPSettingsHandler(t)
	for _, body := range []string{`{"media_port_min":60000,"media_port_max":50000}`, `{"sip_ip":"0.0.0.0"}`, `{"srtp_mandatory":true}`, `{"unknown":true}`, `null`} {
		rr := putVoIP(t, h, body)
		require.Equal(t, 400, rr.Code, rr.Body.String())
	}
	require.Empty(t, fake.calls)
	h.config.Media.Mode = "http"
	require.Equal(t, 400, putVoIP(t, h, `{"enabled":true}`).Code)
	h.config.Media.Mode = "embedded"
	fake.err = fmt.Errorf("address already in use")
	rr := putVoIP(t, h, `{"enabled":true}`)
	require.Equal(t, 500, rr.Code)
	require.False(t, h.config.VoIP.Enable)
	saved, err := config.Load(h.configPath)
	require.NoError(t, err)
	require.False(t, saved.VoIP.Enable)
}
func TestVoIPSettingsSaveFailureRollsBackRuntime(t *testing.T) {
	h, fake := newVoIPSettingsHandler(t)
	h.configPath = filepath.Join(t.TempDir(), "missing", "nvr.yaml")
	rr := putVoIP(t, h, `{"enabled":true}`)
	require.Equal(t, 500, rr.Code)
	require.Len(t, fake.calls, 2)
	require.True(t, fake.calls[0].Enable)
	require.False(t, fake.calls[1].Enable)
	require.False(t, h.config.VoIP.Enable)
}
func TestVoIPSettingsConflictAndPermission(t *testing.T) {
	h, fake := newVoIPSettingsHandler(t)
	watcher, err := config.NewWatcher(h.config, h.configPath)
	require.NoError(t, err)
	h.SetConfigWatcher(watcher)
	require.NoError(t, os.WriteFile(h.configPath, []byte("# external change\n"), 0600))
	require.Equal(t, 409, putVoIP(t, h, `{"enabled":true}`).Code)
	require.Empty(t, fake.calls)
	h.SetMultiUserAuthMW(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.UserContextKey, &model.User{Role: "viewer"})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	require.Equal(t, 403, putVoIP(t, h, `{"enabled":true}`).Code)
}
func TestVoIPSettingsConcurrentReadWrite(t *testing.T) {
	h, _ := newVoIPSettingsHandler(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				rr := putVoIP(t, h, fmt.Sprintf(`{"realm":"realm-%d"}`, i))
				require.Equal(t, 200, rr.Code)
			} else {
				rr := httptest.NewRecorder()
				h.handleGetVoIPSettings(rr, httptest.NewRequest("GET", "/", nil))
				var result map[string]any
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &result))
			}
		}(i)
	}
	wg.Wait()
}

type voipRuntimeFake struct {
	voipTestRestarter
	status voip.Status
	ended  string
}

func (f *voipRuntimeFake) VoIPStatus() voip.Status { return f.status }
func (f *voipRuntimeFake) HangupVoIP(id string) bool {
	if id != "known" {
		return false
	}
	f.ended = id
	return true
}
func TestVoIPRuntimeStatusAndHangup(t *testing.T) {
	h, _ := newVoIPSettingsHandler(t)
	f := &voipRuntimeFake{status: voip.Status{Enabled: true, Endpoints: []voip.EndpointStatus{{User: "door"}}, Calls: []voip.CallStatus{{CallID: "known", FromUser: "door"}}}}
	h.SetVoIPRestarter(f)
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, httptest.NewRequest("GET", "/api/voip/status", nil))
	require.Equal(t, 200, rr.Code)
	require.NotContains(t, rr.Body.String(), "secret-door")
	require.Contains(t, rr.Body.String(), `"call_id":"known"`)
	rr = httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, httptest.NewRequest("POST", "/api/voip/calls/known/hangup", nil))
	require.Equal(t, 200, rr.Code)
	require.Equal(t, "known", f.ended)
	rr = httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, httptest.NewRequest("POST", "/api/voip/calls/missing/hangup", nil))
	require.Equal(t, 404, rr.Code)
}

func TestVoIPCallHistoryEndpoint(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "nvr.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Init(context.Background()))
	started := time.Date(2026, 10, 10, 1, 2, 3, 0, time.UTC)
	require.NoError(t, db.SaveVoIPCall(context.Background(), storage.VoIPCall{
		CallID: "history-call", Direction: "inbound", FromUser: "door-1", ToUser: "nvr", Outcome: "missed",
		StartedAt: started, EndedAt: started.Add(30 * time.Second), FailureReason: "no answer", Transport: "tls",
	}))
	h, _ := newVoIPSettingsHandler(t)
	h.db = db
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/voip/calls/history?limit=1&offset=0", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var response struct {
		Items  []storage.VoIPCall `json:"items"`
		Total  int                `json:"total"`
		Limit  int                `json:"limit"`
		Offset int                `json:"offset"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response))
	require.Equal(t, 1, response.Total)
	require.Equal(t, 1, response.Limit)
	require.Equal(t, "history-call", response.Items[0].CallID)
	require.Equal(t, "missed", response.Items[0].Outcome)

	bad := httptest.NewRecorder()
	h.Routes().ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/api/voip/calls/history?limit=0", nil))
	require.Equal(t, http.StatusBadRequest, bad.Code)
}

func TestVoIPRecordingPolicySavedAndReturned(t *testing.T) {
	h, f := newVoIPSettingsHandler(t)
	rr := putVoIP(t, h, `{"auth_enable":true,"users":[{"username":"door","record_calls":true}]}`)
	require.Equal(t, 200, rr.Code, rr.Body.String())
	require.True(t, f.calls[0].Users[0].RecordCalls)
	rr = httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, httptest.NewRequest("GET", "/api/settings/voip", nil))
	require.Contains(t, rr.Body.String(), `"record_calls":true`)
	require.NotContains(t, rr.Body.String(), "secret-door")
	require.Equal(t, 400, putVoIP(t, h, `{"auth_enable":false}`).Code)
}

type voipTalkTestRestarter struct {
	voipTestRestarter
	user, security, offer, token string
	digits                       []string
	answered, rejected           string
}

func (f *voipTalkTestRestarter) AnswerVoIPCall(id string) error {
	if id != "incoming" {
		return fmt.Errorf("call not found")
	}
	f.answered = id
	return nil
}
func (f *voipTalkTestRestarter) RejectVoIPCall(id string) error {
	if id != "incoming" {
		return fmt.Errorf("call not found")
	}
	f.rejected = id
	return nil
}

func (f *voipTalkTestRestarter) DialVoIP(user, security, offer string) (voip.DialResult, error) {
	f.user, f.security, f.offer = user, security, offer
	return voip.DialResult{CallID: "outgoing", State: "dialing", TalkToken: "private-token"}, nil
}
func (f *voipTalkTestRestarter) ClaimVoIPTalk(id string) (string, error) {
	if id != "incoming" {
		return "", fmt.Errorf("call not found")
	}
	return "inbound-token", nil
}
func (f *voipTalkTestRestarter) AttachVoIPTalk(_ context.Context, id, token, offer string) (string, error) {
	if id != "outgoing" || token != "private-token" {
		return "", fmt.Errorf("call not found")
	}
	f.token = token
	return "browser-answer", nil
}
func (f *voipTalkTestRestarter) KeepVoIPTalk(id, token string) bool {
	return id == "outgoing" && token == "private-token"
}
func (f *voipTalkTestRestarter) DetachVoIPTalk(id, token string) bool {
	return id == "incoming" && token == "inbound-token"
}
func (f *voipTalkTestRestarter) SendVoIPDTMF(id, digit string) error {
	if id != "incoming" {
		return fmt.Errorf("call not found")
	}
	f.digits = append(f.digits, digit)
	return nil
}
func TestVoIPTalkAPIValidationAndToken(t *testing.T) {
	h, _ := newVoIPSettingsHandler(t)
	runtime := &voipTalkTestRestarter{}
	h.SetVoIPRestarter(runtime)
	req := httptest.NewRequest("POST", "/api/voip/calls", strings.NewReader(`{"user":"1001","security":"dtls","sdp":"browser-offer"}`))
	rec := httptest.NewRecorder()
	h.handleVoIPDial(rec, req)
	require.Equal(t, 202, rec.Code)
	require.Contains(t, rec.Body.String(), "private-token")
	require.Equal(t, "1001", runtime.user)
	for _, body := range []string{`{"user":"1001"}`, `{"user":"1001","sdp":"offer","unknown":true}`} {
		rec = httptest.NewRecorder()
		h.handleVoIPDial(rec, httptest.NewRequest("POST", "/api/voip/calls", strings.NewReader(body)))
		require.Equal(t, 400, rec.Code)
	}
	for _, token := range []string{"wrong", "private-token"} {
		req = httptest.NewRequest("POST", "/api/voip/calls/outgoing/talk", strings.NewReader(fmt.Sprintf(`{"talk_token":%q,"sdp":"offer"}`, token)))
		route := chi.NewRouteContext()
		route.URLParams.Add("call_id", "outgoing")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		rec = httptest.NewRecorder()
		h.handleVoIPTalk(rec, req)
		if token == "wrong" {
			require.Equal(t, 409, rec.Code)
		} else {
			require.Equal(t, 200, rec.Code)
			require.Contains(t, rec.Body.String(), "browser-answer")
		}
	}
}

func TestInboundVoIPTalkClaimAndDetach(t *testing.T) {
	h, _ := newVoIPSettingsHandler(t)
	runtime := &voipTalkTestRestarter{}
	h.SetVoIPRestarter(runtime)
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("call_id", "incoming")
	routeContext := func(req *http.Request) *http.Request {
		return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))
	}
	rec := httptest.NewRecorder()
	h.handleVoIPTalkToken(rec, routeContext(httptest.NewRequest("POST", "/api/voip/calls/incoming/talk-token", nil)))
	require.Equal(t, 200, rec.Code)
	require.JSONEq(t, `{"talk_token":"inbound-token"}`, rec.Body.String())
	rec = httptest.NewRecorder()
	h.handleVoIPTalkDetach(rec, routeContext(httptest.NewRequest("POST", "/api/voip/calls/incoming/talk/detach", strings.NewReader(`{"talk_token":"inbound-token"}`))))
	require.Equal(t, 200, rec.Code)
	rec = httptest.NewRecorder()
	h.handleVoIPTalkDetach(rec, routeContext(httptest.NewRequest("POST", "/api/voip/calls/incoming/talk/detach", strings.NewReader(`{"talk_token":"wrong"}`))))
	require.Equal(t, 404, rec.Code)
	rec = httptest.NewRecorder()
	h.handleVoIPDTMF(rec, routeContext(httptest.NewRequest("POST", "/api/voip/calls/incoming/dtmf", strings.NewReader(`{"digit":"#"}`))))
	require.Equal(t, 200, rec.Code)
	require.Equal(t, []string{"#"}, runtime.digits)
	rec = httptest.NewRecorder()
	h.handleVoIPAnswer(rec, routeContext(httptest.NewRequest("POST", "/api/voip/calls/incoming/answer", nil)))
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "incoming", runtime.answered)
	rec = httptest.NewRecorder()
	h.handleVoIPReject(rec, routeContext(httptest.NewRequest("POST", "/api/voip/calls/incoming/reject", nil)))
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "incoming", runtime.rejected)
}

func TestVoIPCallIDDecodesEscapedSIPIdentity(t *testing.T) {
	router := chi.NewRouter()
	router.Post("/calls/{call_id}/talk", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "abc@lalmax-nvr", voipCallID(r))
		w.WriteHeader(204)
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("POST", "/calls/abc%40lalmax-nvr/talk", nil))
	require.Equal(t, 204, rec.Code)
}
