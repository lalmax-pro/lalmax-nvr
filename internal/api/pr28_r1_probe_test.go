package api

import (
    "fmt"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/lalmax-pro/lalmax-nvr/internal/ban"
    "github.com/lalmax-pro/lalmax-nvr/internal/config"
)

func TestPR28R1ConfigSaveErrorMustNotReturnSuccess(t *testing.T) {
    h := &Handler{config: &config.Config{}, configPath: t.TempDir()}
    w := httptest.NewRecorder()
    h.handleUpdateMergeSettings(w, httptest.NewRequest(http.MethodPut, "/api/settings/merge", strings.NewReader(`{"batch_limit":4}`)))
    if w.Code < 400 { t.Fatal("persistence failure was acknowledged as HTTP success") }
    if h.config.Merge.BatchLimit != 0 { t.Fatal("failed save changed in-memory merge settings") }
}

func TestPR28R1WatcherReloadMustNotLoseConcurrentToken(t *testing.T) {
    enabled := true
    path := filepath.Join(t.TempDir(), "nvr.yaml")
    cfg := &config.Config{}
    cfg.RTMP.Enabled = &enabled
    cfg.RTMP.StreamKeys = map[string]string{"existing": "synthetic"}
    if err := config.Save(path, cfg); err != nil { t.Fatal(err) }
    watcher, err := config.NewWatcher(cfg, path)
    if err != nil { t.Fatal(err) }
    h := &Handler{config: cfg, configPath: path, configWatcher: watcher, banMgr: ban.NewManager(nil, nil)}
    entered := make(chan struct{})
    release := make(chan struct{})
    watcher.Subscribe(func(*config.Config) { close(entered); <-release })
    f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
    if err != nil { t.Fatal(err) }
    _, _ = f.WriteString("\n# external change\n")
    f.Close()
    done := make(chan int, 1)
    go func() {
        rec := httptest.NewRecorder()
        h.handleReloadConfig(rec, httptest.NewRequest(http.MethodPost, "/api/config/reload", nil))
        done <- rec.Code
    }()
    select { case <-entered: case <-time.After(5*time.Second): t.Fatal("reload callback barrier not reached") }
    if err := h.ensureLocalVideoPublishKey("r1-probe"); err != nil { close(release); t.Fatal(err) }
    close(release)
    code := <-done
    mem := h.config.RTMP.StreamKeys["r1-probe"] != ""
    disk, err := config.Load(path)
    if err != nil { t.Fatal(err) }
    ondisk := disk.RTMP.StreamKeys["r1-probe"] != ""
    t.Logf("RELOAD_HTTP=%d MEMORY_HAS_TOKEN=%t DISK_HAS_TOKEN=%t", code, mem, ondisk)
    if !mem || !ondisk { t.Fatal("successful concurrent provision was lost from active configuration") }
}

