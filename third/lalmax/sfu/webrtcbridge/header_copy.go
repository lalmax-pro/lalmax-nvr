package webrtcbridge

import (
	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

// CopyRTPHeaderFactory isolates each outgoing RTP header before downstream
// interceptors mutate it. Register it after the TWCC header writer and before
// Pion's NACK responder, so retransmissions cannot concurrently mutate the
// responder's shared cached header while each gets a fresh transport sequence.
type CopyRTPHeaderFactory struct{}

func (CopyRTPHeaderFactory) NewInterceptor(string) (interceptor.Interceptor, error) {
	return &copyRTPHeader{}, nil
}

type copyRTPHeader struct{ interceptor.NoOp }

func (*copyRTPHeader) BindLocalStream(_ *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	return interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, attrs interceptor.Attributes) (int, error) {
		if header == nil {
			return writer.Write(header, payload, attrs)
		}
		copy := header.Clone()
		return writer.Write(&copy, payload, attrs)
	})
}
