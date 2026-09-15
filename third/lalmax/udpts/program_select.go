package udpts

import (
	"github.com/asticode/go-astits"
	"github.com/q191201771/naza/pkg/nazalog"
)

// programSelector 根据 PAT/PMT 选定 MPEG-TS program_number，并给出该 program 的 elementary PID。
// wanted==0 时自适应：优先含 H264/H265 的 program，否则 PAT 中第一个 program。
type programSelector struct {
	name   string
	wanted uint16

	patOrder []uint16
	pmtPID   map[uint16]uint16
	pmts     map[uint16]*astits.PMTData

	selected uint16
	esType   map[uint16]astits.StreamType
	keep     map[uint16]struct{}

	loggedSelect  bool
	warnedMissing bool
}

func newProgramSelector(wanted uint16, streamName string) *programSelector {
	return &programSelector{
		name:   streamName,
		wanted: wanted,
		pmtPID: make(map[uint16]uint16),
		pmts:   make(map[uint16]*astits.PMTData),
	}
}

func (s *programSelector) onPAT(pat *astits.PATData) {
	if pat == nil {
		return
	}
	s.patOrder = s.patOrder[:0]
	s.pmtPID = make(map[uint16]uint16, len(pat.Programs))
	for _, pgm := range pat.Programs {
		if pgm == nil || pgm.ProgramNumber == 0 {
			continue
		}
		s.pmtPID[pgm.ProgramNumber] = pgm.ProgramMapID
		s.patOrder = append(s.patOrder, pgm.ProgramNumber)
	}
	if s.wanted > 0 {
		if _, ok := s.pmtPID[s.wanted]; ok {
			s.trySelect(s.wanted)
		} else {
			s.warnMissing()
		}
		return
	}
	if s.selected != 0 {
		if _, ok := s.pmtPID[s.selected]; !ok {
			s.clearSelection()
		} else {
			s.refreshKeep()
			return
		}
	}
	s.tryAuto()
}

func (s *programSelector) onPMT(pmt *astits.PMTData) {
	if pmt == nil || pmt.ProgramNumber == 0 {
		return
	}
	s.pmts[pmt.ProgramNumber] = pmt
	if s.wanted > 0 {
		if pmt.ProgramNumber == s.wanted {
			s.selectPMT(pmt)
		}
		return
	}
	if s.selected != 0 {
		if pmt.ProgramNumber == s.selected {
			s.selectPMT(pmt)
		}
		return
	}
	s.tryAuto()
}

func (s *programSelector) tryAuto() {
	if s.wanted != 0 || s.selected != 0 {
		return
	}
	for _, pn := range s.patOrder {
		if pmt, ok := s.pmts[pn]; ok && pmtHasH26x(pmt) {
			s.selectPMT(pmt)
			return
		}
	}
	for _, pmt := range s.pmts {
		if pmtHasH26x(pmt) {
			s.selectPMT(pmt)
			return
		}
	}
	if len(s.pmtPID) == 0 || len(s.pmts) < len(s.pmtPID) {
		return
	}
	for _, pn := range s.patOrder {
		if pmt, ok := s.pmts[pn]; ok {
			s.selectPMT(pmt)
			return
		}
	}
}

func (s *programSelector) trySelect(pn uint16) {
	if pmt, ok := s.pmts[pn]; ok {
		s.selectPMT(pmt)
	}
}

func (s *programSelector) selectPMT(pmt *astits.PMTData) {
	if pmt == nil || pmt.ProgramNumber == 0 {
		return
	}
	changed := s.selected != pmt.ProgramNumber
	s.selected = pmt.ProgramNumber
	s.esType = make(map[uint16]astits.StreamType, len(pmt.ElementaryStreams))
	for _, es := range pmt.ElementaryStreams {
		if es == nil || es.ElementaryPID == 0 {
			continue
		}
		s.esType[es.ElementaryPID] = es.StreamType
	}
	s.refreshKeep()
	if changed && !s.loggedSelect {
		s.loggedSelect = true
		mode := "auto"
		if s.wanted != 0 {
			mode = "specified"
		}
		nazalog.Infof("udp ts [%s] selected program_number=%d mode=%s streams=%d", s.name, s.selected, mode, len(s.esType))
	}
}

func (s *programSelector) refreshKeep() {
	keep := make(map[uint16]struct{}, len(s.esType)+2)
	keep[0] = struct{}{}
	if pid, ok := s.pmtPID[s.selected]; ok {
		keep[pid] = struct{}{}
	}
	for pid := range s.esType {
		keep[pid] = struct{}{}
	}
	s.keep = keep
}

func (s *programSelector) clearSelection() {
	s.selected = 0
	s.esType = nil
	s.keep = nil
	s.loggedSelect = false
}

func (s *programSelector) warnMissing() {
	if s.warnedMissing {
		return
	}
	s.warnedMissing = true
	nazalog.Warnf("udp ts [%s] program_number=%d not found in PAT", s.name, s.wanted)
}

func (s *programSelector) streamType(pid uint16) (astits.StreamType, bool) {
	if s.selected == 0 {
		return 0, false
	}
	st, ok := s.esType[pid]
	return st, ok
}

func (s *programSelector) skipPID(pid uint16) bool {
	if s.selected == 0 {
		return false
	}
	_, ok := s.keep[pid]
	return !ok
}

func pmtHasH26x(pmt *astits.PMTData) bool {
	if pmt == nil {
		return false
	}
	for _, es := range pmt.ElementaryStreams {
		if es == nil {
			continue
		}
		switch es.StreamType {
		case astits.StreamTypeH264Video, astits.StreamTypeH265Video:
			return true
		}
	}
	return false
}

func pesTimeMs(pes *astits.PESData) (pts, dts uint64) {
	if pes == nil || pes.Header == nil || pes.Header.OptionalHeader == nil {
		return 0, 0
	}
	oh := pes.Header.OptionalHeader
	if oh.PTS != nil {
		pts = uint64(oh.PTS.Base / 90)
	}
	if oh.DTS != nil {
		dts = uint64(oh.DTS.Base / 90)
	} else {
		dts = pts
	}
	return pts, dts
}
