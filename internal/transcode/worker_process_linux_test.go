//go:build linux

package transcode

import (
    "bytes"
    "encoding/json"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strconv"
    "strings"
    "syscall"
    "testing"
    "time"
)

// TestWorkerCrashKillsBackend ensures SIGKILL of the worker does not leave a
// media process with inherited RTSP/RTMP bearer credentials.
func TestWorkerCrashKillsBackend(t *testing.T) {
    dir:=t.TempDir()
    pidfile:=filepath.Join(dir,"backend.pid")
    tool:=filepath.Join(dir,"media-backend")
    script:=fmt.Sprintf("#!/bin/sh\necho $$ > %q\nexec sleep 30\n",pidfile)
    if err:=os.WriteFile(tool,[]byte(script),0700);err!=nil{t.Fatal(err)}
    command,key:=workerTestCommand(t)
    command.BackendPath=tool
    frame,err:=encodeWorkerEnvelope(command,key)
    if err!=nil{t.Fatal(err)}
    var envelope workerEnvelope
    if err:=json.Unmarshal(bytes.TrimSpace(frame),&envelope);err!=nil{t.Fatal(err)}

    worker:=exec.Command(os.Args[0],"-test.run=^TestWorkerOneShotHelper$")
    worker.Env=append(allowedWorkerEnvironment(key),"NVR_TEST_WORKER_HELPER=1")
    stdin,err:=worker.StdinPipe()
    if err!=nil{t.Fatal(err)}
    if err:=worker.Start();err!=nil{t.Fatal(err)}
    t.Cleanup(func(){_ = stdin.Close();_ = worker.Process.Kill()})
    if _,err:=stdin.Write(frame);err!=nil{t.Fatal(err)}
    var pid int
    deadline:=time.Now().Add(5*time.Second)
    for time.Now().Before(deadline) {
        bytes,readErr:=os.ReadFile(pidfile)
        if readErr==nil {pid,_=strconv.Atoi(strings.TrimSpace(string(bytes)));break}
        time.Sleep(20*time.Millisecond)
    }
    if pid<=1 {t.Fatal("worker backend never started")}
    defer func(){_=syscall.Kill(pid,syscall.SIGKILL)}()

    if err:=worker.Process.Kill();err!=nil{t.Fatal(err)}
    _=worker.Wait()

    deadline=time.Now().Add(5*time.Second)
    for time.Now().Before(deadline) {
        status,err:=os.ReadFile(fmt.Sprintf("/proc/%d/status",pid))
        if os.IsNotExist(err) {return}
        if err==nil && bytes.Contains(status,[]byte("State:\tZ")) {return}
        time.Sleep(20*time.Millisecond)
    }
    t.Fatal("worker crash left an orphaned live media process")
}
