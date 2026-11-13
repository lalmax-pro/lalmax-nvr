package api

import (
    "testing"

    "github.com/lalmax-pro/lalmax-nvr/internal/storage"
    "github.com/lalmax-pro/lalmax-nvr/internal/transcode"
)

func TestHAPreloadRequiresExplicitMatchingEnabledPublication(t *testing.T) {
    jobs := []transcode.Job{
        {ID:"safe",Source:transcode.SourceRef{StreamID:"camera-A"},Enabled:true,RunPolicy:transcode.OnDemand,Generation:7},
        {ID:"manual",Source:transcode.SourceRef{StreamID:"camera-A"},Enabled:true,RunPolicy:transcode.Manual,Generation:1},
        {ID:"stopped",Source:transcode.SourceRef{StreamID:"camera-A"},Enabled:true,RunPolicy:transcode.OnDemand,StoppedByUser:true,Generation:1},
        {ID:"disabled",Source:transcode.SourceRef{StreamID:"camera-A"},Enabled:false,RunPolicy:transcode.OnDemand,Generation:1},
    }
    cfg := storage.HomeAssistantSettings{Enabled:true,Streams:[]storage.HomeAssistantPublication{
        {StreamID:"camera-A",TranscodeJobID:"safe",PreloadTranscode:true},
        {StreamID:"camera-B",TranscodeJobID:"safe",PreloadTranscode:true},
        {StreamID:"camera-A",TranscodeJobID:"manual",PreloadTranscode:true},
        {StreamID:"camera-A",TranscodeJobID:"stopped",PreloadTranscode:true},
        {StreamID:"camera-A",TranscodeJobID:"disabled",PreloadTranscode:true},
    }}
    got := desiredHAPreloadJobs(cfg,jobs)
    if len(got)!=1 || got["safe"]!=7 {t.Fatalf("unsafe HA preload plan: %+v",got)}
    cfg.Enabled=false
    if got:=desiredHAPreloadJobs(cfg,jobs);len(got)!=0 {t.Fatalf("HA globally disabled, yet preload scheduled: %+v",got)}
    cfg.Enabled=true
    cfg.Streams[0].PreloadTranscode=false
    if got:=desiredHAPreloadJobs(cfg,jobs);len(got)!=0 {t.Fatalf("unselected preload scheduled: %+v",got)}
}
