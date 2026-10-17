package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLalmaxHTTPInternalAuth(t *testing.T) {
	const token = "test-internal-token"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Lalmax-Internal-Token") != token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/stat/lal_info":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"error_code":0,"desp":"succ","data":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	engine, err := NewLalmaxHTTP(LalmaxHTTPConfig{
		BaseURL:           server.URL,
		InternalAuthToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := engine.Ready(context.Background()); err != nil {
		t.Fatalf("Ready with internal token failed: %v", err)
	}
}

func TestLalmaxHTTPSubscribeEventsInternalAuth(t *testing.T) {
	const token = "test-internal-token"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/hook/stream" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Lalmax-Internal-Token") != token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(": connected\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer server.Close()

	engine, err := NewLalmaxHTTP(LalmaxHTTPConfig{
		BaseURL:           server.URL,
		InternalAuthToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := engine.SubscribeEvents(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("SubscribeEvents with internal token failed: %v", err)
	}
	if events == nil {
		t.Fatal("SubscribeEvents returned a nil event channel")
	}
}
