package merge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// --- Shared cover helpers -------------------------------------------------

var covH264SPS = []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
var covH264PPS = []byte{0x68, 0xce, 0x38, 0x80}
var covH264IDR = []byte{0x65, 0x88, 0x80, 0x40}
var covH264P = []byte{0x41, 0x10, 0x00, 0x0c}

// covHourFile merges two freshly created segments into a ftyp+mdat+moov file
// and returns its path plus the parsed base info. This is the layout that
// AppendMP4Samples requires (moov immediately after mdat at EOF).
func covHourFile(t *testing.T) (dir, hour string, base *SegmentInfo) {
	t.Helper()
	dir = t.TempDir()
	seg1 := createH264SegmentWithSamples(t, dir, "a.mp4", covH264SPS, covH264PPS, [][]byte{covH264IDR, covH264P})
	seg2 := createH264SegmentWithSamples(t, dir, "b.mp4", covH264SPS, covH264PPS, [][]byte{covH264IDR, covH264P})
	info1, err := ParseSegment(seg1)
	require.NoError(t, err)
	info2, err := ParseSegment(seg2)
	require.NoError(t, err)
	hour = filepath.Join(dir, "hour.mp4")
	require.NoError(t, MergeMP4Segments([]*SegmentInfo{info1, info2}, hour))
	base, err = ParseSegmentTables(hour)
	require.NoError(t, err)
	return dir, hour, base
}

// covHourFileWithAudio merges two H.264+AAC segments into an appendable hour file.
func covHourFileWithAudio(t *testing.T) (dir, hour string, base *SegmentInfo) {
	t.Helper()
	dir = t.TempDir()
	seg1 := createH264SegmentWithAudio(t, dir, "a.mp4", covH264SPS, covH264PPS,
		[][]byte{covH264IDR, covH264P}, testAudioConfig, [][]byte{testAACFrame, testAACFrame})
	seg2 := createH264SegmentWithAudio(t, dir, "b.mp4", covH264SPS, covH264PPS,
		[][]byte{covH264IDR, covH264P}, testAudioConfig, [][]byte{testAACFrame})
	info1, err := ParseSegment(seg1)
	require.NoError(t, err)
	info2, err := ParseSegment(seg2)
	require.NoError(t, err)
	hour = filepath.Join(dir, "hour.mp4")
	require.NoError(t, MergeMP4Segments([]*SegmentInfo{info1, info2}, hour))
	base, err = ParseSegmentTables(hour)
	require.NoError(t, err)
	require.True(t, base.HasAudio)
	return dir, hour, base
}

func covCopyInfo(in *SegmentInfo) *SegmentInfo {
	out := *in
	out.Samples = append([]SampleEntry(nil), in.Samples...)
	out.AudioSamples = append([]SampleEntry(nil), in.AudioSamples...)
	return &out
}

// --- AppendMP4Samples happy path, including audio ------------------------

func TestCovMerge_AppendWithAudioExtendsHourFile(t *testing.T) {
	dir, hour, base := covHourFileWithAudio(t)
	before, err := ParseSegment(hour)
	require.NoError(t, err)
	require.True(t, before.HasAudio)

	seg3 := createH264SegmentWithAudio(t, dir, "c.mp4", covH264SPS, covH264PPS,
		[][]byte{covH264IDR, covH264P}, testAudioConfig, [][]byte{testAACFrame, testAACFrame})
	info3, err := ParseSegment(seg3)
	require.NoError(t, err)

	require.NoError(t, AppendMP4Samples(base, []*SegmentInfo{info3}))

	grown, err := ParseSegment(hour)
	require.NoError(t, err)
	require.Equal(t, before.SampleCount+info3.SampleCount, grown.SampleCount)
	require.Equal(t, before.AudioSampleCount+info3.AudioSampleCount, grown.AudioSampleCount)
	require.Equal(t, before.MdatOffset+grown.MdatSize, grown.MoovOffset,
		"moov must still sit right after mdat so the file stays appendable")
	require.True(t, segmentAppendable(grown, mustSize(t, hour)))
}

