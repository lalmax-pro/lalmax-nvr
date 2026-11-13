package api

import "testing"

func TestDerivedOfferAudioNegotiationFailClosed(t *testing.T) {
    offer := []byte("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 8 0 111\r\na=recvonly\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:111 opus/48000/2\r\n")
    if !homeAssistantOfferSupportsAudio(offer, "PCMA/8000") {t.Fatal("PCMA should negotiate")}
    if !homeAssistantOfferSupportsAudio(offer, "opus/48000/2") {t.Fatal("Opus should negotiate")}
    for _, bad := range []string{"AAC/48000", "G722/8000", "unknown"} {
        if homeAssistantOfferSupportsAudio(offer,bad) {t.Fatalf("negotiated unsupported codec %s",bad)}
    }
    videoOnly := []byte("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=video 9 UDP/TLS/RTP/SAVPF 102\r\na=rtpmap:102 H264/90000\r\n")
    if homeAssistantOfferSupportsAudio(videoOnly,"PCMA/8000") {t.Fatal("video only offer accepted as audio")}
    muted := []byte("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 8\r\na=sendonly\r\na=rtpmap:8 PCMA/8000\r\n")
    if homeAssistantOfferSupportsAudio(muted,"PCMA/8000") {t.Fatal("sendonly audio accepted")}
}
