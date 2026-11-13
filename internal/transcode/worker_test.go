package transcode

import (
    "bytes"
    "crypto/rand"
    "encoding/json"
    "os"
    "strings"
    "testing"
    "time"
)

func workerTestCommand(t *testing.T) (workerCommand,[]byte) {
    t.Helper()
    key:=make([]byte,32)
    if _,err:=rand.Read(key);err!=nil{t.Fatal(err)}
    spec:=testSpec(t)
    return workerCommand{
        Version:workerProtocolVersion,IssuedAt:time.Now().Unix(),
        Nonce:"0123456789abcdef0123456789abcdef", BackendPath:"ffmpeg",
        Spec:spec,
    },key
}

func TestIsolatedWorkerOneShotAuthorization(t *testing.T) {
    cmd,key:=workerTestCommand(t)
    frame,err:=encodeWorkerEnvelope(cmd,key)
    if err!=nil{t.Fatal(err)}
    var env workerEnvelope
    if err:=json.Unmarshal(bytes.TrimSpace(frame),&env);err!=nil{t.Fatal(err)}
    verified,err:=authenticateWorkerCommand(env.Command,env.MAC,key,time.Now())
    if err!=nil || verified.Nonce!=cmd.Nonce {t.Fatalf("valid worker message rejected: %v",err)}
    var altered workerCommand
    if err:=json.Unmarshal(env.Command,&altered);err!=nil{t.Fatal(err)}
    altered.Spec.OutputURL="rtmp://127.0.0.1:1935/live/different"
    tampered,_:=json.Marshal(altered)
    if _,err=authenticateWorkerCommand(tampered,env.MAC,key,time.Now());err==nil {
        t.Fatal("worker accepted tampered media endpoint")
    }
    env.MAC=strings.Repeat("0",64)
    if _,err=authenticateWorkerCommand(env.Command,env.MAC,key,time.Now());err==nil {
        t.Fatal("worker accepted forged signature")
    }
    cmd.IssuedAt=time.Now().Add(-time.Hour).Unix()
    old,_:=json.Marshal(cmd)
    if _,err=authenticateWorkerCommand(old,signWorkerTest(t,old,key),key,time.Now());err==nil {
        t.Fatal("worker accepted stale replay")
    }
    cmd.IssuedAt=time.Now().Unix()
    cmd.Spec.InputURL="file:///etc/passwd"
    unsafe,_:=json.Marshal(cmd)
    if _,err=authenticateWorkerCommand(unsafe,signWorkerTest(t,unsafe,key),key,time.Now());err==nil {
        t.Fatal("worker accepted unsafe input URI")
    }
}

func signWorkerTest(t *testing.T, data,key []byte) string {
    t.Helper()
    // Reuse the production frame encoder to avoid duplicating HMAC code.
    var command workerCommand
    if err:=json.Unmarshal(data,&command);err!=nil{t.Fatal(err)}
    frame,err:=encodeWorkerEnvelope(command,key)
    if err!=nil{t.Fatal(err)}
    var env workerEnvelope
    if err:=json.Unmarshal(bytes.TrimSpace(frame),&env);err!=nil{t.Fatal(err)}
    return env.MAC
}

func TestWorkerEnvironmentDoesNotInheritSecrets(t *testing.T) {
    t.Setenv("NVR_PASSWORD","never-expose-this")
    t.Setenv("DATABASE_DSN","never-expose-this-either")
    key:=bytes.Repeat([]byte{0x29},32)
    env:=allowedWorkerEnvironment(key)
    for _,s:=range env {
        if strings.Contains(s,"never-expose") || strings.HasPrefix(s,"NVR_PASSWORD=") || strings.HasPrefix(s,"DATABASE_DSN=") {
            t.Fatalf("inherited sensitive variable: %s",strings.SplitN(s,"=",2)[0])
        }
    }
    if len(env)==0 {t.Fatal("worker has no one-shot authorization")}
}

func TestWorkerRejectsMissingAuthenticationWithoutSpawning(t *testing.T) {
    old,had:=os.LookupEnv(workerKeyEnv)
    if had {defer os.Setenv(workerKeyEnv,old)} else {defer os.Unsetenv(workerKeyEnv)}
    if err:=os.Unsetenv(workerKeyEnv);err!=nil{t.Fatal(err)}
    if err:=RunIsolatedWorker(strings.NewReader("{}\n"));err==nil {
        t.Fatal("unauthenticated worker executed request")
    }
}

func TestWorkerRejectsOversizedControlFrame(t *testing.T) {
    t.Setenv(workerKeyEnv,strings.Repeat("4",64))
    oversized:=strings.Repeat("A",workerFrameLimit+512)+"\n"
    if err:=RunIsolatedWorker(strings.NewReader(oversized));err==nil {
        t.Fatal("oversized worker control frame accepted")
    }
}
