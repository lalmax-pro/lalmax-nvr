package main

import (
	"errors"
	"sync"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/rtp"
)

var errSFUBWEUnknownStream = errors.New("sfu bandwidth observer: unknown stream")

// sfuBWEPassThrough does not pace or buffer RTP. It exists to avoid Pion's
// unbounded default pacer queue while bandwidth estimation is observation-only.
type sfuBWEPassThrough struct {
	mu      sync.RWMutex
	writers map[uint32]interceptor.RTPWriter
	closed  bool
}

func (p *sfuBWEPassThrough) AddStream(ssrc uint32, writer interceptor.RTPWriter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.writers[ssrc] = writer
	}
}
func (*sfuBWEPassThrough) SetTargetBitrate(int) {}
func (p *sfuBWEPassThrough) Write(header *rtp.Header, payload []byte, attrs interceptor.Attributes) (int, error) {
	p.mu.RLock()
	writer := p.writers[header.SSRC]
	p.mu.RUnlock()
	if writer == nil {
		return 0, errSFUBWEUnknownStream
	}
	return writer.Write(header, payload, attrs)
}
func (p *sfuBWEPassThrough) remove(ssrc uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.writers, ssrc)
}
func (p *sfuBWEPassThrough) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	clear(p.writers)
	return nil
}

type sfuBWEFactory struct{ onEstimate func(int) }

func (f *sfuBWEFactory) NewInterceptor(id string) (interceptor.Interceptor, error) {
	pacer := &sfuBWEPassThrough{writers: make(map[uint32]interceptor.RTPWriter)}
	factory, err := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) {
		return gcc.NewSendSideBWE(gcc.SendSideBWEPacer(pacer))
	})
	if err != nil {
		return nil, err
	}
	factory.OnNewPeerConnection(func(_ string, estimator cc.BandwidthEstimator) { estimator.OnTargetBitrateChange(f.onEstimate) })
	inner, err := factory.NewInterceptor(id)
	if err != nil {
		return nil, err
	}
	return &sfuBWEInterceptor{Interceptor: inner, pacer: pacer}, nil
}

type sfuBWEInterceptor struct {
	interceptor.Interceptor
	pacer *sfuBWEPassThrough
}

func (i *sfuBWEInterceptor) UnbindLocalStream(info *interceptor.StreamInfo) {
	i.Interceptor.UnbindLocalStream(info)
	i.pacer.remove(info.SSRC)
}