func TestCovMerge_AppendMultipleSegments(t *testing.T) {
	dir, hour, base := covHourFile(t)

	seg3 := createH264SegmentWithSamples(t, dir, "c.mp4", covH264SPS, covH264PPS, [][]byte{covH264IDR})
	seg4 := createH264SegmentWithSamples(t, dir, "d.mp4", covH264SPS, covH264PPS, [][]byte{covH264P, covH264P})
	info3, err := ParseSegment(seg3)
	require.NoError(t, err)
	info4, err := ParseSegment(seg4)
	require.NoError(t, err)

	require.NoError(t, AppendMP4Samples(base, []*SegmentInfo{info3, info4}))
	grown, err := ParseSegment(hour)
	require.NoError(t, err)
	require.Equal(t, base.SampleCount+3, grown.SampleCount)
}

func mustSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	return fi.Size()
}

// --- AppendMP4Samples argument validation --------------------------------

func TestCovMerge_AppendNothingToAppend(t *testing.T) {
	_, _, base := covHourFile(t)

	err := AppendMP4Samples(nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nothing to append")

	empty := &SegmentInfo{}
	err = AppendMP4Samples(empty, []*SegmentInfo{base})
	require.Error(t, err)

	err = AppendMP4Samples(base, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nothing to append")
}

func TestCovMerge_AppendCodecMismatch(t *testing.T) {
	_, _, base := covHourFile(t)
	other := covCopyInfo(base)
	other.Codec = "h265"
	err := AppendMP4Samples(base, []*SegmentInfo{other})
	require.Error(t, err)
	require.Contains(t, err.Error(), "codec mismatch")
}

// --- AppendMP4Samples error branches -------------------------------------

func TestCovMerge_AppendStatError(t *testing.T) {
	_, _, base := covHourFile(t)
	missing := covCopyInfo(base)
	missing.FilePath = filepath.Join(t.TempDir(), "gone.mp4")
	err := AppendMP4Samples(missing, []*SegmentInfo{covCopyInfo(base)})
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestCovMerge_AppendNotAppendable(t *testing.T) {
	dir, _, base := covHourFile(t)
	// Trailing junk after moov means moov no longer ends at EOF: not appendable.
	bad := filepath.Join(dir, "trailing.mp4")
	require.NoError(t, copyFileForTest(base.FilePath, bad))
	f, err := os.OpenFile(bad, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.Write([]byte("junk"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	patched := covCopyInfo(base)
	patched.FilePath = bad
	newSeg := createH264SegmentWithSamples(t, dir, "new.mp4", covH264SPS, covH264PPS, [][]byte{covH264IDR})
	newInfo, err := ParseSegment(newSeg)
	require.NoError(t, err)

	err = AppendMP4Samples(patched, []*SegmentInfo{newInfo})
	require.ErrorIs(t, err, errAppendNotPossible)
}

func TestCovMerge_AppendOpenError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not enforced the same way on Windows")
	}
	dir, _, base := covHourFile(t)
	require.NoError(t, os.Chmod(base.FilePath, 0o400))
	t.Cleanup(func() { _ = os.Chmod(base.FilePath, 0o600) })

	newSeg := createH264SegmentWithSamples(t, dir, "new.mp4", covH264SPS, covH264PPS, [][]byte{covH264IDR})
	newInfo, err := ParseSegment(newSeg)
	require.NoError(t, err)

	err = AppendMP4Samples(base, []*SegmentInfo{newInfo})
	require.Error(t, err)
	require.ErrorIs(t, err, os.ErrPermission)
}

func TestCovMerge_AppendMdatHeaderMismatch(t *testing.T) {
	_, _, base := covHourFile(t)

	dir := t.TempDir()
	bad := filepath.Join(dir, "hour.mp4")
	data, err := os.ReadFile(base.FilePath)
	require.NoError(t, err)
	// Corrupt the mdat size field while keeping the file length identical.
	binary.BigEndian.PutUint32(data[base.MdatOffset:], 1234)
	require.NoError(t, os.WriteFile(bad, data, 0o644))

	patched := covCopyInfo(base)
	patched.FilePath = bad

	err = AppendMP4Samples(patched, []*SegmentInfo{covCopyInfo(base)})
	require.ErrorIs(t, err, errAppendNotPossible)
}

func TestCovMerge_AppendZeroPayload(t *testing.T) {
	_, _, base := covHourFile(t)
	emptySamples := covCopyInfo(base)
	emptySamples.Samples = nil
	emptySamples.AudioSamples = nil
	emptySamples.HasAudio = false

	err := AppendMP4Samples(base, []*SegmentInfo{emptySamples})
	require.ErrorIs(t, err, errAppendNotPossible)
}

func TestCovMerge_AppendTempCreateError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not enforced the same way on Windows")
	}
	dir, _, base := covHourFile(t)
	roDir := filepath.Join(dir, "ro")
	require.NoError(t, os.MkdirAll(roDir, 0o755))
	roPath := filepath.Join(roDir, "hour.mp4")
	require.NoError(t, copyFileForTest(base.FilePath, roPath))

	info, err := ParseSegmentTables(roPath)
	require.NoError(t, err)
	newSeg := createH264SegmentWithSamples(t, dir, "new.mp4", covH264SPS, covH264PPS, [][]byte{covH264IDR})
	newInfo, err := ParseSegment(newSeg)
	require.NoError(t, err)

	require.NoError(t, os.Chmod(roDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })

	err = AppendMP4Samples(info, []*SegmentInfo{newInfo})
	require.Error(t, err)
	require.ErrorIs(t, err, os.ErrPermission)
}

func TestCovMerge_AppendSourceOpenError(t *testing.T) {
	dir, _, base := covHourFile(t)
	newSeg := createH264SegmentWithSamples(t, dir, "new.mp4", covH264SPS, covH264PPS, [][]byte{covH264IDR})
	newInfo, err := ParseSegment(newSeg)
	require.NoError(t, err)

	broken := covCopyInfo(newInfo)
	broken.FilePath = filepath.Join(dir, "missing.mp4")
	err = AppendMP4Samples(base, []*SegmentInfo{broken})
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestCovMerge_AppendSourceCopyError(t *testing.T) {
	dir, _, base := covHourFile(t)
	newSeg := createH264SegmentWithSamples(t, dir, "new.mp4", covH264SPS, covH264PPS, [][]byte{covH264IDR, covH264P})
	newInfo, err := ParseSegment(newSeg)
	require.NoError(t, err)

	badOffset := covCopyInfo(newInfo)
	badOffset.Samples[0].Offset = -1
	err = AppendMP4Samples(base, []*SegmentInfo{badOffset})
	require.Error(t, err)
	require.Contains(t, err.Error(), "copy sample")
}

func TestCovMerge_AppendAudioSourceCopyError(t *testing.T) {
	dir, _, base := covHourFileWithAudio(t)
	newSeg := createH264SegmentWithAudio(t, dir, "new.mp4", covH264SPS, covH264PPS,
		[][]byte{covH264IDR}, testAudioConfig, [][]byte{testAACFrame})
	newInfo, err := ParseSegment(newSeg)
	require.NoError(t, err)

	badAudio := covCopyInfo(newInfo)
	require.NotEmpty(t, badAudio.AudioSamples)
	badAudio.AudioSamples[0].Offset = -1
	err = AppendMP4Samples(base, []*SegmentInfo{badAudio})
	require.Error(t, err)
	require.Contains(t, err.Error(), "copy sample")
}

// --- segmentAppendable branches -----------------------------------------

func TestCovMerge_SegmentAppendableBranches(t *testing.T) {
	require.False(t, segmentAppendable(nil, 100))
	require.False(t, segmentAppendable(&SegmentInfo{}, 100))

	valid := &SegmentInfo{
		FilePath:   "/tmp/x.mp4",
		MdatOffset: 32,
		MdatSize:   100,
		MoovOffset: 132,
		MoovSize:   50,
	}
	require.True(t, segmentAppendable(valid, 182))

	negMdat := *valid
	negMdat.MdatOffset = -1
	require.False(t, segmentAppendable(&negMdat, 182))

	smallMdat := *valid
	smallMdat.MdatSize = 4
	require.False(t, segmentAppendable(&smallMdat, 182))

	noMoov := *valid
	noMoov.MoovOffset = 0
	require.False(t, segmentAppendable(&noMoov, 182))

	tinyMoov := *valid
	tinyMoov.MoovSize = 8
	require.False(t, segmentAppendable(&tinyMoov, 182))

	gapMoov := *valid
	gapMoov.MoovOffset = 200
	require.False(t, segmentAppendable(&gapMoov, 250))

	require.False(t, segmentAppendable(valid, 181), "file size must match moov end")

	hugeMdat := *valid
	hugeMdat.MdatSize = 1 << 33
	hugeMdat.MoovOffset = hugeMdat.MdatOffset + hugeMdat.MdatSize
	hugeMdat.MoovSize = 50
	require.False(t, segmentAppendable(&hugeMdat, hugeMdat.MoovOffset+50))
}

// --- track / chunk / moov helpers ---------------------------------------

func TestCovMerge_ChunksFromSamplesGapAndEmpty(t *testing.T) {
	require.Nil(t, chunksFromSamples(nil))

	samples := []mergedSample{
		{offset: 100, size: 10},
		{offset: 110, size: 10},
		{offset: 500, size: 5}, // non-contiguous -> new chunk
	}
	chunks := chunksFromSamples(samples)
	require.Len(t, chunks, 2)
	require.Equal(t, int64(100), chunks[0].offset)
	require.Len(t, chunks[0].samples, 2)
	require.Equal(t, int64(500), chunks[1].offset)
}

func TestCovMerge_TrackFromSamplesAudio(t *testing.T) {
	base := &SegmentInfo{
		Codec:          "h264",
		SPS:            []byte{1, 2, 3, 4},
		PPS:            []byte{5},
		Timescale:      90000,
		Width:          640,
		Height:         480,
		AudioConfig:    []byte{0x12, 0x10},
		AudioTimescale: 44100,
	}
	audio := trackFromSamples(base, true, []mergedSample{{offset: 10, size: 4, duration: 1024}})
	require.True(t, audio.isAudio)
	require.Equal(t, uint32(1024), audio.duration)
	require.Equal(t, uint32(44100), audio.timescale)
	require.Equal(t, base.AudioConfig, audio.audioConfig)

	video := trackFromSamples(base, false, []mergedSample{{offset: 10, size: 4, duration: 3000}})
	require.False(t, video.isAudio)
	require.False(t, video.isH265)
	require.Equal(t, uint32(90000), video.timescale)
	require.Equal(t, uint16(640), video.width)

	h265 := &SegmentInfo{Codec: "h265", VPS: []byte{1}, SPS: []byte{2}, PPS: []byte{3}, Timescale: 90000}
	require.True(t, trackFromSamples(h265, false, nil).isH265)
}

func TestCovMerge_BuildMoovBytesChunkOverflow(t *testing.T) {
	tr := &mergeTrack{
		sps:       covH264SPS,
		pps:       covH264PPS,
		timescale: 90000,
		chunks: []mergeChunk{{
			offset:  1 << 33, // overflows stco's uint32
			samples: []mergedSample{{offset: 1 << 33, size: 4, duration: 3000}},
		}},
	}
	_, err := buildMoovBytes(tr, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "overflows stco")
}

func TestCovMerge_ValidateMergeSegmentsErrors(t *testing.T) {
	require.Error(t, validateMergeSegments(nil))

	base := &SegmentInfo{Codec: "h264", SPS: []byte{1, 2}, PPS: []byte{3}}
	require.NoError(t, validateMergeSegments([]*SegmentInfo{base, {Codec: "h264", SPS: []byte{1, 2}, PPS: []byte{3}}}))

	err := validateMergeSegments([]*SegmentInfo{base, {Codec: "h265"}})
	require.ErrorContains(t, err, "codec mismatch")

	err = validateMergeSegments([]*SegmentInfo{base, {Codec: "h264", SPS: []byte{9}, PPS: []byte{3}}})
	require.ErrorContains(t, err, "SPS/PPS mismatch")

	err = validateMergeSegments([]*SegmentInfo{base, {Codec: "h264", SPS: []byte{1, 2}, PPS: []byte{9}}})
	require.ErrorContains(t, err, "SPS/PPS mismatch")

	h265 := &SegmentInfo{Codec: "h265", SPS: []byte{1}, PPS: []byte{2}, VPS: []byte{3}}
	err = validateMergeSegments([]*SegmentInfo{h265, {Codec: "h265", SPS: []byte{1}, PPS: []byte{2}, VPS: []byte{4}}})
	require.ErrorContains(t, err, "VPS mismatch")

	audio := &SegmentInfo{Codec: "h264", SPS: []byte{1}, PPS: []byte{2}, HasAudio: true, AudioConfig: []byte{0x12}}
	err = validateMergeSegments([]*SegmentInfo{audio, {Codec: "h264", SPS: []byte{1}, PPS: []byte{2}}})
	require.ErrorContains(t, err, "audio presence mismatch")

	err = validateMergeSegments([]*SegmentInfo{audio, {Codec: "h264", SPS: []byte{1}, PPS: []byte{2}, HasAudio: true, AudioConfig: []byte{0x99}}})
	require.ErrorContains(t, err, "audio config mismatch")
}

func TestCovMerge_BytesEqual(t *testing.T) {
	require.True(t, bytesEqual(nil, nil))
	require.True(t, bytesEqual([]byte{1, 2}, []byte{1, 2}))
	require.False(t, bytesEqual([]byte{1}, []byte{1, 2}))
	require.False(t, bytesEqual([]byte{1, 2}, []byte{1, 3}))
}

// --- Journal write/read/restore -----------------------------------------

type covErrWriter struct{ err error }

func (w covErrWriter) Write([]byte) (int, error) { return 0, w.err }

func TestCovMerge_WriteJournalStringErrors(t *testing.T) {
	require.Error(t, writeJournalString(covErrWriter{errors.New("boom")}, "abc"))
	require.Error(t, writeJournalString(covErrWriter{errors.New("boom")}, ""))

	var buf bytes.Buffer
	require.NoError(t, writeJournalString(&buf, "hi"))
	require.ErrorContains(t, writeJournalString(&buf, strings.Repeat("a", 0x10000)), "too long")
}

func TestCovMerge_WriteMergeJournalInvalid(t *testing.T) {
	dir := t.TempDir()
	mp4Path := filepath.Join(dir, "hour.mp4")
	require.NoError(t, os.WriteFile(mp4Path, []byte("x"), 0o644))

	err := writeMergeJournal(mp4Path, mergeJournal{ids: []string{"a"}, paths: nil})
	require.ErrorContains(t, err, "invalid merge journal")

	err = writeMergeJournal(mp4Path, mergeJournal{moovSize: 10, oldMoov: []byte("short")})
	require.ErrorContains(t, err, "invalid merge journal")
}

func TestCovMerge_WriteMergeJournalCreateError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not enforced the same way on Windows")
	}
	dir := t.TempDir()
	ro := filepath.Join(dir, "ro")
	require.NoError(t, os.Mkdir(ro, 0o755))
	mp4Path := filepath.Join(ro, "hour.mp4")
	require.NoError(t, os.Chmod(ro, 0o500))
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })

	err := writeMergeJournal(mp4Path, mergeJournal{moovSize: 1, oldMoov: []byte("m")})
	require.Error(t, err)
	require.ErrorIs(t, err, os.ErrPermission)
}

