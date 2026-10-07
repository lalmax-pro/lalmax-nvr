package recorder

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/q191201771/lal/pkg/avc"
	"github.com/q191201771/lal/pkg/base"
	maxlogic "github.com/q191201771/lalmax/logic"
	"github.com/stretchr/testify/require"
)

func TestCameraUsesGroupRecording(t *testing.T) {
	require.True(t, CameraUsesGroupRecording(config.CameraConfig{Protocol: "rtsp", Encoding: "h264"}))
	require.True(t, CameraUsesGroupRecording(config.CameraConfig{Protocol: "xiaomi", Encoding: "h265"}))
	require.False(t, CameraUsesGroupRecording(config.CameraConfig{Protocol: "rtsp", Encoding: "mjpeg"}))
	require.False(t, CameraUsesGroupRecording(config.CameraConfig{Protocol: "http", Encoding: "jpeg"}))
	require.False(t, CameraUsesGroupRecording(config.CameraConfig{Protocol: "timelapse"}))
}

func TestGroupWriterWritesCTTS(t *testing.T) {
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	w := NewGroupWriter(store, nil, nil, nil, time.Hour)
	g := &fakeGroup{}
	w.groups = singleGroup{name: "cam1", group: g}
	w.SetLookup(func(id string) (config.CameraConfig, bool) {
		if id != "cam1" {
			return config.CameraConfig{}, false
		}
		return config.CameraConfig{ID: "cam1", Protocol: "rtsp", Encoding: "h264", Enabled: true}, true
	})
	w.Attach("cam1")

	seq, err := avc.BuildSeqHeaderFromSpsPps(groupTestSPS, groupTestPPS)
	require.NoError(t, err)
	g.push(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo, TimestampAbs: 0, MsgLen: uint32(len(seq))},
		Payload: seq,
	})
	g.push(rtmpVideo(0, 40, base.RtmpAvcPacketTypeNalu, true, naluAVCC([]byte{0x65, 0x88, 0x80})))
	g.push(rtmpVideo(40, 0, base.RtmpAvcPacketTypeNalu, false, naluAVCC([]byte{0x41, 0x9a, 0x00})))

	require.Eventually(t, func() bool {
		attached, writing := w.State("cam1")
		return attached && writing && g.calls.Load() >= 3
	}, time.Second, 10*time.Millisecond)

	w.Close()
	var found bool
	_ = filepath.Walk(storeRoot(store), func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr == nil && bytesContains(raw, []byte("ctts")) {
			found = true
		}
		return nil
	})
	require.True(t, found, "expected an mp4 segment with a ctts box")
}

type singleGroup struct {
	name  string
	group *fakeGroup
}

func (s singleGroup) Find(streamName string) (subscribeGroup, bool) {
	if streamName == s.name {
		return s.group, true
	}
	return nil, false
}

func (s singleGroup) Each(fn func(string)) { fn(s.name) }

type fakeGroup struct {
	mu    sync.Mutex
	sub   maxlogic.Subscriber
	calls atomic.Int64
}

func (f *fakeGroup) AddSubscriber(_ maxlogic.SubscriberInfo, subscriber maxlogic.Subscriber) {
	f.mu.Lock()
	f.sub = subscriber
	f.mu.Unlock()
}

func (f *fakeGroup) RemoveSubscriber(string) {
	f.mu.Lock()
	f.sub = nil
	f.mu.Unlock()
}

func (f *fakeGroup) push(msg base.RtmpMsg) {
	f.mu.Lock()
	sub := f.sub
	f.mu.Unlock()
	if sub == nil {
		return
	}
	f.calls.Add(1)
	sub.OnMsg(msg)
}

func rtmpVideo(dts, cts uint32, packetType byte, key bool, body []byte) base.RtmpMsg {
	payload := make([]byte, 5+len(body))
	if key {
		payload[0] = base.RtmpAvcKeyFrame
	} else {
		payload[0] = base.RtmpAvcInterFrame
	}
	payload[1] = packetType
	payload[2] = byte(cts >> 16)
	payload[3] = byte(cts >> 8)
	payload[4] = byte(cts)
	copy(payload[5:], body)
	return base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo, TimestampAbs: dts, MsgLen: uint32(len(payload))},
		Payload: payload,
	}
}

func naluAVCC(nalu []byte) []byte {
	buf := make([]byte, 4+len(nalu))
	binary.BigEndian.PutUint32(buf, uint32(len(nalu)))
	copy(buf[4:], nalu)
	return buf
}

func bytesContains(b, sub []byte) bool {
	return len(sub) == 0 || (len(b) >= len(sub) && indexOf(b, sub) >= 0)
}

func indexOf(b, sub []byte) int {
	for i := 0; i+len(sub) <= len(b); i++ {
		match := true
		for j := range sub {
			if b[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func storeRoot(s *storage.Manager) string {
	return s.RootDir()
}

var groupTestSPS = []byte{0x67, 0x42, 0xc0, 0x1e, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0xc8}
var groupTestPPS = []byte{0x68, 0xce, 0x38, 0x80}
