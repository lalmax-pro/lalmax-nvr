package muxer

import (
	"bytes"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/abema/go-mp4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memWriteSeeker is an in-memory io.WriteSeeker so box-writing helpers can be
// exercised without touching the filesystem.
type memWriteSeeker struct {
	buf []byte
	off int64
}

func (m *memWriteSeeker) Write(p []byte) (int, error) {
	end := m.off + int64(len(p))
	if end > int64(len(m.buf)) {
		m.buf = append(m.buf, make([]byte, end-int64(len(m.buf)))...)
	}
	copy(m.buf[m.off:], p)
	m.off = end
	return len(p), nil
}

func (m *memWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case 0:
		base = 0
	case 1:
		base = m.off
	case 2:
		base = int64(len(m.buf))
	default:
		return 0, errors.New("unsupported whence")
	}
	pos := base + offset
	if pos < 0 {
		return 0, errors.New("negative position")
	}
	if pos > int64(len(m.buf)) {
		m.buf = append(m.buf, make([]byte, pos-int64(len(m.buf)))...)
	}
	m.off = pos
	return pos, nil
}

func newTestBoxWriter() (*mp4.Writer, *memWriteSeeker) {
	ms := &memWriteSeeker{}
	return mp4.NewWriter(ms), ms
}

// testBitWriter builds synthetic SPS payloads bit by bit.
type testBitWriter struct {
	bits []bool
}

func (w *testBitWriter) putBit(v int) {
	w.bits = append(w.bits, v != 0)
}

func (w *testBitWriter) putBits(n int, v uint64) {
	for i := n - 1; i >= 0; i-- {
		w.putBit(int(v>>uint(i)) & 1)
	}
}

// putUE writes an unsigned Exp-Golomb value.
func (w *testBitWriter) putUE(v int) {
	uv := uint64(v) + 1
	n := 0
	for x := uv; x > 1; x >>= 1 {
		n++
	}
	w.putBits(n, 0)
	w.putBits(n+1, uv)
}

// putSE writes a signed Exp-Golomb value.
func (w *testBitWriter) putSE(v int) {
	if v <= 0 {
		w.putUE(-2 * v)
		return
	}
	w.putUE(2*v - 1)
}

// bytes terminates the RBSP and returns whole bytes.
func (w *testBitWriter) bytes() []byte {
	w.putBit(1) // rbsp_stop_one_bit
	for len(w.bits)%8 != 0 {
		w.putBit(0)
	}
	out := make([]byte, len(w.bits)/8)
	for i, b := range w.bits {
		if b {
			out[i/8] |= 1 << uint(7-i%8)
		}
	}
	return out
}

func testHighProfile(profileIDC int) bool {
	switch profileIDC {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134:
		return true
	}
	return false
}

// syntheticH264SPS describes the fields parseSPSResolution consumes.
type syntheticH264SPS struct {
	profileIDC      int
	chromaFormatIDC int
	scalingMatrix   bool
	pocType         int
	numRefFrames    int
	picWidthMbs     int
	picHeightMap    int
	frameMbsOnly    int
	crop            [4]int // crop_left/right/top/bottom_minus1
}

