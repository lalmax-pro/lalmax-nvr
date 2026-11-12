// Copyright 2019, Chef.  All rights reserved.
// https://github.com/q191201771/lal
//
// Use of this source code is governed by a MIT-style license
// that can be found in the License file.
//
// Author: Chef (191201771@qq.com)

package httpflv

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/q191201771/lal/pkg/base"

	"github.com/q191201771/naza/pkg/connection"
)

// A late second track without onMetaData needs a short bounded detection
// window. Never wait indefinitely or accumulate unbounded GOPs per viewer.
const (
	flvTrackProbeWindow = 350 * time.Millisecond
	flvTrackProbeMaxBufferedBytes = 2 << 20
	flvTrackProbeMaxBufferedTags = 128
)

var flvHttpResponseHeader []byte

type SubSession struct {
	core                    *base.BasicHttpSubSession
	IsFresh                 bool
	ShouldWaitVideoKeyFrame bool
	flvHeaderWritten atomic.Bool

	probeMu sync.Mutex
	probeTimer *time.Timer
	probeAudio bool
	probeVideo bool
	probePending [][]byte
	probeBytes int
	probeClosed bool
}

func NewSubSession(conn net.Conn, urlCtx base.UrlContext, isWebSocket bool, websocketKey string) *SubSession {
	uk := base.GenUkFlvSubSession()
	s := &SubSession{
		core: base.NewBasicHttpSubSession(base.BasicHttpSubSessionOption{
			Conn: conn,
			ConnModOption: func(option *connection.Option) {
				option.WriteChanSize = SubSessionWriteChanSize
				option.WriteTimeoutMs = SubSessionWriteTimeoutMs
			},
			SessionType:  base.SessionTypeFlvSub,
			UrlCtx:       urlCtx,
			IsWebSocket:  isWebSocket,
			WebSocketKey: websocketKey,
		}),
		IsFresh:                 true,
		ShouldWaitVideoKeyFrame: true,
	}
	Log.Infof("[%s] lifecycle new httpflv SubSession. session=%p, remote addr=%s", uk, s, conn.RemoteAddr().String())
	return s
}

// ---------------------------------------------------------------------------------------------------------------------
// IServerSessionLifecycle interface
// ---------------------------------------------------------------------------------------------------------------------

func (session *SubSession) RunLoop() error {
	return session.core.RunLoop()
}

func (session *SubSession) Dispose() error {
	Log.Infof("[%s] lifecycle dispose httpflv SubSession.", session.core.UniqueKey())
	session.probeMu.Lock()
	session.probeClosed = true
	if session.probeTimer != nil {
		session.probeTimer.Stop()
		session.probeTimer = nil
	}
	session.probePending = nil
	session.probeBytes = 0
	session.probeMu.Unlock()
	return session.core.Dispose()
}

// ---------------------------------------------------------------------------------------------------------------------

func (session *SubSession) WriteHttpResponseHeader() {
	Log.Debugf("[%s] > W http response header.", session.core.UniqueKey())
	session.core.WriteHttpResponseHeader(flvHttpResponseHeader)
}

// FlvHeaderWritten allows stream groups to defer a header until media
// tracks have been discovered, and prevents duplicate header emission.
func (session *SubSession) FlvHeaderWritten() bool {
    return session.flvHeaderWritten.Load()
}

// ObserveFlvTracks records media discoveries. A known dual-track layout
// flushes immediately; a single known track is given a bounded opportunity
// to discover the second track before publishing an immutable FLV header.
func (session *SubSession) ObserveFlvTracks(audio, video bool) {
	session.probeMu.Lock()
	defer session.probeMu.Unlock()
	if session.probeClosed || session.flvHeaderWritten.Load() { return }
	session.probeAudio = session.probeAudio || audio
	session.probeVideo = session.probeVideo || video
	if session.probeAudio && session.probeVideo {
		session.flushProbeLocked()
		return
	}
	if (session.probeAudio || session.probeVideo) && session.probeTimer == nil {
		session.probeTimer = time.AfterFunc(flvTrackProbeWindow, session.flushProbe)
	}
}

func (session *SubSession) flushProbe() {
	session.probeMu.Lock()
	defer session.probeMu.Unlock()
	session.flushProbeLocked()
}

