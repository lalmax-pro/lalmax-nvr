package webrtcbridge

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type feedbackWriter struct {
	mu      sync.Mutex
	packets []rtcp.Packet
	err     error
}

func (w *feedbackWriter) WriteRTCP(p []rtcp.Packet) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	w.packets = append(w.packets, p...)
	return nil
}
func (w *feedbackWriter) count() int { w.mu.Lock(); defer w.mu.Unlock(); return len(w.packets) }

type feedbackReader struct {
	packets []rtcp.Packet
	done    bool
}

func (r *feedbackReader) ReadRTCP() ([]rtcp.Packet, interceptor.Attributes, error) {
	if r.done {
		return nil, nil, io.EOF
	}
	r.done = true
	return r.packets, nil, nil
}
func TestFeedbackMapsAndFilters(t *testing.T) {
	w := &feedbackWriter{}
	k, err := NewKeyframeRequester(w, 7, 42, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	packets := []rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: 123}, &rtcp.TransportLayerNack{MediaSSRC: 33}}
	if err = k.Handle(33, packets); err != nil || w.count() != 0 {
		t.Fatalf("unrelated RTCP: %v count=%d", err, w.count())
	}
	if err = k.Handle(33, []rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: 33}}); err != nil {
		t.Fatal(err)
	}
	p, ok := w.packets[0].(*rtcp.PictureLossIndication)
	if !ok || p.MediaSSRC != 42 || p.SenderSSRC != 7 {
		t.Fatalf("mapped packet: %+v", p)
	}
	if err = k.Handle(33, []rtcp.Packet{&rtcp.FullIntraRequest{FIR: []rtcp.FIREntry{{SSRC: 33}}}}); err != nil || w.count() != 1 {
		t.Fatalf("rate limit: %v count=%d", err, w.count())
	}
	k.lastSent = time.Now().Add(-2 * time.Second)
	if err = k.Handle(33, []rtcp.Packet{&rtcp.FullIntraRequest{FIR: []rtcp.FIREntry{{SSRC: 44}, {SSRC: 33}}}}); err != nil || w.count() != 2 {
		t.Fatalf("FIR: %v count=%d", err, w.count())
	}
}
func TestConcurrentSubscribersAreAggregated(t *testing.T) {
	w := &feedbackWriter{}
	k, _ := NewKeyframeRequester(w, 7, 42, time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := k.Handle(33, []rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: 33}}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if w.count() != 1 {
		t.Fatalf("writes=%d", w.count())
	}
}
func TestFailedWriteCanRetryAndReaderStops(t *testing.T) {
	sentinel := errors.New("write failed")
	w := &feedbackWriter{err: sentinel}
	k, _ := NewKeyframeRequester(w, 7, 42, time.Minute)
	p := []rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: 33}}
	if err := k.Handle(33, p); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	w.err = nil
	if err := ReadKeyframeFeedback(&feedbackReader{packets: p}, k, 33); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if w.count() != 1 {
		t.Fatal("failed send suppressed retry")
	}
	if _, err := NewKeyframeRequester(nil, 0, 0, time.Second); !errors.Is(err, ErrInvalidFeedbackConfig) {
		t.Fatal(err)
	}
}

func TestPionSenderFeedbackMapsToPublisher(t *testing.T) {
	sender, receiver := pc(t), pc(t)
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, "feedback", "test")
	if err != nil {
		t.Fatal(err)
	}
	rtpSender, err := sender.AddTrack(track)
	if err != nil {
		t.Fatal(err)
	}
	requested := make(chan error, 1)
	receiver.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if _, _, e := remote.ReadRTP(); e != nil {
			requested <- e
			return
		}
		requested <- receiver.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(remote.SSRC())}})
	})
	connect(t, sender, receiver)
	params := rtpSender.GetParameters()
	if len(params.Encodings) == 0 {
		t.Fatal("no sender SSRC")
	}
	writer := &feedbackWriter{}
	requester, _ := NewKeyframeRequester(writer, 7, 42, time.Second)
	stopped := make(chan error, 1)
	go func() { stopped <- ReadKeyframeFeedback(rtpSender, requester, uint32(params.Encodings[0].SSRC)) }()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	var seq uint16
	for writer.count() == 0 {
		select {
		case err := <-requested:
			if err != nil {
				t.Fatal(err)
			}
		case err := <-stopped:
			t.Fatalf("feedback reader stopped: %v", err)
		case <-ticker.C:
			seq++
			if err := track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 3000}, Payload: []byte{0x65, 0x01, 0x02}}); err != nil {
				t.Fatal(err)
			}
		case <-deadline.C:
			t.Fatal("no PLI from Pion receiver")
		}
	}
	if p, ok := writer.packets[0].(*rtcp.PictureLossIndication); !ok || p.MediaSSRC != 42 {
		t.Fatalf("incorrect mapped PLI: %v", writer.packets)
	}
	_ = sender.Close()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("feedback reader did not stop")
	}
}