func buildSyntheticH264SPS(spec syntheticH264SPS) []byte {
	w := &testBitWriter{}
	w.putBits(8, uint64(spec.profileIDC))
	w.putBits(8, 0)  // constraint_set_flags
	w.putBits(8, 30) // level_idc
	w.putUE(0)       // seq_parameter_set_id
	if testHighProfile(spec.profileIDC) {
		w.putUE(spec.chromaFormatIDC)
		if spec.chromaFormatIDC == 3 {
			w.putBit(0) // separate_colour_plane_flag
		}
		w.putUE(0) // bit_depth_luma_minus8
		w.putUE(0) // bit_depth_chroma_minus8
		w.putBit(0)
		if spec.scalingMatrix {
			w.putBit(1) // seq_scaling_matrix_present_flag
			count := 8
			if spec.chromaFormatIDC == 3 {
				count = 12
			}
			for i := 0; i < count; i++ {
				// Present the 4x4 luma matrix and one 8x8 chroma matrix to cover
				// both delta sizes.
				if i == 0 || i == 6 {
					w.putBit(1)
					size := 16
					if i >= 6 {
						size = 64
					}
					for j := 0; j < size; j++ {
						if j%3 == 0 {
							w.putSE(-8)
						} else {
							w.putSE(0)
						}
					}
				} else {
					w.putBit(0)
				}
			}
		} else {
			w.putBit(0)
		}
	}
	w.putUE(0) // log2_max_frame_num_minus4
	w.putUE(spec.pocType)
	switch spec.pocType {
	case 0:
		w.putUE(0) // log2_max_pic_order_cnt_lsb_minus4
	case 1:
		w.putBit(0) // delta_pic_order_always_zero_flag
		w.putSE(0)  // offset_for_non_ref_pic
		w.putSE(0)  // offset_for_top_to_bottom_field
		w.putUE(spec.numRefFrames)
		for i := 0; i < spec.numRefFrames; i++ {
			w.putSE(0)
		}
	}
	w.putUE(1)  // max_num_ref_frames
	w.putBit(0) // gaps_in_frame_num_value_allowed_flag
	w.putUE(spec.picWidthMbs - 1)
	w.putUE(spec.picHeightMap - 1)
	w.putBit(spec.frameMbsOnly)
	if spec.frameMbsOnly == 0 {
		w.putBit(0) // mb_adaptive_frame_field_flag
	}
	w.putBit(1) // direct_8x8_inference_flag
	cropping := spec.crop != [4]int{}
	if cropping {
		w.putBit(1)
		for _, c := range spec.crop {
			w.putUE(c)
		}
	} else {
		w.putBit(0)
	}
	return append([]byte{0x67}, w.bytes()...)
}

// buildSyntheticHEVCSPS builds an HEVC SPS including its 2-byte NAL header.
func buildSyntheticHEVCSPS(width, height, maxSubLayersMinus1 int) []byte {
	return buildSyntheticHEVCSPSChroma(width, height, maxSubLayersMinus1, 1)
}

func buildSyntheticHEVCSPSChroma(width, height, maxSubLayersMinus1, chromaFormatIDC int) []byte {
	w := &testBitWriter{}
	w.putBits(4, 0) // sps_video_parameter_set_id
	w.putBits(3, uint64(maxSubLayersMinus1))
	w.putBit(1)       // sps_temporal_id_nesting_flag
	w.putBits(8, 1)   // profile_space/tier/profile_idc
	w.putBits(32, 0)  // general_profile_compatibility_flag
	w.putBits(48, 0)  // general_constraint_indicator_flags
	w.putBits(8, 120) // general_level_idc
	for i := 0; i < maxSubLayersMinus1; i++ {
		w.putBits(2, 0) // sub_layer_profile_present_flag + level_present_flag
	}
	for i := 0; i < maxSubLayersMinus1; i++ {
		w.putBit(0) // sub_layer_level_present_flag
	}
	w.putUE(0) // sps_seq_parameter_set_id
	w.putUE(chromaFormatIDC)
	if chromaFormatIDC == 3 {
		w.putBit(0) // separate_colour_plane_flag
	}
	w.putUE(width)
	w.putUE(height)
	return append([]byte{0x42, 0x01}, w.bytes()...)
}

