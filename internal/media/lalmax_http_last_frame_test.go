package media

import (
 "testing"
 "time"
)

// LastVideoFrameUnixSec is independent of the consumable FPS sample array.
func TestGroupToStreamInfoRetainsIndependentVideoTimestamp(t *testing.T) {
 sec:=time.Now().Unix()
 group:=groupPayload{
  StreamName:"manual-derived",VideoCodec:"H264",
  LastVideoFrameUnixSec:sec,
 }
 for i:=0;i<3;i++ {
  info:=groupToStreamInfo(group)
  if !info.Active {t.Fatal("recent customize video frame was not considered active")}
  if info.LastFrameTime.Unix()!=sec {t.Fatalf("video timestamp lost on read %d: %v",i,info.LastFrameTime)}
 }
 group.LastVideoFrameUnixSec=sec-60
 if info:=groupToStreamInfo(group);info.Active {t.Fatal("stale video incorrectly active")}
}
