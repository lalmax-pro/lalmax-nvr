package vod

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abema/go-mp4"
	"github.com/lalmax-pro/lalmax-nvr/internal/merge"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

// s2FailWriter fails every Write call after the configured number of successes.
type s2FailWriter struct {
	allowed int // successful writes permitted; negative means unlimited
	calls   int
	written int
}

func (w *s2FailWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.allowed >= 0 && w.calls > w.allowed {
		return 0, errors.New("s2 injected write failure")
	}
	w.written += len(p)
	return len(p), nil
}

// s2Segment returns a hand-crafted segment info with n one-second samples at
// 1000 Hz. Every sample points at the start of the file.
func s2Segment(n int, keyframes map[int]bool) *merge.SegmentInfo {
	samples := make([]merge.SampleEntry, n)
	for i := range samples {
		samples[i] = merge.SampleEntry{
			Offset:     0,
			Size:       16,
			Duration:   1000,
			IsKeyFrame: keyframes[i],
		}
	}
	return &merge.SegmentInfo{
		Codec:     "h264",
		Timescale: 1000,
		Width:     640,
		Height:    360,
		SPS:       []byte{0x67, 0x42, 0x00, 0x0a},
		PPS:       []byte{0x68, 0xce, 0x38, 0x80},
		Samples:   samples,
	}
}

func TestS2SplitFragmentsEdges(t *testing.T) {
	require.Nil(t, SplitFragments(nil))
	require.Nil(t, SplitFragments(&merge.SegmentInfo{}))

	// Zero timescale falls back to 1000 Hz.
	info := &merge.SegmentInfo{
		Timescale: 0,
		Samples:   []merge.SampleEntry{{Duration: 1000}, {Duration: 1000}},
	}
	frags := SplitFragments(info)
	require.Len(t, frags, 1)
	require.Equal(t, 0, frags[0].First)
	require.Equal(t, 1, frags[0].Last)
	require.Equal(t, 2*time.Second, frags[0].Duration)

	// A keyframe after the target duration starts a new fragment.
	info = &merge.SegmentInfo{
		Timescale: 1000,
		Samples: []merge.SampleEntry{
			{Duration: 7000},
			{Duration: 500, IsKeyFrame: true},
			{Duration: 6000},
			{Duration: 500, IsKeyFrame: true},
			{Duration: 500},
		},
	}
	frags = SplitFragments(info)
	require.Len(t, frags, 3)
	require.Equal(t, FragmentRange{First: 0, Last: 0, Duration: 7 * time.Second, DecodeTime: 0}, frags[0])
	require.Equal(t, FragmentRange{First: 1, Last: 2, Duration: 6500 * time.Millisecond, DecodeTime: 7000}, frags[1])
	require.Equal(t, FragmentRange{First: 3, Last: 4, Duration: time.Second, DecodeTime: 13500}, frags[2])

	require.Equal(t, time.Duration(0), ticksToDuration(10, 0))
	require.Equal(t, 500*time.Millisecond, ticksToDuration(500, 1000))
}

func TestS2WriteInitH265(t *testing.T) {
	info := s2Segment(2, map[int]bool{0: true})
	info.Codec = "h265"
	info.VPS = []byte{0x40, 0x01, 0x0c}
	info.SPS = []byte{0x42, 0x01, 0x01}
	info.PPS = []byte{0x44, 0x01, 0xc0}

	var buf bytes.Buffer
	require.NoError(t, WriteInit(&buf, info))
	body := buf.String()
	require.Contains(t, body[:16], "ftyp")
	require.Contains(t, body, "hvc1")
	require.Contains(t, body, "hvcC")
	require.Contains(t, body, "moov")
}

func TestS2WriteInitH264ZeroTimescale(t *testing.T) {
	info := s2Segment(1, map[int]bool{0: true})
	info.Timescale = 0
	var buf bytes.Buffer
	require.NoError(t, WriteInit(&buf, info))
	require.Contains(t, buf.String(), "avc1")

	require.Error(t, WriteInit(&buf, nil))
}

