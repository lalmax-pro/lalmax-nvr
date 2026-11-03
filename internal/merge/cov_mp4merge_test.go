package merge

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/abema/go-mp4"
	"github.com/stretchr/testify/require"
)

// --- Validation errors ---------------------------------------------------

func TestCovMerge_MergeValidationErrors(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")

	base := &SegmentInfo{
		Codec:     "h264",
		SPS:       []byte{0x67, 0x42},
		PPS:       []byte{0x68},
		Timescale: 90000,
		Width:     64,
		Height:    64,
	}

	err := MergeMP4Segments([]*SegmentInfo{base, {Codec: "h265"}}, out)
	require.ErrorContains(t, err, "codec mismatch")

	err = MergeMP4Segments([]*SegmentInfo{base, {Codec: "h264", SPS: []byte{1}, PPS: []byte{0x68}}}, out)
	require.ErrorContains(t, err, "SPS/PPS mismatch")

	h265Base := &SegmentInfo{Codec: "h265", SPS: []byte{1}, PPS: []byte{2}, VPS: []byte{3}, Timescale: 90000}
	err = MergeMP4Segments([]*SegmentInfo{h265Base, {Codec: "h265", SPS: []byte{1}, PPS: []byte{2}, VPS: []byte{9}}}, out)
	require.ErrorContains(t, err, "VPS mismatch")

	audioBase := &SegmentInfo{Codec: "h264", SPS: []byte{1}, PPS: []byte{2}, HasAudio: true, AudioConfig: []byte{0x12, 0x10}}
	err = MergeMP4Segments([]*SegmentInfo{audioBase, {Codec: "h264", SPS: []byte{1}, PPS: []byte{2}}}, out)
	require.ErrorContains(t, err, "audio presence mismatch")

	err = MergeMP4Segments([]*SegmentInfo{audioBase, {Codec: "h264", SPS: []byte{1}, PPS: []byte{2}, HasAudio: true, AudioConfig: []byte{9}}}, out)
	require.ErrorContains(t, err, "audio config mismatch")
}

// TestCovMerge_MergeCreateOutputError covers the os.Create failure branch.
func TestCovMerge_MergeCreateOutputError(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "missing-dir", "out.mp4")
	info := &SegmentInfo{Codec: "h264", SPS: []byte{1}, PPS: []byte{2}, Timescale: 90000}
	err := MergeMP4Segments([]*SegmentInfo{info}, out)
	require.Error(t, err)
	require.ErrorIs(t, err, os.ErrNotExist)
}

// TestCovMerge_MergeSegmentFileErrors covers empty path, unreadable source and
// a sample whose offset cannot be seeked to.
func TestCovMerge_MergeSegmentFileErrors(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")

	emptyPath := &SegmentInfo{Codec: "h264", SPS: []byte{1}, PPS: []byte{2}, Timescale: 90000}
	err := MergeMP4Segments([]*SegmentInfo{emptyPath}, out)
	require.ErrorContains(t, err, "empty FilePath")

	missing := &SegmentInfo{
		Codec: "h264", SPS: []byte{1}, PPS: []byte{2}, Timescale: 90000,
		FilePath: filepath.Join(dir, "nope.mp4"),
		Samples:  []SampleEntry{{Offset: 0, Size: 4, Duration: 3000}},
	}
	err = MergeMP4Segments([]*SegmentInfo{missing}, out)
	require.ErrorIs(t, err, os.ErrNotExist)

	// Real file, but the sample offset is negative: Seek fails.
	src := createH264SegmentWithSamples(t, dir, "src.mp4", covH264SPS, covH264PPS, [][]byte{covH264IDR, covH264P})
	good, err := ParseSegment(src)
	require.NoError(t, err)
	badOffset := *good
	badOffset.Samples = append([]SampleEntry(nil), good.Samples...)
	badOffset.Samples[0].Offset = -1
	err = MergeMP4Segments([]*SegmentInfo{&badOffset}, filepath.Join(dir, "out2.mp4"))
	require.ErrorContains(t, err, "copy sample")
}

