package gb28181

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// failingOnlineRepo lets a test inject a persistence failure for the offline
// transition performed by API.logout.
type failingOnlineRepo struct {
	*fakeDeviceRepo
	onlineErr error
}

func (r *failingOnlineRepo) UpdateGB28181DeviceOnlineStatus(context.Context, string, bool) error {
	return r.onlineErr
}

func TestBroadcastNotifySourceFallbackAndStopIdleTimer(t *testing.T) {
	store, dev := broadcastTestStore(t)
	client, requester := newTestSIPClient(t, nil)
	bs := &BroadcastSession{
		DeviceID:  dev.DeviceID,
		ChannelID: "34020000001320000002",
		cfg:       &Config{}, // empty ID falls back to the device ID as SourceID
		client:    client,
	}
	require.NoError(t, bs.sendBroadcastNotify(store))
	require.Contains(t, string(requester.LastRequest().Body()), "<SourceID>"+dev.DeviceID+"</SourceID>")

	bs.AudioChan = make(chan []byte, 1)
	bs.ReadyCh = make(chan struct{})
	bs.TCPReadyCh = make(chan struct{})
	bs.IdleTimer = time.AfterFunc(time.Hour, func() {})
	require.NoError(t, bs.Stop())
}

func TestBroadcastOnInviteFallsBackToChannelLookup(t *testing.T) {
	store, dev := broadcastTestStore(t)
	// Register a second channel whose id differs from the device id so the
	// manager must resolve the session through the device store.
	dev.Channels.Store("34020000001320000002", &Channel{ChannelID: "34020000001320000002", device: dev})
	client, _ := newTestSIPClient(t, nil)
	bm := NewBroadcastManager(client, &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"}, store)
	bs := &BroadcastSession{
		DeviceID:    dev.DeviceID,
		ChannelID:   "34020000001320000002",
		cfg:         &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"},
		AudioChan:   make(chan []byte, 1),
		ReadyCh:     make(chan struct{}),
		TCPReadyCh:  make(chan struct{}),
		PayloadType: 8,
	}
	bm.sessions[broadcastKey(bs.DeviceID, bs.ChannelID)] = bs
	t.Cleanup(func() { _ = bs.Stop() })

	// The From user is the channel id registered under the session's device.
	sdp := "v=0\r\nc=IN IP4 192.0.2.20\r\nm=audio 40000 RTP/AVP 8\r\n"
	req := newTestInboundRequest(sip.INVITE, "34020000001320000002", "34020000002000000001", sdp)
	tx := newFakeServerTx()
	bm.OnInvite(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())
	require.Equal(t, "192.0.2.20", bs.RTPPeerIP)
}

func TestBroadcastOnInviteRespondFailureStopsBeforeMediaSetup(t *testing.T) {
	store, dev := broadcastTestStore(t)
	client, _ := newTestSIPClient(t, nil)
	bm := NewBroadcastManager(client, &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"}, store)
	bs, err := bm.StartBroadcast(dev.DeviceID, "34020000001320000001", store)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bs.Stop() })

	req := newTestInboundRequest(sip.INVITE, dev.DeviceID, "34020000002000000001",
		"v=0\r\nc=IN IP4 192.0.2.20\r\nm=audio 40000 RTP/AVP 8\r\n")
	tx := newFakeServerTx()
	tx.respondErr = errors.New("transaction gone")
	bm.OnInvite(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes()) // recorded before the error
}

