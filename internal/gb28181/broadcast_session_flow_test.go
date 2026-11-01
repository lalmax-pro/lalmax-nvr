package gb28181

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/require"
)

func broadcastTestStore(t *testing.T) (*DeviceStore, *Device) {
	t.Helper()
	store := NewDeviceStore(nil, NewWSHub())
	dev := &Device{DeviceID: "34020000001320000001", IsOnline: true, Address: "192.0.2.20:5060"}
	ch := &Channel{ChannelID: "34020000001320000001", device: dev}
	dev.Channels.Store(ch.ChannelID, ch)
	store.Store(dev.DeviceID, dev)
	return store, dev
}

func TestBroadcastManagerStartAndStopLifecycle(t *testing.T) {
	store, dev := broadcastTestStore(t)
	client, _ := newTestSIPClient(t, nil)
	bm := NewBroadcastManager(client, &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"}, store)

	bs, err := bm.StartBroadcast(dev.DeviceID, "34020000001320000001", store)
	require.NoError(t, err)
	require.NotZero(t, bs.RTPPort)
	require.NotEmpty(t, bs.SSRC)
	require.Equal(t, uint8(8), bs.PayloadType)
	require.NotNil(t, bs.RTPConn)

	again, err := bm.StartBroadcast(dev.DeviceID, "34020000001320000001", store)
	require.NoError(t, err)
	require.Same(t, bs, again)

	_, ok := bm.GetSession(dev.DeviceID, "34020000001320000001")
	require.True(t, ok)
	_, ok = bm.GetSession(dev.DeviceID, "other")
	require.False(t, ok)

	_, err = bm.StartBroadcast("34020000001320009999", "34020000001320000001", store)
	require.ErrorIs(t, err, ErrDeviceOffline)
	_, err = bm.StartBroadcast(dev.DeviceID, "missing", store)
	require.ErrorIs(t, err, ErrChannelNotExist)

	require.NoError(t, bm.StopBroadcast(dev.DeviceID, "34020000001320000001"))
	require.NoError(t, bm.StopBroadcast(dev.DeviceID, "34020000001320000001"))
	bm.RemoveSession("unused-key")

	// A failing SIP peer aborts the session and removes it from the manager.
	failingClient, _ := newTestSIPClientFailing(t, errors.New("device unreachable"))
	failing := NewBroadcastManager(failingClient, &Config{ID: "34020000002000000001"}, store)
	_, err = failing.StartBroadcast(dev.DeviceID, "34020000001320000001", store)
	require.ErrorContains(t, err, "send broadcast notify failed")
	_, ok = failing.GetSession(dev.DeviceID, "34020000001320000001")
	require.False(t, ok)

	// Notify without a device is reported before any SIP traffic.
	orphan := &BroadcastSession{DeviceID: "missing", ChannelID: "ch", cfg: &Config{}}
	require.ErrorIs(t, orphan.sendBroadcastNotify(store), ErrDeviceNotExist)
}

func TestBroadcastSessionAudioQueueGuards(t *testing.T) {
	bs := &BroadcastSession{
		AudioChan:  make(chan []byte, 2),
		ReadyCh:    make(chan struct{}),
		TCPReadyCh: make(chan struct{}),
	}
	require.NoError(t, bs.SendAudioData([]byte{1}))
	require.NoError(t, bs.SendAudioData([]byte{2}))
	require.ErrorContains(t, bs.SendAudioData([]byte{3}), "audio channel full")

	bs.markReady()
	bs.markReady() // ReadyOnce keeps the close single-shot
	bs.markTCPReady()
	bs.markTCPReady()
	select {
	case <-bs.ReadyCh:
	default:
		t.Fatal("markReady did not close the ready channel")
	}
	select {
	case <-bs.TCPReadyCh:
	default:
		t.Fatal("markTCPReady did not close the TCP ready channel")
	}

	require.NoError(t, bs.Stop())
	require.ErrorContains(t, bs.SendAudioData([]byte{4}), "stopped")
	require.NoError(t, bs.Stop())
}

func TestBroadcastSessionSendAudioGuards(t *testing.T) {
	tcpSession := &BroadcastSession{IsTCP: true}
	require.ErrorContains(t, tcpSession.sendAudioData([]byte{1}), "TCP connection not ready")
	require.ErrorContains(t, tcpSession.sendRTPPayload(nil, []byte{1}), "TCP connection not ready")

	udpSession := &BroadcastSession{}
	require.ErrorContains(t, udpSession.sendAudioData([]byte{1}), "RTP connection not ready")

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer conn.Close()
	udpSession.RTPConn = conn
	require.ErrorContains(t, udpSession.sendAudioData([]byte{1}), "RTP connection not ready")

	// Empty payloads are accepted once the peer is known.
	udpSession.RTPPeerIP = "127.0.0.1"
	udpSession.RTPPeerPort = 9
	require.NoError(t, udpSession.sendAudioData(nil))
}

func TestBroadcastSessionOnInviteUDPStreamsAudio(t *testing.T) {
	store, dev := broadcastTestStore(t)
	client, _ := newTestSIPClient(t, nil)
	cfg := &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"}
	bm := NewBroadcastManager(client, cfg, store)

	bs, err := bm.StartBroadcast(dev.DeviceID, "34020000001320000001", store)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bs.Stop() })

	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer peer.Close()
	peerPort := peer.LocalAddr().(*net.UDPAddr).Port

	sdp := "v=0\r\n" +
		"o=34020000001320000001 0 0 IN IP4 127.0.0.1\r\n" +
		"s=Play\r\n" +
		"c=IN IP4 127.0.0.1\r\n" +
		"t=0 0\r\n" +
		"m=audio " + strconv.Itoa(peerPort) + " RTP/AVP 8\r\n" +
		"a=recvonly\r\n" +
		"a=rtpmap:8 PCMA/8000\r\n" +
		"y=0000000042\r\n"
	req := newTestInboundRequest(sip.INVITE, dev.DeviceID, cfg.ID, sdp)
	req.SetTransport("UDP")
	tx := newFakeServerTx()
	bm.OnInvite(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())
	require.Equal(t, "127.0.0.1", bs.RTPPeerIP)
	require.Equal(t, peerPort, bs.RTPPeerPort)
	require.False(t, bs.IsTCP)
	require.Equal(t, uint32(42), bs.RTPSSRC)
	require.Equal(t, "0000000042", bs.SSRC)
	select {
	case <-bs.ReadyCh:
	default:
		t.Fatal("UDP session was not marked ready")
	}
	require.NotContains(t, string(tx.LastResponse().Body()), "a=setup")

	// 320 bytes PCM become two 160-byte RTP packets.
	require.NoError(t, bs.SendAudioData(make([]byte, 320)))
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(2*time.Second)))
	payload := make([]byte, 2048)
	for i := 0; i < 2; i++ {
		n, _, err := peer.ReadFromUDP(payload)
		require.NoError(t, err)
		require.Equal(t, 12+160, n)
		require.Equal(t, byte(0x80), payload[0])
		require.Equal(t, uint8(8), payload[1])
		require.Equal(t, uint32(42), binary.BigEndian.Uint32(payload[8:12]))
	}
}