func TestCovMerge_JournalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	mp4Path := filepath.Join(dir, "hour.mp4")
	require.NoError(t, os.WriteFile(mp4Path, []byte("media"), 0o644))

	j := mergeJournal{
		mdatOffset: 4,
		moovOffset: 8,
		moovSize:   4,
		mdatSize:   12,
		ids:        []string{"id-1", "id-2"},
		paths:      []string{filepath.Join(dir, "s1.mp4"), filepath.Join(dir, "s2.mp4")},
		oldMoov:    []byte("moov"),
	}
	require.NoError(t, writeMergeJournal(mp4Path, j))

	got, err := readMergeJournal(mp4Path)
	require.NoError(t, err)
	require.Equal(t, j, got)
}

func TestCovMerge_ReadMergeJournalErrors(t *testing.T) {
	dir := t.TempDir()
	mp4Path := filepath.Join(dir, "hour.mp4")
	journalFile := journalPath(mp4Path)

	write := func(data []byte) {
		require.NoError(t, os.WriteFile(journalFile, data, 0o644))
	}
	header := func(version uint32) []byte {
		var buf bytes.Buffer
		buf.WriteString("NVRJ")
		_ = binary.Write(&buf, binary.BigEndian, version)
		return buf.Bytes()
	}

	// Missing journal.
	_, err := readMergeJournal(mp4Path)
	require.ErrorIs(t, err, os.ErrNotExist)

	// Empty file -> magic read fails.
	write(nil)
	_, err = readMergeJournal(mp4Path)
	require.Error(t, err)

	// Bad magic.
	write([]byte("XXXX\x00\x00\x00\x01"))
	_, err = readMergeJournal(mp4Path)
	require.ErrorContains(t, err, "bad merge journal magic")

	// Truncated version.
	write([]byte("NVRJ\x00"))
	_, err = readMergeJournal(mp4Path)
	require.Error(t, err)

	// Unsupported version.
	write(header(2))
	_, err = readMergeJournal(mp4Path)
	require.ErrorContains(t, err, "unsupported merge journal version")

	// Truncated numeric fields (moovOffset missing).
	truncated := append(header(1), make([]byte, 4)...)
	write(truncated)
	_, err = readMergeJournal(mp4Path)
	require.Error(t, err)

	// Fully truncated after mdatSize.
	var buf bytes.Buffer
	buf.WriteString("NVRJ")
	_ = binary.Write(&buf, binary.BigEndian, uint32(1))
	_ = binary.Write(&buf, binary.BigEndian, int64(0))
	_ = binary.Write(&buf, binary.BigEndian, int64(0))
	_ = binary.Write(&buf, binary.BigEndian, int64(0))
	_ = binary.Write(&buf, binary.BigEndian, uint32(0))
	write(buf.Bytes())
	_, err = readMergeJournal(mp4Path)
	require.Error(t, err) // count read fails

	// Count greater than the strings actually present.
	buf.Reset()
	buf.WriteString("NVRJ")
	_ = binary.Write(&buf, binary.BigEndian, uint32(1))
	_ = binary.Write(&buf, binary.BigEndian, int64(0))
	_ = binary.Write(&buf, binary.BigEndian, int64(0))
	_ = binary.Write(&buf, binary.BigEndian, int64(0))
	_ = binary.Write(&buf, binary.BigEndian, uint32(0))
	_ = binary.Write(&buf, binary.BigEndian, uint32(1))
	write(buf.Bytes())
	_, err = readMergeJournal(mp4Path)
	require.Error(t, err) // id string length read fails

	// String claims 4 bytes but the journal ends after 2.
	buf.Reset()
	buf.WriteString("NVRJ")
	_ = binary.Write(&buf, binary.BigEndian, uint32(1))
	_ = binary.Write(&buf, binary.BigEndian, int64(0))
	_ = binary.Write(&buf, binary.BigEndian, int64(0))
	_ = binary.Write(&buf, binary.BigEndian, int64(0))
	_ = binary.Write(&buf, binary.BigEndian, uint32(0))
	_ = binary.Write(&buf, binary.BigEndian, uint32(1))
	_ = binary.Write(&buf, binary.BigEndian, uint16(4))
	buf.WriteString("ab")
	write(buf.Bytes())
	_, err = readMergeJournal(mp4Path)
	require.Error(t, err)

	// moovSize claims bytes that are not present.
	buf.Reset()
	buf.WriteString("NVRJ")
	_ = binary.Write(&buf, binary.BigEndian, uint32(1))
	_ = binary.Write(&buf, binary.BigEndian, int64(0))
	_ = binary.Write(&buf, binary.BigEndian, int64(0))
	_ = binary.Write(&buf, binary.BigEndian, int64(10))
	_ = binary.Write(&buf, binary.BigEndian, uint32(0))
	_ = binary.Write(&buf, binary.BigEndian, uint32(0))
	buf.WriteString("mo")
	write(buf.Bytes())
	_, err = readMergeJournal(mp4Path)
	require.Error(t, err)
}