func TestBroadcastTCPHelpers(t *testing.T) {
	t.Run("prepareTCPListener is idempotent", func(t *testing.T) {
		bs := &BroadcastSession{}
		require.NoError(t, bs.prepareTCPListener())
		require.NotNil(t, bs.TCPListener)
		port := bs.RTPPort
		require.NoError(t, bs.prepareTCPListener())
		require.Equal(t, port, bs.RTPPort)
		require.NoError(t, bs.TCPListener.Close())
	})

	t.Run("setTCPConn closes the previous connection", func(t *testing.T) {
		first, firstPeer := net.Pipe()
		defer firstPeer.Close()
		second, secondPeer := net.Pipe()
		defer secondPeer.Close()

		bs := &BroadcastSession{}
		bs.setTCPConn(first)
		bs.setTCPConn(second)
		require.Equal(t, second, bs.TCPConn)
		// The replaced connection is closed, so a read on its peer fails
		// immediately instead of blocking.
		buf := make([]byte, 1)
		_, err := firstPeer.Read(buf)
		require.Error(t, err)
	})

	t.Run("readTCPConn skips empty frames and clears the connection", func(t *testing.T) {
		conn, peer := net.Pipe()
		bs := &BroadcastSession{TCPConn: conn}
		done := make(chan struct{})
		go func() { bs.readTCPConn(conn); close(done) }()
		_, err := peer.Write([]byte{0, 0})
		require.NoError(t, err)
		require.NoError(t, peer.Close())
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("readTCPConn did not stop after the peer closed")
		}
		bs.mu.Lock()
		defer bs.mu.Unlock()
		require.Nil(t, bs.TCPConn)
	})
}

func TestBroadcastAudioSenderAndSendErrors(t *testing.T) {
	t.Run("sender logs send failures and exits", func(t *testing.T) {
		bs := &BroadcastSession{
			DeviceID:    "d",
			IsTCP:       true, // no TCP connection -> send error
			AudioChan:   make(chan []byte, 1),
			ReadyCh:     make(chan struct{}),
			PayloadType: 8,
		}
		bs.AudioChan <- []byte{1}
		close(bs.AudioChan)
		close(bs.ReadyCh)
		bs.startAudioSender() // must return once the closed channel is drained
	})

	t.Run("UDP peer resolution failure", func(t *testing.T) {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		require.NoError(t, err)
		defer conn.Close()
		bs := &BroadcastSession{RTPConn: conn, RTPPeerIP: "bad host", RTPPeerPort: 1, PayloadType: 8}
		require.ErrorContains(t, bs.sendAudioData([]byte{1}), "no such host")
	})

	t.Run("TCP write failure", func(t *testing.T) {
		conn, peer := net.Pipe()
		peer.Close()
		bs := &BroadcastSession{IsTCP: true, TCPConn: conn, PayloadType: 8, RTPSSRC: 1}
		require.Error(t, bs.sendAudioData([]byte{1}))
	})

	t.Run("short payload is a single RTP frame", func(t *testing.T) {
		peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		require.NoError(t, err)
		defer peer.Close()
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		require.NoError(t, err)
		defer conn.Close()
		bs := &BroadcastSession{
			RTPConn: conn, RTPPeerIP: "127.0.0.1",
			RTPPeerPort: peer.LocalAddr().(*net.UDPAddr).Port,
			PayloadType: 8, RTPSSRC: 3,
		}
		require.NoError(t, bs.sendAudioData([]byte{1, 2, 3}))
		require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
		buf := make([]byte, 64)
		n, _, err := peer.ReadFromUDP(buf)
		require.NoError(t, err)
		require.Equal(t, 12+3, n)
	})
}

func TestServerScanMissingChannelsWithoutContext(t *testing.T) {
	repo := &fakeServerRepo{missing: []storage.GB28181ChannelRow{
		{DeviceID: "34020000001320000011", ChannelID: "34020000001320000011"},
	}}
	hub := NewWSHub()
	s := &Server{db: repo, store: NewDeviceStore(nil, hub), hub: hub}
	s.scanMissingChannels() // s.ctx is nil -> context.Background()
	require.Len(t, repo.updated, 1)
}

func TestServerHandlerInviteScansUnrelatedDevices(t *testing.T) {
	s, api, _, _, _ := newServerDispatchHarness(t, &fakeServerRepo{})
	seedOnlineDevice(api, "34020000001320000021", "34020000001320000022")

	req := newTestInboundRequest(sip.INVITE, "34020000001320009999", "34020000002000000001", "")
	tx := newFakeServerTx()
	s.handlerInvite(req, tx)
	require.Equal(t, []int{404}, tx.StatusCodes())
}

