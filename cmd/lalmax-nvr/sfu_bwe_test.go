package main

import (
	"sync"
	"testing"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

func TestSFUBWEPassThroughLifecycle(t *testing.T) {
	p := &sfuBWEPassThrough{writers: make(map[uint32]interceptor.RTPWriter)}
	packet := &rtp.Header{Version: 2, SSRC: 1}
	_, err := p.Write(packet, []byte{1}, nil)
	require.ErrorIs(t, err, errSFUBWEUnknownStream)
	p.AddStream(1, interceptor.RTPWriterFunc(func(_ *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) { return len(payload), nil }))
	n, err := p.Write(packet, []byte{1}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	wrapped := &sfuBWEInterceptor{Interceptor: &interceptor.NoOp{}, pacer: p}
	wrapped.UnbindLocalStream(&interceptor.StreamInfo{SSRC: 1})
	_, err = p.Write(packet, []byte{1}, nil)
	require.ErrorIs(t, err, errSFUBWEUnknownStream)
	p.AddStream(1, interceptor.RTPWriterFunc(func(_ *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) { return len(payload), nil }))
	require.NoError(t, p.Close())
	p.AddStream(1, interceptor.RTPWriterFunc(func(_ *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) { return len(payload), nil }))
	_, err = p.Write(packet, []byte{1}, nil)
	require.ErrorIs(t, err, errSFUBWEUnknownStream)
	require.NoError(t, p.Close())
}

func TestSFUBWEPassThroughConcurrentLifecycle(t *testing.T) {
	p := &sfuBWEPassThrough{writers: make(map[uint32]interceptor.RTPWriter)}
	writer := interceptor.RTPWriterFunc(func(_ *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) { return len(payload), nil })
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				p.AddStream(1, writer)
				_, _ = p.Write(&rtp.Header{Version: 2, SSRC: 1}, []byte{1}, nil)
				p.remove(1)
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Close() }()
	wg.Wait()
	_, err := p.Write(&rtp.Header{Version: 2, SSRC: 1}, []byte{1}, nil)
	require.ErrorIs(t, err, errSFUBWEUnknownStream)
}

func TestSFUBWEFactoryUsesIndependentPeerState(t *testing.T) {
	factory := &sfuBWEFactory{onEstimate: func(int) {}}
	first, err := factory.NewInterceptor("first")
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	second, err := factory.NewInterceptor("second")
	require.NoError(t, err)
	firstPacer := first.(*sfuBWEInterceptor).pacer
	secondPacer := second.(*sfuBWEInterceptor).pacer
	require.NotSame(t, firstPacer, secondPacer)
	firstPacer.AddStream(1, interceptor.RTPWriterFunc(func(_ *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) { return len(payload), nil }))
	require.NoError(t, second.Close())
	n, err := firstPacer.Write(&rtp.Header{Version: 2, SSRC: 1}, []byte{1}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}
