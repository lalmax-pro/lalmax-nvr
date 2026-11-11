package server

import (
	"github.com/q191201771/lal/pkg/base"
	maxlogic "github.com/q191201771/lalmax/logic"
)

type LalmaxGroupStat struct {
	ExtSubs []base.StatSub `json:"ext_subs"`
}

type LalmaxStatGroup struct {
	StreamName  string              `json:"stream_name"`
	AppName     string              `json:"app_name"`
	AudioCodec  string              `json:"audio_codec"`
	VideoCodec  string              `json:"video_codec"`
	VideoWidth  int                 `json:"video_width"`
	VideoHeight int                 `json:"video_height"`
	StatPub     base.StatPub        `json:"pub"`
	StatSubs    []base.StatSub      `json:"subs"`
	StatPull    base.StatPull       `json:"pull"`
	Fps         []base.RecordPerSec `json:"in_frame_per_sec"`
	// LastVideoFrameUnixSec is independent of the consumable FPS buckets.
	// Preserve it for both /api/stat/group and /api/stat/all_group.
	LastVideoFrameUnixSec int64 `json:"last_video_frame_unix_sec,omitempty"`
	Lalmax      LalmaxGroupStat     `json:"lalmax"`
}

type ApiStatGroupResp struct {
	base.ApiRespBasic
	Data *LalmaxStatGroup `json:"data"`
}

type ApiStatAllGroupResp struct {
	base.ApiRespBasic
	Data struct {
		Groups []LalmaxStatGroup `json:"groups"`
	} `json:"data"`
}

func newLalmaxStatGroup(view maxlogic.StatGroupView) LalmaxStatGroup {
	group := view.Group
	return LalmaxStatGroup{
		StreamName:  group.StreamName,
		AppName:     group.AppName,
		AudioCodec:  group.AudioCodec,
		VideoCodec:  group.VideoCodec,
		VideoWidth:  group.VideoWidth,
		VideoHeight: group.VideoHeight,
		StatPub:     group.StatPub,
		StatSubs:    group.StatSubs,
		StatPull:    group.StatPull,
		Fps:         group.Fps,
		LastVideoFrameUnixSec: group.LastVideoFrameUnixSec,
		Lalmax: LalmaxGroupStat{
			ExtSubs: view.ExtSubs,
		},
	}
}

func newLalmaxStatGroups(views []maxlogic.StatGroupView) []LalmaxStatGroup {
	if len(views) == 0 {
		return nil
	}

	out := make([]LalmaxStatGroup, len(views))
	for i, view := range views {
		out[i] = newLalmaxStatGroup(view)
	}
	return out
}
