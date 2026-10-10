package media

import (
	"net"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/q191201771/lalmax/voip/sdp"
	"github.com/q191201771/naza/pkg/nazalog"
)

func TestRTCPMuxPacketClassification(t *testing.T) {
	tests := []struct {
		name   string
		packet []byte
		want   bool
	}{
		{name: "receiver report", packet: []byte{0x80, 201, 0, 1}, want: true},
		{name: "feedback", packet: []byte{0x81, 206, 0, 1}, want: true},
		{name: "rtp payload below overlap", packet: []byte{0x80, 111, 0, 1}, want: false},
		{name: "rtp marker payload below overlap", packet: []byte{0x80, 239, 0, 1}, want: false},
		{name: "invalid version with rtcp type", packet: []byte{0x00, 201, 0, 1}, want: false},
		{name: "short packet", packet: []byte{0x80}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRTCPMuxPacket(tt.packet); got != tt.want {
				t.Fatalf("isRTCPMuxPacket(%x) = %v, want %v", tt.packet, got, tt.want)
			}
		})
	}
}

func TestReceiverReportCanBeDecodedByPeer(t *testing.T) {
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	stats := &receptionStats{}
	stats.receive(123, 1000, 8000, time.Now())
	s := &RtpSession{conn: sender, remoteAddr: peer.LocalAddr().(*net.UDPAddr), rtcpMux: true, ssrc: 0xabcdef01,
		reports: map[uint32]*receptionStats{0xabcdef01: stats}, log: nazalog.GetGlobalLogger()}
	s.sendRTCP_RR()
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1500)
	n, _, err := peer.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	packets, err := rtcp.Unmarshal(buf[:n])
	if err != nil {
		t.Fatalf("peer cannot decode receiver report: %v", err)
	}
	if len(packets) != 2 {
		t.Fatalf("got %d RTCP packets", len(packets))
	}
	report, ok := packets[0].(*rtcp.ReceiverReport)
	if !ok || len(report.Reports) != 1 || report.Reports[0].SSRC != s.ssrc {
		t.Fatalf("unexpected receiver report: %#v", packets[0])
	}
	if report.Reports[0].LastSequenceNumber != 123 {
		t.Fatal("RR must report sequence number, not packet count")
	}
	if _, ok := packets[1].(*rtcp.SourceDescription); !ok {
		t.Fatal("compound RTCP must include CNAME")
	}
}

func TestIsSTUNPacket(t *testing.T) {
	packet := []byte{
		0x00, 0x01, 0x00, 0x00,
		0x21, 0x12, 0xA4, 0x42,
		0x01, 0x02, 0x03, 0x04,
		0x05, 0x06, 0x07, 0x08,
		0x09, 0x0A, 0x0B, 0x0C,
	}
	if !isSTUNPacket(packet) {
		t.Fatalf("expected STUN packet")
	}
}

func TestIsSTUNPacketRejectsRTP(t *testing.T) {
	packet := []byte{
		0x80, 0x60, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x01,
		0x12, 0x34, 0x56, 0x78,
	}
	if isSTUNPacket(packet) {
		t.Fatalf("expected RTP packet to be rejected")
	}
}

func TestWriteDTMFSendsRFC4733Event(t *testing.T) {
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	s := &RtpSession{
		conn: socket, remoteAddr: peer.LocalAddr().(*net.UDPAddr), payload: &sdp.Payload{PayloadType: 8},
		dtmfPayloadType: 101, dtmfClockRate: 8000, sendSSRC: 0x12345678,
	}
	if err = s.WriteDTMF("5", 120*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err = peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var timestamp uint32
	for i := 0; i < 8; i++ {
		buf := make([]byte, 1500)
		n, _, readErr := peer.ReadFromUDP(buf)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var packet rtp.Packet
		if err = packet.Unmarshal(buf[:n]); err != nil {
			t.Fatal(err)
		}
		if packet.PayloadType != 101 || len(packet.Payload) != 4 || packet.Payload[0] != 5 || packet.SSRC != 0x12345678 {
			t.Fatalf("unexpected telephone-event packet: %+v", packet)
		}
		if packet.Marker != (i == 0) {
			t.Fatalf("unexpected marker on packet %d", i)
		}
		if (packet.Payload[1]&0x80 != 0) != (i >= 5) {
			t.Fatalf("unexpected end flag on packet %d: %08b", i, packet.Payload[1])
		}
		if i == 0 {
			timestamp = packet.Timestamp
		} else if packet.Timestamp != timestamp {
			t.Fatal("event timestamp changed during one digit")
		}
		if packet.SequenceNumber != uint16(i) {
			t.Fatalf("sequence did not advance: %d", packet.SequenceNumber)
		}
	}
}
