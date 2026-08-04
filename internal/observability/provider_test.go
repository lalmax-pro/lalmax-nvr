package observability

import "testing"

func TestSignalEndpoint(t *testing.T) {
	tests := []struct {
		base   string
		signal string
		want   string
	}{
		{"http://localhost:4318", "traces", "http://localhost:4318/v1/traces"},
		{"https://otel.example.com/prefix/", "metrics", "https://otel.example.com/prefix/v1/metrics"},
	}
	for _, tt := range tests {
		if got := signalEndpoint(tt.base, tt.signal); got != tt.want {
			t.Fatalf("signalEndpoint(%q, %q) = %q, want %q", tt.base, tt.signal, got, tt.want)
		}
	}
}

func TestCloneHeaders(t *testing.T) {
	original := map[string]string{"Authorization": "Bearer token"}
	cloned := cloneHeaders(original)
	cloned["Authorization"] = "changed"
	if original["Authorization"] != "Bearer token" {
		t.Fatal("cloneHeaders mutated its source")
	}
}
