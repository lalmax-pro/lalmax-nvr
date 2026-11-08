package bridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lalmax/sfu/rtpforward"
)

func TestRunnerSlowSinkDoesNotBlockOtherSubscribers(t *testing.T) {
	f, err := rtpforward.New(2, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sub, err := f.Subscribe("bridge")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := f.Subscribe("viewer")
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	entered := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunH264(ctx, sub, func(ctx context.Context, _ *base.AvPacket) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	if err := f.WriteRTP(packet(1, 1, true, stap([]byte{0x67, 1}, []byte{0x68, 2}, []byte{0x65, 0xb0})...)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("sink did not start")
	}
	<-viewer.Packets()
	for i := uint16(2); i <= 3; i++ {
		if err := f.WriteRTP(packet(i, uint32(i), true, 0x41, 0xc0)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-viewer.Packets():
		case <-time.After(time.Second):
			t.Fatal("viewer stalled")
		}
	}
	if !errors.Is(sub.Err(), rtpforward.ErrSlowSubscriber) {
		t.Fatalf("subscriber error: %v", sub.Err())
	}
	if viewer.Err() != nil {
		t.Fatalf("viewer disconnected: %v", viewer.Err())
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
}

func TestRunnerSinkFailureClosesSubscription(t *testing.T) {
	f, _ := rtpforward.New(1, 1)
	defer f.Close()
	sub, _ := f.Subscribe("bridge")
	want := errors.New("sink failed")
	done := make(chan error, 1)
	go func() {
		done <- RunH264(context.Background(), sub, func(context.Context, *base.AvPacket) error { return want })
	}()
	if err := f.WriteRTP(packet(1, 1, true, stap([]byte{0x67, 1}, []byte{0x68, 2}, []byte{0x65, 0xb0})...)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
	if !errors.Is(sub.Err(), rtpforward.ErrSubscriberClosed) {
		t.Fatalf("subscriber not released: %v", sub.Err())
	}
}

func TestUnsupportedSliceStopsBridgeButNotViewer(t *testing.T) {
	f, err := rtpforward.New(2, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sub, err := f.Subscribe("bridge")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := f.Subscribe("viewer")
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	done := make(chan error, 1)
	go func() {
		done <- RunH264(context.Background(), sub, func(context.Context, *base.AvPacket) error { return nil })
	}()
	if err := f.WriteRTP(packet(1, 1, true, 0x41, 0x9e, 0x42)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnsupportedSlice) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop")
	}
	select {
	case <-viewer.Packets():
	case <-time.After(time.Second):
		t.Fatal("viewer did not receive packet")
	}
	if err := f.WriteRTP(packet(2, 9001, true, 0x41, 0xc0)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-viewer.Packets():
	case <-time.After(time.Second):
		t.Fatal("viewer stopped with bridge")
	}
}
