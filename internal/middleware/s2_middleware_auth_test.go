package middleware

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

// s2ResetAuthFailures clears the package-level rate-limit map.
func s2ResetAuthFailures(t *testing.T) {
	t.Helper()
	clear := func() {
		authFailures.Range(func(key, _ any) bool {
			authFailures.Delete(key)
			return true
		})
	}
	clear()
	t.Cleanup(clear)
}

// s2UsersProvider builds a provider over a fixed user table.
func s2UsersProvider(users map[string]*model.User, lookupErr error) MultiUserProvider {
	return MultiUserProvider{
		GetUserByUsername: func(_ context.Context, username string) (*model.User, error) {
			if lookupErr != nil {
				return nil, lookupErr
			}
			return users[username], nil
		},
		CountUsers: func(context.Context) (int, error) { return len(users), nil },
		GetLegacyUsername: func() string {
			return "legacy-admin"
		},
	}
}

func s2Serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestS2MultiUserAuthRateLimit(t *testing.T) {
	s2ResetAuthFailures(t)
	hash, err := HashPassword("secret")
	require.NoError(t, err)
	mw := NewMultiUserAuthMiddleware(s2UsersProvider(map[string]*model.User{
		"alice": {Username: "alice", PasswordHash: hash, Role: model.RoleSuperAdmin, Enabled: true},
	}, nil), func(next http.Handler) http.Handler { return next })

	const ip = "203.0.113.7"
	// An unexpired window at the failure cap blocks the request outright.
	authFailures.Store(ip, rateLimitEntry{count: authMaxFailures, windowStart: time.Now()})
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = ip + ":5000"
	req.SetBasicAuth("alice", "secret")
	rec := s2Serve(mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run while rate limited")
	})), req)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)

	// An expired window is discarded and the request proceeds.
	authFailures.Store(ip, rateLimitEntry{count: authMaxFailures, windowStart: time.Now().Add(-2 * time.Duration(authWindowMinutes) * time.Minute)})
	called := false
	rec = s2Serve(mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		require.Equal(t, "alice", ContextUser(r).Username)
	})), req)
	require.True(t, called)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestS2MultiUserAuthCountUsersError(t *testing.T) {
	s2ResetAuthFailures(t)
	provider := MultiUserProvider{
		CountUsers: func(context.Context) (int, error) { return 0, errors.New("db is down") },
	}
	mw := NewMultiUserAuthMiddleware(provider, func(next http.Handler) http.Handler { return next })
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "203.0.113.8:5000"
	rec := s2Serve(mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run")
	})), req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestS2MultiUserAuthLegacyFallback(t *testing.T) {
	s2ResetAuthFailures(t)
	provider := MultiUserProvider{
		CountUsers:        func(context.Context) (int, error) { return 0, nil },
		GetLegacyUsername: func() string { return "legacy-admin" },
	}
	legacyCalled := false
	legacy := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			legacyCalled = true
			w.Header().Set("X-Legacy", "1")
			next.ServeHTTP(w, r)
		})
	}
	var seen *model.User
	mw := NewMultiUserAuthMiddleware(provider, legacy)
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "203.0.113.9:5000"
	rec := s2Serve(mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = ContextUser(r)
	})), req)
	require.True(t, legacyCalled)
	require.Equal(t, "1", rec.Header().Get("X-Legacy"))
	require.NotNil(t, seen)
	require.Equal(t, "legacy-admin", seen.Username)
	require.Equal(t, model.RoleSuperAdmin, seen.Role)
	require.True(t, seen.Enabled)
}

