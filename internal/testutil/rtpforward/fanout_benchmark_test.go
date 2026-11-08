package rtpforward

import (
	"fmt"
	"testing"

	"github.com/pion/rtp"
)

// BenchmarkFanout measures in-process packet copying and queue delivery with
// immediately drained subscribers. It does not measure WebRTC or network I/O.
func BenchmarkFanout(b *testing.B) {
	for _, count := range []int{1, 8, 32, 128} {
		b.Run(fmt.Sprintf("subscribers_%d", count), func(b *testing.B) {
			fanout, err := New(count, 1)
			if err != nil {
				b.Fatal(err)
			}
			defer fanout.Close()
			subscribers := make([]*Subscriber, count)
			for i := range subscribers {
				subscribers[i], err = fanout.Subscribe(fmt.Sprintf("subscriber-%d", i))
				if err != nil {
					b.Fatal(err)
				}
			}
			packet := &rtp.Packet{Header: rtp.Header{Version: 2}, Payload: make([]byte, 1200)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := fanout.WriteRTP(packet); err != nil {
					b.Fatal(err)
				}
				for _, subscriber := range subscribers {
					if _, ok := <-subscriber.Packets(); !ok {
						b.Fatal("subscriber unexpectedly closed")
					}
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(b.N)*float64(count)/b.Elapsed().Seconds(), "deliveries/s")
		})
	}
}