func TestS2WriteFragmentInvalidRanges(t *testing.T) {
	var buf bytes.Buffer
	require.Error(t, WriteFragment(&buf, nil, 1, 0, 0))

	info := s2Segment(3, map[int]bool{0: true})
	require.Error(t, WriteFragment(&buf, info, 1, -1, 0))
	require.Error(t, WriteFragment(&buf, info, 1, 2, 1))
	require.Error(t, WriteFragment(&buf, info, 1, 0, 3))

	// A non-first fragment accumulates decode time from the preceding samples.
	path := filepath.Join(t.TempDir(), "src.mp4")
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte{0xab}, 64), 0o600))
	info.FilePath = path
	for i := range info.Samples {
		info.Samples[i].Offset = int64(i * 16)
	}
	buf.Reset()
	require.NoError(t, WriteFragment(&buf, info, 1, 1, 2))
	require.Contains(t, buf.String()[:16], "moof")
	require.Contains(t, buf.String(), "mdat")
}

func TestS2WriteFragmentWriteFailures(t *testing.T) {
	info := s2Segment(2, map[int]bool{0: true})
	info.FilePath = filepath.Join(t.TempDir(), "missing.mp4")

	// Fails while writing the moof box.
	w := &s2FailWriter{allowed: 0}
	require.Error(t, WriteFragment(w, info, 1, 0, 1))

	// Fails while copying sample data.
	path := filepath.Join(t.TempDir(), "src.mp4")
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte{0xcd}, 64), 0o600))
	okInfo := s2Segment(2, map[int]bool{0: true})
	okInfo.FilePath = path
	w = &s2FailWriter{allowed: 2} // moof + mdat header succeed
	require.Error(t, WriteFragment(w, okInfo, 1, 0, 1))

	// Fails opening the sample file after the headers were written.
	w = &s2FailWriter{allowed: -1}
	require.Error(t, WriteFragment(w, info, 1, 0, 1))
	require.Greater(t, w.written, 0)
}

func TestS2ClipWindowEdges(t *testing.T) {
	recStart := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	require.False(t, func() bool {
		_, _, ok := ClipWindow(nil, recStart, recStart, recStart.Add(time.Second))
		return ok
	}())
	_, _, ok := ClipWindow(&merge.SegmentInfo{}, recStart, recStart, recStart.Add(time.Second))
	require.False(t, ok)

	info := s2Segment(3, map[int]bool{0: true})
	// windowEnd before windowStart.
	_, _, ok = ClipWindow(info, recStart, recStart.Add(2*time.Second), recStart)
	require.False(t, ok)

	// Zero timescale falls back to 1000 Hz.
	zeroScale := s2Segment(3, map[int]bool{0: true})
	zeroScale.Timescale = 0
	first, last, ok := ClipWindow(zeroScale, recStart, recStart, recStart.Add(2*time.Second))
	require.True(t, ok)
	require.Equal(t, 0, first)
	require.Equal(t, 1, last)

	// Window starting before the recording is clamped to zero.
	first, last, ok = ClipWindow(info, recStart, recStart.Add(-5*time.Second), recStart.Add(2*time.Second))
	require.True(t, ok)
	require.Equal(t, 0, first)
	require.Equal(t, 1, last)

	// Window entirely before the recording has no samples.
	_, _, ok = ClipWindow(info, recStart, recStart.Add(-2*time.Second), recStart.Add(-time.Second))
	require.False(t, ok)

	// Window after every sample: no first sample found.
	_, _, ok = ClipWindow(info, recStart, recStart.Add(10*time.Second), recStart.Add(12*time.Second))
	require.False(t, ok)

	// Mid-recording window stops as soon as the end tick is covered.
	first, last, ok = ClipWindow(info, recStart, recStart.Add(500*time.Millisecond), recStart.Add(2500*time.Millisecond))
	require.True(t, ok)
	require.Equal(t, 0, first)
	require.Equal(t, 2, last)
}

func TestS2FilterFragments(t *testing.T) {
	require.Nil(t, FilterFragments(nil, 0, 10))

	frags := []FragmentRange{
		{First: 0, Last: 1},
		{First: 2, Last: 8},
		{First: 9, Last: 12},
	}
	// Fragments entirely outside [4,6] are dropped; overlapping ones are clipped.
	clipped := FilterFragments(frags, 4, 6)
	require.Len(t, clipped, 1)
	require.Equal(t, 4, clipped[0].First)
	require.Equal(t, 6, clipped[0].Last)

	// A wider window keeps everything unchanged.
	require.Equal(t, frags, FilterFragments(frags, 0, 20))
}