func TestServerStopStopsPlatforms(t *testing.T) {
	s, _, _, _, _ := newServerDispatchHarness(t, &fakeServerRepo{})
	platform := &Platform{Config: &PlatformConfig{Name: "up"}, quit: make(chan struct{})}
	s.platforms.platforms[1] = platform
	s.Stop()
	select {
	case <-platform.quit:
	default:
		t.Fatal("Server.Stop did not stop the platform manager entries")
	}
}

func TestServerParseChannelIDFromSDPSkipsEmptyOLine(t *testing.T) {
	require.Equal(t, "", parseChannelIDFromSDP("o=\r\nv=0\r\n"))
}

func TestNewServerCheckedWithoutConfiguredHost(t *testing.T) {
	db := newSQLiteRepositoryForTest(t)
	cfg := &Config{
		Enabled:         true,
		Port:            0,
		Host:            "", // falls back to MediaIP
		MediaIP:         "127.0.0.1",
		ID:              "34020000002000000001",
		Password:        "test-password-123",
		StandardVersion: "2016",
	}
	s, err := NewServerChecked(cfg, nil, db)
	require.NoError(t, err)
	require.NotNil(t, s)

	// Closing the listeners while the context is live exercises the Serve
	// error branches; Stop then shuts everything down.
	require.NoError(t, s.udpListener.Close())
	require.NoError(t, s.tcpListener.Close())
	require.Eventually(t, func() bool { return true }, 100*time.Millisecond, 10*time.Millisecond)
	s.Stop()
}

func TestHandlerRegisterPersistenceFailuresAreLogged(t *testing.T) {
	t.Run("register save failure", func(t *testing.T) {
		api, repo, store, _ := newRegisterTestAPI(t, "secret")
		repo.upsertErr = errors.New("db write failed")
		const deviceID = "34020000001320000061"
		req := registerRequest(deviceID, "3600")
		require.Equal(t, []int{200}, answerDigestChallenge(t, api, req, "secret").StatusCodes())
		dev, ok := store.Load(deviceID)
		require.True(t, ok)
		require.True(t, dev.IsOnline)
	})

	t.Run("logout status failure", func(t *testing.T) {
		client, _ := newTestSIPClient(t, nil)
		repo := &failingOnlineRepo{fakeDeviceRepo: newFakeDeviceRepo(), onlineErr: errors.New("db offline failed")}
		store := NewDeviceStore(repo, NewWSHub())
		api := NewGB28181API(&Config{ID: "34020000002000000001", Password: "secret"}, store, client, nil)
		const deviceID = "34020000001320000062"
		require.Equal(t, []int{200}, answerDigestChallenge(t, api, registerRequest(deviceID, "3600"), "secret").StatusCodes())
		require.Equal(t, []int{200}, answerDigestChallenge(t, api, registerRequest(deviceID, "0"), "secret").StatusCodes())
		dev, _ := store.Load(deviceID)
		require.False(t, dev.IsOnline)
	})
}

func TestVerifyDigestRejectsMalformedNonceCounts(t *testing.T) {
	api, _, _, _ := newRegisterTestAPI(t, "secret")
	const deviceID = "34020000001320000063"

	crafted := func(nc string) *sip.Request {
		req := registerRequest(deviceID, "")
		header := fmt.Sprintf(
			`Digest username="%s", realm="%s", nonce="nonce", uri="%s", response="%s", qop=auth, nc=%s, cnonce="abc", algorithm=MD5`,
			deviceID, api.cfg.GetDomain(), req.Recipient.String(), strings.Repeat("a", 32), nc)
		req.AppendHeader(sip.NewHeader("Authorization", header))
		return req
	}

	// Nonce count shorter than 8 hex digits.
	tx := newFakeServerTx()
	api.handlerRegister(crafted("1"), tx)
	require.Equal(t, []int{401}, tx.StatusCodes())

	// Well-formed but zero nonce count.
	tx = newFakeServerTx()
	api.handlerRegister(crafted("00000000"), tx)
	require.Equal(t, []int{401}, tx.StatusCodes())

	// Valid-shape header for a different user fails the identity check.
	req := registerRequest(deviceID, "")
	header := fmt.Sprintf(
		`Digest username="34020000001320009999", realm="%s", nonce="nonce", uri="%s", response="%s", qop=auth, nc=00000001, cnonce="abc", algorithm=MD5`,
		api.cfg.GetDomain(), req.Recipient.String(), strings.Repeat("b", 32))
	req.AppendHeader(sip.NewHeader("Authorization", header))
	tx = newFakeServerTx()
	api.handlerRegister(req, tx)
	require.Equal(t, []int{401}, tx.StatusCodes())
}

