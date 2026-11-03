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

func talkTestStore(deviceID, channelID string) (*DeviceStore, *Device) {
	store := NewDeviceStore(nil, NewWSHub())
	dev := &Device{DeviceID: deviceID, IsOnline: true, Address: "192.0.2.20:5060"}
	ch := &Channel{ChannelID: channelID, device: dev}
	dev.Channels.Store(channelID, ch)
	store.Store(deviceID, dev)
	return store, dev
}

func TestStartTalkPreparesAllTransportModes(t *testing.T) {
	const deviceID = "34020000001320000001"
	const channelID = "34020000001320000002"
	store, _ := talkTestStore(deviceID, channelID)
	client, requester := newTestSIPClient(t, nil)
	tm := NewTalkManager(client, &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"}, store)
	t.Cleanup(tm.StopAll)

	udp, err := tm.StartTalk(deviceID, channelID, TransportUDP, store)
	require.NoError(t, err)
	require.NotZero(t, udp.RTPPort)
	require.NotNil(t, udp.RTPConn)
	require.NotEmpty(t, udp.SSRC)
	require.Equal(t, TransportUDP, udp.TransportMode)
	require.Equal(t, sip.MESSAGE, requester.LastRequest().Method)
	require.Contains(t, string(requester.LastRequest().Body()), "<CmdType>Broadcast</CmdType>")

	again, err := tm.StartTalk(deviceID, channelID, TransportUDP, store)
	require.NoError(t, err)
	require.Same(t, udp, again)

	tcpPassive, err := tm.StartTalk(deviceID, channelID, TransportTCPPassive, store)
	require.NoError(t, err)
	require.Same(t, udp, tcpPassive) // existing session is reused for a second mode

	otherChannel := "34020000001320000003"
	dev, _ := store.Load(deviceID)
	dev.Channels.Store(otherChannel, &Channel{ChannelID: otherChannel, device: dev})
	passive, err := tm.StartTalk(deviceID, otherChannel, TransportTCPPassive, store)
	require.NoError(t, err)
	require.NotNil(t, passive.TCPListener)
	require.NotZero(t, passive.RTPPort)

	activeChannel := "34020000001320000004"
	dev.Channels.Store(activeChannel, &Channel{ChannelID: activeChannel, device: dev})
	active, err := tm.StartTalk(deviceID, activeChannel, TransportTCPActive, store)
	require.NoError(t, err)
	require.Zero(t, active.RTPPort)
	require.Nil(t, active.RTPConn)

	// A failing SIP peer aborts the notify step and removes the new session.
	failingClient, _ := newTestSIPClientFailing(t, errors.New("device unreachable"))
	tmFail := NewTalkManager(failingClient, cfgForTalkTest(), store)
	_, err = tmFail.StartTalk(deviceID, channelID, TransportUDP, store)
	require.ErrorContains(t, err, "send broadcast notify")
	_, ok := tmFail.GetSession(deviceID, channelID)
	require.False(t, ok)
}

func TestTalkManagerOnInviteUDPDeliversAudio(t *testing.T) {
	const deviceID = "34020000001320000001"
	const channelID = "34020000001320000002"
	store, _ := talkTestStore(deviceID, channelID)
	client, _ := newTestSIPClient(t, nil)
	cfg := &Config{ID: "34020000002000000001", Host: "192.0.2.10", MediaIP: "192.0.2.10", Port: 5060}
	tm := NewTalkManager(client, cfg, store)
	t.Cleanup(tm.StopAll)

	session, err := tm.StartTalk(deviceID, channelID, TransportUDP, store)
	require.NoError(t, err)

	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer peer.Close()
	peerPort := peer.LocalAddr().(*net.UDPAddr).Port

	sdp := "v=0\r\n" +
		"c=IN IP4 127.0.0.1\r\n" +
		"m=audio " + strconv.Itoa(peerPort) + " RTP/AVP 8\r\n" +
		"y=0000000045\r\n"
	req := newTestInboundRequest(sip.INVITE, deviceID, channelID, sdp)
	tx := newFakeServerTx()
	tm.OnInvite(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())
	require.Equal(t, "127.0.0.1", session.RTPPeerIP)
	require.Equal(t, peerPort, session.RTPPeerPort)
	require.Equal(t, "0000000045", session.SSRC)
	require.Equal(t, uint8(8), session.PayloadType)
	select {
	case <-session.ReadyCh:
	default:
		t.Fatal("UDP talk session was not marked ready")
	}
	require.Equal(t, TransportUDP, session.TransportMode)
	require.Contains(t, string(tx.LastResponse().Body()), "m=audio")

	require.NoError(t, session.SendAudioData(make([]byte, 160)))
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(2*time.Second)))
	packet := make([]byte, 512)
	n, _, err := peer.ReadFromUDP(packet)
	require.NoError(t, err)
	require.Equal(t, 12+160, n)
	require.Equal(t, RTPVersion2, packet[0])
	require.Equal(t, uint32(45), binary.BigEndian.Uint32(packet[8:12]))

	require.NoError(t, tm.StopTalk(deviceID, channelID))
	_, ok := tm.GetSession(deviceID, channelID)
	require.False(t, ok)
	require.NoError(t, tm.StopTalk(deviceID, channelID))
}

