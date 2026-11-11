package logic

import (
    "testing"

    "github.com/q191201771/lal/pkg/base"
    "github.com/q191201771/lal/pkg/rtmp"
)

func TestHTTPFLVTracksReflectCurrentMedia(t *testing.T) {
    for _,tt:=range []struct{
        name,audio,video string
        msgType uint8
        wantAudio,wantVideo bool
    }{
        {"video-only","",base.VideoCodecAvc,0,false,true},
        {"audio-only",base.AudioCodecAac,"",0,true,false},
        {"audio-video",base.AudioCodecAac,base.VideoCodecAvc,0,true,true},
        {"unknown","", "",0,false,false},
        {"late-first-video","", "",base.RtmpTypeIdVideo,false,true},
        {"late-first-audio","", "",base.RtmpTypeIdAudio,true,false},
    }{
        t.Run(tt.name,func(t *testing.T){
            g:=&Group{stat:base.StatGroup{AudioCodec:tt.audio,VideoCodec:tt.video}}
            var msg *base.RtmpMsg
            if tt.msgType!=0 {msg=&base.RtmpMsg{Header:base.RtmpHeader{MsgTypeId:tt.msgType}}}
            a,v:=g.httpflvTracks(msg)
            if a!=tt.wantAudio || v!=tt.wantVideo {
                t.Fatalf("got audio=%t video=%t, want %t %t",a,v,tt.wantAudio,tt.wantVideo)
            }
        })
    }
}

func TestHTTPFLVMetadataAnnouncesDelayedTrack(t *testing.T) {
    metadata,err:=rtmp.BuildMetadata(-1,-1,10,7)
    if err!=nil {t.Fatal(err)}
    fields,err:=rtmp.ParseMetadata(metadata)
    if err!=nil {t.Fatal(err)}
    g:=&Group{}
    if a,v:=g.httpflvTracks(nil);a||v {t.Fatal("unknown tracks incorrectly assumed AV")}
    g.noteFlvTrackMetadata(fields)
    // Source sends its video first and audio later. Its metadata announced
    // both tracks, so even a client joining early receives a 0x05 header.
    msg:=&base.RtmpMsg{Header:base.RtmpHeader{MsgTypeId:base.RtmpTypeIdVideo}}
    if a,v:=g.httpflvTracks(msg);!a||!v {
        t.Fatalf("metadata-announced late audio missing: %t,%t",a,v)
    }
    // Pure video metadata must not imply a nonexistent audio track.
    metadata,err=rtmp.BuildMetadata(-1,-1,-1,7)
    if err!=nil {t.Fatal(err)}
    fields,err=rtmp.ParseMetadata(metadata)
    if err!=nil {t.Fatal(err)}
    h:=&Group{}
    h.noteFlvTrackMetadata(fields)
    if a,v:=h.httpflvTracks(nil);a||!v {
        t.Fatalf("false audio hint in pure video metadata: %t,%t",a,v)
    }
    // Audio-only metadata must not imply an absent video track.
    metadata,err=rtmp.BuildMetadata(-1,-1,10,-1)
    if err!=nil {t.Fatal(err)}
    fields,err=rtmp.ParseMetadata(metadata)
    if err!=nil {t.Fatal(err)}
    onlyAudio:=&Group{}
    onlyAudio.noteFlvTrackMetadata(fields)
    if a,v:=onlyAudio.httpflvTracks(nil);!a||v {
        t.Fatalf("false video hint in pure audio metadata: %t,%t",a,v)
    }
}
