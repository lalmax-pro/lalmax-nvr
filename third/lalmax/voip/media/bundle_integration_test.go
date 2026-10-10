package media

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/srtp/v3"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lalmax/voip/sdp"
)

func TestBundleDTLSSRTPReceivesAudioAndVideoOnOnePort(t *testing.T) {
	serverCert, serverFP, err := GenerateDTLSCertificate()
	if err != nil {
		t.Fatal(err)
	}
	clientCert, clientFP, err := GenerateDTLSCertificate()
	if err != nil {
		t.Fatal(err)
	}
	frames := make(chan base.AvPacketPt, 32)
	receiver, err := NewRtpSession(RtpSessionConfig{MediaIP: "127.0.0.1", RTCPMux: true,
		Payload:      &sdp.Payload{PayloadType: 8, Codec: base.AvPacketPtG711A, ClockRate: 8000},
		VideoPayload: &sdp.Payload{PayloadType: 96, Codec: base.AvPacketPtAvc, ClockRate: 90000},
		DTLSSRTP:     &DTLSSRTPConfig{Certificate: serverCert, RemoteFingerprint: clientFP},
		OnAvPacket:   func(packet base.AvPacket) { frames <- packet.PayloadType }})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	pc := newDTLSPacketConn(peer, peer.LocalAddr(), receiver.conn.LocalAddr())
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 2048)
		for {
			n, addr, err := peer.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			if isDTLSPacket(buffer[:n]) {
				pc.enqueue(buffer[:n], addr)
			}
		}
	}()
	defer func() { peer.Close(); pc.Close(); <-done }()
	trusted, err := x509.ParseCertificate(serverCert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(trusted)
	client, err := dtls.Client(pc, receiver.conn.LocalAddr(), &dtls.Config{Certificates: []tls.Certificate{clientCert}, RootCAs: roots,
		SRTPProtectionProfiles: []dtls.SRTPProtectionProfile{dtls.SRTP_AES128_CM_HMAC_SHA1_80},
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return fmt.Errorf("missing server certificate")
			}
			return verifyFingerprint(raw[0], serverFP)
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	state, ok := client.ConnectionState()
	if !ok {
		t.Fatal("DTLS state missing")
	}
	keys := &srtp.Config{Profile: srtp.ProtectionProfileAes128CmHmacSha1_80}
	if err := keys.ExtractSessionKeysFromDTLS(&state, true); err != nil {
		t.Fatal(err)
	}
	crypto, err := CreateSrtpContextPair(append(keys.Keys.LocalMasterKey, keys.Keys.LocalMasterSalt...),
		append(keys.Keys.RemoteMasterKey, keys.Keys.RemoteMasterSalt...), CryptoSuiteAES128_CM_HMAC_SHA1_80)
	if err != nil {
		t.Fatal(err)
	}
	defer crypto.Close()
	for {
		ready, _ := receiver.getSrtpContext()
		if ready != nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("receiver keys missing")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	for seq := uint16(1); seq <= 3; seq++ {
		for _, video := range []bool{false, true} {
			payload, pt, id, ts := make([]byte, 160), byte(8), uint32(42), uint32(seq)*160
			if video {
				payload, pt, id, ts = []byte{0x65, 0x88, 0x84}, 0x80|96, 43, uint32(seq)*4500
			}
			packet := make([]byte, 12+len(payload))
			packet[0], packet[1] = 0x80, pt
			binary.BigEndian.PutUint16(packet[2:], seq)
			binary.BigEndian.PutUint32(packet[4:], ts)
			binary.BigEndian.PutUint32(packet[8:], id)
			copy(packet[12:], payload)
			packet, err = crypto.EncryptRTP(packet)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := peer.WriteToUDP(packet, receiver.conn.LocalAddr().(*net.UDPAddr)); err != nil {
				t.Fatal(err)
			}
		}
	}
	audio, video := false, false
	for !audio || !video {
		select {
		case codec := <-frames:
			audio = audio || codec == base.AvPacketPtG711A
			video = video || codec == base.AvPacketPtAvc
		case <-ctx.Done():
			t.Fatalf("bundle frames missing: audio=%v video=%v", audio, video)
		}
	}
	receiver.mutex.Lock()
	defer receiver.mutex.Unlock()
	if len(receiver.reports) != 2 {
		t.Fatal("BUNDLE merged audio/video RTCP source statistics")
	}
}