func TestParseSPSResolutionBranches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                  string
		spec                  syntheticH264SPS
		wantWidth, wantHeight int
	}{
		{
			name:       "high profile 4:2:0 with scaling matrices",
			spec:       syntheticH264SPS{profileIDC: 100, chromaFormatIDC: 1, scalingMatrix: true, picWidthMbs: 80, picHeightMap: 45, frameMbsOnly: 1},
			wantWidth:  1280,
			wantHeight: 720,
		},
		{
			name:       "high profile 4:4:4 with separate colour plane",
			spec:       syntheticH264SPS{profileIDC: 244, chromaFormatIDC: 3, scalingMatrix: true, picWidthMbs: 40, picHeightMap: 30, frameMbsOnly: 1},
			wantWidth:  640,
			wantHeight: 480,
		},
		{
			name:       "high profile 4:4:4 cropping uses units 1:1",
			spec:       syntheticH264SPS{profileIDC: 244, chromaFormatIDC: 3, picWidthMbs: 40, picHeightMap: 30, frameMbsOnly: 1, crop: [4]int{1, 1, 1, 1}},
			wantWidth:  638,
			wantHeight: 478,
		},
		{
			name:       "high profile monochrome cropping uses units 1:1",
			spec:       syntheticH264SPS{profileIDC: 110, chromaFormatIDC: 0, picWidthMbs: 120, picHeightMap: 68, frameMbsOnly: 1, crop: [4]int{1, 1, 1, 1}},
			wantWidth:  1918,
			wantHeight: 1086,
		},
		{
			name:       "high profile 4:2:2 cropping uses units 2:1",
			spec:       syntheticH264SPS{profileIDC: 122, chromaFormatIDC: 2, picWidthMbs: 120, picHeightMap: 68, frameMbsOnly: 1, crop: [4]int{2, 2, 2, 2}},
			wantWidth:  1912,
			wantHeight: 1084,
		},
		{
			name:       "poc type 1 with reference frames",
			spec:       syntheticH264SPS{profileIDC: 66, pocType: 1, numRefFrames: 3, picWidthMbs: 40, picHeightMap: 30, frameMbsOnly: 1},
			wantWidth:  640,
			wantHeight: 480,
		},
		{
			name:       "interlaced frame doubles height",
			spec:       syntheticH264SPS{profileIDC: 66, picWidthMbs: 40, picHeightMap: 30, frameMbsOnly: 0},
			wantWidth:  640,
			wantHeight: 960,
		},
		{
			name:       "dimensions beyond the supported range are rejected",
			spec:       syntheticH264SPS{profileIDC: 66, picWidthMbs: 600, picHeightMap: 30, frameMbsOnly: 1},
			wantWidth:  0,
			wantHeight: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sps := buildSyntheticH264SPS(tt.spec)
			width, height := parseSPSResolution(sps)
			assert.Equal(t, tt.wantWidth, width)
			assert.Equal(t, tt.wantHeight, height)
		})
	}
}

func TestParseHEVCSPSResolutionBranches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                  string
		sps                   []byte
		wantWidth, wantHeight int
	}{
		{name: "1920x1080", sps: buildSyntheticHEVCSPS(1920, 1080, 0), wantWidth: 1920, wantHeight: 1080},
		{name: "sub layers present", sps: buildSyntheticHEVCSPS(1280, 720, 2), wantWidth: 1280, wantHeight: 720},
		{name: "4:4:4 chroma format", sps: buildSyntheticHEVCSPSChroma(640, 480, 0, 3), wantWidth: 640, wantHeight: 480},
		{name: "zero width rejected", sps: buildSyntheticHEVCSPS(0, 720, 0)},
		{name: "oversized rejected", sps: buildSyntheticHEVCSPS(9000, 720, 0)},
		{name: "short sps", sps: []byte{0x42, 0x01, 0x00}},
		{name: "short rbsp", sps: []byte{0x42, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			width, height := parseHEVCSPSResolution(tt.sps)
			assert.Equal(t, tt.wantWidth, width)
			assert.Equal(t, tt.wantHeight, height)
		})
	}
}

func TestBitReaderHelpers(t *testing.T) {
	t.Parallel()

	// readUE with more than 32 leading zero bits gives up and returns 0.
	r := &bitReader{data: make([]byte, 8)}
	assert.Equal(t, 0, r.readUE())

	// readBits past the end keeps returning zero instead of panicking.
	r = &bitReader{data: []byte{0xff}}
	assert.Equal(t, 0xff, r.readBits(8))
	assert.Equal(t, 0, r.readBits(4))

	w := &testBitWriter{}
	w.putUE(2) // signed value -1
	r = &bitReader{data: w.bytes()}
	assert.Equal(t, -1, r.readSE())

	w = &testBitWriter{}
	w.putUE(3) // signed value 2
	r = &bitReader{data: w.bytes()}
	assert.Equal(t, 2, r.readSE())

	w = &testBitWriter{}
	w.putUE(0) // signed value 0
	r = &bitReader{data: w.bytes()}
	assert.Equal(t, 0, r.readSE())
}

func TestRemoveEmulationPrevention(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []byte{0, 0, 0, 0, 0}, removeEmulationPrevention([]byte{0, 0, 3, 0, 0, 3, 0}))
	assert.Equal(t, []byte{1, 2, 3, 0, 0, 4}, removeEmulationPrevention([]byte{1, 2, 3, 0, 0, 4}))
	// A trailing 0x00 0x03 without a preceding zero pair is preserved.
	assert.Equal(t, []byte{0, 3}, removeEmulationPrevention([]byte{0, 3}))
}

