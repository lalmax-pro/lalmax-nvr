//go:build !windows

package transcode

import (
    "bytes"
    "encoding/json"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

// TestWorkerOneShotHelper provides the exact one-shot protocol runner in a
// separate process without relying on the NVR server executable.
func TestWorkerOneShotHelper(t *testing.T) {
    if os.Getenv("NVR_TEST_WORKER_HELPER")!="1" {return}
    if err:=RunIsolatedWorker(os.Stdin);err!=nil {os.Exit(79)}
    os.Exit(0)
}

func TestWorkerPrivatePipeEOFReapsBackend(t *testing.T) {
    dir:=t.TempDir()
    marker:=filepath.Join(dir,"backend-started")
    tool:=filepath.Join(dir,"fake-ffmpeg")
    script:=fmt.Sprintf("#!/bin/sh\necho ready > %q\nexec sleep 30\n",marker)
    if err:=os.WriteFile(tool,[]byte(script),0700);err!=nil{t.Fatal(err)}
    cmd,key:=workerTestCommand(t)
    cmd.BackendPath=tool
    frame,err:=encodeWorkerEnvelope(cmd,key)
    if err!=nil{t.Fatal(err)}
    var env workerEnvelope
    if err:=json.Unmarshal(bytes.TrimSpace(frame),&env);err!=nil{t.Fatal(err)}
    if _,err:=authenticateWorkerCommand(env.Command,env.MAC,key,time.Now());err!=nil{t.Fatal(err)}

    child:=exec.Command(os.Args[0],"-test.run=^TestWorkerOneShotHelper$")
    child.Env=append(allowedWorkerEnvironment(key),"NVR_TEST_WORKER_HELPER=1")
    stdin,err:=child.StdinPipe()
    if err!=nil{t.Fatal(err)}
    if err:=child.Start();err!=nil{t.Fatal(err)}
    t.Cleanup(func(){_ = stdin.Close();_ = child.Process.Kill();_,_=child.Process.Wait()})
    if _,err:=stdin.Write(frame);err!=nil{t.Fatal(err)}
    deadline:=time.After(5*time.Second)
    for {
        if _,err:=os.Stat(marker);err==nil {break}
        select {
        case <-deadline:t.Fatal("worker did not start validated backend")
        case <-time.After(20*time.Millisecond):
        }
    }
    // Closing the only parent-owned write end must stop the media process and
    // let the worker exit, even without a protocol STOP command.
    if err:=stdin.Close();err!=nil{t.Fatal(err)}
    done:=make(chan error,1)
    go func(){done<-child.Wait()}()
    select {
    case err:=<-done:
        if err!=nil && !strings.Contains(err.Error(),"exit status") {t.Fatalf("worker error: %v",err)}
        // Expected normal exit if backend cancellation is clean.
        if err!=nil {t.Fatalf("worker did not shut down cleanly on EOF: %v",err)}
    case <-time.After(6*time.Second):
        _=child.Process.Kill()
        t.Fatal("worker stayed alive after parent control pipe closed")
    }
}