func TestS2BuildPlaylistEdges(t *testing.T) {
	start := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	items := []PlaylistItem{
		{Recording: nil, Frags: []FragmentRange{{First: 0, Last: 1, Duration: time.Second}}},
		{Recording: &model.Recording{ID: "zero-start", Format: model.FormatH264}, Frags: []FragmentRange{{First: 0, Last: 1, Duration: 2 * time.Second}}},
		{Recording: &model.Recording{ID: "a/b", StartedAt: start, Format: model.FormatH264}, Frags: []FragmentRange{{First: 0, Last: 4, Duration: 9 * time.Second}}},
	}
	body := BuildPlaylist("cam1", items, "/api/vod/{recId}")
	require.Contains(t, body, "#EXT-X-TARGETDURATION:9")
	require.Contains(t, body, "zero-start")
	require.Contains(t, body, "a%2Fb")
	require.NotContains(t, body, "#EXT-X-PROGRAM-DATE-TIME:\n")
	require.True(t, strings.HasSuffix(strings.TrimSpace(body), "#EXT-X-ENDLIST"))
}

// s2FailSeeker fails both Write and Seek once its operation budget is spent.
type s2FailSeeker struct {
	allowed int
	ops     int
}

func (s *s2FailSeeker) fail() bool {
	s.ops++
	return s.ops > s.allowed
}

func (s *s2FailSeeker) Write(p []byte) (int, error) {
	if s.fail() {
		return 0, errors.New("s2 injected seeker write failure")
	}
	return len(p), nil
}

func (s *s2FailSeeker) Seek(offset int64, whence int) (int64, error) {
	if s.fail() {
		return 0, errors.New("s2 injected seeker seek failure")
	}
	return offset, nil
}

// TestS2BoxWriterFailurePaths exercises every error return inside the box
// builders by failing the underlying writer at each possible operation.
func TestS2BoxWriterFailurePaths(t *testing.T) {
	h264 := s2Segment(2, map[int]bool{0: true})
	h265 := s2Segment(2, map[int]bool{0: true})
	h265.Codec = "h265"
	h265.VPS = []byte{0x40, 0x01, 0x0c}
	h265.SPS = []byte{0x42, 0x01, 0x01}
	h265.PPS = []byte{0x44, 0x01, 0xc0}

	failures := 0
	for budget := 0; budget < 200; budget++ {
		calls := []func(*mp4.Writer) error{
			func(w *mp4.Writer) error { return writeFtyp(w, "h264") },
			func(w *mp4.Writer) error { return writeFtyp(w, "h265") },
			func(w *mp4.Writer) error { return writeInitMoov(w, h264) },
			func(w *mp4.Writer) error { return writeInitMvhd(w) },
			func(w *mp4.Writer) error { return writeInitTrak(w, h264) },
			func(w *mp4.Writer) error { return writeInitMdia(w, h264) },
			func(w *mp4.Writer) error { return writeInitMinf(w, h265) },
			func(w *mp4.Writer) error { return writeInitStbl(w, h264) },
			func(w *mp4.Writer) error { return writeInitStbl(w, h265) },
			func(w *mp4.Writer) error { return writeH264Entry(w, h264) },
			func(w *mp4.Writer) error { return writeH265Entry(w, h265) },
			func(w *mp4.Writer) error { return writeMvex(w) },
		}
		for _, call := range calls {
			err := call(mp4.NewWriter(&s2FailSeeker{allowed: budget}))
			if err != nil {
				failures++
			}
		}
	}
	require.Greater(t, failures, 0)
}

func TestS2BytesWriterSeekModes(t *testing.T) {
	b := &bytesWriter{}
	require.NoError(t, func() error {
		_, err := b.Write([]byte("0123456789"))
		return err
	}())
	pos, err := b.Seek(4, io.SeekStart)
	require.NoError(t, err)
	require.Equal(t, int64(4), pos)
	pos, err = b.Seek(2, io.SeekCurrent)
	require.NoError(t, err)
	require.Equal(t, int64(6), pos)
	pos, err = b.Seek(-3, io.SeekEnd)
	require.NoError(t, err)
	require.Equal(t, int64(7), pos)
	pos, err = b.Seek(-100, io.SeekStart)
	require.NoError(t, err)
	require.Equal(t, int64(0), pos, "negative positions are clamped to zero")
	require.Equal(t, []byte("0123456789"), b.bytes())
}
