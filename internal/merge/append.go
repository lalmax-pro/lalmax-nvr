package merge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

var (
	errAppendNotPossible = errors.New("hour file is not appendable")
	errMergeNeedSpace    = errors.New("insufficient disk space for merge")
)

// segmentAppendable reports whether the file is ftyp + mdat + moov with moov
// sitting immediately after mdat, so new samples can extend mdat in place.
func segmentAppendable(info *SegmentInfo, fileSize int64) bool {
	if info == nil || info.FilePath == "" {
		return false
	}
	if info.MdatOffset < 0 || info.MdatSize < 8 || info.MoovOffset <= 0 || info.MoovSize <= 8 {
		return false
	}
	if info.MoovOffset != info.MdatOffset+info.MdatSize {
		return false
	}
	if fileSize != info.MoovOffset+info.MoovSize {
		return false
	}
	if info.MdatSize > math.MaxUint32 {
		return false
	}
	return true
}

// AppendMP4Samples extends an ftyp+mdat+moov file with newSegments.
// Media already in base is not copied. On a write error the previous moov and
// mdat size are restored before returning.
func AppendMP4Samples(base *SegmentInfo, newSegments []*SegmentInfo) error {
	if base == nil || base.FilePath == "" || len(newSegments) == 0 {
		return fmt.Errorf("nothing to append")
	}
	combined := make([]*SegmentInfo, 0, 1+len(newSegments))
	combined = append(combined, base)
	combined = append(combined, newSegments...)
	if err := validateMergeSegments(combined); err != nil {
		return err
	}

	fi, err := os.Stat(base.FilePath)
	if err != nil {
		return err
	}
	if !segmentAppendable(base, fi.Size()) {
		return errAppendNotPossible
	}

	f, err := os.OpenFile(base.FilePath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	var mdatHdr [8]byte
	if _, err := f.ReadAt(mdatHdr[:], base.MdatOffset); err != nil {
		return err
	}
	mdatSizeField := binary.BigEndian.Uint32(mdatHdr[:4])
	if mdatSizeField == 1 || int64(mdatSizeField) != base.MdatSize || string(mdatHdr[4:8]) != "mdat" {
		return errAppendNotPossible
	}
	oldMoov := make([]byte, base.MoovSize)
	if _, err := f.ReadAt(oldMoov, base.MoovOffset); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(base.FilePath), ".merge-append-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	buf := make([]byte, mergeBufferSize)
	var payload int64
	videoAdded, payload, err := appendSamples(tmp, newSegments, false, base.MoovOffset, payload, buf)
	if err != nil {
		return err
	}
	var audioAdded []mergedSample
	if base.HasAudio {
		audioAdded, payload, err = appendSamples(tmp, newSegments, true, base.MoovOffset, payload, buf)
		if err != nil {
			return err
		}
	}
	if payload <= 0 || base.MdatSize+payload > math.MaxUint32 {
		return errAppendNotPossible
	}

	videoTrack := trackFromSamples(base, false, append(samplesToMerged(base.Samples), videoAdded...))
	var audioTrack *mergeTrack
	if base.HasAudio {
		audioTrack = trackFromSamples(base, true, append(samplesToMerged(base.AudioSamples), audioAdded...))
	}
	moovBytes, err := buildMoovBytes(videoTrack, audioTrack)
	if err != nil {
		return fmt.Errorf("build moov: %w", err)
	}

	fi, err = f.Stat()
	if err != nil {
		return err
	}
	if fi.Size() != base.MoovOffset+base.MoovSize {
		return fmt.Errorf("hour file changed during append")
	}

	mutated := false
	restore := func() {
		if !mutated {
			return
		}
		_ = f.Truncate(base.MoovOffset)
		_, _ = f.WriteAt(oldMoov, base.MoovOffset)
		var sz [4]byte
		binary.BigEndian.PutUint32(sz[:], mdatSizeField)
		_, _ = f.WriteAt(sz[:], base.MdatOffset)
		_ = f.Truncate(base.MoovOffset + int64(len(oldMoov)))
		_ = f.Sync()
	}

	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := f.Seek(base.MoovOffset, io.SeekStart); err != nil {
		return err
	}
	mutated = true
	if _, err := io.Copy(f, tmp); err != nil {
		restore()
		return fmt.Errorf("write appended samples: %w", err)
	}
	if _, err := f.Write(moovBytes); err != nil {
		restore()
		return fmt.Errorf("write moov: %w", err)
	}
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], uint32(base.MdatSize+payload))
	if _, err := f.WriteAt(sz[:], base.MdatOffset); err != nil {
		restore()
		return fmt.Errorf("patch mdat size: %w", err)
	}
	newEnd := base.MoovOffset + payload + int64(len(moovBytes))
	if err := f.Truncate(newEnd); err != nil {
		restore()
		return err
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync append: %w", err)
	}
	return nil
}

