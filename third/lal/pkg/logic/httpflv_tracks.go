package logic

import (
    "github.com/q191201771/lal/pkg/base"
    "github.com/q191201771/lal/pkg/rtmp"
)

// Called under group.mutex. Metadata can announce tracks well before
// their first media packets arrive; retain affirmative hints for this source.
func (group *Group) noteFlvTrackMetadata(pairs rtmp.ObjectPairArray) {
    if n, ok := pairs.Find("audiocodecid").(float64); ok && n > 0 {
        group.flvAudioAnnounced = true
    }
    if n, ok := pairs.Find("videocodecid").(float64); ok && n > 0 {
        group.flvVideoAnnounced = true
    }
    if b, ok := pairs.Find("hasAudio").(bool); ok && b {
        group.flvAudioAnnounced = true
    }
    if b, ok := pairs.Find("hasVideo").(bool); ok && b {
        group.flvVideoAnnounced = true
    }
}

// Called under group.mutex. A header is immutable once sent: publishers
// introducing a truly new track later should advertise it via metadata.
func (group *Group) httpflvTracks(msg *base.RtmpMsg) (audio, video bool) {
    audio = group.stat.AudioCodec != "" || group.flvAudioAnnounced
    video = group.stat.VideoCodec != "" || group.flvVideoAnnounced
    if group.httpflvGopCache != nil {
        audio = audio || group.httpflvGopCache.AacSeqHeader != nil
        video = video || group.httpflvGopCache.VideoSeqHeader != nil
    }
    if msg != nil {
        switch msg.Header.MsgTypeId {
        case base.RtmpTypeIdAudio:
            audio = true
        case base.RtmpTypeIdVideo:
            video = true
        }
    }
    return audio, video
}
