package api

import (
 "os"
 "path/filepath"
 "strings"
 "testing"
 "strconv"
 "sync"

 "github.com/lalmax-pro/lalmax-nvr/internal/ban"
 "github.com/lalmax-pro/lalmax-nvr/internal/config"
)

func TestLocalVideoDirectoryAndSafeRemoval(t *testing.T) {
 root:=t.TempDir()
 h:=&Handler{config:&config.Config{}}
 h.config.Storage.RootDir=root
 dir,err:=h.localVideoDirectory()
 if err!=nil {t.Fatal(err)}
 inside:=filepath.Join(dir,"video.mp4")
 if err:=os.WriteFile(inside,[]byte("video"),0600);err!=nil {t.Fatal(err)}
 outside:=filepath.Join(root,"keep.mp4")
 if err:=os.WriteFile(outside,[]byte("keep"),0600);err!=nil {t.Fatal(err)}
 h.removeLocalVideoFile(outside)
 if _,err:=os.Stat(outside);err!=nil {t.Fatalf("removed file outside video directory: %v",err)}
 h.removeLocalVideoFile(inside)
 if _,err:=os.Stat(inside);!os.IsNotExist(err) {t.Fatalf("managed video not removed: %v",err)}
}

func TestLocalVideoRTMPRequiresEnabledIngestAndAuthToken(t *testing.T) {
 enabled:=true
 h:=&Handler{config:&config.Config{}}
 if _,err:=h.localVideoRTMP("demo");err==nil {t.Fatal("disabled RTMP accepted")}
 h.config.RTMP.Enabled=&enabled
 h.config.RTMP.Port=11935
 if _,err:=h.localVideoRTMP("demo");err==nil {t.Fatal("missing token accepted")}
 h.config.RTMP.StreamKeys=map[string]string{"demo":"secret"}
 got,err:=h.localVideoRTMP("demo")
 if err!=nil {t.Fatal(err)}
 if !strings.Contains(got,"rtmp://127.0.0.1:11935/live/demo") || !strings.Contains(got,"stream_key=secret") {
  t.Fatalf("unexpected RTMP target %q",got)
 }
 noAuth:=false
 h.config.RTMP.PublishAuthEnabled=&noAuth
 got,err=h.localVideoRTMP("demo")
 if err!=nil || strings.Contains(got,"stream_key") {t.Fatalf("anonymous RTMP target %q, %v",got,err)}
}

func TestLocalVideoPublishKeyConcurrentReadAndProvision(t *testing.T) {
    enabled := true
    h := &Handler{config: &config.Config{}, configPath: filepath.Join(t.TempDir(), "nvr.yaml"), banMgr: ban.NewManager(nil,nil)}
    h.config.RTMP.Enabled = &enabled
    h.config.RTMP.StreamKeys = map[string]string{"existing":"fixture-key"}
    var workers sync.WaitGroup
    done := make(chan struct{})
    workers.Add(1)
    go func() {
        defer workers.Done()
        for {
            select {case <-done: return; default:}
            _, _ = h.localVideoRTMP("existing")
        }
    }()
    for i:=0; i<8; i++ {
        if err:=h.ensureLocalVideoPublishKey("new-"+strconv.Itoa(i));err!=nil {
            close(done)
            workers.Wait()
            t.Fatal(err)
        }
    }
    close(done)
    workers.Wait()
}
