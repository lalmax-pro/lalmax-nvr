package webhook

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestReceiverDispatchesValidDetections(t *testing.T) {
	receiver := NewReceiver()
	got := make(chan DetectionResult, 1)
	id := receiver.OnDetection(func(result DetectionResult) { got <- result })
	require.Equal(t, "webhook-cb-1", id)
	require.False(t, receiver.UnregisterCallback("missing"))
	require.True(t, receiver.UnregisterCallback(id))

	id = receiver.OnDetection(func(result DetectionResult) { got <- result })
	body := `{"camera_id":"cam-1","pts":7,"timestamp":9,"image_url":"data:image/jpeg;base64,YQ==","detections":[{"label":"person","confidence":0.9,"box":[0,0,1,1],"track_id":3,"zone_id":"door"}]}`
	req := httptest.NewRequest(http.MethodPost, "/ai/detect", strings.NewReader(body))
	rec := httptest.NewRecorder()
	receiver.HandleHTTP()(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
	select {
	case result := <-got:
		require.Equal(t, "cam-1", result.CameraID)
		require.Equal(t, int64(7), result.PTS)
		require.Len(t, result.Detections, 1)
		require.Equal(t, "person", result.Detections[0].Label)
		require.NotNil(t, result.Detections[0].TrackID)
	case <-time.After(time.Second):
		t.Fatal("detection callback was not called")
	}
	require.True(t, receiver.UnregisterCallback(id))
}

func TestReceiverRejectsInvalidRequests(t *testing.T) {
	receiver := NewReceiver()
	handler := receiver.HandleHTTP()

	cases := []struct {
		name   string
		method string
		body   io.Reader
		status int
	}{
		{name: "method", method: http.MethodGet, body: strings.NewReader(`{}`), status: http.StatusMethodNotAllowed},
		{name: "read", method: http.MethodPost, body: errReader{}, status: http.StatusBadRequest},
		{name: "json", method: http.MethodPost, body: strings.NewReader(`{`), status: http.StatusBadRequest},
		{name: "camera", method: http.MethodPost, body: strings.NewReader(`{"pts":1}`), status: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/ai/detect", tc.body)
			rec := httptest.NewRecorder()
			handler(rec, req)
			require.Equal(t, tc.status, rec.Code)
		})
	}
}

func TestReceiverFansOutConcurrentCallbacks(t *testing.T) {
	receiver := NewReceiver()
	var wg sync.WaitGroup
	wg.Add(2)
	receiver.OnDetection(func(DetectionResult) { wg.Done() })
	receiver.OnDetection(func(DetectionResult) { wg.Done() })

	req := httptest.NewRequest(http.MethodPost, "/ai/detect", strings.NewReader(`{"camera_id":"cam"}`))
	rec := httptest.NewRecorder()
	receiver.HandleHTTP()(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("callbacks did not finish")
	}
}
