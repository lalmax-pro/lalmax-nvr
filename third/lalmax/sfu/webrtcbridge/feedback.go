package webrtcbridge

import (
	"errors"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
)

var ErrInvalidFeedbackConfig = errors.New("webrtcbridge: invalid feedback configuration")

// RTCPWriter is implemented by a publisher-side Pion PeerConnection.
type RTCPWriter interface{ WriteRTCP([]rtcp.Packet) error }

// RTCPReader is implemented by a subscriber-side Pion RTPSender.
type RTCPReader interface {
	ReadRTCP() ([]rtcp.Packet, interceptor.Attributes, error)
}

// KeyframeRequester aggregates subscribers' requests for one upstream video
// track. Share one instance per source SSRC across all its subscribers.
// It sends upstream PLI; it does not forward NACK or other RTCP packets.
type KeyframeRequester struct {
	mu                     sync.Mutex
	writer                 RTCPWriter
	senderSSRC, sourceSSRC uint32
	minInterval            time.Duration
	lastSent               time.Time
}

func NewKeyframeRequester(writer RTCPWriter, senderSSRC, sourceSSRC uint32, minInterval time.Duration) (*KeyframeRequester, error) {
	if writer == nil || minInterval <= 0 {
		return nil, ErrInvalidFeedbackConfig
	}
	return &KeyframeRequester{writer: writer, senderSSRC: senderSSRC, sourceSSRC: sourceSSRC, minInterval: minInterval}, nil
}

// Handle accepts only PLI/FIR referring to the expected outgoing SSRC, maps
// it to the source SSRC, and rate-limits across all calls. Caller must bind
// the expected SSRC to the correct negotiated subscription and manage auth.
// FIR is translated into PLI rather than forwarding FIR sequence state.
func (k *KeyframeRequester) Handle(outgoingSSRC uint32, packets []rtcp.Packet) error {
	matched := false
	for _, packet := range packets {
		switch p := packet.(type) {
		case *rtcp.PictureLossIndication:
			if p != nil && p.MediaSSRC == outgoingSSRC {
				matched = true
			}
		case *rtcp.FullIntraRequest:
			if p != nil {
				for _, entry := range p.FIR {
					if entry.SSRC == outgoingSSRC {
						matched = true
						break
					}
				}
			}
		}
		if matched {
			break
		}
	}
	if !matched {
		return nil
	}
	return k.Request()
}

// Request asks the source for a keyframe, sharing Handle's rate limit.
func (k *KeyframeRequester) Request() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := time.Now()
	if !k.lastSent.IsZero() && now.Sub(k.lastSent) < k.minInterval {
		return nil
	}
	if err := k.writer.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{SenderSSRC: k.senderSSRC, MediaSSRC: k.sourceSSRC}}); err != nil {
		return err
	}
	k.lastSent = now
	return nil
}

// ReadKeyframeFeedback drains RTCP for one subscriber until reader or writer
// fails. Closing its PeerConnection/RTPSender must unblock ReadRTCP; the caller
// owns that lifecycle. NACK handling remains the responsibility of Pion's
// configured interceptors and is not forwarded to the publisher here.
func ReadKeyframeFeedback(reader RTCPReader, requester *KeyframeRequester, outgoingSSRC uint32) error {
	if reader == nil || requester == nil {
		return ErrInvalidFeedbackConfig
	}
	for {
		packets, _, err := reader.ReadRTCP()
		if err != nil {
			return err
		}
		if err = requester.Handle(outgoingSSRC, packets); err != nil {
			return err
		}
	}
}
