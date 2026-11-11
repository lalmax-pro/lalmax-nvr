// Copyright 2019, Chef.  All rights reserved.
// https://github.com/q191201771/lal
//
// Use of this source code is governed by a MIT-style license
// that can be found in the License file.
//
// Author: Chef (191201771@qq.com)

package httpflv

import "github.com/q191201771/naza/pkg/nazalog"

var (
	SubSessionWriteChanSize  = 1024 // SubSession发送数据时channel的大小
	SubSessionWriteTimeoutMs = 10000
	FlvHeader                = []byte{0x46, 0x4c, 0x56, 0x01, 0x05, 0x00, 0x00, 0x00, 0x09, 0x00, 0x00, 0x00, 0x00}

	Log = nazalog.GetGlobalLogger()
)

// FlvHeaderWithTracks returns a per-stream FLV v1 header without altering
// the global legacy header. Bit 2 is audio; bit 0 is video.
func FlvHeaderWithTracks(audio, video bool) []byte {
    header := append([]byte(nil), FlvHeader...)
    header[4] = 0
    if audio { header[4] |= 0x04 }
    if video { header[4] |= 0x01 }
    return header
}

var readBufSize = 256 //16384 // ClientPullSession读取数据时