func TestS2MultiUserAuthCredentials(t *testing.T) {
	hash, err := HashPassword("secret")
	require.NoError(t, err)
	users := map[string]*model.User{
		"alice":   {Username: "alice", PasswordHash: hash, Role: model.RoleSuperAdmin, Enabled: true},
		"bob":     {Username: "bob", PasswordHash: hash, Role: model.RoleUser, Enabled: true},
		"off":     {Username: "off", PasswordHash: hash, Role: model.RoleUser, Enabled: false},
		"nohash":  {Username: "nohash", PasswordHash: "", Role: model.RoleUser, Enabled: true},
		"alice2":  {Username: "alice2", PasswordHash: hash, Role: model.RoleSuperAdmin, Enabled: true},
		"alice10": {Username: "alice10", PasswordHash: hash, Role: model.RoleSuperAdmin, Enabled: true},
		"alice11": {Username: "alice11", PasswordHash: hash, Role: model.RoleSuperAdmin, Enabled: true},
	}
	mw := NewMultiUserAuthMiddleware(s2UsersProvider(users, nil), nil)

	token := base64.StdEncoding.EncodeToString([]byte("alice:secret"))
	cases := []struct {
		name     string
		query    string
		basic    []string
		want     int
		wantUser string
	}{
		{name: "token query param", query: "?token=" + token, want: http.StatusOK, wantUser: "alice"},
		{name: "bad base64 token", query: "?token=!!!", want: http.StatusUnauthorized},
		{name: "token without colon", query: "?token=" + base64.StdEncoding.EncodeToString([]byte("alice")), want: http.StatusUnauthorized},
		{name: "missing credentials", want: http.StatusUnauthorized},
		{name: "unknown user", basic: []string{"ghost", "secret"}, want: http.StatusUnauthorized},
		{name: "wrong password", basic: []string{"bob", "nope"}, want: http.StatusUnauthorized},
		{name: "disabled user", basic: []string{"off", "secret"}, want: http.StatusUnauthorized},
		{name: "user without stored hash", basic: []string{"nohash", "secret"}, want: http.StatusUnauthorized},
		{name: "valid user", basic: []string{"bob", "secret"}, want: http.StatusOK, wantUser: "bob"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s2ResetAuthFailures(t)
			req := httptest.NewRequest(http.MethodGet, "/api/x"+tc.query, nil)
			req.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", i+1)
			if tc.basic != nil {
				req.SetBasicAuth(tc.basic[0], tc.basic[1])
			}
			var user *model.User
			rec := s2Serve(mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				user = ContextUser(r)
			})), req)
			require.Equal(t, tc.want, rec.Code)
			if tc.wantUser != "" {
				require.NotNil(t, user)
				require.Equal(t, tc.wantUser, user.Username)
			} else {
				require.Nil(t, user)
			}
			if tc.want == http.StatusUnauthorized && len(tc.basic) == 0 {
				require.Equal(t, `Basic realm="lalmax-nvr"`, rec.Header().Get("WWW-Authenticate"))
			}
		})
	}
}

func TestS2MultiUserAuthLookupErrorAndFailureWindow(t *testing.T) {
	s2ResetAuthFailures(t)
	// Lookup errors are reported as unauthorized.
	mw := NewMultiUserAuthMiddleware(s2UsersProvider(map[string]*model.User{"alice": {Enabled: true}}, errors.New("lookup exploded")), nil)
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "198.51.100.50:4000"
	req.SetBasicAuth("alice", "secret")
	rec := s2Serve(mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run")
	})), req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// Repeated failures accumulate in an existing window.
	s2ResetAuthFailures(t)
	hash, err := HashPassword("secret")
	require.NoError(t, err)
	users := map[string]*model.User{"alice": {Username: "alice", PasswordHash: hash, Enabled: true}}
	mw = NewMultiUserAuthMiddleware(s2UsersProvider(users, nil), nil)
	const ip = "198.51.100.51"
	for attempt := 1; attempt <= 2; attempt++ {
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		req.RemoteAddr = ip + ":4000"
		req.SetBasicAuth("alice", "wrong")
		rec := s2Serve(mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})), req)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		value, ok := authFailures.Load(ip)
		require.True(t, ok)
		require.Equal(t, attempt, value.(rateLimitEntry).count)
	}

	// An expired window restarts counting at one.
	authFailures.Store(ip, rateLimitEntry{count: 5, windowStart: time.Now().Add(-2 * time.Duration(authWindowMinutes) * time.Minute)})
	req = httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = ip + ":4000"
	req.SetBasicAuth("alice", "wrong")
	s2Serve(mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})), req)
	value, ok := authFailures.Load(ip)
	require.True(t, ok)
	require.Equal(t, 1, value.(rateLimitEntry).count)

	// A successful login clears the recorded failures.
	req = httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = ip + ":4000"
	req.SetBasicAuth("alice", "secret")
	rec = s2Serve(mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})), req)
	require.Equal(t, http.StatusOK, rec.Code)
	_, ok = authFailures.Load(ip)
	require.False(t, ok)
}

func TestS2RBACMiddleware(t *testing.T) {
	require.Nil(t, ContextUser(httptest.NewRequest(http.MethodGet, "/", nil)))

	injectUser := func(role model.Role) func(http.Handler) http.Handler {
		return InjectUser(&model.User{Username: "u", Role: role})
	}

	// No user in context.
	rec := s2Serve(RequireRole(model.RoleSuperAdmin)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run without a user")
	})), httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// Wrong role is forbidden.
	rec = s2Serve(injectUser(model.RoleUser)(RequireRole(model.RoleSuperAdmin)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run for a forbidden role")
	}))), httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusForbidden, rec.Code)

	// Allowed role passes through and keeps the user in context.
	rec = s2Serve(injectUser(model.RoleUser)(RequireRole(model.RoleUser)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, model.RoleUser, ContextUser(r).Role)
		w.WriteHeader(http.StatusNoContent)
	}))), httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusNoContent, rec.Code)

	// Operate permission requires a super admin.
	rec = s2Serve(injectUser(model.RoleSuperAdmin)(RequireOperatePermission()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))), httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusAccepted, rec.Code)
}