func TestIsSyncNALBranches(t *testing.T) {
	t.Parallel()
	assert.False(t, isSyncNAL(nil, false))
	assert.True(t, isSyncNAL([]byte{0x65}, false))
	assert.False(t, isSyncNAL([]byte{0x41}, false))
	// H.265 IRAP types are 16..21 in the NAL type field.
	assert.True(t, isSyncNAL([]byte{0x26, 0x01}, true))  // type 19
	assert.True(t, isSyncNAL([]byte{0x20, 0x01}, true))  // type 16
	assert.True(t, isSyncNAL([]byte{0x2a, 0x01}, true))  // type 21
	assert.False(t, isSyncNAL([]byte{0x02, 0x01}, true)) // type 1
	assert.False(t, isSyncNAL([]byte{0x2c, 0x01}, true)) // type 22
}

func TestAddH265TrackWritesHEVCFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "hevc.mp4")

	sps := buildSyntheticHEVCSPS(1280, 720, 0)
	vps := []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x78, 0x99, 0x98, 0x09}
	pps := []byte{0x44, 0x01, 0xc1, 0x72, 0xb4, 0x62, 0x40}

	m := NewMP4Muxer(path)
	trackID, err := m.AddH265Track(vps, sps, pps)
	require.NoError(t, err)
	require.Equal(t, 1, trackID)
	require.Len(t, m.tracks, 1)
	assert.True(t, m.tracks[0].isH265)
	assert.Equal(t, 1280, m.tracks[0].width)
	assert.Equal(t, 720, m.tracks[0].height)

	irap := []byte{0x26, 0x01, 0xaf, 0x09}
	nonIRAP := []byte{0x02, 0x01, 0x9a, 0x24}
	require.NoError(t, m.WriteSample(trackID, irap, 0, 40*time.Millisecond))
	require.NoError(t, m.WriteSample(trackID, nonIRAP, 40*time.Millisecond, 40*time.Millisecond))
	require.NoError(t, m.Close())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "hvc1")
	assert.Contains(t, string(data), "hvcC")
	assert.Contains(t, string(data), "hev1")
	assert.Contains(t, string(data), "stss")
}

func TestMP4MuxerOpusTrack(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "opus.mp4")

	m := NewMP4Muxer(path)
	trackID, err := m.AddAudioTrack("opus", nil)
	require.NoError(t, err)

	for i := 0; i < 6; i++ {
		require.NoError(t, m.WriteAudioSample(trackID,
			[]byte{0xfc, byte(i), 0x00},
			time.Duration(i)*20*time.Millisecond,
			20*time.Millisecond))
	}
	// Exercise WriteTimedSample on an audio track (cts is dropped for audio).
	require.NoError(t, m.WriteTimedSample(trackID, []byte{0xfc, 0x01}, 120*time.Millisecond, 140*time.Millisecond, 20*time.Millisecond))
	require.NoError(t, m.Close())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, box := range []string{"Opus", "dOps", "edts", "elst", "sgpd", "sbgp"} {
		assert.Contains(t, string(data), box, "opus track should write %s", box)
	}
}

func TestG711SampleEntryVariants(t *testing.T) {
	t.Parallel()

	// A-law with a short config: muLaw false and the sample rate falls back to 8000.
	dir := t.TempDir()
	m := NewMP4Muxer(filepath.Join(dir, "alaw.mp4"))
	_, err := m.AddAudioTrack("g711", []byte{0x00})
	require.NoError(t, err)
	require.Len(t, m.tracks, 1)
	assert.False(t, m.tracks[0].g711MULaw)
	assert.Equal(t, 8000, m.tracks[0].g711Rate)
	require.NoError(t, m.Close())

	// Empty config: defaults are used as well.
	m = NewMP4Muxer(filepath.Join(dir, "empty.mp4"))
	_, err = m.AddAudioTrack("g711", nil)
	require.NoError(t, err)
	assert.False(t, m.tracks[0].g711MULaw)
	assert.Equal(t, 8000, m.tracks[0].g711Rate)
	require.NoError(t, m.Close())

	// Explicit zero rate in the config keeps the fallback.
	m = NewMP4Muxer(filepath.Join(dir, "zero.mp4"))
	_, err = m.AddAudioTrack("g711", []byte{0x01, 0x00, 0x00, 0x00, 0x00})
	require.NoError(t, err)
	assert.True(t, m.tracks[0].g711MULaw)
	assert.Equal(t, 8000, m.tracks[0].g711Rate)
	require.NoError(t, m.Close())

	// Direct writer call with a zero-rate track still uses 8000 in the box.
	w, _ := newTestBoxWriter()
	require.NoError(t, writeG711SampleEntry(w, &track{audioCodec: "g711"}))
	_ = w
}

