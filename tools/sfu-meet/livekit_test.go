package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/livekit/protocol/auth"
)

func TestLiveKitDemoAuthorization(t *testing.T) {
	const origin, launch, key, secret = "http://127.0.0.1:12345", "01234567890123456789012345678901", "key", "livekit-test-secret"
	h, err := newLiveKitDemoHandler(origin, launch, "http://127.0.0.1:12346", key, secret)
	if err != nil {
		t.Fatal(err)
	}
	request := func(originHeader, password, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, origin+"/api/sfu/livekit/join", strings.NewReader(body))
		r.Header.Set("Origin", originHeader)
		r.SetBasicAuth("demo", password)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		origin, password, body string
		code                   int
	}{
		{"http://evil.example", launch, `{"room":"demo"}`, http.StatusForbidden},
		{origin, "wrong", `{"room":"demo"}`, http.StatusUnauthorized},
		{origin, launch, `{"room":"other"}`, http.StatusBadRequest},
		{origin, launch, `{"room":"demo"} {}`, http.StatusBadRequest},
	} {
		if w := request(tc.origin, tc.password, tc.body); w.Code != tc.code {
			t.Fatalf("unexpected status %d", w.Code)
		}
	}
	w := request(origin, launch, `{"room":"demo"}`)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("join status %d", w.Code)
	}
	var result struct{ Token, Room, Identity string }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.ParseAPIToken(result.Token)
	if err != nil {
		t.Fatal(err)
	}
	_, grants, err := verifier.Verify(secret)
	if err != nil {
		t.Fatal(err)
	}
	if result.Room != "demo" || verifier.Identity() != result.Identity || grants.Video == nil || grants.Video.Room != "demo" || !grants.Video.RoomJoin || grants.Video.RoomAdmin {
		t.Fatal("invalid room grant")
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, origin+"/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `data-mode="local"`) {
		t.Fatalf("page status %d", page.Code)
	}
}