func TestTalkManagerOnInviteTCPActiveConnects(t *testing.T) {
	const deviceID = "34020000001320000011"
	const channelID = "34020000001320000012"
	store, _ := talkTestStore(deviceID, channelID)
	client, _ := newTestSIPClient(t, nil)
	cfg := &Config{ID: "34020000002000000001", Host: "192.0.2.10", MediaIP: "192.0.2.10", Port: 5060}
	tm := NewTalkManager(client, cfg, store)
	t.Cleanup(tm.StopAll)

	session, err := tm.StartTalk(deviceID, channelID, TransportTCPActive, store)
	require.NoError(t, err)

	deviceListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer deviceListener.Close()
	devicePort := deviceListener.Addr().(*net.TCPAddr).Port

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := deviceListener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	sdp := "v=0\r\n" +
		"c=IN IP4 127.0.0.1\r\n" +
		"m=audio " + strconv.Itoa(devicePort) + " TCP/RTP/AVP 8\r\n" +
		"a=setup:passive\r\n" +
		"y=0000000046\r\n"
	req := newTestInboundRequest(sip.INVITE, deviceID, channelID, sdp)
	req.SetTransport("TCP")
	tx := newFakeServerTx()
	tm.OnInvite(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())

	var deviceConn net.Conn
	select {
	case deviceConn = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("talk session did not dial the device")
	}
	defer deviceConn.Close()
	// The talk manager closes ReadyCh before the outbound dial completes, so
	// wait for the actual connection before queueing audio.
	require.Eventually(t, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.TCPConn != nil
	}, 2*time.Second, 10*time.Millisecond, "TCP active talk session never connected")
	select {
	case <-session.ReadyCh:
	default:
		t.Fatal("TCP active talk session was not marked ready")
	}
	require.Equal(t, TransportTCPActive, session.TransportMode)
	require.Contains(t, string(tx.LastResponse().Body()), "TCP/RTP/AVP")
	require.Contains(t, string(tx.LastResponse().Body()), "a=setup:active")

	require.NoError(t, session.SendAudioData(make([]byte, 160)))
	require.NoError(t, deviceConn.SetReadDeadline(time.Now().Add(2*time.Second)))
	length := make([]byte, 2)
	_, err = io.ReadFull(deviceConn, length)
	require.NoError(t, err)
	require.Equal(t, 12+160, int(binary.BigEndian.Uint16(length)))
	packet := make([]byte, 12+160)
	_, err = io.ReadFull(deviceConn, packet)
	require.NoError(t, err)
	require.Equal(t, uint32(46), binary.BigEndian.Uint32(packet[8:12]))
}

