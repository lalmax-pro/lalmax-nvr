package linkage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

type fakeRecorder struct{ n atomic.Int32 }

func (f *fakeRecorder) HandleActivity(cameraID, source string) { f.n.Add(1) }

func TestEngineDispatchWebhookAndRecord(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.New(dir + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Init(context.Background()); err != nil {
		t.Fatal(err)
	}

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(204)
	}))
	t.Cleanup(srv.Close)

	rec := &fakeRecorder{}
	eng := New(db, rec, nil)
	_ = db.InsertAlarmRule(context.Background(), &model.AlarmRule{Name: "rec", Enabled: true, Action: model.AlarmActionRecord, CameraID: "cam1"})
	_ = db.InsertAlarmRule(context.Background(), &model.AlarmRule{Name: "hook", Enabled: true, Action: model.AlarmActionWebhook, ActionTarget: srv.URL})

	eng.Dispatch(model.Event{CameraID: "cam1", Source: "health", Type: "offline", Severity: "critical"})
	if rec.n.Load() != 1 {
		t.Fatalf("record trigger=%d", rec.n.Load())
	}
	for i := 0; i < 50 && hits.Load() == 0; i++ {
		// webhook is sync in run()
	}
	if hits.Load() != 1 {
		t.Fatalf("webhook hits=%d", hits.Load())
	}
}
