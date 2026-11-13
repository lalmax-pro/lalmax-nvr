package api

import (
 "context"
 "path/filepath"
 "strconv"
 "sync"
 "testing"

 "github.com/lalmax-pro/lalmax-nvr/internal/ban"
 "github.com/lalmax-pro/lalmax-nvr/internal/config"
)

// The stream-listing URL path must synchronize with token provisioning.
func TestBuildIngestURLsConcurrentWithLocalVideoTokenProvision(t *testing.T) {
 enabled:=true
 h:=&Handler{config:&config.Config{},configPath:filepath.Join(t.TempDir(),"nvr.yaml"),banMgr:ban.NewManager(nil,nil)}
 h.config.RTMP.Enabled=&enabled
 h.config.RTMP.StreamKeys=map[string]string{"existing":"fixture-key"}
 var wg sync.WaitGroup
 done:=make(chan struct{})
 wg.Add(1)
 go func(){
  defer wg.Done()
  for {
   select{case <-done:return;default:}
   _=h.buildIngestURLs(context.Background(),"existing","live")
  }
 }()
 for i:=0;i<8;i++ {
  if err:=h.ensureLocalVideoPublishKey("new-"+strconv.Itoa(i));err!=nil {
   close(done);wg.Wait();t.Fatal(err)
  }
 }
 close(done)
 wg.Wait()
}
