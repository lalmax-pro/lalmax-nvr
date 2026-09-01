package vod

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/abema/go-mp4"
	"github.com/lalmax-pro/lalmax-nvr/internal/merge"
)

const TargetFragmentDur = 6 * time.Second

// FragmentRange is an inclusive sample index range inside one recording.
type FragmentRange struct {
	First      int
	Last       int
	Duration   time.Duration
	DecodeTime uint64
}

// SplitFragments groups video samples into ~TargetFragmentDur pieces, splitting on keyframes.
func SplitFragments(info *merge.SegmentInfo) []FragmentRange {
	if info == nil || len(info.Samples) == 0 {
		return nil
	}
	timescale := info.Timescale
	if timescale == 0 {
		timescale = 1000
	}
	targetTicks := uint64(TargetFragmentDur.Seconds() * float64(timescale))
	if targetTicks == 0 {
		targetTicks = uint64(timescale) * 6
	}

	var out []FragmentRange
	start := 0
	var acc uint64
	var decode uint64
	startDecode := uint64(0)
	for i, s := range info.Samples {
		if i > start && s.IsKeyFrame && acc >= targetTicks {
			out = append(out, FragmentRange{
				First: start, Last: i - 1,
				Duration:   ticksToDuration(acc, timescale),
				DecodeTime: startDecode,
			})
			start = i
			startDecode = decode
			acc = 0
		}
		acc += uint64(s.Duration)
		decode += uint64(s.Duration)
	}
	if start < len(info.Samples) {
		out = append(out, FragmentRange{
			First: start, Last: len(info.Samples) - 1,
			Duration:   ticksToDuration(acc, timescale),
			DecodeTime: startDecode,
		})
	}
	return out
}

func ticksToDuration(ticks uint64, timescale uint32) time.Duration {
	if timescale == 0 {
		return 0
	}
	return time.Duration(float64(ticks) / float64(timescale) * float64(time.Second))
}

// WriteInit writes a CMAF/fMP4 initialization segment (ftyp+moov, no mdat).
func WriteInit(w io.Writer, info *merge.SegmentInfo) error {
	if info == nil {
		return fmt.Errorf("nil segment")
	}
	buf := &bytesWriter{}
	mw := mp4.NewWriter(buf)
	if err := writeFtyp(mw, info.Codec); err != nil {
		return err
	}
	if err := writeInitMoov(mw, info); err != nil {
		return err
	}
	_, err := w.Write(buf.bytes())
	return err
}

// WriteFragment writes one CMAF media segment (moof+mdat) covering samples [first, last].
func WriteFragment(w io.Writer, info *merge.SegmentInfo, seq uint32, first, last int) error {
	if info == nil || first < 0 || last < first || last >= len(info.Samples) {
		return fmt.Errorf("invalid fragment range")
	}
	samples := info.Samples[first : last+1]
	var decode uint64
	for i := 0; i < first; i++ {
		decode += uint64(info.Samples[i].Duration)
	}

	var dataSize uint32
	entries := make([]mp4.TrunEntry, len(samples))
	for i, s := range samples {
		flags := uint32(0x01010000)
		if s.IsKeyFrame {
			flags = 0x02000000
		}
		entries[i] = mp4.TrunEntry{
			SampleDuration: s.Duration,
			SampleSize:     s.Size,
			SampleFlags:    flags,
		}
		dataSize += s.Size
	}

	var moofBuf bytes.Buffer
	if err := writeMoof(&moofBuf, seq, 1, decode, entries, 0); err != nil {
		return err
	}
	dataOffset := int32(moofBuf.Len() + 8)
	moofBuf.Reset()
	if err := writeMoof(&moofBuf, seq, 1, decode, entries, dataOffset); err != nil {
		return err
	}
	if _, err := w.Write(moofBuf.Bytes()); err != nil {
		return err
	}

	var hdr [8]byte
	size := uint32(8 + dataSize)
	hdr[0] = byte(size >> 24)
	hdr[1] = byte(size >> 16)
	hdr[2] = byte(size >> 8)
	hdr[3] = byte(size)
	copy(hdr[4:], "mdat")
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}

	src, err := os.Open(info.FilePath)
	if err != nil {
		return err
	}
	defer src.Close()
	buf := make([]byte, 64*1024)
	for _, s := range samples {
		if _, err := src.Seek(s.Offset, io.SeekStart); err != nil {
			return err
		}
		if _, err := io.CopyBuffer(w, io.LimitReader(src, int64(s.Size)), buf); err != nil {
			return err
		}
	}
	return nil
}