func TestBroadcastSessionOnInviteTCPPassiveAcceptsDevice(t *testing.T) {
	store, dev := broadcastTestStore(t)
	client, _ := newTestSIPClient(t, nil)
	cfg := &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"}
	bm := NewBroadcastManager(client, cfg, store)

	bs, err := bm.StartBroadcast(dev.DeviceID, "34020000001320000001", store)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bs.Stop() })

	// Device declares a=setup:active, so the NVR listens and accepts.
	sdp := "v=0\r\n" +
		"o=34020000001320000001 0 0 IN IP4 127.0.0.1\r\n" +
		"c=IN IP4 127.0.0.1\r\n" +
		"m=audio 9 TCP/RTP/AVP 8\r\n" +
		"a=setup:active\r\n" +
		"y=0000000043\r\n"
	req := newTestInboundRequest(sip.INVITE, dev.DeviceID, cfg.ID, sdp)
	req.SetTransport("TCP")
	tx := newFakeServerTx()
	bm.OnInvite(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())
	require.True(t, bs.IsTCP)
	require.False(t, bs.TCPActive)
	require.NotNil(t, bs.TCPListener)
	require.Nil(t, bs.RTPConn)
	require.Contains(t, string(tx.LastResponse().Body()), "a=setup:passive")
	require.Contains(t, string(tx.LastResponse().Body()), "TCP/RTP/AVP")

	deviceConn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(bs.RTPPort))
	require.NoError(t, err)
	defer deviceConn.Close()

	select {
	case <-bs.TCPReadyCh:
	case <-time.After(2 * time.Second):
		t.Fatal("TCP passive session never became ready")
	}

	require.NoError(t, bs.SendAudioData(make([]byte, 160)))
	require.NoError(t, deviceConn.SetReadDeadline(time.Now().Add(2*time.Second)))
	length := make([]byte, 2)
	_, err = io.ReadFull(deviceConn, length)
	require.NoError(t, err)
	require.Equal(t, 12+160, int(binary.BigEndian.Uint16(length)))
	packet := make([]byte, 12+160)
	_, err = io.ReadFull(deviceConn, packet)
	require.NoError(t, err)
	require.Equal(t, byte(0x80), packet[0])
	require.Equal(t, uint32(43), binary.BigEndian.Uint32(packet[8:12]))

	// Closing the device side releases the tracked connection.
	deviceConn.Close()
	require.Eventually(t, func() bool {
		bs.mu.Lock()
		defer bs.mu.Unlock()
		return bs.TCPConn == nil
	}, 2*time.Second, 20*time.Millisecond)
}

