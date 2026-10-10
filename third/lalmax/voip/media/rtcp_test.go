package media

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lalmax/voip/sdp"
)

func TestReceptionReportLossWrapDuplicateAndSR(t *testing.T) {
	stats := &receptionStats{}
	now := time.Now()
	for i, seq := range []uint16{65534, 65535, 0, 2, 2} {
		stats.receive(seq, uint32(i*160), 8000, now.Add(time.Duration(i)*30*time.Millisecond))
	}
	stats.lastSR, stats.lastSRAt = 0x33445566, now.Add(-time.Second)
	report := stats.report(42, now)
	if report.LastSequenceNumber != 65538 || report.TotalLost != 1 || report.FractionLost != 51 || report.Jitter == 0 || report.LastSenderReport != 0x33445566 || report.Delay != 65536 {
		t.Fatalf("incorrect loss/jitter/SR statistics: %#v", report)
	}
	stats.receive(1, 480, 8000, now.Add(140*time.Millisecond))
	report = stats.report(42, now)
	if report.TotalLost != 0 || report.FractionLost != 0 {
		t.Fatal("late packet did not recover cumulative loss")
	}
	if _, fresh := stats.receive(1, 480, 8000, now); fresh {
		t.Fatal("duplicate packet counted twice")
	}
}

func TestSeparateRTCPPortReceivesSRAndSendsCompoundRR(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "RTP", true: "SRTP"}[secure], func(t *testing.T) { testSeparateRTCP(t, secure) })
	}
}

func testSeparateRTCP(t *testing.T, secure bool) {
	t.Helper()
	var receiveCrypto, peerCrypto *SrtpContext
	if secure {
		var err error
		local, remote := bytes.Repeat([]byte{1}, 30), bytes.Repeat([]byte{2}, 30)
		receiveCrypto, err = CreateSrtpContextPair(local, remote, CryptoSuiteAES128_CM_HMAC_SHA1_80)
		if err != nil {
			t.Fatal(err)
		}
		peerCrypto, err = CreateSrtpContextPair(remote, local, CryptoSuiteAES128_CM_HMAC_SHA1_80)
		if err != nil {
			t.Fatal(err)
		}
		defer receiveCrypto.Close()
		defer peerCrypto.Close()
	}
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	pa := NewPortAllocator(62000, 65000)
	rtpPort, rtcpPort, err := pa.AllocMedia(false)
	if err != nil {
		t.Fatal(err)
	}
	defer pa.Free(rtpPort)
	defer pa.Free(rtcpPort)
	s, err := NewRtpSession(RtpSessionConfig{MediaIP: "127.0.0.1", Port: rtpPort, RTCPPort: rtcpPort,
		SrtpContext: receiveCrypto, RemoteRTCPAddress: peer.LocalAddr().String(), Payload: &sdp.Payload{PayloadType: 8, Codec: base.AvPacketPtG711A, ClockRate: 8000}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rtp, err := net.DialUDP("udp", nil, s.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer rtp.Close()
	for _, seq := range []uint16{100, 102, 102} {
		packet := make([]byte, 172)
		packet[0], packet[1] = 0x80, 8
		binary.BigEndian.PutUint16(packet[2:], seq)
		binary.BigEndian.PutUint32(packet[8:], 42)
		if secure {
			packet, err = peerCrypto.EncryptRTP(packet)
			if err != nil {
				t.Fatal(err)
			}
		}
		rtp.Write(packet)
	}
	sr, _ := (&rtcp.SenderReport{SSRC: 42, NTPTime: 0x1122334455667788}).Marshal()
	if secure {
		sr, err = peerCrypto.EncryptRTCP(sr)
		if err != nil {
			t.Fatal(err)
		}
	}
	peer.WriteToUDP(sr, s.rtcpConn.LocalAddr().(*net.UDPAddr))
	deadline := time.Now().Add(time.Second)
	for {
		s.mutex.Lock()
		stats := s.reports[42]
		ready := stats != nil && stats.received == 2 && stats.lastSR == 0x33445566
		s.mutex.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("RTP/SR not received on separate ports")
		}
		time.Sleep(time.Millisecond)
	}
	s.sendRTCP_RR()
	peer.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 1500)
	n, from, err := peer.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if from.Port != rtcpPort {
		t.Fatal("RR was sent from RTP port")
	}
	data := buffer[:n]
	if secure {
		data, err = peerCrypto.DecryptRTCP(data)
		if err != nil {
			t.Fatal(err)
		}
	}
	packets, err := rtcp.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	rr := packets[0].(*rtcp.ReceiverReport)
	if len(packets) != 2 || len(rr.Reports) != 1 || rr.Reports[0].LastSequenceNumber != 102 || rr.Reports[0].TotalLost != 1 || rr.Reports[0].LastSenderReport != 0x33445566 {
		t.Fatalf("bad compound RR: %#v", rr)
	}
	s.Close()
	for _, port := range []int{rtpPort, rtcpPort} {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
		if err != nil {
			t.Fatalf("port %d leaked: %v", port, err)
		}
		c.Close()
	}
}

func TestPortAllocatorReservesRTCPPartner(t *testing.T) {
	pa := NewPortAllocator(62000, 62003)
	port, control, err := pa.AllocMedia(false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := pa.Alloc()
	if err != nil {
		t.Fatal(err)
	}
	if control != port+1 || other == control {
		t.Fatal("another stream took reserved RTCP port")
	}
	pa.Free(port)
	pa.Free(control)
	again, _, err := pa.AllocMedia(false)
	if err != nil || again != port {
		t.Fatal("RTP/RTCP pair was not reusable")
	}
}

func TestNegotiatedPLIUsesCorrectVideoSSRCAndIsRateLimited(t *testing.T) {
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	s, err := NewRtpSession(RtpSessionConfig{MediaIP: "127.0.0.1", RTCPMux: true, VideoPLI: true,
		Payload: &sdp.Payload{PayloadType: 96, Codec: base.AvPacketPtAvc, ClockRate: 90000}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	packet := make([]byte, 14)
	packet[0], packet[1] = 0x80, 96
	binary.BigEndian.PutUint32(packet[8:], 42)
	packet[12] = 0x65
	peer.WriteToUDP(packet, s.conn.LocalAddr().(*net.UDPAddr))
	peer.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1500)
	n, _, err := peer.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	packets, err := rtcp.Unmarshal(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) != 3 {
		t.Fatal("PLI must be in compound RTCP")
	}
	pli, ok := packets[2].(*rtcp.PictureLossIndication)
	if !ok || pli.MediaSSRC != 42 || pli.SenderSSRC != s.receiverSSRC {
		t.Fatal("wrong PLI source")
	}
	s.sendPLI(42)
	peer.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	_, _, err = peer.ReadFromUDP(buf)
	if e, ok := err.(net.Error); !ok || !e.Timeout() {
		t.Fatal("PLI was not rate limited")
	}
}