func TestCovMerge_ReadMergeJournalEveryTruncation(t *testing.T) {
	dir := t.TempDir()
	mp4Path := filepath.Join(dir, "hour.mp4")
	require.NoError(t, os.WriteFile(mp4Path, []byte("media"), 0o644))

	j := mergeJournal{
		mdatOffset: 4,
		moovOffset: 8,
		moovSize:   4,
		mdatSize:   12,
		ids:        []string{"id-1"},
		paths:      []string{filepath.Join(dir, "s1.mp4")},
		oldMoov:    []byte("moov"), // moovSize must equal len(oldMoov)
	}
	require.NoError(t, writeMergeJournal(mp4Path, j))
	full, err := os.ReadFile(journalPath(mp4Path))
	require.NoError(t, err)

	for cut := 0; cut < len(full); cut++ {
		require.NoError(t, os.WriteFile(journalPath(mp4Path), full[:cut], 0o644))
		_, err := readMergeJournal(mp4Path)
		require.Error(t, err, "truncated journal at %d bytes must not parse", cut)
	}
}

func TestCovMerge_ReadJournalStringTruncated(t *testing.T) {
	_, err := readJournalString(bytes.NewReader([]byte{0x00}))
	require.Error(t, err)

	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.BigEndian, uint16(5))
	buf.WriteString("ab")
	_, err = readJournalString(&buf)
	require.Error(t, err)
}