// TestCovMerge_MergeAudioSampleEdgeCases covers the audio loop `continue`
// (no audio samples) and an unseekable audio sample offset.
func TestCovMerge_MergeAudioSampleEdgeCases(t *testing.T) {
	dir := t.TempDir()
	seg := createH264SegmentWithAudio(t, dir, "av.mp4", covH264SPS, covH264PPS,
		[][]byte{covH264IDR, covH264P}, testAudioConfig, [][]byte{testAACFrame, testAACFrame})
	base, err := ParseSegment(seg)
	require.NoError(t, err)
	require.True(t, base.HasAudio)

	// A second segment that claims audio but carries no audio samples: the
	// audio loop skips it via `len(seg.AudioSamples) == 0`.
	noAudioSamples := *base
	noAudioSamples.Samples = []SampleEntry{base.Samples[0]}
	noAudioSamples.AudioSamples = nil

	out := filepath.Join(dir, "merged.mp4")
	require.NoError(t, MergeMP4Segments([]*SegmentInfo{base, &noAudioSamples}, out))
	merged, err := ParseSegment(out)
	require.NoError(t, err)
	require.True(t, merged.HasAudio)

	// Negative audio sample offset triggers the audio copy error branch.
	badAudio := *base
	badAudio.Samples = []SampleEntry{base.Samples[0]}
	badAudio.AudioSamples = append([]SampleEntry(nil), base.AudioSamples...)
	badAudio.AudioSamples[0].Offset = -1
	err = MergeMP4Segments([]*SegmentInfo{&badAudio, &noAudioSamples}, filepath.Join(dir, "merged2.mp4"))
	require.ErrorContains(t, err, "copy audio sample")
}

// --- copySampleData ------------------------------------------------------

// covClosedFile returns an *os.File that has already been closed.
func covClosedFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "closed-*.bin")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f
}

type covMp4ErrWriter struct{}

var errCovWriter = errors.New("cov writer failure")

func (covMp4ErrWriter) Write([]byte) (int, error) { return 0, errCovWriter }

func TestCovMerge_CopySampleDataBranches(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.bin")
	require.NoError(t, os.WriteFile(srcPath, []byte("0123456789"), 0o644))

	src, err := os.Open(srcPath)
	require.NoError(t, err)
	defer src.Close()

	// Seek error: the source file is already closed.
	_, err = copySampleData(covClosedFile(t), io.Discard, 0, 4, make([]byte, 4))
	require.Error(t, err)

	// Zero-byte read breaks out: offset past EOF.
	n, err := copySampleData(src, io.Discard, 100, 8, make([]byte, 4))
	require.NoError(t, err)
	require.Equal(t, int64(0), n)

	// Write error.
	_, err = copySampleData(src, covMp4ErrWriter{}, 0, 4, make([]byte, 4))
	require.ErrorIs(t, err, errCovWriter)

	// Multi-buffer copy with a tiny buffer.
	var buf []byte
	w := &covBytesBuffer{data: &buf}
	n, err = copySampleData(src, w, 2, 6, make([]byte, 2))
	require.NoError(t, err)
	require.Equal(t, int64(6), n)
	require.Equal(t, []byte("234567"), buf)
}

type covBytesBuffer struct{ data *[]byte }

func (b *covBytesBuffer) Write(p []byte) (int, error) {
	*b.data = append(*b.data, p...)
	return len(p), nil
}

// --- mergeTrack.sampleList ----------------------------------------------

func TestCovMerge_SampleListNil(t *testing.T) {
	var tr *mergeTrack
	require.Nil(t, tr.sampleList())
}

// --- moov writer error injection ----------------------------------------

var errCovSeek = errors.New("cov seek failure")