func TestTalkManagerOnInviteTCPPassiveAccepts(t *testing.T) {
	const deviceID = "34020000001320000021"
	const channelID = "34020000001320000022"
	store, _ := talkTestStore(deviceID, channelID)
	client, _ := newTestSIPClient(t, nil)
	cfg := &Config{ID: "34020000002000000001", Host: "192.0.2.10", MediaIP: "192.0.2.10", Port: 5060}
	tm := NewTalkManager(client, cfg, store)
	t.Cleanup(tm.StopAll)

	session, err := tm.StartTalk(deviceID, channelID, TransportTCPPassive, store)
	require.NoError(t, err)
	require.NotNil(t, session.TCPListener)

	sdp := "v=0\r\n" +
		"c=IN IP4 127.0.0.1\r\n" +
		"m=audio 9 TCP/RTP/AVP 8\r\n" +
		"a=setup:active\r\n" +
		"y=0000000047\r\n"
	req := newTestInboundRequest(sip.INVITE, deviceID, channelID, sdp)
	req.SetTransport("TCP")
	tx := newFakeServerTx()
	tm.OnInvite(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())

	deviceConn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(session.RTPPort))
	require.NoError(t, err)
	defer deviceConn.Close()
	select {
	case <-session.ReadyCh:
	case <-time.After(2 * time.Second):
		t.Fatal("TCP passive talk session never became ready")
	}
	require.Equal(t, TransportTCPPassive, session.TransportMode)

	require.NoError(t, session.SendAudioData(make([]byte, 160)))
	require.NoError(t, deviceConn.SetReadDeadline(time.Now().Add(2*time.Second)))
	length := make([]byte, 2)
	_, err = io.ReadFull(deviceConn, length)
	require.NoError(t, err)
	require.Equal(t, 12+160, int(binary.BigEndian.Uint16(length)))
}

func TestTalkManagerOnInviteGuardsAndFallbacks(t *testing.T) {
	const deviceID = "34020000001320000031"
	const channelID = "34020000001320000032"
	store, dev := talkTestStore(deviceID, channelID)
	client, _ := newTestSIPClient(t, nil)
	cfg := &Config{ID: "34020000002000000001", Host: "192.0.2.10", MediaIP: "192.0.2.10", Port: 5060}
	tm := NewTalkManager(client, cfg, store)
	t.Cleanup(tm.StopAll)

	// Missing From / To headers are rejected without a SIP response.
	tm.OnInvite(&sip.Request{Method: sip.INVITE}, newFakeServerTx())
	noTo := newTestInboundRequest(sip.INVITE, deviceID, "", "")
	tx := newFakeServerTx()
	tm.OnInvite(noTo, tx)
	require.Empty(t, tx.StatusCodes())

	// Invalid SDP port yields 400.
	session, err := tm.StartTalk(deviceID, channelID, TransportUDP, store)
	require.NoError(t, err)
	badSDP := "v=0\r\nm=audio not-a-port RTP/AVP 8\r\n"
	tx = newFakeServerTx()
	tm.OnInvite(newTestInboundRequest(sip.INVITE, deviceID, channelID, badSDP), tx)
	require.Equal(t, []int{400}, tx.StatusCodes())

	// Invalid payload type is also a 400.
	tx = newFakeServerTx()
	tm.OnInvite(newTestInboundRequest(sip.INVITE, deviceID, channelID, "v=0\r\nm=audio 4000 RTP/AVP abc\r\n"), tx)
	require.Equal(t, []int{400}, tx.StatusCodes())

	// No session at all yields 404.
	tx = newFakeServerTx()
	tm.OnInvite(newTestInboundRequest(sip.INVITE, "34020000001320009999", "34020000001320009999", ""), tx)
	require.Equal(t, []int{404}, tx.StatusCodes())

	// A request whose From carries the channel id resolves the session through
	// the device store instead of the session map.
	dev.Channels.Store("34020000001320000033", &Channel{ChannelID: "34020000001320000033", device: dev})
	session.ChannelID = channelID
	tx = newFakeServerTx()
	tm.OnInvite(newTestInboundRequest(sip.INVITE, "34020000001320000033", "34020000002000000001", ""), tx)
	require.Equal(t, []int{200}, tx.StatusCodes())
}

