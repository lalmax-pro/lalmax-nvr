package httpflv

import (
    "bytes"
    "io"
    "net"
    "testing"
    "time"

    "github.com/q191201771/lal/pkg/base"
)

func TestFLVHeaderTrackFlags(t *testing.T) {
    cases := []struct {
        name string
        audio bool
        video bool
        want byte
    }{
        {"av",true,true,0x05},
        {"video",false,true,0x01},
        {"audio",true,false,0x04},
        {"unknown",false,false,0x00},
    }
    before := append([]byte(nil),FlvHeader...)
    for _,tt := range cases {
        t.Run(tt.name,func(t *testing.T) {
            hdr:=FlvHeaderWithTracks(tt.audio,tt.video)
            if len(hdr)!=13 || string(hdr[:4])!="FLV\x01" || hdr[4]!=tt.want ||
                !bytes.Equal(hdr[5:],before[5:]) {
                t.Fatalf("bad FLV header: %x expected flag=%x",hdr,tt.want)
            }
            hdr[4]=0xff
            if !bytes.Equal(FlvHeader,before) {
                t.Fatal("mutated global FLV header shared by other streams")
            }
        })
    }
}

// A client attaching before stream discovery must receive exactly one header,
// not a speculative A/V header followed by a second corrective header.
func TestFLVSubSessionDelayedHeaderWrittenOnce(t *testing.T) {
    server,client:=net.Pipe()
    defer server.Close()
    defer client.Close()
    session:=NewSubSession(server,base.UrlContext{},false,"")
    defer session.Dispose()
    session.WriteFlvHeaderWithTracks(false,false)
    if session.FlvHeaderWritten(){t.Fatal("header was written with unknown tracks")}
    done:=make(chan struct{})
    go func(){
        session.WriteFlvHeaderWithTracks(false,true)
        session.WriteFlvHeaderWithTracks(true,true)
        close(done)
    }()
    _=client.SetReadDeadline(time.Now().Add(3*time.Second))
    buf:=make([]byte,13)
    if _,err:=io.ReadFull(client,buf);err!=nil {t.Fatal(err)}
    if !session.FlvHeaderWritten() || buf[4]!=0x01 {
        t.Fatalf("wrong delayed video-only header: %x",buf)
    }
    select{case <-done:case <-time.After(3*time.Second):t.Fatal("writer stuck")}
    _=client.SetReadDeadline(time.Now().Add(30*time.Millisecond))
    extra:=make([]byte,13)
    if n,err:=client.Read(extra);err==nil || n!=0 {
        t.Fatalf("duplicate FLV header emitted after track changed: n=%d err=%v bytes=%x",n,err,extra[:n])
    }
}