// covFailSeeker is an io.WriteSeeker that starts failing once limit Write
// calls have happened. Seeking never fails so box-size patching stays valid.
type covFailSeeker struct {
	data  []byte
	pos   int64
	calls int
	limit int
}

func (w *covFailSeeker) Write(p []byte) (int, error) {
	if w.calls >= w.limit {
		return 0, errCovWriter
	}
	w.calls++
	if w.pos+int64(len(p)) > int64(len(w.data)) {
		grow := w.pos + int64(len(p)) - int64(len(w.data))
		w.data = append(w.data, make([]byte, grow)...)
	}
	copy(w.data[w.pos:], p)
	w.pos += int64(len(p))
	return len(p), nil
}

func (w *covFailSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		w.pos = offset
	case io.SeekCurrent:
		w.pos += offset
	case io.SeekEnd:
		w.pos = int64(len(w.data)) + offset
	default:
		return 0, errCovSeek
	}
	if w.pos < 0 {
		w.pos = 0
	}
	return w.pos, nil
}

func covVideoTrack(isH265 bool) *mergeTrack {
	tr := &mergeTrack{
		isH265:    isH265,
		sps:       covH264SPS,
		pps:       covH264PPS,
		timescale: 90000,
		width:     640,
		height:    480,
		chunks: []mergeChunk{{
			offset: 8,
			samples: []mergedSample{
				{offset: 8, size: 4, duration: 3000, isKeyFrame: true},
				{offset: 12, size: 4, duration: 3000},
			},
		}},
	}
	if isH265 {
		tr.vps = []byte{0x40, 0x01, 0x0c}
	}
	return tr
}

func covAudioTrack() *mergeTrack {
	return &mergeTrack{
		isAudio:     true,
		audioConfig: testAudioConfig,
		timescale:   44100,
		chunks: []mergeChunk{{
			offset:  20,
			samples: []mergedSample{{offset: 20, size: 6, duration: 1024}},
		}},
	}
}

// TestCovMerge_WriteMoovErrorInjection calls the moov writer with a writer that
// fails after each possible write, covering every box-writer error return.
func TestCovMerge_WriteMoovErrorInjection(t *testing.T) {
	cases := []struct {
		name  string
		video *mergeTrack
		audio *mergeTrack
	}{
		{"h264", covVideoTrack(false), nil},
		{"h264+audio", covVideoTrack(false), covAudioTrack()},
		{"h265", covVideoTrack(true), nil},
		{"h265+audio", covVideoTrack(true), covAudioTrack()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var succeeded, injected int
			for limit := 0; limit <= 900; limit++ {
				w := &covFailSeeker{limit: limit}
				err := writeMergeMoov(mp4.NewWriter(w), tc.video, tc.audio)
				if err == nil {
					succeeded++
					continue
				}
				if errors.Is(err, errCovWriter) {
					injected++
				}
			}
			require.Greater(t, succeeded, 0, "some write limit must allow a full moov")
			require.Greater(t, injected, 0, "write failures must be injected")
		})
	}
}

func TestCovMerge_WriteFtypErrorInjection(t *testing.T) {
	for _, codec := range []string{"h264", "h265", "mjpeg"} {
		var succeeded bool
		for limit := 0; limit <= 12; limit++ {
			w := &covFailSeeker{limit: limit}
			size, err := writeMergeFtyp(w, codec)
			if err == nil {
				succeeded = true
				require.Greater(t, size, int64(0))
				continue
			}
			require.ErrorIs(t, err, errCovWriter)
		}
		require.True(t, succeeded)
	}
}

