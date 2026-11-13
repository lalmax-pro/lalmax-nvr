package api

import (
    "slices"
    "strings"

    "github.com/pion/sdp/v3"
)

// Validate that the offered receive-only audio m-section can actually
// negotiate the derived RTP codec. An AAC-only offer must not be accepted.
func homeAssistantOfferSupportsAudio(offer []byte, codec string) bool {
    var description sdp.SessionDescription
    if description.Unmarshal(offer) != nil { return false }
    for _, m := range description.MediaDescriptions {
        if m.MediaName.Media != "audio" || m.MediaName.Port.Value == 0 { continue }
        if _, inactive := m.Attribute("inactive"); inactive { continue }
        if _, sendonly := m.Attribute("sendonly"); sendonly { continue }
        for _, a := range m.Attributes {
            if a.Key != "rtpmap" { continue }
            fields := strings.Fields(a.Value)
            if len(fields) == 2 && slices.Contains(m.MediaName.Formats, fields[0]) &&
                strings.EqualFold(fields[1], codec) {
                return true
            }
        }
    }
    return false
}
