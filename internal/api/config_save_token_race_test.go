package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/ban"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
)

// The maintainer's reproduction saved merge settings while provisioning a
// local-video RTMP token. The whole config snapshot must share the token lock,
// with or without a live ConfigWatcher.
func TestMergeSettingsSaveConcurrentWithLocalVideoRTMPTokens(t *testing.T) {
	for _, withWatcher := range []bool{false, true} {
		t.Run(fmt.Sprintf("watcher=%t", withWatcher), func(t *testing.T) {
			enabled := true
			h := &Handler{
				config: &config.Config{},
				configPath: filepath.Join(t.TempDir(), "nvr.yaml"),
				banMgr: ban.NewManager(nil, nil),
			}
			h.config.RTMP.Enabled = &enabled
			h.config.RTMP.StreamKeys = map[string]string{"existing": "original"}
			if err := config.Save(h.configPath, h.config); err != nil { t.Fatal(err) }
			if withWatcher {
				watcher, err := config.NewWatcher(h.config, h.configPath)
				if err != nil { t.Fatal(err) }
				h.configWatcher = watcher
			}
			const count = 20
			results := make(chan error, count*2)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				for i := 0; i < count; i++ {
					if err := h.ensureLocalVideoPublishKey(fmt.Sprintf("uploaded-%02d", i)); err != nil {
						results <- fmt.Errorf("publish token %d: %w", i, err)
						return
					}
				}
			}()
			go func() {
				defer wg.Done()
				for i := 0; i < count; i++ {
					body := fmt.Sprintf(`{"enabled":true,"batch_limit":%d}`, i+1)
					req := httptest.NewRequest(http.MethodPut, "/api/settings/merge", strings.NewReader(body))
					resp := httptest.NewRecorder()
					h.handleUpdateMergeSettings(resp, req)
					if resp.Code != http.StatusOK {
						results <- fmt.Errorf("merge save %d: HTTP %d %s", i, resp.Code, resp.Body.String())
						return
					}
				}
			}()
			wg.Wait()
			close(results)
			for err := range results { t.Error(err) }
			if t.Failed() { return }
			reloaded, err := config.Load(h.configPath)
			if err != nil { t.Fatal(err) }
			if reloaded.Merge.BatchLimit != count {
				t.Fatalf("merge setting lost in concurrent save: %d", reloaded.Merge.BatchLimit)
			}
			if len(reloaded.RTMP.StreamKeys) != count+1 {
				t.Fatalf("RTMP keys lost in concurrent config snapshot: got %d want %d",len(reloaded.RTMP.StreamKeys),count+1)
			}
			for i := 0; i < count; i++ {
				id := fmt.Sprintf("uploaded-%02d",i)
				if reloaded.RTMP.StreamKeys[id] == "" { t.Errorf("lost stream key %s",id) }
			}
		})
	}
}
