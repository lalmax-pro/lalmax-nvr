package transcode

import (
    "context"
    "os"
    "testing"
)

// Real local media probes guard hardware- and plugin-specific templates. CI
// requires these tests only on the dedicated Issue #66 media runner.
func TestFFmpegRealG711ASelfTest(t *testing.T) {
    if os.Getenv("NVR_TRANSCODE_MEDIA_TEST")!="1" {t.Skip("requires installed FFmpeg")}
    p:=testProfile(t)
    p.Audio=AudioSpec{Codec:"g711a",SampleRate:8000,Channels:1,BitrateKbps:64}
    report,err:=ProbeFFmpeg(context.Background(),(Config{}).Tools(),p,CommandProbeRunner{})
    if err!=nil{t.Fatal(err)}
    if !report.CanEnable{t.Fatalf("H264 + G711A FLV encode/decode failed: %+v",report.Checks)}
    if err:=p.CheckMedia("h265","");err==nil{t.Fatal("accepted video-only input for required WebRTC audio")}
}
func TestGStreamerRealG711ASelfTest(t *testing.T) {
    if os.Getenv("NVR_GSTREAMER_SELFTEST")!="1" {t.Skip("requires installed GStreamer")}
    p:=DefaultProfile("gstreamer")
    p.Audio=AudioSpec{Codec:"g711a",SampleRate:8000,Channels:1,BitrateKbps:64}
    report,err:=ProbeGStreamer(context.Background(),(Config{}).Tools(),p,CommandProbeRunner{})
    if err!=nil{t.Fatal(err)}
    if !report.CanEnable{t.Fatalf("GStreamer H264 + G711A FLV encode/decode failed: %+v",report.Checks)}
}