func TestCovMerge_RestoreJournalFile(t *testing.T) {
	dir := t.TempDir()
	mp4Path := filepath.Join(dir, "hour.mp4")
	require.NoError(t, os.WriteFile(mp4Path, []byte("0123456789"), 0o644))

	j := mergeJournal{
		mdatOffset: 0,
		moovOffset: 4,
		moovSize:   2,
		mdatSize:   4,
		oldMoov:    []byte("MO"),
	}
	require.NoError(t, restoreJournalFile(mp4Path, j))

	data, err := os.ReadFile(mp4Path)
	require.NoError(t, err)
	require.Equal(t, []byte{0, 0, 0, 4, 'M', 'O'}, data)

	err = restoreJournalFile(filepath.Join(dir, "missing.mp4"), j)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestCovMerge_HideUnhideConsumed(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "seg.mp4")
	require.NoError(t, os.WriteFile(src, []byte("data"), 0o644))

	require.NoError(t, hideConsumed(src))
	_, err := os.Stat(src)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(consumedPath(src))
	require.NoError(t, err)

	unhideConsumed([]string{src, filepath.Join(dir, "never-hidden.mp4")})
	_, err = os.Stat(src)
	require.NoError(t, err)
	_, err = os.Stat(consumedPath(src))
	require.ErrorIs(t, err, os.ErrNotExist)

	require.ErrorIs(t, hideConsumed(filepath.Join(dir, "missing.mp4")), os.ErrNotExist)
}