// All callers hold probeMu, ensuring header-before-tag ordering even when a
// probe deadline fires concurrently with the publisher's next media packet.
func (session *SubSession) flushProbeLocked() {
	if session.probeClosed || session.flvHeaderWritten.Load() || (!session.probeAudio && !session.probeVideo) {
		return
	}
	if session.probeTimer != nil {
		session.probeTimer.Stop()
		session.probeTimer = nil
	}
	session.flvHeaderWritten.Store(true)
	session.core.Write(FlvHeaderWithTracks(session.probeAudio, session.probeVideo))
	for _, tag := range session.probePending {
		session.core.Write(tag)
	}
	session.probePending = nil
	session.probeBytes = 0
}

// WriteFlvHeaderWithTracks keeps the explicit header API immediate; the lal
// stream group uses ObserveFlvTracks to provide the bounded probe window.
func (session *SubSession) WriteFlvHeaderWithTracks(audio, video bool) {
	session.probeMu.Lock()
	defer session.probeMu.Unlock()
	if session.probeClosed || session.flvHeaderWritten.Load() { return }
	session.probeAudio = session.probeAudio || audio
	session.probeVideo = session.probeVideo || video
	session.flushProbeLocked()
}

// Compatibility entrypoint for callers without track information.
func (session *SubSession) WriteFlvHeader() {
	session.WriteFlvHeaderWithTracks(true, true)
}

func (session *SubSession) WriteTag(tag *Tag) {
	session.Write(tag.Raw)
}

func (session *SubSession) Write(b []byte) {
	if len(b) == 0 { return }
	session.probeMu.Lock()
	defer session.probeMu.Unlock()
	if session.probeClosed { return }
	if !session.flvHeaderWritten.Load() {
		// Bound per-subscriber memory and the number of queued FLV tags.
		// If media is already known, a full queue forces early publication.
		if session.probeBytes+len(b) > flvTrackProbeMaxBufferedBytes ||
			len(session.probePending) >= flvTrackProbeMaxBufferedTags {
			if session.probeAudio || session.probeVideo {
				session.flushProbeLocked()
			} else {
				// No media track exists yet. Trim old metadata/control tags
				// rather than manufacture an invalid speculative A/V header.
				for len(session.probePending) > 0 &&
					(session.probeBytes+len(b) > flvTrackProbeMaxBufferedBytes ||
					len(session.probePending) >= flvTrackProbeMaxBufferedTags) {
					session.probeBytes -= len(session.probePending[0])
					session.probePending[0] = nil
					session.probePending = session.probePending[1:]
				}
			}
		}
		if !session.flvHeaderWritten.Load() {
			if len(b) <= flvTrackProbeMaxBufferedBytes {
				session.probePending = append(session.probePending, append([]byte(nil), b...))
				session.probeBytes += len(b)
			}
			return
		}
	}
	session.core.Write(b)
}

// ---------------------------------------------------------------------------------------------------------------------
// IObject interface
// ---------------------------------------------------------------------------------------------------------------------

func (session *SubSession) UniqueKey() string {
	return session.core.UniqueKey()
}

// ---------------------------------------------------------------------------------------------------------------------
// ISessionUrlContext interface
// ---------------------------------------------------------------------------------------------------------------------

func (session *SubSession) Url() string {
	return session.core.Url()
}

func (session *SubSession) AppName() string {
	return session.core.AppName()
}

func (session *SubSession) StreamName() string {
	return session.core.StreamName()
}

func (session *SubSession) RawQuery() string {
	return session.core.RawQuery()
}

// ----- ISessionStat --------------------------------------------------------------------------------------------------

func (session *SubSession) UpdateStat(intervalSec uint32) {
	session.core.UpdateStat(intervalSec)
}

func (session *SubSession) GetStat() base.StatSession {
	return session.core.GetStat()
}

func (session *SubSession) IsAlive() (readAlive, writeAlive bool) {
	return session.core.IsAlive()
}

// ---------------------------------------------------------------------------------------------------------------------

func init() {
	flvHttpResponseHeaderStr := "HTTP/1.1 200 OK\r\n" +
		"Server: " + base.LalHttpflvSubSessionServer + "\r\n" +
		"Cache-Control: no-cache\r\n" +
		"Content-Type: video/x-flv\r\n" +
		"Connection: close\r\n" +
		"Expires: -1\r\n" +
		"Pragma: no-cache\r\n" +
		base.CorsHeaders +
		"\r\n"

	flvHttpResponseHeader = []byte(flvHttpResponseHeaderStr)
}
