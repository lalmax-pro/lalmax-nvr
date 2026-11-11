package base

import "testing"

// GetFpsFrom drains rolling FPS buckets, but last frame evidence must remain
// available to independent consumers (NVR status, readiness, dashboards).
func TestPeriodRecordLastVideoFrameSurvivesStatsRead(t *testing.T) {
 p:=NewPeriodRecord(8)
 p.Add(100,1)
 p.Add(101,2)
 var first,second StatGroup
 first.GetFpsFrom(&p,102)
 second.GetFpsFrom(&p,102)
 if first.LastVideoFrameUnixSec!=101 || second.LastVideoFrameUnixSec!=101 {
  t.Fatalf("consumed frame timestamp: first=%d second=%d",first.LastVideoFrameUnixSec,second.LastVideoFrameUnixSec)
 }
 if len(first.Fps)==0 || len(second.Fps)!=0 {t.Fatalf("expected FPS drain but repeatable frame timestamp: first=%v second=%v",first.Fps,second.Fps)}
 p.Clear()
 var afterClear StatGroup
 afterClear.GetFpsFrom(&p,102)
 if afterClear.LastVideoFrameUnixSec!=0 {t.Fatal("frame timestamp survived stream reset")}
}