func writeMoof(w io.Writer, seq, trackID uint32, decodeTime uint64, entries []mp4.TrunEntry, dataOffset int32) error {
	buf := &bytesWriter{}
	mw := mp4.NewWriter(buf)
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoof()}); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMfhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Mfhd{SequenceNumber: seq}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTraf()}); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTfhd()}); err != nil {
		return err
	}
	tfhd := &mp4.Tfhd{TrackID: trackID}
	tfhd.SetFlags(mp4.TfhdDefaultBaseIsMoof)
	if _, err := mp4.Marshal(mw, tfhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTfdt()}); err != nil {
		return err
	}
	tfdt := &mp4.Tfdt{BaseMediaDecodeTimeV1: decodeTime}
	tfdt.SetVersion(1)
	if _, err := mp4.Marshal(mw, tfdt, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrun()}); err != nil {
		return err
	}
	trun := &mp4.Trun{
		SampleCount: uint32(len(entries)),
		DataOffset:  dataOffset,
		Entries:     entries,
	}
	trun.SetFlags(0x000001 | 0x000100 | 0x000200 | 0x000400) // offset + duration + size + flags
	if _, err := mp4.Marshal(mw, trun, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	_, err := w.Write(buf.bytes())
	return err
}

func writeFtyp(w *mp4.Writer, codec string) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	compatible := []mp4.CompatibleBrandElem{
		{CompatibleBrand: [4]byte{'i', 's', 'o', '6'}},
		{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		{CompatibleBrand: [4]byte{'i', 's', 'o', '5'}},
	}
	if codec == "h265" {
		compatible = append(compatible, mp4.CompatibleBrandElem{CompatibleBrand: [4]byte{'h', 'v', 'c', '1'}})
	} else {
		compatible = append(compatible, mp4.CompatibleBrandElem{CompatibleBrand: [4]byte{'a', 'v', 'c', '1'}})
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:       [4]byte{'i', 's', 'o', '5'},
		MinorVersion:     0x200,
		CompatibleBrands: compatible,
	}
	if _, err := mp4.Marshal(w, ftyp, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeInitMoov(w *mp4.Writer, info *merge.SegmentInfo) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if err := writeInitMvhd(w); err != nil {
		return err
	}
	if err := writeInitTrak(w, info); err != nil {
		return err
	}
	if err := writeMvex(w); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeInitMvhd(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return err
	}
	mvhd := &mp4.Mvhd{
		Timescale:   1000,
		Rate:        0x00010000,
		Volume:      0x0100,
		NextTrackID: 2,
		Matrix: [9]int32{
			0x00010000, 0, 0,
			0, 0x00010000, 0,
			0, 0, 0x40000000,
		},
	}
	if _, err := mp4.Marshal(w, mvhd, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeInitTrak(w *mp4.Writer, info *merge.SegmentInfo) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{
		TrackID: 1,
		Width:   uint32(info.Width) << 16,
		Height:  uint32(info.Height) << 16,
		Volume:  0,
		Matrix: [9]int32{
			0x00010000, 0, 0,
			0, 0x00010000, 0,
			0, 0, 0x40000000,
		},
	}
	tkhd.SetFlags(0x000007)
	if _, err := mp4.Marshal(w, tkhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if err := writeInitMdia(w, info); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeInitMdia(w *mp4.Writer, info *merge.SegmentInfo) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	ts := info.Timescale
	if ts == 0 {
		ts = 1000
	}
	if _, err := mp4.Marshal(w, &mp4.Mdhd{Timescale: ts, Language: [3]byte{0x15, 0xC0, 0x00}}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "VideoHandler"}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if err := writeInitMinf(w, info); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeInitMinf(w *mp4.Writer, info *merge.SegmentInfo) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeVmhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Vmhd{}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Dref{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("url ")}); err != nil {
		return err
	}
	urlBox := &mp4.Url{}
	urlBox.SetFlags(0x000001)
	if _, err := mp4.Marshal(w, urlBox, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if err := writeInitStbl(w, info); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeInitStbl(w *mp4.Writer, info *merge.SegmentInfo) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if info.Codec == "h265" {
		if err := writeH265Entry(w, info); err != nil {
			return err
		}
	} else {
		if err := writeH264Entry(w, info); err != nil {
			return err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	empties := []mp4.IImmutableBox{&mp4.Stts{}, &mp4.Stsc{}, &mp4.Stsz{}, &mp4.Stco{}}
	for _, box := range empties {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: box.GetType()}); err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, box, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
	}
	_, err := w.EndBox()
	return err
}

func writeH264Entry(w *mp4.Writer, info *merge.SegmentInfo) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avc1")}); err != nil {
		return err
	}
	avc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("avc1")},
			DataReferenceIndex: 1,
		},
		Width:           info.Width,
		Height:          info.Height,
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, avc1, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avcC")}); err != nil {
		return err
	}
	profile, compat, level := byte(0x42), byte(0), byte(0x1e)
	if len(info.SPS) >= 4 {
		profile, compat, level = info.SPS[1], info.SPS[2], info.SPS[3]
	}
	avcC := &mp4.AVCDecoderConfiguration{
		AnyTypeBox:                 mp4.AnyTypeBox{Type: mp4.StrToBoxType("avcC")},
		ConfigurationVersion:       1,
		Profile:                    profile,
		ProfileCompatibility:       compat,
		Level:                      level,
		LengthSizeMinusOne:         3,
		NumOfSequenceParameterSets: 1,
		SequenceParameterSets:      []mp4.AVCParameterSet{{Length: uint16(len(info.SPS)), NALUnit: info.SPS}},
		NumOfPictureParameterSets:  1,
		PictureParameterSets:       []mp4.AVCParameterSet{{Length: uint16(len(info.PPS)), NALUnit: info.PPS}},
	}
	if _, err := mp4.Marshal(w, avcC, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeH265Entry(w *mp4.Writer, info *merge.SegmentInfo) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvc1")}); err != nil {
		return err
	}
	hvc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("hvc1")},
			DataReferenceIndex: 1,
		},
		Width:           info.Width,
		Height:          info.Height,
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, hvc1, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvcC")}); err != nil {
		return err
	}
	hvcC := &mp4.HvcC{
		ConfigurationVersion: 1,
		GeneralProfileIdc:    1,
		GeneralLevelIdc:      93,
		ChromaFormatIdc:      1,
		NumTemporalLayers:    1,
		LengthSizeMinusOne:   3,
		NumOfNaluArrays:      3,
		NaluArrays: []mp4.HEVCNaluArray{
			{Completeness: true, NaluType: 32, NumNalus: 1, Nalus: []mp4.HEVCNalu{{Length: uint16(len(info.VPS)), NALUnit: info.VPS}}},
			{Completeness: true, NaluType: 33, NumNalus: 1, Nalus: []mp4.HEVCNalu{{Length: uint16(len(info.SPS)), NALUnit: info.SPS}}},
			{Completeness: true, NaluType: 34, NumNalus: 1, Nalus: []mp4.HEVCNalu{{Length: uint16(len(info.PPS)), NALUnit: info.PPS}}},
		},
	}
	if _, err := mp4.Marshal(w, hvcC, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeMvex(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvex()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrex()}); err != nil {
		return err
	}
	trex := &mp4.Trex{
		TrackID:                       1,
		DefaultSampleDescriptionIndex: 1,
	}
	if _, err := mp4.Marshal(w, trex, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

// bytesWriter is an io.WriteSeeker backed by a growable buffer.
type bytesWriter struct {
	data []byte
	pos  int64
}

func (b *bytesWriter) Write(p []byte) (int, error) {
	if b.pos+int64(len(p)) > int64(len(b.data)) {
		grow := b.pos + int64(len(p)) - int64(len(b.data))
		b.data = append(b.data, make([]byte, grow)...)
	}
	copy(b.data[b.pos:], p)
	b.pos += int64(len(p))
	return len(p), nil
}

func (b *bytesWriter) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		b.pos = offset
	case io.SeekCurrent:
		b.pos += offset
	case io.SeekEnd:
		b.pos = int64(len(b.data)) + offset
	}
	if b.pos < 0 {
		b.pos = 0
	}
	return b.pos, nil
}

func (b *bytesWriter) bytes() []byte {
	return b.data
}