func TestTalkManagerOnByeAckAndStopAll(t *testing.T) {
	const deviceID = "34020000001320000041"
	const channelID = "34020000001320000042"
	store, _ := talkTestStore(deviceID, channelID)
	client, _ := newTestSIPClient(t, nil)
	tm := NewTalkManager(client, &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"}, store)

	_, err := tm.StartTalk(deviceID, channelID, TransportUDP, store)
	require.NoError(t, err)

	ack := newTestInboundRequest(sip.ACK, deviceID, channelID, "")
	tm.OnAck(ack, newFakeServerTx())
	tm.OnAck(&sip.Request{Method: sip.ACK}, newFakeServerTx())

	tm.OnBye(&sip.Request{Method: sip.BYE}, newFakeServerTx())
	bye := newTestInboundRequest(sip.BYE, deviceID, channelID, "")
	byeTx := newFakeServerTx()
	tm.OnBye(bye, byeTx)
	require.Equal(t, []int{200}, byeTx.StatusCodes())
	_, ok := tm.GetSession(deviceID, channelID)
	require.False(t, ok)

	// StopAll drains every remaining session.
	_, err = tm.StartTalk(deviceID, channelID, TransportUDP, store)
	require.NoError(t, err)
	tm.StopAll()
	_, ok = tm.GetSession(deviceID, channelID)
	require.False(t, ok)
	tm.StopAll() // empty manager stays safe
}

func TestTalkSessionSendPathsAndStop(t *testing.T) {
	t.Run("UDP", func(t *testing.T) {
		peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		require.NoError(t, err)
		defer peer.Close()
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		require.NoError(t, err)
		ts := &TalkSession{
			DeviceID: "d", ChannelID: "c", TransportMode: TransportUDP,
			SSRC: "123", PayloadType: PayloadTypePCMA, RTPConn: conn,
			RTPPeerIP: "127.0.0.1", RTPPeerPort: peer.LocalAddr().(*net.UDPAddr).Port,
			AudioChan: make(chan []byte, 1), ReadyCh: make(chan struct{}),
		}
		require.NoError(t, ts.sendAudioData([]byte{1, 2, 3}))
		require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
		buf := make([]byte, 128)
		n, _, err := peer.ReadFromUDP(buf)
		require.NoError(t, err)
		require.Equal(t, 12+3, n)
		require.NoError(t, ts.Stop())
		require.ErrorContains(t, ts.SendAudioData([]byte{4}), "stopped")
		require.ErrorContains(t, ts.sendAudioData([]byte{4}), "stopped")
		require.NoError(t, ts.Stop())
	})

	t.Run("UDP not ready", func(t *testing.T) {
		ts := &TalkSession{TransportMode: TransportUDP, SSRC: "1"}
		require.ErrorContains(t, ts.sendAudioData([]byte{1}), "UDP connection not ready")
	})

	t.Run("TCP", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer clientConn.Close()
		defer serverConn.Close()
		ts := &TalkSession{
			DeviceID: "d", ChannelID: "c", TransportMode: TransportTCPPassive,
			SSRC: "9", PayloadType: PayloadTypePCMA, TCPConn: clientConn,
		}
		readDone := make(chan int, 1)
		go func() {
			length := make([]byte, 2)
			if _, err := io.ReadFull(serverConn, length); err != nil {
				readDone <- -1
				return
			}
			packet := make([]byte, int(binary.BigEndian.Uint16(length)))
			if _, err := io.ReadFull(serverConn, packet); err != nil {
				readDone <- -1
				return
			}
			readDone <- len(packet)
		}()
		require.NoError(t, ts.sendAudioData([]byte{7, 8}))
		require.Equal(t, 12+2, <-readDone)
	})

	t.Run("TCP not ready", func(t *testing.T) {
		ts := &TalkSession{TransportMode: TransportTCPActive, SSRC: "1"}
		require.ErrorContains(t, ts.sendAudioData([]byte{1}), "TCP connection not ready")
	})

	t.Run("unsupported mode", func(t *testing.T) {
		ts := &TalkSession{TransportMode: TransportMode(9), SSRC: "1"}
		require.ErrorContains(t, ts.sendAudioData([]byte{1}), "unsupported transport mode")
	})

	t.Run("bad SSRC", func(t *testing.T) {
		ts := &TalkSession{TransportMode: TransportUDP, SSRC: "not-a-number"}
		require.ErrorContains(t, ts.sendAudioData([]byte{1}), "build RTP packet")
	})

	t.Run("audio channel full", func(t *testing.T) {
		ts := NewTalkSession("d", "c", TransportUDP, &Config{}, nil)
		ts.AudioChan = make(chan []byte, 1)
		require.NoError(t, ts.SendAudioData([]byte{1}))
		require.ErrorContains(t, ts.SendAudioData([]byte{2}), "audio channel full")
	})
}

