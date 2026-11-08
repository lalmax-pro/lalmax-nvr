package signaling

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
)

type senderReportTarget struct {
	session     *access.Session
	publication string
	stopped     <-chan struct{}
}
type senderReportCollector struct {
	targets map[uint32]*senderReportTarget
	seen    map[uint32]struct{}
}

func isSenderReportTimeout(err error) bool {
	var netErr net.Error
	return errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout())
}

// bind is called with the factory lock held.
func (c *senderReportCollector) bind(ssrc uint32, target *senderReportTarget) bool {
	if _, reused := c.seen[ssrc]; reused {
		delete(c.targets, ssrc)
		return false
	}
	if len(c.seen) >= 1024 {
		return false
	}
	c.seen[ssrc] = struct{}{}
	c.targets[ssrc] = target
	return true
}

// Each receiver has exactly one RTCP reader, even when a transceiver is
// reused for a replacement track. Bindings are removed conditionally so a
// delayed callback from the old track cannot remove a new SSRC mapping.
func (f *RoomFactory) trackSenderReports(ctx context.Context, receiver *webrtc.RTPReceiver, session *access.Session, publication string, ssrc uint32, stopped <-chan struct{}) {
	f.reportMu.Lock()
	if f.reportReaders == nil {
		f.reportReaders = make(map[*webrtc.RTPReceiver]*senderReportCollector)
	}
	collector := f.reportReaders[receiver]
	if collector == nil {
		collector = &senderReportCollector{targets: make(map[uint32]*senderReportTarget), seen: make(map[uint32]struct{})}
		f.reportReaders[receiver] = collector
		go f.readSenderReports(ctx, receiver, collector)
	}
	// RTCP has no publication generation. On SSRC reuse, an in-flight report
	// from an earlier track cannot be distinguished from the replacement.
	target := &senderReportTarget{session: session, publication: publication, stopped: stopped}
	if !collector.bind(ssrc, target) {
		f.reportMu.Unlock()
		return
	}
	f.reportMu.Unlock()
	go func() {
		select {
		case <-stopped:
		case <-ctx.Done():
		}
		f.reportMu.Lock()
		defer f.reportMu.Unlock()
		if collector.targets[ssrc] == target {
			delete(collector.targets, ssrc)
		}
	}()
}

// Short deadlines let the reader exit after PeerConnection shutdown without
// closing its RTP receiver. Simulcast is deliberately excluded by the caller.
func (f *RoomFactory) readSenderReports(ctx context.Context, receiver *webrtc.RTPReceiver, collector *senderReportCollector) {
	defer func() {
		f.reportMu.Lock()
		defer f.reportMu.Unlock()
		if f.reportReaders[receiver] == collector {
			delete(f.reportReaders, receiver)
		}
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := receiver.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			return
		}
		packets, _, err := receiver.ReadRTCP()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if isSenderReportTimeout(err) {
				continue
			}
			return
		}
		for _, packet := range packets {
			report, ok := packet.(*rtcp.SenderReport)
			if !ok || report.NTPTime == 0 {
				continue
			}
			f.reportMu.Lock()
			target := collector.targets[report.SSRC]
			f.reportMu.Unlock()
			if target == nil {
				continue
			}
			select {
			case <-target.stopped:
				continue
			default:
			}
			_ = target.session.RecordIncomingSenderReport(target.publication, sfu.SenderReport{SSRC: report.SSRC, NTP: report.NTPTime, RTP: report.RTPTime, ReceivedAt: time.Now()})
		}
	}
}