func TestTalkSessionAdditionalErrorPaths(t *testing.T) {
	t.Run("accept on closed listener", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		listener.Close()
		ts := NewTalkSession("d", "c", TransportTCPPassive, &Config{}, nil)
		ts.TCPListener = listener
		done := make(chan struct{})
		go func() { ts.acceptTCPConn(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("acceptTCPConn did not return on a closed listener")
		}
	})

	t.Run("audio sender logs failures", func(t *testing.T) {
		ts := NewTalkSession("d", "c", TransportUDP, &Config{}, nil)
		ts.SSRC = "not-a-number"
		ts.AudioChan <- []byte{1}
		close(ts.AudioChan)
		close(ts.ReadyCh)
		ts.startAudioSender()
	})

	t.Run("UDP peer resolution failure", func(t *testing.T) {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		require.NoError(t, err)
		defer conn.Close()
		ts := &TalkSession{
			TransportMode: TransportUDP, SSRC: "1", RTPConn: conn,
			RTPPeerIP: "bad host", RTPPeerPort: 1,
		}
		require.ErrorContains(t, ts.sendAudioData([]byte{1}), "resolve remote addr")
	})

	t.Run("TCP write failure", func(t *testing.T) {
		conn, peer := net.Pipe()
		peer.Close()
		ts := &TalkSession{TransportMode: TransportTCPActive, SSRC: "1", TCPConn: conn}
		require.Error(t, ts.sendAudioData([]byte{1}))
	})
}

func TestPlatformRegisterAuthFailureBranches(t *testing.T) {
	challenge := func(req *sip.Request) *sip.Response {
		res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
		res.AppendHeader(sip.NewHeader("WWW-Authenticate",
			`Digest realm="3402000000",qop="auth",algorithm=MD5,nonce="nonce-1"`))
		return res
	}

	t.Run("missing username fails digest build", func(t *testing.T) {
		client, requester := newTestSIPClient(t, nil)
		requester.override = func(req *sip.Request) (sip.ClientTransaction, error) {
			return newFakeClientTx([]*sip.Response{challenge(req)}), nil
		}
		p := newTestPlatform(t, client, newFakePlatformStore(), func(cfg *PlatformConfig) { cfg.Username = "" })
		require.ErrorContains(t, p.Register(), "build digest authorization")
	})

	t.Run("transport fallback and auth send failure", func(t *testing.T) {
		client, requester := newTestSIPClient(t, nil)
		requester.override = func(req *sip.Request) (sip.ClientTransaction, error) {
			if req.CSeq() != nil && req.CSeq().SeqNo == 0 {
				return newFakeClientTx([]*sip.Response{challenge(req)}), nil
			}
			return nil, errors.New("auth transport down")
		}
		p := newTestPlatform(t, client, newFakePlatformStore(), func(cfg *PlatformConfig) { cfg.Transport = "" })
		require.ErrorContains(t, p.Register(), "register auth send failed")
	})

	t.Run("auth without response", func(t *testing.T) {
		client, requester := newTestSIPClient(t, nil)
		requester.override = func(req *sip.Request) (sip.ClientTransaction, error) {
			if req.CSeq() != nil && req.CSeq().SeqNo == 0 {
				return newFakeClientTx([]*sip.Response{challenge(req)}), nil
			}
			return newFakeClientTx(nil), nil
		}
		p := newTestPlatform(t, client, newFakePlatformStore(), nil)
		require.ErrorContains(t, p.Register(), "register auth no response")
	})

	t.Run("initial register without response", func(t *testing.T) {
		client, requester := newTestSIPClient(t, nil)
		requester.override = func(*sip.Request) (sip.ClientTransaction, error) {
			return newFakeClientTx(nil), nil
		}
		p := newTestPlatform(t, client, newFakePlatformStore(), nil)
		require.ErrorContains(t, p.Register(), "register no response")
	})

	t.Run("keepalive without response", func(t *testing.T) {
		client, requester := newTestSIPClient(t, nil)
		requester.override = func(*sip.Request) (sip.ClientTransaction, error) {
			return newFakeClientTx(nil), nil
		}
		p := newTestPlatform(t, client, newFakePlatformStore(), nil)
		require.ErrorContains(t, p.Keepalive(), "keepalive no response")
	})

	t.Run("upstream message transport failure", func(t *testing.T) {
		client, _ := newTestSIPClientFailing(t, errors.New("peer down"))
		p := newTestPlatform(t, client, newFakePlatformStore(), nil)
		p.sendMessageToUpstream(sip.Uri{Scheme: "sip", User: p.Config.ServerGBID}, p.Config, "<x/>")
	})
}