func TestTalkSessionAcceptConnectAndAudioSender(t *testing.T) {
	t.Run("accept", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer listener.Close()
		ts := NewTalkSession("d", "c", TransportTCPPassive, &Config{}, nil)
		ts.TCPListener = listener
		done := make(chan struct{})
		go func() { ts.acceptTCPConn(); close(done) }()
		conn, err := net.Dial("tcp", listener.Addr().String())
		require.NoError(t, err)
		defer conn.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("acceptTCPConn did not store the connection")
		}
		select {
		case <-ts.ReadyCh:
		default:
			t.Fatal("acceptTCPConn did not mark the session ready")
		}
		require.NotNil(t, ts.TCPConn)
	})

	t.Run("connect success", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer listener.Close()
		accepted := make(chan net.Conn, 1)
		go func() {
			conn, err := listener.Accept()
			if err == nil {
				accepted <- conn
			}
		}()
		ts := NewTalkSession("d", "c", TransportTCPActive, &Config{}, nil)
		port := listener.Addr().(*net.TCPAddr).Port
		ts.connectTCP("127.0.0.1", port)
		require.NotNil(t, ts.TCPConn)
		require.Equal(t, "127.0.0.1", ts.RTPPeerIP)
		require.Equal(t, port, ts.RTPPeerPort)
		select {
		case conn := <-accepted:
			conn.Close()
		case <-time.After(2 * time.Second):
			t.Fatal("connectTCP never reached the listener")
		}
	})

	t.Run("connect failure", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close() // closed port -> immediate refusal
		ts := NewTalkSession("d", "c", TransportTCPActive, &Config{}, nil)
		done := make(chan struct{})
		go func() { ts.connectTCP("127.0.0.1", port); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("connectTCP did not return on a refused connection")
		}
		require.Nil(t, ts.TCPConn)
	})

	t.Run("startAudioSender drains then exits on Stop", func(t *testing.T) {
		peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		require.NoError(t, err)
		defer peer.Close()
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		require.NoError(t, err)
		ts := &TalkSession{
			DeviceID: "d", ChannelID: "c", TransportMode: TransportUDP,
			SSRC: "77", PayloadType: PayloadTypePCMA, RTPConn: conn,
			RTPPeerIP: "127.0.0.1", RTPPeerPort: peer.LocalAddr().(*net.UDPAddr).Port,
			AudioChan: make(chan []byte, 4), ReadyCh: make(chan struct{}),
		}
		go ts.startAudioSender()
		ts.ReadyOnce.Do(func() { close(ts.ReadyCh) })
		require.NoError(t, ts.SendAudioData([]byte{1, 2, 3}))
		require.NoError(t, peer.SetReadDeadline(time.Now().Add(2*time.Second)))
		buf := make([]byte, 128)
		_, _, err = peer.ReadFromUDP(buf)
		require.NoError(t, err)
		require.NoError(t, ts.Stop())
	})
}

func TestTalkSessionStopClosesUDPAndListener(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ts := &TalkSession{
		DeviceID: "d", ChannelID: "c", TransportMode: TransportUDP,
		RTPConn: conn, TCPListener: listener,
		AudioChan: make(chan []byte, 1), ReadyCh: make(chan struct{}),
	}
	require.NoError(t, ts.Stop())
	require.NoError(t, ts.Stop())
}

func cfgForTalkTest() *Config {
	return &Config{ID: "34020000002000000001", Host: "192.0.2.10", MediaIP: "192.0.2.10", Port: 5060}
}