// covRepo is a minimal storage.MergeRepository fake used by journal tests.
type covRepo struct {
	storage.MergeRepository
	recs       map[string]*model.Recording
	getErr     error
	mergeErr   error
	statusErr  error
	growErr    error
	listErr    error
	windows    []storage.MergeWindow
	singletons []*model.Recording
	segments   []*model.Recording
	buckets    []*model.Recording
	cameras    []string
	calls      *covRepoCalls
}

type covRepoCalls struct {
	created int
	merged  int
	grown   int
}

func newCovRepo() *covRepo {
	return &covRepo{recs: map[string]*model.Recording{}, calls: &covRepoCalls{}}
}

func (r *covRepo) GetRecording(_ context.Context, id string) (*model.Recording, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.recs[id], nil
}

func (r *covRepo) ListCameraMergeWindows(_ context.Context, _ string, _, _ time.Duration) ([]storage.MergeWindow, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.windows, nil
}

func (r *covRepo) ListPendingMergeCameraIDs(context.Context) ([]string, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.cameras, nil
}

func (r *covRepo) ListMergeableSegments(context.Context, string, time.Time, time.Time) ([]*model.Recording, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.segments, nil
}

func (r *covRepo) ListSingletonPendingRecordings(context.Context, string, time.Duration, time.Duration) ([]*model.Recording, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.singletons, nil
}