func writeTrackStbl(t *testing.T, tr *track) []byte {
	t.Helper()
	w, ms := newTestBoxWriter()
	require.NoError(t, writeStbl(w, tr))
	return ms.buf
}

func TestWriteAACSampleEntryConfigBranches(t *testing.T) {
	t.Parallel()
	base := func(cfg []byte) *track {
		return &track{id: 1, isAudio: true, audioCodec: "aac", audioConfig: cfg}
	}
	tests := []struct {
		name string
		cfg  []byte
	}{
		{name: "no config", cfg: nil},
		{name: "explicit 24-bit sample rate", cfg: []byte{0x78, 0x08, 0x00}},
		{name: "sampling index in table", cfg: []byte{0x12, 0x10}},
		{name: "sampling index beyond table", cfg: []byte{0x68, 0x00}},
		{name: "channel config zero falls back to stereo", cfg: []byte{0x00, 0x00}},
		{name: "explicit channel config", cfg: []byte{0x13, 0x10}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data := writeTrackStbl(t, base(tt.cfg))
			assert.True(t, bytes.Contains(data, []byte("mp4a")))
			assert.True(t, bytes.Contains(data, []byte("esds")))
		})
	}
}

func TestWriteStblCo64AndEmptyTrack(t *testing.T) {
	t.Parallel()

	// Offsets above 4 GiB must switch to the co64 box.
	large := &track{
		id:  1,
		sps: testSPS,
		pps: testPPS,
		samples: []sample{{
			offset:   math.MaxUint32 + 16,
			size:     12,
			pts:      0,
			duration: 40 * time.Millisecond,
			sync:     true,
		}},
	}
	co64 := writeTrackStbl(t, large)
	assert.True(t, bytes.Contains(co64, []byte("co64")))
	assert.False(t, bytes.Contains(co64, []byte("stco")))

	// A track without samples still yields a well-formed stbl.
	empty := &track{id: 1, sps: testSPS, pps: testPPS}
	emptyData := writeTrackStbl(t, empty)
	assert.True(t, bytes.Contains(emptyData, []byte("stbl")))
	assert.True(t, bytes.Contains(emptyData, []byte("stco")))

	// co64 pure helper path through writeMoov.
	w, ms := newTestBoxWriter()
	require.NoError(t, writeMoov(w, []*track{large, empty}))
	assert.True(t, bytes.Contains(ms.buf, []byte("moov")))
}

func TestWriteCTTSBranches(t *testing.T) {
	t.Parallel()

	// nil track and audio tracks are ignored.
	require.NoError(t, writeCTTS(nil, nil))

	withCTS := &track{
		id:  1,
		sps: testSPS,
		pps: testPPS,
		samples: []sample{
			{duration: 40 * time.Millisecond},
			{duration: 40 * time.Millisecond, cts: -20 * time.Millisecond},
			{duration: 40 * time.Millisecond, cts: 20 * time.Millisecond},
		},
	}
	w, ms := newTestBoxWriter()
	require.NoError(t, writeCTTS(w, withCTS))
	assert.True(t, bytes.Contains(ms.buf, []byte("ctts")))

	// Only zero offsets: the box is omitted.
	zero := &track{id: 1, samples: []sample{{duration: 40 * time.Millisecond}}}
	w, ms = newTestBoxWriter()
	require.NoError(t, writeCTTS(w, zero))
	assert.False(t, bytes.Contains(ms.buf, []byte("ctts")))

	// No samples at all.
	w, ms = newTestBoxWriter()
	require.NoError(t, writeCTTS(w, &track{id: 1}))
	assert.False(t, bytes.Contains(ms.buf, []byte("ctts")))
}