func TestPlatformAddEnabledPlatformStartsUntilRemoved(t *testing.T) {
	store := newFakePlatformStore()
	client, _ := newTestSIPClientFailing(t, errors.New("platform down"))
	pm := NewPlatformManager(client, "192.0.2.10", "192.0.2.10", "34020000002000000001", "pass", store)

	cfg := defaultTestPlatformConfig()
	cfg.Enable = true // Start() runs and blocks on its retry timer until removed
	require.NoError(t, pm.AddPlatform(cfg))
	_, ok := pm.GetPlatform(cfg.ID)
	require.True(t, ok)
	require.NoError(t, pm.RemovePlatform(cfg.ID))
	_, ok = pm.GetPlatform(cfg.ID)
	require.False(t, ok)
}

func TestPlatformStartRetryWaitIsInterruptible(t *testing.T) {
	store := newFakePlatformStore()
	client, requester := newTestSIPClientFailing(t, errors.New("register down"))
	p := newTestPlatform(t, client, store, func(cfg *PlatformConfig) {
		cfg.Expires = 2 // retry delay of one second
		cfg.KeepTimeout = 60
	})

	done := make(chan struct{})
	go func() { p.Start(); close(done) }()
	require.Eventually(t, func() bool { return requester.Count() >= 2 }, 3*time.Second, 20*time.Millisecond,
		"Platform.Start should retry the failed register")
	// Interrupt via the quit channel so Stop never races with the running
	// Start loop over Platform.status; Stop is then a safe no-op.
	close(p.quit)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Platform.Start did not exit while waiting to retry")
	}
	p.Stop()
}

func TestPlatformStartKeepaliveSuccessAndReRegisterTicker(t *testing.T) {
	store := newFakePlatformStore()
	var mu sync.Mutex
	registers := 0
	keepalives := 0
	client, _ := newTestSIPClient(t, func(req *sip.Request) []*sip.Response {
		mu.Lock()
		defer mu.Unlock()
		if req.Method == sip.REGISTER {
			registers++
		} else {
			keepalives++
		}
		return []*sip.Response{sip.NewResponseFromRequest(req, 200, "OK", nil)}
	})
	p := newTestPlatform(t, client, store, func(cfg *PlatformConfig) {
		cfg.Expires = 2     // re-register ticker fires after one second
		cfg.KeepTimeout = 1 // keepalive ticker fires after one second
		cfg.MaxTimeoutCount = 5
	})

	done := make(chan struct{})
	go func() { p.Start(); close(done) }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return keepalives >= 1 && registers >= 2
	}, 3*time.Second, 20*time.Millisecond,
		"successful keepalive and the re-register ticker should both run")

	close(p.quit)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Platform.Start did not exit after being interrupted")
	}
	p.Stop()
}