func TestBroadcastSessionOnInviteTCPActiveDialsDevice(t *testing.T) {
	store, dev := broadcastTestStore(t)
	client, _ := newTestSIPClient(t, nil)
	cfg := &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"}
	bm := NewBroadcastManager(client, cfg, store)

	bs, err := bm.StartBroadcast(dev.DeviceID, "34020000001320000001", store)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bs.Stop() })

	deviceListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer deviceListener.Close()
	devicePort := deviceListener.Addr().(*net.TCPAddr).Port

	// Device declares a=setup:passive, so the NVR dials out.
	sdp := "v=0\r\n" +
		"o=34020000001320000001 0 0 IN IP4 127.0.0.1\r\n" +
		"c=IN IP4 127.0.0.1\r\n" +
		"m=audio " + strconv.Itoa(devicePort) + " TCP/RTP/AVP 8\r\n" +
		"a=setup:passive\r\n" +
		"y=0000000044\r\n"
	req := newTestInboundRequest(sip.INVITE, dev.DeviceID, cfg.ID, sdp)
	req.SetTransport("TCP")
	tx := newFakeServerTx()
	bm.OnInvite(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())
	require.True(t, bs.TCPActive)
	require.Contains(t, string(tx.LastResponse().Body()), "a=setup:active")

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := deviceListener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	var deviceConn net.Conn
	select {
	case deviceConn = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("NVR did not dial the device TCP port")
	}
	defer deviceConn.Close()

	select {
	case <-bs.TCPReadyCh:
	case <-time.After(2 * time.Second):
		t.Fatal("TCP active session never became ready")
	}
	require.NoError(t, bs.SendAudioData(make([]byte, 160)))
	require.NoError(t, deviceConn.SetReadDeadline(time.Now().Add(2*time.Second)))
	length := make([]byte, 2)
	_, err = io.ReadFull(deviceConn, length)
	require.NoError(t, err)
	require.Equal(t, 12+160, int(binary.BigEndian.Uint16(length)))
}

func TestBroadcastOnInviteGuardsAndBye(t *testing.T) {
	store, dev := broadcastTestStore(t)
	client, _ := newTestSIPClient(t, nil)
	cfg := &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"}
	bm := NewBroadcastManager(client, cfg, store)

	// Missing From header is ignored without a response.
	invalid := &sip.Request{Method: sip.INVITE}
	tx := newFakeServerTx()
	bm.OnInvite(invalid, tx)
	require.Empty(t, tx.StatusCodes())

	// Unknown device yields 404.
	unknown := newTestInboundRequest(sip.INVITE, "34020000001320009999", cfg.ID, "")
	tx = newFakeServerTx()
	bm.OnInvite(unknown, tx)
	require.Equal(t, []int{404}, tx.StatusCodes())

	bs, err := bm.StartBroadcast(dev.DeviceID, "34020000001320000001", store)
	require.NoError(t, err)
	// Match by device ID when the SDP carries no channel id.
	invite := newTestInboundRequest(sip.INVITE, dev.DeviceID, cfg.ID, "v=0\r\nc=IN IP4 192.0.2.20\r\nm=audio 40000 RTP/AVP 8\r\n")
	tx = newFakeServerTx()
	bm.OnInvite(invite, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())

	// ACK is a no-op, including the invalid-header form.
	bm.OnAck(invite, newFakeServerTx())
	bm.OnAck(&sip.Request{Method: sip.ACK}, newFakeServerTx())

	// BYE removes and stops the session.
	bye := newTestInboundRequest(sip.BYE, dev.DeviceID, cfg.ID, "")
	byeTx := newFakeServerTx()
	bm.OnBye(bye, byeTx)
	require.Equal(t, []int{200}, byeTx.StatusCodes())
	_, ok := bm.GetSession(dev.DeviceID, "34020000001320000001")
	require.False(t, ok)

	bm.OnBye(&sip.Request{Method: sip.BYE}, newFakeServerTx())
	require.NoError(t, bs.Stop())
}

func TestBroadcastTCPDialSkipsWithoutPeer(t *testing.T) {
	bs := &BroadcastSession{DeviceID: "d", ChannelID: "c", RTPPort: 0}
	done := make(chan struct{})
	go func() { bs.dialTCPConn(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dialTCPConn did not skip without a peer")
	}

	bs2 := &BroadcastSession{DeviceID: "d", ChannelID: "c"}
	bs2.acceptTCPConn() // nil listener returns immediately
}
