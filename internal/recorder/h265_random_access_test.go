package recorder

import (
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/merge"
	"github.com/stretchr/testify/require"
)

func TestH265RecorderStartsAndRotatesAtCRA(t *testing.T) {
	mgr := newTestManager(t)
	rec := NewH265Recorder(H265Config{CameraID: "cra", SegmentDur: time.Minute, RingBufCap: 32}, mgr)
	// Drive the writer synchronously, so assertions do not depend on wall-clock
	// delivery or RTSP buffering. Slice bodies are only needed for MP4 sample tables.
	write := func(nals ...[]byte) {
		t.Helper()
		rec.frameCh = make(chan []byte, len(nals))
		for _, nal := range nals {
			rec.frameCh <- annexB(nal)
		}
		close(rec.frameCh)
		rec.writeFrames(make(chan struct{}))
	}
	cra := []byte{21 << 1, 1, 0x80, 0x11}
	rasl := []byte{8 << 1, 1, 0x80, 0x22}
	trail := []byte{1 << 1, 1, 0x80, 0x33}
	write(testVPS265, testSPS265, testPPS265, trail, cra, rasl, trail)
	require.NotNil(t, rec.muxer, "a mid-stream recorder must start at CRA")
	require.Equal(t, 2, rec.frameCount, "drop dependent pre-roll and initial RASL")
	rec.pendingRotate = true
	write(cra, rasl, trail)
	require.Equal(t, 2, rec.frameCount, "new CRA segment drops its leading RASL")
	rec.closeCurrentSegment()
	files, err := mgr.ListFiles("cra")
	require.NoError(t, err)
	require.Len(t, files, 2)
	for _, path := range files {
		info, err := merge.ParseSegment(path)
		require.NoError(t, err)
		require.EqualValues(t, 2, info.SampleCount)
	}
}
