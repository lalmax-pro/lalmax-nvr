package transcode

import (
    "bufio"
    "context"
    "crypto/hmac"
    "crypto/rand"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "os"
    "os/exec"
    "strings"
    "time"
)

// IsolatedWorkerArg is an internal, one-shot subprocess mode, not a public
// service. No TCP listener, HTTP route, shell or arbitrary pipeline is exposed.
const IsolatedWorkerArg = "__nvr_transcode_worker_v1"
const workerKeyEnv = "NVR_TRANSCODE_WORKER_ONESHOT_KEY"
const workerProtocolVersion = 1
const workerFrameLimit = 64 << 10

type workerCommand struct {
    Version int `json:"version"`
    IssuedAt int64 `json:"issued_at"`
    Nonce string `json:"nonce"`
    BackendPath string `json:"backend_path"`
    Spec JobSpec `json:"spec"`
    Width int `json:"width"`
    Height int `json:"height"`
}

type workerEnvelope struct {
    Command json.RawMessage `json:"command"`
    MAC string `json:"mac"`
}

func authenticateWorkerCommand(data []byte, mac string, key []byte, now time.Time) (workerCommand, error) {
    var command workerCommand
    if len(key)!=32 || len(data)==0 || len(data)>workerFrameLimit {
        return command, errors.New("invalid worker protocol")
    }
    received,err:=hex.DecodeString(mac)
    if err!=nil || len(received)!=sha256.Size {
        return command,errors.New("invalid worker authorization")
    }
    digest:=hmac.New(sha256.New,key)
    _,_=digest.Write(data)
    if !hmac.Equal(digest.Sum(nil),received) {
        return command,errors.New("invalid worker authorization")
    }
    decoder:=json.NewDecoder(strings.NewReader(string(data)))
    decoder.DisallowUnknownFields()
    if err:=decoder.Decode(&command);err!=nil {return command,errors.New("invalid worker request")}
    if err:=decoder.Decode(new(any));err!=io.EOF {return command,errors.New("invalid worker framing")}
    if command.Version!=workerProtocolVersion || command.Nonce=="" ||
        len(command.Nonce)!=32 || command.IssuedAt<now.Add(-30*time.Second).Unix() ||
        command.IssuedAt>now.Add(30*time.Second).Unix() {
        return command,errors.New("expired or invalid worker request")
    }
    if command.BackendPath=="" || strings.ContainsAny(command.BackendPath,"\x00\r\n") {
        return command,errors.New("invalid worker executable")
    }
    if err:=command.Spec.validate(command.Spec.Profile.Backend);err!=nil {
        return command,errors.New("invalid worker media specification")
    }
    switch command.Spec.Profile.Backend {
    case "ffmpeg":
        if _,err:=FFmpegArgs(command.Spec);err!=nil {return command,errors.New("unsupported worker FFmpeg template")}
    case "gstreamer":
        if _,err:=GStreamerArgs(command.Spec,command.Width,command.Height);err!=nil {
            return command,errors.New("unsupported worker GStreamer template")
        }
    default:
        return command,errors.New("unsupported worker backend")
    }
    return command,nil
}

func encodeWorkerEnvelope(cmd workerCommand,key []byte)([]byte,error) {
    payload,err:=json.Marshal(cmd)
    if err!=nil{return nil,err}
    digest:=hmac.New(sha256.New,key)
    _,_=digest.Write(payload)
    frame,err:=json.Marshal(workerEnvelope{Command:payload,MAC:hex.EncodeToString(digest.Sum(nil))})
    if err!=nil{return nil,err}
    frame=append(frame,'\n')
    if len(frame)>workerFrameLimit{return nil,errors.New("worker frame exceeds limit")}
    return frame,nil
}

func allowedWorkerEnvironment(key []byte) []string {
    // Do not inherit NVR_PASSWORD, database DSNs, API tokens or other secrets.
    allowed:=[]string{"PATH","HOME","TMPDIR","LD_LIBRARY_PATH","GST_PLUGIN_PATH","GST_PLUGIN_SYSTEM_PATH","CUDA_VISIBLE_DEVICES","CUDA_DEVICE_ORDER","SYSTEMROOT","SystemRoot","WINDIR","TEMP","TMP"}
    env:=make([]string,0,len(allowed)+1)
    for _,name:=range allowed {
        if value,ok:=os.LookupEnv(name);ok {env=append(env,name+"="+value)}
    }
    return append(env,workerKeyEnv+"="+hex.EncodeToString(key))
}

type isolatedWorkerProcess struct {
    process *commandProcess
    stdin io.WriteCloser
}
func(p *isolatedWorkerProcess) Stop(ctx context.Context) error {
    // EOF tells the worker to cancel FFmpeg/GStreamer first. Do not SIGTERM
    // the worker until its descendant process has had time to exit.
    _=p.stdin.Close()
    grace:=time.NewTimer(4*time.Second)
    defer grace.Stop()
    select {
    case <-p.process.done: return nil
    case <-ctx.Done():
        return p.process.Stop(ctx)
    case <-grace.C:
        return p.process.Stop(ctx)
    }
}
func(p *isolatedWorkerProcess) Wait() error {
    err:=p.process.Wait()
    _=p.stdin.Close()
    return err
}

