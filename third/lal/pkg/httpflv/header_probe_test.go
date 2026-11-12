package httpflv

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/q191201771/lal/pkg/base"
)

func probeTestSession(t *testing.T) (*SubSession, net.Conn) {
	t.Helper()
	server, client := net.Pipe()
	s := NewSubSession(server, base.UrlContext{}, false, "")
	t.Cleanup(func() { _ = s.Dispose(); _ = client.Close() })
	return s, client
}

func readProbeBytes(t *testing.T, client net.Conn, size int) []byte {
	t.Helper()
	if err := client.SetReadDeadline(time.Now().Add(3*time.Second)); err != nil { t.Fatal(err) }
	data := make([]byte, size)
	if _, err := io.ReadFull(client, data); err != nil { t.Fatal(err) }
	return data
}

func TestFLVNoMetadataDiscoversSecondTrackBeforeHeader(t *testing.T) {
	video := PackHttpflvTag(TagTypeVideo, 0, []byte{0x17, 0x01, 0, 0, 0, 0x65})
	audio := PackHttpflvTag(TagTypeAudio, 20, []byte{0xaf, 0x01, 0x11})
	for _, tt := range []struct {
		name string
		firstAudio bool
	}{
		{"video-then-audio", false},
		{"audio-then-video", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, peer := probeTestSession(t)
			first, second := video, audio
			if tt.firstAudio { first, second = audio, video }
			s.ObserveFlvTracks(tt.firstAudio, !tt.firstAudio)
			s.Write(first)
			if s.FlvHeaderWritten() {
				t.Fatal("header locked to one track before bounded probe finished")
			}
			s.ObserveFlvTracks(true, true)
			s.Write(second)
			result := readProbeBytes(t, peer, 13+len(first)+len(second))
			if result[4] != 0x05 {
				t.Fatalf("metadata-free dual track advertises flags=%02x, want 05",result[4])
			}
			if !bytes.Equal(result[13:13+len(first)], first) || !bytes.Equal(result[13+len(first):], second) {
				t.Fatal("buffered FLV tags were reordered or duplicated")
			}
			if !s.FlvHeaderWritten() { t.Fatal("header not written") }
		})
	}
}

func TestFLVSingleTrackProbeHasBoundedLatency(t *testing.T) {
	for _, tt := range []struct {
		name string
		audio, video bool
		want byte
	}{
		{"video-only", false,true,0x01},
		{"audio-only", true,false,0x04},
	} {
		t.Run(tt.name,func(t *testing.T){
			s,peer := probeTestSession(t)
			tagType := TagTypeVideo
			if tt.audio { tagType = TagTypeAudio }
			tag := PackHttpflvTag(tagType,0,[]byte{0x01,0x02,0x03})
			started := time.Now()
			s.ObserveFlvTracks(tt.audio,tt.video)
			s.Write(tag)
			result:=readProbeBytes(t,peer,13+len(tag))
			if result[4]!=tt.want || !bytes.Equal(result[13:],tag) {
				t.Fatalf("wrong header/packet for %s: header=%02x",tt.name,result[4])
			}
			elapsed:=time.Since(started)
			if elapsed>2*time.Second { t.Fatalf("bounded probe took %v",elapsed) }
		})
	}
}

func TestFLVMetadataAlreadyAnnouncesBothTracks(t *testing.T) {
	s,peer:=probeTestSession(t)
	// A metadata-announced delayed audio track must be present in the first
	// FLV header even before its initial AAC or PCMA packet arrives.
	s.ObserveFlvTracks(true,true)
	tag:=PackHttpflvTag(TagTypeVideo,0,[]byte{0x17,0x01,0,0,0,0x65})
	s.Write(tag)
	got:=readProbeBytes(t,peer,13+len(tag))
	if got[4]!=0x05 {t.Fatalf("advertised delayed audio omitted: %x",got[:13])}
}

func TestFLVNoMediaDoesNotEmitSpeculativeHeader(t *testing.T) {
	s,peer:=probeTestSession(t)
	metadata:=PackHttpflvTag(TagTypeMetadata,0,[]byte{0x02,0x01})
	s.ObserveFlvTracks(false,false)
	s.Write(metadata)
	if s.FlvHeaderWritten(){t.Fatal("metadata-only stream wrote FLV header")}
	// The data remains buffered until media is observed.
	s.ObserveFlvTracks(false,true)
	got:=readProbeBytes(t,peer,13+len(metadata))
	if got[4]!=0x01 || !bytes.Equal(got[13:],metadata) {
		t.Fatalf("metadata was not flushed after actual track discovery: %x",got)
	}
}
