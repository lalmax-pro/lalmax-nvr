package server

import (
	"encoding/json"
	"testing"

	"github.com/q191201771/lal/pkg/base"
	maxlogic "github.com/q191201771/lalmax/logic"
)

// Regression for PR #27: the HTTP stat view must not drop the stable
// video-frame timestamp supplied by the lower lal module.
func TestPR27StatViewPreservesVideoTimestamp(t *testing.T) {
	view := maxlogic.StatGroupView{Group: base.StatGroup{
		StreamName: "fixture",
		AppName: "live",
		VideoCodec: base.VideoCodecAvc,
		LastVideoFrameUnixSec: 123456,
	}}
	for i := 0; i < 2; i++ {
		one := newLalmaxStatGroup(view)
		if one.LastVideoFrameUnixSec != 123456 {
			t.Fatalf("single group stats lost timestamp on read %d: %+v", i, one)
		}
		payload, err := json.Marshal(ApiStatGroupResp{Data: &one})
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Data struct { LastVideoFrameUnixSec int64 `json:"last_video_frame_unix_sec"` } `json:"data"`
		}
		if err := json.Unmarshal(payload, &response); err != nil {
			t.Fatal(err)
		}
		if response.Data.LastVideoFrameUnixSec != 123456 {
			t.Fatalf("/api/stat/group dropped timestamp: %s", payload)
		}
	}
}

func TestPR27AllGroupStatViewsPreserveVideoTimestamp(t *testing.T) {
	views := []maxlogic.StatGroupView{
		{Group: base.StatGroup{StreamName: "video", VideoCodec: base.VideoCodecAvc, LastVideoFrameUnixSec: 654321}},
		{Group: base.StatGroup{StreamName: "audio", AudioCodec: base.AudioCodecAac}},
	}
	var out ApiStatAllGroupResp
	out.Data.Groups = newLalmaxStatGroups(views)
	payload, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Data struct {
			Groups []struct {
				StreamName string `json:"stream_name"`
				LastVideoFrameUnixSec int64 `json:"last_video_frame_unix_sec"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Groups) != 2 || response.Data.Groups[0].LastVideoFrameUnixSec != 654321 || response.Data.Groups[1].LastVideoFrameUnixSec != 0 {
		t.Fatalf("/api/stat/all_group timestamp lost or leaked: %s", payload)
	}
}
