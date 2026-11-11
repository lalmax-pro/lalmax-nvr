// Package bwe provides a Send Side Bandwidth Estimation interceptor for SFU
// senders. The estimate belongs to the downstream PeerConnection.
package bwe

import (
	"errors"
	"sync"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/rtp"
)

const InitialEstimateBPS = 1_000_000

var ErrUnknownStream = errors.New("sfu bwe: unknown stream")

// ObserverFactory creates isolated GCC state for each PeerConnection.
type ObserverFactory struct{ onEstimate func(int) }

func NewObserverFactory(onEstimate func(int)) *ObserverFactory {
	return &ObserverFactory{onEstimate: onEstimate}
}

func (f *ObserverFactory) NewInterceptor(id string) (interceptor.Interceptor, error) {
	pacer := &passThrough{writers: make(map[uint32]interceptor.RTPWriter)}
	factory, err := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) {
		return gcc.NewSendSideBWE(gcc.SendSideBWEPacer(pacer), gcc.SendSideBWEInitialBitrate(InitialEstimateBPS))
	})
	if err != nil {
		return nil, err
	}
	factory.OnNewPeerConnection(func(_ string, estimator cc.BandwidthEstimator) {
		if f.onEstimate != nil {
			estimator.OnTargetBitrateChange(f.onEstimate)
		}
	})
	inner, err := factory.NewInterceptor(id)
	if err != nil {
		return nil, err
	}
	return &observer{Interceptor: inner, pacer: pacer}, nil
}

type passThrough struct {
	mu      sync.RWMutex
	writers map[uint32]interceptor.RTPWriter
	closed  bool
}

func (p *passThrough) AddStream(ssrc uint32, writer interceptor.RTPWriter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.writers[ssrc] = writer
	}
}
func (*passThrough) SetTargetBitrate(int) {}
func (p *passThrough) Write(header *rtp.Header, payload []byte, attrs interceptor.Attributes) (int, error) {
	p.mu.RLock()
	writer := p.writers[header.SSRC]
	p.mu.RUnlock()
	if writer == nil {
		return 0, ErrUnknownStream
	}
	return writer.Write(header, payload, attrs)
}
func (p *passThrough) RemoveStream(ssrc uint32) {
	p.mu.Lock()
	delete(p.writers, ssrc)
	p.mu.Unlock()
}
func (p *passThrough) Close() error {
	p.mu.Lock()
	p.closed = true
	clear(p.writers)
	p.mu.Unlock()
	return nil
}

type observer struct {
	interceptor.Interceptor
	pacer *passThrough
}

func (i *observer) UnbindLocalStream(info *interceptor.StreamInfo) {
	i.Interceptor.UnbindLocalStream(info)
	i.pacer.RemoveStream(info.SSRC)
}
