package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
)

func TestTranscodeJobUnavailableFailsClosed(t *testing.T) {
	h := &Handler{}
	for _, handler := range []http.HandlerFunc{h.handleSaveTranscodeJob, h.handleStartTranscodeJob, h.handleStopTranscodeJob, h.handleDeleteTranscodeJob} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest("POST", "/jobs", nil))
		if w.Code != 503 {
			t.Fatalf("unconfigured runtime returned %d", w.Code)
		}
	}
}
func TestTranscodeGenerationValidation(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "abc", "18446744073709551616"} {
		w := httptest.NewRecorder()
		_, ok := transcodeGeneration(w, httptest.NewRequest("POST", "/jobs/x/start?generation="+value, nil))
		if ok || w.Code != 400 {
			t.Fatalf("accepted generation %q", value)
		}
	}
}
func TestTranscodeErrorStatus(t *testing.T) {
	for code, want := range map[string]int{"revision_conflict": 409, "job_running": 409, "job_not_found": 404, "probe_required": 412, "transcoding_disabled": 412, "backend_unavailable": 503, "capacity_exceeded": 429} {
		w := httptest.NewRecorder()
		transcodeAPIError(w, &transcode.Error{Code: code, Message: "safe"})
		if w.Code != want {
			t.Fatalf("%s: got %d want %d", code, w.Code, want)
		}
	}
}