func TestWriteSTSSBranches(t *testing.T) {
	t.Parallel()
	require.NoError(t, writeSTSS(nil, nil))
	require.NoError(t, writeSTSS(nil, &track{id: 1}))

	allSync := &track{id: 1, samples: []sample{{sync: true}, {sync: true}}}
	w, ms := newTestBoxWriter()
	require.NoError(t, writeSTSS(w, allSync))
	assert.False(t, bytes.Contains(ms.buf, []byte("stss")))

	noneSync := &track{id: 1, samples: []sample{{}, {}}}
	w, ms = newTestBoxWriter()
	require.NoError(t, writeSTSS(w, noneSync))
	assert.False(t, bytes.Contains(ms.buf, []byte("stss")))

	mixed := &track{id: 1, samples: []sample{{sync: true}, {}, {sync: true}}}
	w, ms = newTestBoxWriter()
	require.NoError(t, writeSTSS(w, mixed))
	assert.True(t, bytes.Contains(ms.buf, []byte("stss")))
}

func TestWriteFtypBrands(t *testing.T) {
	t.Parallel()

	audioOnly := &track{id: 1, isAudio: true, audioCodec: "aac"}
	w, ms := newTestBoxWriter()
	_, err := writeFtyp(w, []*track{audioOnly})
	require.NoError(t, err)
	assert.False(t, bytes.Contains(ms.buf, []byte("avc1")))
	assert.False(t, bytes.Contains(ms.buf, []byte("hev1")))

	w, ms = newTestBoxWriter()
	_, err = writeFtyp(w, []*track{{id: 1, isH265: true}})
	require.NoError(t, err)
	assert.True(t, bytes.Contains(ms.buf, []byte("hev1")))
	assert.False(t, bytes.Contains(ms.buf, []byte("avc1")))

	w, ms = newTestBoxWriter()
	_, err = writeFtyp(w, []*track{{id: 1}})
	require.NoError(t, err)
	assert.True(t, bytes.Contains(ms.buf, []byte("avc1")))
}

func TestMP4MuxerClosedState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := NewMP4Muxer(filepath.Join(dir, "closed.mp4"))
	trackID, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)
	audioID, err := m.AddAudioTrack("aac", []byte{0x12, 0x10})
	require.NoError(t, err)
	require.NoError(t, m.Close())

	// Closing twice is a no-op.
	require.NoError(t, m.Close())

	_, err = m.AddH264Track(testSPS, testPPS)
	assert.ErrorContains(t, err, "closed")
	_, err = m.AddH265Track(nil, testSPS, testPPS)
	assert.ErrorContains(t, err, "closed")
	_, err = m.AddAudioTrack("aac", nil)
	assert.ErrorContains(t, err, "closed")
	assert.ErrorContains(t, m.WriteSample(trackID, []byte{0x65}, 0, time.Millisecond), "closed")
	assert.ErrorContains(t, m.WriteTimedSample(trackID, []byte{0x65}, 0, 0, time.Millisecond), "closed")
	assert.ErrorContains(t, m.WriteAudioSample(audioID, []byte{0x01}, 0, time.Millisecond), "closed")
}

func TestMP4MuxerCreateFileError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := NewMP4Muxer(filepath.Join(dir, "missing", "nested", "x.mp4"))
	trackID, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)

	require.ErrorContains(t, m.WriteSample(trackID, []byte{0x65, 0x88}, 0, time.Millisecond), "create file")
	// Close must surface the same creation error because mdat was never opened.
	require.Error(t, m.Close())
}

func TestMP4MuxerSampleValidation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := NewMP4Muxer(filepath.Join(dir, "samples.mp4"))
	videoID, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)
	_, err = m.AddAudioTrack("aac", []byte{0x12, 0x10})
	require.NoError(t, err)

	// An Annex-B prefix that strips to nothing is rejected.
	assert.ErrorContains(t, m.WriteSample(videoID, []byte{0, 0, 0, 1}, 0, time.Millisecond), "empty nal")

	// Audio samples cannot be written to a video track.
	assert.ErrorContains(t, m.WriteAudioSample(videoID, []byte{0x01}, 0, time.Millisecond),
		"not an audio track")

	// WriteTimedSample clamps sub-millisecond durations.
	require.NoError(t, m.WriteTimedSample(videoID, []byte{0x65, 0x88}, 0, 0, 0))
	require.Len(t, m.tracks[0].samples, 1)
	assert.Equal(t, time.Millisecond, m.tracks[0].samples[0].duration)

	// Writing to a missing track reports the id.
	assert.ErrorContains(t, m.WriteSample(99, []byte{0x65}, 0, time.Millisecond), "track 99 not found")
	assert.ErrorContains(t, m.WriteTimedSample(99, []byte{0x65}, 0, 0, time.Millisecond), "track 99 not found")
	assert.ErrorContains(t, m.WriteAudioSample(99, []byte{0x65}, 0, time.Millisecond), "track 99 not found")

	require.NoError(t, m.Close())
}

func TestMP4MuxerSecondSampleReusesFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := NewMP4Muxer(filepath.Join(dir, "reuse.mp4"))
	trackID, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)

	require.NoError(t, m.WriteSample(trackID, []byte{0x65, 0x88, 0x80}, 0, 40*time.Millisecond))
	require.NoError(t, m.WriteSample(trackID, []byte{0x41, 0x9a}, 40*time.Millisecond, 40*time.Millisecond))
	require.NoError(t, m.Close())
	assert.Equal(t, 80*time.Millisecond, m.Duration())
}

// scriptedWriteSeeker fails the Nth (and every later) write or seek, which
// lets tests exercise the error branches of the box writers deterministically.
type scriptedWriteSeeker struct {
	buf         []byte
	off         int64
	writes      int
	seeks       int
	failWriteAt int
	failSeekAt  int
}

type injectedIOError struct {
	op  string
	seq int
}

func (e *injectedIOError) Error() string {
	return "injected " + e.op + " failure"
}

func (s *scriptedWriteSeeker) Write(p []byte) (int, error) {
	s.writes++
	if s.failWriteAt > 0 && s.writes >= s.failWriteAt {
		return 0, &injectedIOError{op: "write", seq: s.writes}
	}
	end := s.off + int64(len(p))
	if end > int64(len(s.buf)) {
		s.buf = append(s.buf, make([]byte, end-int64(len(s.buf)))...)
	}
	copy(s.buf[s.off:], p)
	s.off = end
	return len(p), nil
}

func (s *scriptedWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	s.seeks++
	if s.failSeekAt > 0 && s.seeks >= s.failSeekAt {
		return 0, &injectedIOError{op: "seek", seq: s.seeks}
	}
	var base int64
	switch whence {
	case 0:
		base = 0
	case 1:
		base = s.off
	case 2:
		base = int64(len(s.buf))
	default:
		return 0, &injectedIOError{op: "seek", seq: -1}
	}
	pos := base + offset
	if pos < 0 || pos > int64(len(s.buf)) {
		return 0, &injectedIOError{op: "seek", seq: -1}
	}
	s.off = pos
	return pos, nil
}

// exerciseBoxWriterErrors runs fn once successfully to learn how many I/O
// operations it performs, then injects a failure at every one of them.
func exerciseBoxWriterErrors(t *testing.T, fn func(*mp4.Writer) error) {
	t.Helper()
	clean := &scriptedWriteSeeker{}
	require.NoError(t, fn(mp4.NewWriter(clean)))
	require.Greater(t, clean.writes, 0)
	require.Greater(t, clean.seeks, 0)
	for i := 1; i <= clean.writes; i++ {
		require.Error(t, fn(mp4.NewWriter(&scriptedWriteSeeker{failWriteAt: i})),
			"write failure %d must surface", i)
	}
	ignored := 0
	for i := 1; i <= clean.seeks; i++ {
		err := fn(mp4.NewWriter(&scriptedWriteSeeker{failSeekAt: i}))
		if err == nil {
			// writeFtyp deliberately ignores the error of its opening and
			// closing position probes; every other seek has a caller check.
			require.True(t, i == 1 || i == clean.seeks,
				"seek failure %d was silently ignored", i)
			ignored++
		}
	}
	require.LessOrEqual(t, ignored, 2, "only the writeFtyp position probes may ignore errors")
}

