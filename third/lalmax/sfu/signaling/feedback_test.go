package signaling

import (
	"sync"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/webrtcbridge"
)

type feedbackSink struct {
	mu      sync.Mutex
	packets []rtcp.Packet
}

func (s *feedbackSink) WriteRTCP(p []rtcp.Packet) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.packets = append(s.packets, p...)
	return nil
}
func (s *feedbackSink) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.packets) }

func TestFeedbackIsScopedRateLimitedAndStopsWithSource(t *testing.T) {
	sink := &feedbackSink{}
	requester, err := webrtcbridge.NewKeyframeRequester(sink, 0, 123, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	key := feedbackKey{room: sfu.Key{Namespace: "a", ID: "room"}, generation: "generation", publication: "publication"}
	factory := &RoomFactory{feedback: map[feedbackKey]*feedbackBinding{key: {requester: requester, stopped: stopped}}}
	pli := []rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: 456}}
	wrongRoom := key
	wrongRoom.room.Namespace = "b"
	wrongGeneration := key
	wrongGeneration.generation = "new"
	wrongPublication := key
	wrongPublication.publication = "other"
	for _, candidate := range []feedbackKey{wrongRoom, wrongGeneration, wrongPublication} {
		factory.forwardFeedback(candidate, 456, pli)
	}
	factory.forwardFeedback(key, 999, pli)
	if sink.count() != 0 {
		t.Fatal("unrelated feedback reached publisher")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); factory.forwardFeedback(key, 456, pli) }()
	}
	wg.Wait()
	if sink.count() != 1 {
		t.Fatalf("feedback was not aggregated: %d", sink.count())
	}
	close(stopped)
	other, _ := webrtcbridge.NewKeyframeRequester(sink, 0, 123, time.Second)
	factory.feedback[key].requester = other
	factory.forwardFeedback(key, 456, pli)
	if sink.count() != 1 {
		t.Fatal("ended source received feedback")
	}
}
