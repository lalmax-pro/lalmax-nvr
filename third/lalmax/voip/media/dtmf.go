package media

import (
	"github.com/q191201771/lal/pkg/rtprtcp"
)

// DTMFEvent represents a DTMF telephone-event as defined in RFC 4733
type DTMFEvent struct {
	Key      string // "0"-"9", "*", "#", "A"-"D"
	Duration int    // Duration in milliseconds
	Volume   int    // Volume level (0-63)
}

// DTMFEventHandler is called when a complete DTMF event is detected
type DTMFEventHandler func(event DTMFEvent)

type DTMFParser struct {
	clockRate int
	handler   DTMFEventHandler
	lastEvent *dtmfRaw
}

type dtmfRaw struct {
	event    uint8
	endFlag  bool
	volume   uint8
	duration uint16
}

var dtmfKeyMap = map[uint8]string{
	0:  "0",
	1:  "1",
	2:  "2",
	3:  "3",
	4:  "4",
	5:  "5",
	6:  "6",
	7:  "7",
	8:  "8",
	9:  "9",
	10: "*",
	11: "#",
	12: "A",
	13: "B",
	14: "C",
	15: "D",
}

func NewDTMFParser(clockRate int, handler DTMFEventHandler) *DTMFParser {
	if clockRate == 0 {
		clockRate = 8000
	}
	return &DTMFParser{
		clockRate: clockRate,
		handler:   handler,
	}
}

func (p *DTMFParser) Feed(pkt rtprtcp.RtpPacket) {
	if p.handler == nil {
		return
	}

	raw := p.parse(pkt.Body())
	if raw == nil {
		return
	}

	if raw.endFlag {
		if p.lastEvent != nil && p.lastEvent.event == raw.event {
			durationMs := int(raw.duration) * 1000 / p.clockRate
			p.handler(DTMFEvent{
				Key:      dtmfKeyMap[raw.event],
				Duration: durationMs,
				Volume:   int(raw.volume),
			})
			p.lastEvent = nil
		}
	} else {
		p.lastEvent = raw
	}
}

func (p *DTMFParser) parse(payload []byte) *dtmfRaw {
	if len(payload) < 4 {
		return nil
	}

	event := payload[0]
	if event > 15 {
		return nil
	}

	endFlag := (payload[1] & 0x80) != 0
	volume := payload[1] & 0x3F
	duration := uint16(payload[2])<<8 | uint16(payload[3])

	return &dtmfRaw{
		event:    event,
		endFlag:  endFlag,
		volume:   volume,
		duration: duration,
	}
}