func TestBoxWritersSurfaceIOErrors(t *testing.T) {
	t.Parallel()

	video := &track{
		id: 1, sps: testSPS, pps: testPPS, width: 640, height: 128,
		samples: []sample{
			{offset: 0x20, size: 8, duration: 40 * time.Millisecond, sync: true},
			{offset: 0x28, size: 8, duration: 40 * time.Millisecond, cts: 20 * time.Millisecond},
		},
	}
	h265 := &track{
		id: 2, isH265: true, vps: []byte{0x40, 0x01}, sps: buildSyntheticHEVCSPS(320, 240, 0), pps: []byte{0x44, 0x01},
		width: 320, height: 240,
		samples: []sample{{offset: 8, size: 4, duration: 40 * time.Millisecond, sync: true}},
	}
	aac := &track{id: 3, isAudio: true, audioCodec: "aac", audioConfig: []byte{0x12, 0x10}}
	alaw := &track{id: 4, isAudio: true, audioCodec: "g711", width: 0, height: 0}
	opus := &track{
		id: 5, isAudio: true, audioCodec: "opus",
		samples: []sample{{duration: 20 * time.Millisecond}, {duration: 20 * time.Millisecond}},
	}
	co64 := &track{
		id: 6, sps: testSPS, pps: testPPS,
		samples: []sample{{offset: math.MaxUint32 + 1, size: 4, duration: 40 * time.Millisecond}},
	}

	cases := []struct {
		name string
		fn   func(*mp4.Writer) error
	}{
		{"writeFtyp", func(w *mp4.Writer) error { _, err := writeFtyp(w, []*track{video, h265, aac}); return err }},
		{"writeMoov", func(w *mp4.Writer) error { return writeMoov(w, []*track{video, aac}) }},
		{"writeMvhd", func(w *mp4.Writer) error { return writeMvhd(w, []*track{video, aac}) }},
		{"writeMvhdEmpty", func(w *mp4.Writer) error { return writeMvhd(w, nil) }},
		{"writeTrakVideo", func(w *mp4.Writer) error { return writeTrak(w, video) }},
		{"writeTrakH265", func(w *mp4.Writer) error { return writeTrak(w, h265) }},
		{"writeTrakOpus", func(w *mp4.Writer) error { return writeTrak(w, opus) }},
		{"writeMdiaAudio", func(w *mp4.Writer) error { return writeMdia(w, aac) }},
		{"writeMinfVideo", func(w *mp4.Writer) error { return writeMinf(w, video) }},
		{"writeMinfAudio", func(w *mp4.Writer) error { return writeMinf(w, aac) }},
		{"writeStblVideo", func(w *mp4.Writer) error { return writeStbl(w, video) }},
		{"writeStblCo64", func(w *mp4.Writer) error { return writeStbl(w, co64) }},
		{"writeStblEmpty", func(w *mp4.Writer) error { return writeStbl(w, &track{id: 7, sps: testSPS, pps: testPPS}) }},
		{"writeStblG711", func(w *mp4.Writer) error { return writeStbl(w, alaw) }},
		{"writeCTTS", func(w *mp4.Writer) error { return writeCTTS(w, video) }},
		{"writeSTSS", func(w *mp4.Writer) error { return writeSTSS(w, video) }},
		{"writeH264SampleEntry", func(w *mp4.Writer) error { return writeH264SampleEntry(w, video) }},
		{"writeH265SampleEntry", func(w *mp4.Writer) error { return writeH265SampleEntry(w, h265) }},
		{"writeAACSampleEntry", func(w *mp4.Writer) error { return writeAACSampleEntry(w, aac) }},
		{"writeG711SampleEntry", func(w *mp4.Writer) error { return writeG711SampleEntry(w, alaw) }},
		{"writeOpusSampleEntry", func(w *mp4.Writer) error { return writeOpusSampleEntry(w) }},
		{"writeOpusRecovery", func(w *mp4.Writer) error { return writeOpusRecovery(w, opus) }},
		{"writeRawBox", func(w *mp4.Writer) error { return writeRawBox(w, "free", []byte{1, 2, 3, 4}) }},
		{"writeOpusEditLong", func(w *mp4.Writer) error { return writeOpusEdit(w, opus) }},
		{"writeOpusEditShort", func(w *mp4.Writer) error {
			return writeOpusEdit(w, &track{id: 8, samples: []sample{{duration: 10 * time.Millisecond}}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			exerciseBoxWriterErrors(t, tc.fn)
		})
	}
}
