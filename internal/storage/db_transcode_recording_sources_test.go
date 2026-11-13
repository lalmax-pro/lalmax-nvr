package storage

import (
    "context"
    "path/filepath"
    "testing"
)

func TestDerivedRecordingSourceIsImmutableAndIdempotent(t *testing.T) {
    db,err:=New(filepath.Join(t.TempDir(),"provenance.db"))
    if err!=nil {t.Fatal(err)}
    defer db.Close()
    ctx:=context.Background()
    if err:=db.Init(ctx);err!=nil {t.Fatal(err)}
    row:=DerivedRecordingSource{
        PhysicalStreamID:"nvr_tc_unique1",SourceStreamID:"door",
        JobID:"job720",Generation:3,ProfileID:"h264",ProfileRevision:2,
        VideoCodec:"h264",AudioCodec:"pcm_alaw",
    }
    if err:=db.SaveDerivedRecordingSource(ctx,row);err!=nil{t.Fatal(err)}
    if err:=db.SaveDerivedRecordingSource(ctx,row);err!=nil{t.Fatalf("retry should be idempotent: %v",err)}
    found,err:=db.GetDerivedRecordingSource(ctx,row.PhysicalStreamID)
    if err!=nil{t.Fatal(err)}
    if found.JobID!=row.JobID || found.VideoCodec!="h264" || found.AudioCodec!="pcm_alaw" {
        t.Fatalf("provenance not preserved: %+v",found)
    }
    row.JobID="different-job"
    if err:=db.SaveDerivedRecordingSource(ctx,row);err==nil {
        t.Fatal("physical stream identity was rebound to a different job")
    }
}