func (r *covRepo) SetMergeStatus(context.Context, []string, string) error { return r.statusErr }

func (r *covRepo) MergeAndReplaceRecordings(context.Context, *model.Recording, []string) error {
	if r.mergeErr != nil {
		return r.mergeErr
	}
	r.calls.merged++
	return nil
}

func (r *covRepo) ListWindowMergedRecordings(context.Context, string, time.Time, time.Time) ([]*model.Recording, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.buckets, nil
}

func (r *covRepo) GrowMergedRecording(context.Context, *model.Recording, []string) error {
	if r.growErr != nil {
		return r.growErr
	}
	r.calls.grown++
	return nil
}

func TestCovMerge_ReconcileJournalErrors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mp4Path := filepath.Join(dir, "hour.mp4")
	require.NoError(t, os.WriteFile(mp4Path, []byte("0123456789"), 0o644))

	// No journal at all -> nil.
	require.NoError(t, reconcileMergeJournal(ctx, newCovRepo(), mp4Path))

	// Corrupt journal -> error.
	require.NoError(t, os.WriteFile(journalPath(mp4Path), []byte("XXXX"), 0o644))
	require.Error(t, reconcileMergeJournal(ctx, newCovRepo(), mp4Path))
	require.NoError(t, os.Remove(journalPath(mp4Path)))

	// Journal exists but DB lookup fails.
	journal := mergeJournal{mdatOffset: 0, moovOffset: 4, moovSize: 2, mdatSize: 4,
		ids: []string{"id"}, paths: []string{filepath.Join(dir, "s.mp4")}, oldMoov: []byte("MO")}
	require.NoError(t, writeMergeJournal(mp4Path, journal))
	repo := newCovRepo()
	repo.getErr = errors.New("db down")
	require.ErrorContains(t, reconcileMergeJournal(ctx, repo, mp4Path), "db down")

	// Restore target is missing -> restore error.
	repo = newCovRepo()
	repo.recs["id"] = &model.Recording{ID: "id"}
	missing := filepath.Join(dir, "no-hour.mp4")
	require.NoError(t, writeMergeJournal(missing, journal))
	err := reconcileMergeJournal(ctx, repo, missing)
	require.Error(t, err)
}