func TestPR28R1ReloadSubscriberCanProvision(t *testing.T) {
 enabled := true
 path := filepath.Join(t.TempDir(), "nvr.yaml")
 cfg := &config.Config{}
 cfg.RTMP.Enabled = &enabled
 if err := config.Save(path, cfg); err != nil { t.Fatal(err) }
 watcher, err := config.NewWatcher(cfg, path)
 if err != nil { t.Fatal(err) }
 h := &Handler{config: cfg, configPath: path, configWatcher: watcher, banMgr: ban.NewManager(nil, nil)}
 callbackErr := make(chan error, 1)
 watcher.Subscribe(func(*config.Config) { callbackErr <- h.ensureLocalVideoPublishKey("callback-probe") })
 f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
 if err != nil { t.Fatal(err) }
 _, _ = f.WriteString("\n# external change\n")
 f.Close()
 done := make(chan int, 1)
 go func() { rec := httptest.NewRecorder(); h.handleReloadConfig(rec, httptest.NewRequest(http.MethodPost, "/api/config/reload", nil)); done <- rec.Code }()
 select { case code := <-done: if code != http.StatusOK { t.Fatalf("reload status %d", code) }; case <-time.After(5*time.Second): t.Fatal("subscriber deadlocked during guarded reload") }
 if err := <-callbackErr; err != nil { t.Fatal(err) }
 if h.config.RTMP.StreamKeys["callback-probe"] == "" { t.Fatal("callback token missing") }
}
func TestPR28R1SettingsSaveFailureRollsBack(t *testing.T) {
 for _, which := range []string{"system", "streaming"} {
  t.Run(which, func(t *testing.T) {
   enabled := true
   dlna := false
   cfg := &config.Config{}
   cfg.Cleanup.RetentionDays = 3
   cfg.DLNA.Enabled = &dlna
   cfg.RTMP.Enabled = &enabled
   cfg.RTMP.Port = 1935
   h := &Handler{config: cfg, configPath: t.TempDir()}
   body := `{"cleanup":{"retention_days":14},"dlna":{"enabled":true}}`
   if which == "streaming" { body = `{"rtmp":{"enabled":false,"port":24000}}` }
   rec := httptest.NewRecorder()
   req := httptest.NewRequest(http.MethodPut, "/api/settings/"+which, strings.NewReader(body))
   if which == "system" { h.handleUpdateSettings(rec, req) } else { h.handleUpdateStreamingSettings(rec, req) }
   if rec.Code < 400 { t.Fatalf("persistence failure returned HTTP %d", rec.Code) }
   if cfg.Cleanup.RetentionDays != 3 || cfg.DLNA.Enabled == nil || *cfg.DLNA.Enabled || cfg.RTMP.Enabled == nil || !*cfg.RTMP.Enabled || cfg.RTMP.Port != 1935 { t.Fatal("failed save changed in-memory settings") }
   if !enabled || dlna { t.Fatal("failed save changed preexisting pointer values") }
  })
 }
}
func TestPR28R1ExternalChangeConflictRollsBack(t *testing.T) {
 path := filepath.Join(t.TempDir(), "nvr.yaml")
 cfg := &config.Config{}
 cfg.Merge.BatchLimit = 2
 if err := config.Save(path, cfg); err != nil { t.Fatal(err) }
 watcher, err := config.NewWatcher(cfg, path)
 if err != nil { t.Fatal(err) }
 h := &Handler{config: cfg, configPath: path, configWatcher: watcher}
 f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
 if err != nil { t.Fatal(err) }
 _, _ = f.WriteString("\n# external modification\n")
 f.Close()
 rec := httptest.NewRecorder()
 h.handleUpdateMergeSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings/merge", strings.NewReader(`{"batch_limit":9}`)))
 if rec.Code != http.StatusConflict || cfg.Merge.BatchLimit != 2 { t.Fatalf("conflict status=%d batch=%d", rec.Code, cfg.Merge.BatchLimit) }
}
func TestPR28R1ValidationFailureRollsBack(t *testing.T) {
 for _, which := range []string{"merge", "system"} {
  t.Run(which, func(t *testing.T) {
   cfg := &config.Config{}
   cfg.Cleanup.RetentionDays = 3
   h := &Handler{config: cfg, configPath: filepath.Join(t.TempDir(), "nvr.yaml")}
   body := `{"enabled":true,"check_interval":"invalid"}`
   if which == "system" { body = `{"cleanup":{"retention_days":14,"disk_threshold_percent":999}}` }
   rec := httptest.NewRecorder()
   req := httptest.NewRequest(http.MethodPut, "/api/settings/"+which, strings.NewReader(body))
   if which == "system" { h.handleUpdateSettings(rec, req) } else { h.handleUpdateMergeSettings(rec, req) }
   if rec.Code != http.StatusBadRequest || cfg.Merge.Enabled || cfg.Cleanup.RetentionDays != 3 { t.Fatalf("validation failed without rollback: status=%d", rec.Code) }
  })
 }
}
func TestPR28R1FourWayConfigStress(t *testing.T) {
 enabled := true
 path := filepath.Join(t.TempDir(), "nvr.yaml")
 cfg := &config.Config{}
 cfg.RTMP.Enabled = &enabled
 cfg.RTMP.StreamKeys = map[string]string{"persistent": "synthetic"}
 if err := config.Save(path, cfg); err != nil { t.Fatal(err) }
 watcher, err := config.NewWatcher(cfg, path)
 if err != nil { t.Fatal(err) }
 h := &Handler{config: cfg, configPath: path, configWatcher: watcher, banMgr: ban.NewManager(nil, nil)}
 var wg sync.WaitGroup
 errors := make(chan string, 4)
 wg.Add(4)
 go func(){ defer wg.Done(); for i:=0;i<30;i++ { id:=fmt.Sprintf("stress-%02d",i); if err:=h.ensureLocalVideoPublishKey(id); err!=nil {errors<-"token provision failed";return};if i%2==0 {h.revokeLocalVideoPublishKey(id)} } }()
 go func(){ defer wg.Done(); for i:=0;i<30;i++ { rec:=httptest.NewRecorder();h.handleUpdateMergeSettings(rec,httptest.NewRequest(http.MethodPut,"/api/settings/merge",strings.NewReader(fmt.Sprintf(`{"batch_limit":%d}`,i+1))));if rec.Code!=200 {errors<-"merge save failed";return} } }()
 go func(){ defer wg.Done(); for i:=0;i<30;i++ { rec:=httptest.NewRecorder();h.handleUpdateSettings(rec,httptest.NewRequest(http.MethodPut,"/api/settings/system",strings.NewReader(fmt.Sprintf(`{"cleanup":{"retention_days":%d}}`,i+1))));if rec.Code!=200 {errors<-"system save failed";return} } }()
 go func(){ defer wg.Done(); for i:=0;i<30;i++ { rec:=httptest.NewRecorder();h.handleUpdateStreamingSettings(rec,httptest.NewRequest(http.MethodPut,"/api/settings/streaming",strings.NewReader(fmt.Sprintf(`{"rtmp":{"port":%d}}`,24000+i))));if rec.Code!=200 {errors<-"streaming save failed";return};g:=httptest.NewRecorder();h.handleGetSettings(g,httptest.NewRequest(http.MethodGet,"/api/settings",nil)) } }()
 wg.Wait();close(errors);for e:=range errors {t.Error(e)};if t.Failed(){return}
 loaded,err:=config.Load(path);if err!=nil{t.Fatal(err)}
 if loaded.Merge.BatchLimit!=30 || loaded.Cleanup.RetentionDays!=30 || loaded.RTMP.Port!=24029 {t.Fatal("persisted settings lost")}
 if len(loaded.RTMP.StreamKeys)!=16 || len(cfg.RTMP.StreamKeys)!=16 {t.Fatalf("key count differs: disk=%d memory=%d",len(loaded.RTMP.StreamKeys),len(cfg.RTMP.StreamKeys))}
 for id,key:=range cfg.RTMP.StreamKeys {if loaded.RTMP.StreamKeys[id]!=key {t.Fatalf("key mismatch for %s",id)}}
}
func TestPR28R1ReloadAppliesRuntimeRTMPKeys(t *testing.T) {
 path := filepath.Join(t.TempDir(), "nvr.yaml")
 cfg := &config.Config{}
 cfg.RTMP.StreamKeys = map[string]string{"reload-stream": "old-test-key"}
 if err := config.Save(path, cfg); err != nil { t.Fatal(err) }
 watcher, err := config.NewWatcher(cfg, path)
 if err != nil { t.Fatal(err) }
 manager := ban.NewManager(nil, nil)
 manager.SetRTMPStreamKeys(cfg.RTMP.StreamKeys)
 h := &Handler{config: cfg, configPath: path, configWatcher: watcher, banMgr: manager}
 next := &config.Config{}
 next.RTMP.StreamKeys = map[string]string{"reload-stream": "new-synthetic-key"}
 if err := config.Save(path, next); err != nil { t.Fatal(err) }
 rec := httptest.NewRecorder()
 h.handleReloadConfig(rec, httptest.NewRequest(http.MethodPost, "/api/config/reload", nil))
 if rec.Code != http.StatusOK { t.Fatalf("reload status=%d", rec.Code) }
 if err := manager.MediaAuthorizer().AuthorizePublish("rtmp", "reload-stream", "new-synthetic-key"); err != nil { t.Fatal("reloaded key not installed in runtime authorizer") }
 if err := manager.MediaAuthorizer().AuthorizePublish("rtmp", "reload-stream", "old-test-key"); err == nil { t.Fatal("revoked old key still accepted") }
}