func appendSamples(dst io.Writer, segs []*SegmentInfo, audio bool, base int64, payload int64, buf []byte) ([]mergedSample, int64, error) {
	var added []mergedSample
	for _, seg := range segs {
		samples := seg.Samples
		if audio {
			samples = seg.AudioSamples
		}
		if len(samples) == 0 {
			continue
		}
		src, err := os.Open(seg.FilePath)
		if err != nil {
			return nil, payload, err
		}
		for _, s := range samples {
			if _, err := copySampleData(src, dst, s.Offset, int64(s.Size), buf); err != nil {
				src.Close()
				return nil, payload, fmt.Errorf("copy sample from %s: %w", seg.FilePath, err)
			}
			added = append(added, mergedSample{
				offset:   base + payload,
				size:     s.Size,
				duration: s.Duration,
			})
			payload += int64(s.Size)
		}
		src.Close()
	}
	return added, payload, nil
}

func samplesToMerged(samples []SampleEntry) []mergedSample {
	out := make([]mergedSample, len(samples))
	for i, s := range samples {
		out[i] = mergedSample{offset: s.Offset, size: s.Size, duration: s.Duration, isKeyFrame: s.IsKeyFrame}
	}
	return out
}

func trackFromSamples(base *SegmentInfo, audio bool, samples []mergedSample) *mergeTrack {
	var duration uint32
	for _, s := range samples {
		duration += s.duration
	}
	tr := &mergeTrack{duration: duration, chunks: chunksFromSamples(samples)}
	if audio {
		tr.isAudio = true
		tr.audioConfig = base.AudioConfig
		tr.timescale = base.AudioTimescale
		return tr
	}
	tr.isH265 = base.Codec == "h265"
	tr.sps = base.SPS
	tr.pps = base.PPS
	tr.vps = base.VPS
	tr.timescale = base.Timescale
	tr.width = base.Width
	tr.height = base.Height
	return tr
}

func chunksFromSamples(samples []mergedSample) []mergeChunk {
	if len(samples) == 0 {
		return nil
	}
	chunks := []mergeChunk{{offset: samples[0].offset, samples: []mergedSample{samples[0]}}}
	for i := 1; i < len(samples); i++ {
		prev := samples[i-1]
		s := samples[i]
		last := &chunks[len(chunks)-1]
		if s.offset == prev.offset+int64(prev.size) {
			last.samples = append(last.samples, s)
			continue
		}
		chunks = append(chunks, mergeChunk{offset: s.offset, samples: []mergedSample{s}})
	}
	return chunks
}

func buildMoovBytes(video, audio *mergeTrack) ([]byte, error) {
	buf := &bytesWriter{}
	if err := writeMergeMoov(mp4.NewWriter(buf), video, audio); err != nil {
		return nil, err
	}
	return append([]byte(nil), buf.Bytes()...), nil
}