func TestCovMerge_WriteSTSSBranches(t *testing.T) {
	buf := &covFailSeeker{limit: 100}
	w := mp4.NewWriter(buf)

	// nil track / audio track / empty samples are all no-ops.
	require.NoError(t, writeMergeSTSS(w, nil, nil))
	require.NoError(t, writeMergeSTSS(w, &mergeTrack{isAudio: true}, []mergedSample{{offset: 0, size: 4}}))
	require.NoError(t, writeMergeSTSS(w, &mergeTrack{}, nil))

	// No keyframes -> no stss box.
	require.NoError(t, writeMergeSTSS(w, &mergeTrack{}, []mergedSample{{offset: 0, size: 4}, {offset: 4, size: 4}}))

	// Every sample a keyframe -> no stss box.
	require.NoError(t, writeMergeSTSS(w, &mergeTrack{}, []mergedSample{{offset: 0, size: 4, isKeyFrame: true}, {offset: 4, size: 4, isKeyFrame: true}}))

	// Mixed -> writes stss (exercises the writer error returns too).
	require.NoError(t, writeMergeSTSS(w, &mergeTrack{}, []mergedSample{{offset: 0, size: 4, isKeyFrame: true}, {offset: 4, size: 4}}))
	for limit := 0; limit < 4; limit++ {
		fw := &covFailSeeker{limit: limit}
		err := writeMergeSTSS(mp4.NewWriter(fw), &mergeTrack{}, []mergedSample{{offset: 0, size: 4, isKeyFrame: true}, {offset: 4, size: 4}})
		require.ErrorIs(t, err, errCovWriter)
	}
}

func TestCovMerge_ParseAudioConfigBranches(t *testing.T) {
	// Defaults for a config that is too short.
	ch, rate := parseAudioConfig(nil)
	require.Equal(t, uint16(2), ch)
	require.Equal(t, uint32(44100), rate)

	// Explicit sample-rate escape (index 0x0F) with a 4-byte config.
	ch, rate = parseAudioConfig([]byte{0x7F, 0x01, 0x02, 0x00})
	require.NotZero(t, ch)
	require.NotZero(t, rate)

	// channelConfig > 0 sets the channel count (0x13 has the low bit set).
	ch, _ = parseAudioConfig([]byte{0x13, 0x10})
	require.Equal(t, uint16(4), ch)

	// Known sample-rate index.
	_, rate = parseAudioConfig([]byte{0x12, 0x10})
	require.Equal(t, uint32(64000), rate)
}

func TestCovMerge_BuildMergeHvcCShortSPS(t *testing.T) {
	h := buildMergeHvcC([]byte{1}, []byte{2}, []byte{3})
	require.Equal(t, uint8(0), h.GeneralProfileIdc)
	require.Equal(t, uint8(0), h.GeneralLevelIdc)

	h = buildMergeHvcC(nil, nil, nil)
	require.Equal(t, uint8(0), h.GeneralProfileIdc)
}

// --- bytesWriter ---------------------------------------------------------

func TestCovMerge_BytesWriter(t *testing.T) {
	b := &bytesWriter{}
	require.Equal(t, int64(0), b.len())

	// Seek beyond the buffer then write: the buffer grows.
	pos, err := b.Seek(4, io.SeekStart)
	require.NoError(t, err)
	require.Equal(t, int64(4), pos)
	n, err := b.Write([]byte("ab"))
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Equal(t, int64(6), b.len())

	// Relative and end-relative seeks.
	pos, err = b.Seek(-2, io.SeekCurrent)
	require.NoError(t, err)
	require.Equal(t, int64(4), pos)
	pos, err = b.Seek(-1, io.SeekEnd)
	require.NoError(t, err)
	require.Equal(t, int64(5), pos)

	// Negative positions clamp to zero.
	pos, err = b.Seek(-100, io.SeekCurrent)
	require.NoError(t, err)
	require.Equal(t, int64(0), pos)

	// Unknown whence is treated as a no-op seek position (default branch).
	_, err = b.Seek(1, 9)
	require.NoError(t, err)

	// Write inside the existing buffer.
	pos, err = b.Seek(0, io.SeekStart)
	require.NoError(t, err)
	require.Equal(t, int64(0), pos)
	_, err = b.Write([]byte("Z"))
	require.NoError(t, err)
	require.Equal(t, byte('Z'), b.Bytes()[0])
}