func TestCovMerge_ReconcileJournalPendingRestores(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mp4Path := filepath.Join(dir, "hour.mp4")
	original := []byte("0123456789")
	require.NoError(t, os.WriteFile(mp4Path, original, 0o644))

	src := filepath.Join(dir, "seg.mp4")
	require.NoError(t, os.WriteFile(src, []byte("segdata"), 0o644))
	require.NoError(t, hideConsumed(src))

	journal := mergeJournal{mdatOffset: 0, moovOffset: 4, moovSize: 2, mdatSize: 4,
		ids: []string{"pending"}, paths: []string{src}, oldMoov: []byte("MO")}
	require.NoError(t, writeMergeJournal(mp4Path, journal))

	repo := newCovRepo()
	repo.recs["pending"] = &model.Recording{ID: "pending"}
	require.NoError(t, reconcileMergeJournal(ctx, repo, mp4Path))

	restored, err := os.ReadFile(mp4Path)
	require.NoError(t, err)
	require.Equal(t, []byte{0, 0, 0, 4, 'M', 'O'}, restored)

	_, err = os.Stat(src)
	require.NoError(t, err, "hidden source should be restored")
	_, err = os.Stat(journalPath(mp4Path))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestCovMerge_ReconcileJournalCompletedDropsConsumed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mp4Path := filepath.Join(dir, "hour.mp4")
	require.NoError(t, os.WriteFile(mp4Path, []byte("0123456789"), 0o644))

	src := filepath.Join(dir, "seg.mp4")
	require.NoError(t, os.WriteFile(src, []byte("segdata"), 0o644))
	require.NoError(t, hideConsumed(src))

	journal := mergeJournal{mdatOffset: 0, moovOffset: 4, moovSize: 2, mdatSize: 4,
		ids: []string{"gone"}, paths: []string{src}, oldMoov: []byte("MO")}
	require.NoError(t, writeMergeJournal(mp4Path, journal))

	repo := newCovRepo() // returns nil recording -> append is kept
	require.NoError(t, reconcileMergeJournal(ctx, repo, mp4Path))

	_, err := os.Stat(consumedPath(src))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(mp4Path)
	require.NoError(t, err)
}

func TestCovMerge_ReconcileJournalUnhideRenameError(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mp4Path := filepath.Join(dir, "hour.mp4")
	require.NoError(t, os.WriteFile(mp4Path, []byte("0123456789"), 0o644))

	// Make the hidden entry a directory and the destination an existing file:
	// renaming a directory onto a file fails on all supported platforms.
	src := filepath.Join(dir, "seg.mp4")
	require.NoError(t, os.WriteFile(src, []byte("segdata"), 0o644))
	hidden := consumedPath(src)
	require.NoError(t, os.Mkdir(hidden, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hidden, "child"), []byte("x"), 0o644))

	journal := mergeJournal{mdatOffset: 0, moovOffset: 4, moovSize: 2, mdatSize: 4,
		ids: []string{"pending"}, paths: []string{src}, oldMoov: []byte("MO")}
	require.NoError(t, writeMergeJournal(mp4Path, journal))

	repo := newCovRepo()
	repo.recs["pending"] = &model.Recording{ID: "pending"}
	require.Error(t, reconcileMergeJournal(ctx, repo, mp4Path))
}

// copyFileForTest duplicates a file (small merge fixtures only).
func copyFileForTest(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