func validateMergeSegments(segments []*SegmentInfo) error {
	if len(segments) == 0 {
		return fmt.Errorf("no segments to merge")
	}
	first := segments[0]
	for i, seg := range segments {
		if seg.Codec != first.Codec {
			return fmt.Errorf("segment %d: codec mismatch (%s vs %s)", i, seg.Codec, first.Codec)
		}
		if i == 0 {
			continue
		}
		if !bytesEqual(seg.SPS, first.SPS) || !bytesEqual(seg.PPS, first.PPS) {
			return fmt.Errorf("segment %d: SPS/PPS mismatch", i)
		}
		if first.Codec == "h265" && !bytesEqual(seg.VPS, first.VPS) {
			return fmt.Errorf("segment %d: VPS mismatch", i)
		}
		if seg.HasAudio != first.HasAudio {
			return fmt.Errorf("segment %d: audio presence mismatch", i)
		}
		if seg.HasAudio && !bytesEqual(seg.AudioConfig, first.AudioConfig) {
			return fmt.Errorf("segment %d: audio config mismatch", i)
		}
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type mergeJournal struct {
	mdatOffset int64
	moovOffset int64
	moovSize   int64
	mdatSize   uint32
	ids        []string
	paths      []string
	oldMoov    []byte
}

func journalPath(mp4Path string) string {
	return filepath.Join(filepath.Dir(mp4Path), "."+filepath.Base(mp4Path)+".mjournal")
}

func consumedPath(mp4Path string) string {
	return filepath.Join(filepath.Dir(mp4Path), "."+filepath.Base(mp4Path)+".consumed")
}

func writeMergeJournal(mp4Path string, j mergeJournal) error {
	if len(j.ids) != len(j.paths) || int64(len(j.oldMoov)) != j.moovSize {
		return fmt.Errorf("invalid merge journal")
	}
	final := journalPath(mp4Path)
	tmp := final + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	defer f.Close()

	if _, err := f.Write([]byte("NVRJ")); err != nil {
		return err
	}
	if err := binary.Write(f, binary.BigEndian, uint32(1)); err != nil {
		return err
	}
	if err := binary.Write(f, binary.BigEndian, j.mdatOffset); err != nil {
		return err
	}
	if err := binary.Write(f, binary.BigEndian, j.moovOffset); err != nil {
		return err
	}
	if err := binary.Write(f, binary.BigEndian, j.moovSize); err != nil {
		return err
	}
	if err := binary.Write(f, binary.BigEndian, j.mdatSize); err != nil {
		return err
	}
	if err := binary.Write(f, binary.BigEndian, uint32(len(j.ids))); err != nil {
		return err
	}
	for i := range j.ids {
		if err := writeJournalString(f, j.ids[i]); err != nil {
			return err
		}
		if err := writeJournalString(f, j.paths[i]); err != nil {
			return err
		}
	}
	if _, err := f.Write(j.oldMoov); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

func writeJournalString(w io.Writer, s string) error {
	if len(s) > 0xFFFF {
		return fmt.Errorf("journal string too long")
	}
	if err := binary.Write(w, binary.BigEndian, uint16(len(s))); err != nil {
		return err
	}
	_, err := w.Write([]byte(s))
	return err
}

func readMergeJournal(mp4Path string) (mergeJournal, error) {
	f, err := os.Open(journalPath(mp4Path))
	if err != nil {
		return mergeJournal{}, err
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return mergeJournal{}, err
	}
	if string(magic) != "NVRJ" {
		return mergeJournal{}, fmt.Errorf("bad merge journal magic")
	}
	var version uint32
	if err := binary.Read(f, binary.BigEndian, &version); err != nil {
		return mergeJournal{}, err
	}
	if version != 1 {
		return mergeJournal{}, fmt.Errorf("unsupported merge journal version %d", version)
	}
	var j mergeJournal
	if err := binary.Read(f, binary.BigEndian, &j.mdatOffset); err != nil {
		return mergeJournal{}, err
	}
	if err := binary.Read(f, binary.BigEndian, &j.moovOffset); err != nil {
		return mergeJournal{}, err
	}
	if err := binary.Read(f, binary.BigEndian, &j.moovSize); err != nil {
		return mergeJournal{}, err
	}
	if err := binary.Read(f, binary.BigEndian, &j.mdatSize); err != nil {
		return mergeJournal{}, err
	}
	var n uint32
	if err := binary.Read(f, binary.BigEndian, &n); err != nil {
		return mergeJournal{}, err
	}
	j.ids = make([]string, n)
	j.paths = make([]string, n)
	for i := uint32(0); i < n; i++ {
		id, err := readJournalString(f)
		if err != nil {
			return mergeJournal{}, err
		}
		p, err := readJournalString(f)
		if err != nil {
			return mergeJournal{}, err
		}
		j.ids[i] = id
		j.paths[i] = p
	}
	j.oldMoov = make([]byte, j.moovSize)
	if _, err := io.ReadFull(f, j.oldMoov); err != nil {
		return mergeJournal{}, err
	}
	return j, nil
}

func readJournalString(r io.Reader) (string, error) {
	var n uint16
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return "", err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func restoreJournalFile(mp4Path string, j mergeJournal) error {
	f, err := os.OpenFile(mp4Path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(j.moovOffset); err != nil {
		return err
	}
	if _, err := f.WriteAt(j.oldMoov, j.moovOffset); err != nil {
		return err
	}
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], j.mdatSize)
	if _, err := f.WriteAt(sz[:], j.mdatOffset); err != nil {
		return err
	}
	if err := f.Truncate(j.moovOffset + int64(len(j.oldMoov))); err != nil {
		return err
	}
	return f.Sync()
}

// reconcileMergeJournal finishes or undoes an append that was interrupted.
// If any absorbed segment is still in the database, the hour file is restored
// and hidden sources are put back. If those rows are gone, the append is kept.
func reconcileMergeJournal(ctx context.Context, db *storage.DB, mp4Path string) error {
	j, err := readMergeJournal(mp4Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	pending := false
	for _, id := range j.ids {
		rec, err := db.GetRecording(ctx, id)
		if err != nil {
			return err
		}
		if rec != nil {
			pending = true
			break
		}
	}
	if pending {
		if err := restoreJournalFile(mp4Path, j); err != nil {
			return err
		}
		for _, p := range j.paths {
			hidden := consumedPath(p)
			if _, statErr := os.Stat(hidden); statErr == nil {
				if err := os.Rename(hidden, p); err != nil {
					return err
				}
			}
		}
	} else {
		for _, p := range j.paths {
			_ = os.Remove(consumedPath(p))
		}
	}
	return os.Remove(journalPath(mp4Path))
}

func hideConsumed(path string) error {
	return os.Rename(path, consumedPath(path))
}

func unhideConsumed(paths []string) {
	for _, p := range paths {
		hidden := consumedPath(p)
		if _, err := os.Stat(hidden); err == nil {
			_ = os.Rename(hidden, p)
		}
	}
}