// StartIsolatedWorker owns a separate, supervised worker process. The media
// URLs and one-shot authentication token are delivered via anonymous stdin
// and environment, never through CLI arguments, logs or an HTTP endpoint.
func StartIsolatedWorker(ctx context.Context, tools ToolPaths, spec JobSpec, width,height int)(Process,error) {
    if ctx.Err()!=nil{return nil, &Error{Code:"process_start_canceled",Message:"worker start canceled"}}
    if err:=spec.validate(spec.Profile.Backend);err!=nil{return nil,err}
    var tool string
    switch spec.Profile.Backend {
    case "ffmpeg": tool=tools.FFmpeg
    case "gstreamer": tool=tools.GSTLaunch
    default: return nil,invalid("unsupported worker backend")
    }
    // Probe paths are configured by the server, not supplied by the API.
    if _,err:=exec.LookPath(tool);err!=nil{return nil,&Error{Code:"tool_unavailable",Message:"worker backend is not executable"}}
    self,err:=os.Executable()
    if err!=nil{return nil,&Error{Code:"worker_unavailable",Message:"worker executable unavailable"}}
    key:=make([]byte,32)
    if _,err=rand.Read(key);err!=nil{return nil,&Error{Code:"worker_unavailable",Message:"worker authorization unavailable"}}
    nonce:=make([]byte,16)
    if _,err=rand.Read(nonce);err!=nil{return nil,&Error{Code:"worker_unavailable",Message:"worker nonce unavailable"}}
    frame,err:=encodeWorkerEnvelope(workerCommand{
        Version:workerProtocolVersion,IssuedAt:time.Now().Unix(),Nonce:hex.EncodeToString(nonce),
        BackendPath:tool,Spec:spec,Width:width,Height:height,
    },key)
    if err!=nil{return nil,&Error{Code:"worker_unavailable",Message:"could not serialize worker request"}}
    cmd:=exec.Command(self,IsolatedWorkerArg)
    cmd.Env=allowedWorkerEnvironment(key)
    cmd.Stdout=io.Discard
    cmd.Stderr=io.Discard
    stdin,err:=cmd.StdinPipe()
    if err!=nil{return nil,&Error{Code:"worker_unavailable",Message:"worker control pipe unavailable"}}
    configureProcess(cmd)
    // exec.Command (unlike CommandContext) forbids a non-nil Cancel callback.
    // Lifecycle cancellation is supervised through the private stdin pipe.
    cmd.Cancel=nil
    cmd.WaitDelay=2*time.Second
    owner,err:=startOwnedProcess(cmd)
    if err!=nil{_ = stdin.Close();return nil,&Error{Code:"worker_unavailable",Message:"could not start isolated worker"}}
    p:=&commandProcess{cmd:cmd,owner:owner,done:make(chan struct{})}
    go func(){
        if err:=cmd.Wait();err!=nil {
            p.err=&Error{Code:"worker_failed",Message:"isolated media worker exited unsuccessfully"}
        }
        owner.close()
        close(p.done)
    }()
    go func(){
        if _,err:=stdin.Write(frame);err!=nil { _=stdin.Close() }
    }()
    isolated:=&isolatedWorkerProcess{process:p,stdin:stdin}
    go func(){
        select {
        case <-ctx.Done():
            stopCtx,cancel:=context.WithTimeout(context.Background(),5*time.Second)
            _=isolated.Stop(stopCtx)
            cancel()
        case <-p.done:
            _=stdin.Close()
        }
    }()
    return isolated,nil
}

// RunIsolatedWorker is invoked only by the internal CLI mode. The isolated
// process executes exactly one validated fixed media template. Closing the
// parent-owned pipe cancels the backend and reaps it (parent death included).
func RunIsolatedWorker(input io.Reader) error {
    raw:=os.Getenv(workerKeyEnv)
    key,err:=hex.DecodeString(raw)
    // Never pass the one-shot authorization key to the FFmpeg/GStreamer child.
    _=os.Unsetenv(workerKeyEnv)
    if err!=nil || len(key)!=32{return errors.New("worker authorization not configured")}
    reader:=bufio.NewReader(io.LimitReader(input,workerFrameLimit+1))
    frame,err:=reader.ReadBytes('\n')
    if err!=nil || len(frame)>workerFrameLimit{return errors.New("invalid worker frame")}
    var envelope workerEnvelope
    decode:=json.NewDecoder(strings.NewReader(string(frame)))
    decode.DisallowUnknownFields()
    if err=decode.Decode(&envelope);err!=nil{return errors.New("invalid worker envelope")}
    command,err:=authenticateWorkerCommand(envelope.Command,envelope.MAC,key,time.Now())
    if err!=nil{return err}
    ctx,cancel:=context.WithCancel(context.Background())
    defer cancel()
    // No more commands may be submitted. Reading to EOF is a heartbeat:
    // disappearance of the supervisor immediately triggers cancellation.
    go func(){_,_=io.Copy(io.Discard,io.MultiReader(reader,input));cancel()}()
    var process Process
    switch command.Spec.Profile.Backend {
    case "ffmpeg":
        process,err=StartFFmpeg(ctx,command.BackendPath,command.Spec)
    case "gstreamer":
        process,err=StartGStreamer(ctx,command.BackendPath,command.Spec,command.Width,command.Height)
    default:
        return fmt.Errorf("unsupported worker backend")
    }
    if err!=nil{return errors.New("worker failed to launch media")}
    err=process.Wait()
    if ctx.Err()!=nil{return nil}
    if err!=nil{return errors.New("worker media failed")}
    return nil
}
