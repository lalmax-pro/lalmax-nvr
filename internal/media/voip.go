package media

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lalmax/voip"
	"github.com/q191201771/lalmax/voip/sdp"
)

// VoIPPublisher connects lalmax RTP sessions to the NVR media engine.
// The server disposes media before Remove, ensuring no more frames are fed.
type VoIPPublisher struct {
	engine   Engine
	mu       sync.Mutex
	sessions map[*voip.PubSession]*voipPublishEntry
}

func NewVoIPPublisher(engine Engine) *VoIPPublisher {
	return &VoIPPublisher{engine: engine, sessions: make(map[*voip.PubSession]*voipPublishEntry)}
}

func (p *VoIPPublisher) Add(session *voip.PubSession) error {
	asc, err := voipASC(session)
	if err != nil {
		return err
	}
	pub, err := p.engine.AddCustomizePubSession(context.Background(), session.StreamName())
	if err != nil {
		return err
	}
	pub.WithOption(func(opt *base.AvPacketStreamOption) {
		opt.VideoFormat = base.AvPacketStreamVideoFormatAvcc
		opt.AudioFormat = base.AvPacketStreamAudioFormatRawAac
	})
	if len(asc) > 0 {
		if err := pub.FeedAudioSpecificConfig(asc); err != nil {
			_ = p.engine.DelCustomizePubSession(context.Background(), pub)
			return err
		}
	}
	p.mu.Lock()
	entry := &voipPublishEntry{pub: pub, active: session}
	p.sessions[session] = entry
	p.mu.Unlock()
	session.SetOnAvPacket(entry.feedFor(session))
	return nil
}

func (p *VoIPPublisher) Remove(session *voip.PubSession) error {
	_ = session.Dispose()
	p.mu.Lock()
	entry := p.sessions[session]
	delete(p.sessions, session)
	p.mu.Unlock()
	if entry == nil {
		return nil
	}
	return p.engine.DelCustomizePubSession(context.Background(), entry.pub)
}

type voipPublishEntry struct {
	mu           sync.Mutex
	pub          CustomizePubSession
	active       *voip.PubSession
	offset, last int64
}

func (e *voipPublishEntry) feedFor(session *voip.PubSession) func(base.AvPacket) {
	return func(pkt base.AvPacket) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.active != session {
			return
		}
		pkt.Timestamp += e.offset
		if pkt.Timestamp > e.last {
			e.last = pkt.Timestamp
		}
		_ = e.pub.FeedAvPacket(pkt)
	}
}
func voipASC(session *voip.PubSession) ([]byte, error) {
	var asc []byte
	n := session.Negotiated()
	if n.Audio != nil && n.Audio.Codec == base.AvPacketPtAac {
		for _, parameter := range strings.Split(n.Audio.Fmtp, ";") {
			key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if ok && strings.EqualFold(key, "config") {
				var err error
				asc, err = hex.DecodeString(strings.TrimSpace(value))
				if err != nil {
					return nil, fmt.Errorf("voip AAC config: %w", err)
				}
			}
		}
		if len(asc) < 2 {
			return nil, fmt.Errorf("voip AAC requires AudioSpecificConfig in SDP fmtp")
		}
	}
	return asc, nil
}

// Replace preserves the lal publisher while replacing its RTP receivers.
func (p *VoIPPublisher) Replace(old, next *voip.PubSession) error {
	asc, err := voipASC(next)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.sessions[old]
	if entry == nil {
		return fmt.Errorf("VoIP publisher no longer exists")
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	previous, current := old.Negotiated(), next.Negotiated()
	codec := func(p *sdp.Payload) base.AvPacketPt {
		if p == nil {
			return base.AvPacketPtUnknown
		}
		return p.Codec
	}
	if codec(previous.Audio) != codec(current.Audio) || codec(previous.Video) != codec(current.Video) {
		if reset, ok := entry.pub.(interface{ ResetMedia() error }); ok {
			if err := reset.ResetMedia(); err != nil {
				return err
			}
		}
	}
	if len(asc) > 0 {
		err = entry.pub.FeedAudioSpecificConfig(asc)
		if err != nil {
			return err
		}
	}
	entry.active = next
	entry.offset = entry.last + 1
	next.SetOnAvPacket(entry.feedFor(next))
	delete(p.sessions, old)
	p.sessions[next] = entry
	return nil
}
