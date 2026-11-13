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

func TestSystemAndStreamingSettingsSaveConcurrentRTMPKeys(t *testing.T) {
	for _, which := range []string{"system", "streaming"} {
		t.Run(which,func(t *testing.T){
			enabled:=true
			h:=&Handler{
				config:&config.Config{},
				configPath:filepath.Join(t.TempDir(),"nvr.yaml"),
				banMgr:ban.NewManager(nil,nil),
			}
			h.config.RTMP.Enabled=&enabled
			h.config.RTMP.StreamKeys=map[string]string{"original":"original-token"}
			if err:=config.Save(h.configPath,h.config);err!=nil{t.Fatal(err)}
			watcher,err:=config.NewWatcher(h.config,h.configPath)
			if err!=nil{t.Fatal(err)}
			h.configWatcher=watcher
			var wg sync.WaitGroup
			results:=make(chan error,64)
			wg.Add(2)
			go func(){
				defer wg.Done()
				for i:=0;i<16;i++ {
					if err:=h.ensureLocalVideoPublishKey(fmt.Sprintf("stream-%02d",i));err!=nil {
						results<-err
						return
					}
				}
			}()
			go func(){
				defer wg.Done()
				for i:=0;i<16;i++ {
					var body string
					switch which {
					case "system":
						body=fmt.Sprintf(`{"cleanup":{"retention_days":%d}}`,i+7)
					case "streaming":
						body=fmt.Sprintf(`{"rtmp":{"port":%d}}`,24000+i)
					}
					req:=httptest.NewRequest(http.MethodPut,"/api/settings/"+which,strings.NewReader(body))
					resp:=httptest.NewRecorder()
					if which=="system" {h.handleUpdateSettings(resp,req)} else {h.handleUpdateStreamingSettings(resp,req)}
					if resp.Code!=http.StatusOK {
						results<-fmt.Errorf("%s config save %d returned %d: %s",which,i,resp.Code,resp.Body.String())
						return
					}
				}
			}()
			wg.Wait()
			close(results)
			for err:=range results {t.Error(err)}
			if t.Failed(){return}
			reloaded,err:=config.Load(h.configPath)
			if err!=nil{t.Fatal(err)}
			if len(reloaded.RTMP.StreamKeys)!=17 {
				t.Fatalf("lost RTMP token in shared config snapshot, keys=%d",len(reloaded.RTMP.StreamKeys))
			}
			if which=="system"&&reloaded.Cleanup.RetentionDays!=22{
				t.Fatalf("lost cleanup setting: %d",reloaded.Cleanup.RetentionDays)
			}
			if which=="streaming"&&reloaded.RTMP.Port!=24015{
				t.Fatalf("lost streaming setting: %d",reloaded.RTMP.Port)
			}
		})
	}
}
