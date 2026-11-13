package api

import (
    "fmt"
    "net/http"
    "net/http/httptest"
    "path/filepath"
    "strings"
    "sync"
    "testing"

    "github.com/lalmax-pro/lalmax-nvr/internal/config"
)

func TestPR28R1ReadWhileSettingsSave(t *testing.T) {
    cfg := &config.Config{}
    h := &Handler{config: cfg, configPath: filepath.Join(t.TempDir(), "nvr.yaml")}
    var wg sync.WaitGroup
    wg.Add(2)
    go func() {
        defer wg.Done()
        for i := 0; i < 60; i++ {
            rec := httptest.NewRecorder()
            h.handleGetSettings(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
        }
    }()
    go func() {
        defer wg.Done()
        for i := 0; i < 60; i++ {
            rec := httptest.NewRecorder()
            body := fmt.Sprintf(`{"cleanup":{"retention_days":%d}}`, i+1)
            h.handleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings/system", strings.NewReader(body)))
            if rec.Code != http.StatusOK { t.Errorf("unexpected response %d", rec.Code); return }
        }
    }()
    wg.Wait()
}

func TestPR28R1StreamingReadWhileSave(t *testing.T) {
    enabled := true
    cfg := &config.Config{}
    cfg.RTMP.Enabled = &enabled
    h := &Handler{config: cfg, configPath: filepath.Join(t.TempDir(), "nvr.yaml")}
    var wg sync.WaitGroup
    wg.Add(2)
    go func() {
        defer wg.Done()
        for i := 0; i < 60; i++ {
            rec := httptest.NewRecorder()
            h.handleGetStreamingSettings(rec, httptest.NewRequest(http.MethodGet, "/api/settings/streaming", nil))
        }
    }()
    go func() {
        defer wg.Done()
        for i := 0; i < 60; i++ {
            rec := httptest.NewRecorder()
            body := fmt.Sprintf(`{"rtmp":{"port":%d}}`, 24000+i)
            h.handleUpdateStreamingSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings/streaming", strings.NewReader(body)))
            if rec.Code != http.StatusOK { t.Errorf("unexpected response %d", rec.Code); return }
        }
    }()
    wg.Wait()
}

func TestPR28R1LocalVideoDirectoryConcurrentConfigSwap(t *testing.T) {
    root := t.TempDir()
    first := &config.Config{}
    second := &config.Config{}
    first.Storage.RootDir = root
    second.Storage.RootDir = root
    h := &Handler{config: first}
    var wg sync.WaitGroup
    wg.Add(2)
    go func() {
        defer wg.Done()
        for i := 0; i < 100; i++ {
            if _, err := h.localVideoDirectory(); err != nil { t.Error(err); return }
        }
    }()
    go func() {
        defer wg.Done()
        for i := 0; i < 100; i++ {
            h.localVideos.authMu.Lock()
            if i%2 == 0 { h.config = second } else { h.config = first }
            h.localVideos.authMu.Unlock()
        }
    }()
    wg.Wait()
}
