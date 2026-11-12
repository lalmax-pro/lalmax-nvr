package api

import (
 "context"
 "crypto/rand"
 "encoding/hex"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "net/http"
 "net/url"
 "os"
 "os/exec"
 "path/filepath"
 "strings"
 "sync"
 "time"

 "github.com/lalmax-pro/lalmax-nvr/internal/config"
 "github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

const localVideoMaxBytes int64 = 2 << 30

type localVideoPlayers struct {
 authMu sync.Mutex
 mu sync.Mutex
 cancels map[string]context.CancelFunc
 done map[string]chan struct{}
}

func (h *Handler) localVideoDirectory() (string,error) {
 if h.config == nil || strings.TrimSpace(h.config.Storage.RootDir)=="" {
  return "",errors.New("storage root directory is not configured")
 }
 dir,absErr:=filepath.Abs(filepath.Join(h.config.Storage.RootDir,"local-videos"))
 if absErr!=nil {return "",absErr}
 if err:=os.MkdirAll(dir,0700);err!=nil{return "",err}
 return dir,nil
}

// ensureLocalVideoPublishKey provisions a cryptographically random RTMP key
// without weakening the existing publish authentication policy.
func (h *Handler) ensureLocalVideoPublishKey(streamID string) error {
 h.localVideos.authMu.Lock()
 defer h.localVideos.authMu.Unlock()
 if h.config==nil || !h.config.RTMP.RTMPPublishAuthEnabled() {return nil}
 if h.config.RTMP.StreamKeys[streamID]!="" {return nil}
 updater,ok:=h.banMgr.(interface{SetRTMPStreamKeys(map[string]string)})
 if !ok {return errors.New("runtime RTMP credential manager is unavailable")}
 key:=make([]byte,32)
 if _,err:=rand.Read(key);err!=nil{return err}
 previous:=h.config.RTMP.StreamKeys
 next:=make(map[string]string,len(previous)+1)
 for id,token:=range previous {next[id]=token}
 next[streamID]=hex.EncodeToString(key)
 h.config.RTMP.StreamKeys=next
 if err:=h.persistConfigLocked();err!=nil {
  h.config.RTMP.StreamKeys=previous
  return fmt.Errorf("persist local video publish token: %w",err)
 }
 updater.SetRTMPStreamKeys(next)
 return nil
}

func (h *Handler) revokeLocalVideoPublishKey(streamID string) {
 h.localVideos.authMu.Lock()
 defer h.localVideos.authMu.Unlock()
 if h.config==nil || h.config.RTMP.StreamKeys[streamID]=="" {return}
 updater,ok:=h.banMgr.(interface{SetRTMPStreamKeys(map[string]string)})
 if !ok {logger.Warn("unable to revoke local video RTMP token", "stream_id",streamID);return}
 previous:=h.config.RTMP.StreamKeys
 next:=make(map[string]string,len(previous))
 for id,token:=range previous {if id!=streamID {next[id]=token}}
 h.config.RTMP.StreamKeys=next
 if err:=h.persistConfigLocked();err!=nil {
  h.config.RTMP.StreamKeys=previous
  logger.Warn("failed to persist RTMP token revocation", "stream_id",streamID,"err",err)
  return
 }
 updater.SetRTMPStreamKeys(next)
}

func (h *Handler) localVideoRTMP(streamID string) (string,error) {
 // StreamKeys map header changes during provisioning/revocation. Match the
 // writer's mutex for the whole read, not only the token lookup.
 h.localVideos.authMu.Lock()
 defer h.localVideos.authMu.Unlock()
 if h.config==nil || h.config.RTMP.Enabled==nil || !*h.config.RTMP.Enabled {
  return "",errors.New("RTMP ingest must be enabled to loop local video")
 }
 port:=h.config.RTMP.Port
 if port==0 {port=config.DefaultLalRTMPPort}
 if port<1 || port>65535 {return "",errors.New("invalid RTMP port")}
 target:=fmt.Sprintf("rtmp://127.0.0.1:%d/live/%s",port,streamID)
 if h.config.RTMP.RTMPPublishAuthEnabled() {
  key:=h.config.RTMP.StreamKeys[streamID]
  if key=="" {return "",errors.New("RTMP publish token for this stream is not configured")}
  target+="?"+url.Values{"stream_key":{key}}.Encode()
 }
 return target,nil
}

type localVideoProbe struct {
 Streams []struct {
  CodecType string `json:"codec_type"`
  CodecName string `json:"codec_name"`
 } `json:"streams"`
}

// probeLocalVideo rejects invalid uploads and chooses stream-copy codecs where
// FLV/RTMP supports them, minimizing CPU consumption for H264/AAC source files.
func probeLocalVideo(file string) (videoCopy, audioCopy, hasAudio bool, err error) {
 if _,err=exec.LookPath("ffprobe");err!=nil{return false,false,false,errors.New("ffprobe is required alongside ffmpeg")}
 ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
 defer cancel()
 out,err:=exec.CommandContext(ctx,"ffprobe","-v","error","-show_entries","stream=codec_type,codec_name",
  "-of","json",file).Output()
 if err!=nil{return false,false,false,fmt.Errorf("invalid video file: %w",err)}
 var probe localVideoProbe
 if err=json.Unmarshal(out,&probe);err!=nil{return false,false,false,err}
 hasVideo:=false
 for _,stream:=range probe.Streams {
  if stream.CodecType=="video" && !hasVideo {
   hasVideo=true
   videoCopy=strings.EqualFold(stream.CodecName,"h264")
  }
  if stream.CodecType=="audio" && !hasAudio {
   hasAudio=true
   audioCopy=strings.EqualFold(stream.CodecName,"aac")
  }
 }
 if !hasVideo {return false,false,false,errors.New("file contains no video stream")}
 return videoCopy,audioCopy,hasAudio,nil
}

func (h *Handler) startLocalVideo(row storage.CreatedStream) error {
 if _,err:=exec.LookPath("ffmpeg");err!=nil{return fmt.Errorf("ffmpeg not installed: %w",err)}
 target,err:=h.localVideoRTMP(row.StreamID)
 if err!=nil{return err}
 dir,err:=h.localVideoDirectory()
 if err!=nil{return err}
 abs,err:=filepath.Abs(row.SourceURL)
 if err!=nil{return err}
 relative,err:=filepath.Rel(dir,abs)
 if err!=nil || relative==".." || strings.HasPrefix(relative,".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
  return errors.New("local video must be under the managed video directory")
 }
 if fi,err:=os.Stat(abs);err!=nil || !fi.Mode().IsRegular() {return errors.New("local video file is unavailable")}
 copyVideo,copyAudio,hasAudio,err:=probeLocalVideo(abs)
 if err!=nil{return err}
 h.stopLocalVideo(row.StreamID)
 ctx,cancel:=context.WithCancel(context.Background())
 h.localVideos.mu.Lock()
 if h.localVideos.cancels==nil {h.localVideos.cancels=make(map[string]context.CancelFunc)}
 h.localVideos.cancels[row.StreamID]=cancel
 if h.localVideos.done==nil {h.localVideos.done=make(map[string]chan struct{})}
 done:=make(chan struct{})
 h.localVideos.done[row.StreamID]=done
 h.localVideos.mu.Unlock()
 go func(){
  defer close(done)
  for ctx.Err()==nil {
   // Re-encode to H264/AAC for predictable FLV/RTMP playback.
   args:=[]string{"-hide_banner","-nostdin","-loglevel","error","-re","-stream_loop","-1","-i",abs}
   if copyVideo {args=append(args,"-c:v","copy")} else {
    args=append(args,"-c:v","libx264","-preset","veryfast","-tune","zerolatency","-pix_fmt","yuv420p")
   }
   if !hasAudio {args=append(args,"-an")} else if copyAudio {args=append(args,"-c:a","copy")} else {
    args=append(args,"-c:a","aac")
   }
   args=append(args,"-f","flv",target)
   cmd:=exec.CommandContext(ctx,"ffmpeg",args...)
   cmd.SysProcAttr=localVideoProcessAttr()
   cmd.Stdout=io.Discard
   cmd.Stderr=io.Discard
   if err:=cmd.Run();err!=nil && ctx.Err()==nil {
    logger.Warn("local video publisher exited, retrying", "stream_id",row.StreamID,"err",err)
   }
   select {case <-ctx.Done():return;case <-time.After(3*time.Second):}
  }
 }()
 return nil
}

func (h *Handler) removeLocalVideoFile(file string) {
 dir,err:=h.localVideoDirectory()
 if err!=nil{return}
 abs,err:=filepath.Abs(file)
 if err!=nil{return}
 rel,err:=filepath.Rel(dir,abs)
 if err!=nil || rel==".." || strings.HasPrefix(rel,".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {return}
 if err:=os.Remove(abs);err!=nil && !os.IsNotExist(err) {
  logger.Warn("failed to remove local video", "file",abs,"err",err)
 }
}

func (h *Handler) stopLocalVideo(streamID string){
 h.localVideos.mu.Lock()
 cancel:=h.localVideos.cancels[streamID]
 done:=h.localVideos.done[streamID]
 delete(h.localVideos.cancels,streamID)
 delete(h.localVideos.done,streamID)
 h.localVideos.mu.Unlock()
 if cancel!=nil {cancel()}
 if done!=nil {
  select {case <-done: case <-time.After(5*time.Second):
   logger.Warn("timed out waiting for local video publisher to exit", "stream_id",streamID)
  }
 }
}

// newLocalVideoUploadFile keeps the original extension for FFmpeg probing
// while isolating files from simultaneous requests for the same stream ID.
func newLocalVideoUploadFile(dir, id, ext string) (*os.File, error) {
 return os.CreateTemp(dir,id+"-upload-*"+ext)
}

// handleCreateLocalVideo accepts a browser-uploaded media file and creates a
// persistent stream slot. Uploaded bytes stay inside the configured storage root.
func (h *Handler) handleCreateLocalVideo(w http.ResponseWriter,r *http.Request){
 if h.mediaEngine==nil || h.db==nil {writeError(w,http.StatusServiceUnavailable,"stream management unavailable");return}
 if _,err:=exec.LookPath("ffmpeg");err!=nil {writeError(w,http.StatusServiceUnavailable,"ffmpeg is required");return}
 r.Body=http.MaxBytesReader(w,r.Body,localVideoMaxBytes+(16<<20))
 if err:=r.ParseMultipartForm(16<<20);err!=nil {writeError(w,http.StatusBadRequest,"invalid or oversized video upload");return}
 if r.MultipartForm!=nil {defer r.MultipartForm.RemoveAll()}
 id:=strings.TrimSpace(r.FormValue("stream_id"))
 if !pushStreamIDPattern.MatchString(id) {writeError(w,http.StatusBadRequest,"invalid stream_id");return}
 // Validate RTMP availability before accepting a potentially large upload.
 if h.config==nil || h.config.RTMP.Enabled==nil || !*h.config.RTMP.Enabled {
  writeError(w,http.StatusBadRequest,"RTMP ingest must be enabled for local video");return
 }
 existing,err:=h.db.GetCreatedStream(r.Context(),id)
 if err!=nil {writeError(w,http.StatusInternalServerError,"stream lookup failed");return}
 if existing!=nil {writeError(w,http.StatusConflict,"stream already exists");return}
 existingMedia,err:=h.mediaEngine.GetStream(r.Context(),id)
 if err!=nil {writeError(w,http.StatusInternalServerError,"stream lookup failed");return}
 if existingMedia!=nil {writeError(w,http.StatusConflict,"stream already active");return}
 cam,err:=h.db.GetCamera(r.Context(),id)
 if err!=nil {writeError(w,http.StatusInternalServerError,"camera lookup failed");return}
 if cam!=nil && !cam.Archived {writeError(w,http.StatusConflict,"stream id belongs to camera");return}
 binding,err:=h.db.GetStreamBinding(r.Context(),id)
 if err!=nil {writeError(w,http.StatusInternalServerError,"binding lookup failed");return}
 if binding!=nil {writeError(w,http.StatusConflict,"stream id is bound to camera");return}

 input,header,err:=r.FormFile("video")
 if err!=nil {writeError(w,http.StatusBadRequest,"video file is required");return}
 defer input.Close()
 ext:=strings.ToLower(filepath.Ext(header.Filename))
 switch ext {case ".mp4",".mkv",".mov",".avi",".webm",".ts":default:
  writeError(w,http.StatusBadRequest,"unsupported video file extension");return
 }
 dir,err:=h.localVideoDirectory()
 if err!=nil {writeError(w,http.StatusInternalServerError,"video storage unavailable");return}
 // Each request owns one final upload path. Never overwrite a competing
 // request's file merely because the stream ID and extension match.
 temp,err:=newLocalVideoUploadFile(dir,id,ext)
 if err!=nil {writeError(w,http.StatusInternalServerError,"cannot create video file");return}
 dest:=temp.Name()
 keepFile:=false
 defer func(){ if !keepFile { _=os.Remove(dest) } }()
 n,copyErr:=io.Copy(temp,io.LimitReader(input,localVideoMaxBytes+1))
 closeErr:=temp.Close()
 if copyErr!=nil || closeErr!=nil || n==0 || n>localVideoMaxBytes {
  writeError(w,http.StatusBadRequest,"invalid or oversized video file");return
 }
 // Upload bytes were written directly to this request's unique final path.
 if _,_,_,err=probeLocalVideo(dest);err!=nil {
  _=os.Remove(dest)
  writeError(w,http.StatusBadRequest,err.Error());return
 }
 if err:=h.ensureLocalVideoPublishKey(id);err!=nil {
  _=os.Remove(dest)
  writeError(w,http.StatusInternalServerError,err.Error());return
 }
 row:=storage.CreatedStream{StreamID:id,Name:normalizeStreamDisplayName(r.FormValue("name"),id),
  AppName:"live",InputMode:storage.CreatedStreamLocalVideo,SourceURL:dest}
 if err:=h.db.InsertCreatedStream(r.Context(),row);err!=nil {
  _=os.Remove(dest)
  if storage.IsUniqueViolation(err){writeError(w,http.StatusConflict,"stream already exists")}else{writeError(w,http.StatusInternalServerError,"failed to create stream")}
  return
 }
 if err:=h.startLocalVideo(row);err!=nil {
  _,_=h.db.DeleteCreatedStream(r.Context(),id)
  _=os.Remove(dest)
  writeError(w,http.StatusInternalServerError,err.Error())
  return
 }
 // Do not return a false success when FFmpeg cannot publish (for example,
 // because an external MediaServer rejects the stream or RTMP is unreachable).
 readyCtx,stopWaiting:=context.WithTimeout(r.Context(),15*time.Second)
 defer stopWaiting()
 ticker:=time.NewTicker(350*time.Millisecond)
 defer ticker.Stop()
 published:=false
 for !published {
  info,err:=h.mediaEngine.GetStream(readyCtx,id)
  if err==nil && info!=nil && info.Active {published=true;break}
  select {
  case <-readyCtx.Done():
   h.stopLocalVideo(id)
   _,_=h.db.DeleteCreatedStream(context.Background(),id)
   h.removeLocalVideoFile(dest)
   writeError(w,http.StatusBadGateway,"video saved but RTMP publication failed; check MediaServer and FFmpeg configuration")
   return
  case <-ticker.C:
  }
 }
 keepFile=true
 writeJSON(w,http.StatusCreated,h.summaryFromCreated(r.Context(),row))
}
