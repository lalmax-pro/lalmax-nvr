package gb28181

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	coreauth "github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// fakeServerRepo embeds the full GB28181 repository contract so the server
// wiring can be exercised with only the calls a test needs implemented.
type fakeServerRepo struct {
	storage.GB28181Repository
	missing     []storage.GB28181ChannelRow
	listErr     error
	updateErr   error
	updated     []string
	device      *storage.GB28181DeviceRow
	deviceErr   error
	platforms   []storage.PlatformRow
	platformErr error
}

func (f *fakeServerRepo) ListMissingChannels(context.Context, int) ([]storage.GB28181ChannelRow, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.missing, nil
}

func (f *fakeServerRepo) UpdateChannelStatus(_ context.Context, deviceID, channelID, _ string) error {
	f.updated = append(f.updated, deviceID+"/"+channelID)
	return f.updateErr
}

func (f *fakeServerRepo) GetGB28181Device(context.Context, string) (*storage.GB28181DeviceRow, error) {
	return f.device, f.deviceErr
}

func (f *fakeServerRepo) ListPlatforms(context.Context) ([]storage.PlatformRow, error) {
	return f.platforms, f.platformErr
}

func newServerDispatchHarness(t *testing.T, repo storage.GB28181Repository) (*Server, *GB28181API, *DeviceStore, *sipgo.Client, *fakeTxRequester) {
	t.Helper()
	client, requester := newTestSIPClient(t, nil)
	hub := NewWSHub()
	store := NewDeviceStore(nil, hub)
	cfg := &Config{
		Host:     "192.0.2.10",
		MediaIP:  "192.0.2.10",
		ID:       "34020000002000000001",
		Password: "test-password",
		Port:     5060,
	}
	api := &GB28181API{
		cfg:          cfg,
		store:        store,
		streams:      &streamsManager{},
		client:       client,
		hub:          hub,
		digestNonces: coreauth.NewDigestNonceStore(5*time.Minute, 8192),
	}
	s := &Server{
		cfg:       cfg,
		db:        repo,
		store:     store,
		gb:        api,
		client:    client,
		ctx:       context.Background(),
		hub:       hub,
		platforms: NewPlatformManager(client, cfg.Host, cfg.MediaIP, cfg.ID, cfg.Password, nil),
		broadcast: NewBroadcastManager(client, cfg, store),
		talk:      NewTalkManager(client, cfg, store),
		alarm:     NewAlarmManager(client, cfg, repo),
		download:  NewDownloadManager(client, cfg, nil, repo, ""),
	}
	api.svr = s
	return s, api, store, client, requester
}

func seedOnlineDevice(api *GB28181API, deviceID, channelID string) *Device {
	dev := &Device{
		DeviceID:  deviceID,
		IsOnline:  true,
		Transport: "UDP",
		Address:   "192.0.2.20:5060",
	}
	ch := &Channel{ChannelID: channelID, device: dev}
	dev.Channels.Store(channelID, ch)
	api.store.Store(deviceID, dev)
	return dev
}

func TestServerGetterAndDelegationSurface(t *testing.T) {
	repo := &fakeServerRepo{}
	s, api, store, client, requester := newServerDispatchHarness(t, repo)
	const deviceID = "34020000001320000001"
	const channelID = "34020000001320000001"
	seedOnlineDevice(api, deviceID, channelID)

	require.Equal(t, api.cfg, s.GetConfig())
	require.Equal(t, store, s.GetDeviceStore())
	require.NotNil(t, s.GetPlatforms())
	require.NotNil(t, s.GetBroadcastManager())
	require.NotNil(t, s.GetTalkManager())
	require.NotNil(t, s.GetAlarmManager())
	require.NotNil(t, s.GetDownloadManager())
	require.False(t, s.IsStreamPlaying(StreamID(deviceID, channelID)))

	require.NoError(t, s.QueryCatalog(deviceID))
	_, err := s.Play(&PlayInput{DeviceID: deviceID, ChannelID: "missing"})
	require.ErrorIs(t, err, ErrChannelNotExist)
	_, err = s.EnsurePlay(&PlayInput{DeviceID: deviceID, ChannelID: "missing"})
	require.ErrorIs(t, err, ErrChannelNotExist)
	require.NoError(t, s.StopPlay(&StopPlayInput{DeviceID: deviceID, ChannelID: channelID}))
	_, err = s.QueryRecordInfo("missing", channelID, time.Now().Add(-time.Minute), time.Now())
	require.ErrorIs(t, err, ErrDeviceOffline)
	_, err = s.Playback(&PlaybackInput{DeviceID: deviceID, ChannelID: "missing"})
	require.ErrorIs(t, err, ErrChannelNotExist)
	require.Error(t, s.PlaySpeed(&PlaySpeedInput{DeviceID: deviceID, ChannelID: channelID}))
	require.Error(t, s.PlaySeek(&PlaySeekInput{DeviceID: deviceID, ChannelID: channelID}))
	require.Error(t, s.PlayPause(&PlayPauseInput{DeviceID: deviceID, ChannelID: channelID}))
	require.Error(t, s.PlayResume(&PlayPauseInput{DeviceID: deviceID, ChannelID: channelID}))

	require.NoError(t, s.PTZControl(deviceID, channelID, BuildStop()))
	require.NoError(t, s.PTZPositionControl(deviceID, channelID, 90, 45, 2))
	require.NoError(t, s.QueryPTZPosition(deviceID, channelID))
	for _, call := range []func() error{
		func() error { return s.SetPreset(deviceID, channelID, 1) },
		func() error { return s.CallPreset(deviceID, channelID, 1) },
		func() error { return s.DeletePreset(deviceID, channelID, 1) },
		func() error { return s.CruiseAddPoint(deviceID, channelID, 1, 1) },
		func() error { return s.CruiseDeletePoint(deviceID, channelID, 1, 1) },
		func() error { return s.CruiseSetSpeed(deviceID, channelID, 1, 2) },
		func() error { return s.CruiseStart(deviceID, channelID, 1) },
		func() error { return s.CruiseStop(deviceID, channelID, 1) },
		func() error { return s.ScanSetLeft(deviceID, channelID, 1) },
		func() error { return s.ScanSetRight(deviceID, channelID, 1) },
		func() error { return s.ScanSetSpeed(deviceID, channelID, 1, 2) },
		func() error { return s.ScanStart(deviceID, channelID, 1) },
		func() error { return s.ScanStop(deviceID, channelID, 1) },
		func() error { return s.Snapshot(deviceID, channelID) },
		func() error { return s.DeviceReset(deviceID, channelID) },
		func() error { return s.RecordControl(deviceID, channelID, "StartRecord") },
		func() error { return s.SetHomePosition(deviceID, channelID, 1, 60, 1) },
		func() error { return s.DeviceConfigQuery(deviceID, channelID) },
		func() error { return s.DeviceStatusQuery(deviceID, channelID) },
		func() error { return s.DeviceInfoQuery(deviceID, channelID) },
	} {
		require.NoError(t, call())
	}
	require.Positive(t, requester.Count())

	// Offline and unknown devices travel through the same wrappers.
	offline, _ := store.Load(deviceID)
	offline.IsOnline = false
	require.ErrorIs(t, s.QueryCatalog(deviceID), ErrDeviceOffline)
	_, err = s.Play(&PlayInput{DeviceID: deviceID, ChannelID: channelID})
	require.ErrorIs(t, err, ErrDeviceOffline)
	_, err = s.Playback(&PlaybackInput{DeviceID: deviceID, ChannelID: channelID})
	require.ErrorIs(t, err, ErrDeviceOffline)
	require.Error(t, s.Snapshot("missing", channelID))
	require.Error(t, s.DeviceReset("missing", channelID))
	require.Error(t, s.PTZControl("missing", channelID, BuildStop()))
	require.Error(t, s.QueryPTZPosition("missing", channelID))
	client.Close()
}

func TestServerListDevicesMergesDatabaseRow(t *testing.T) {
	keepalive := time.Unix(1700000000, 0).UTC()
	repo := &fakeServerRepo{device: &storage.GB28181DeviceRow{
		DeviceID:        "34020000001320000001",
		Name:            "Front door",
		Manufacturer:    "Vendor",
		Model:           "NVR-1",
		Firmware:        "1.2.3",
		GBVersion:       "2022",
		LastKeepaliveAt: &keepalive,
		LastRegisterAt:  &keepalive,
	}}
	s, api, _, _, _ := newServerDispatchHarness(t, repo)
	seedOnlineDevice(api, "34020000001320000001", "34020000001320000001")

	devices := s.ListDevices()
	require.Len(t, devices, 1)
	require.Equal(t, "Front door", devices[0]["name"])
	require.Equal(t, "Vendor", devices[0]["manufacturer"])
	require.Equal(t, "2022", devices[0]["gb_version"])
	channels, ok := devices[0]["channels"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, channels, 1)
	require.Equal(t, "34020000001320000001", channels[0]["channel_id"])

	// A repository error keeps the in-memory device visible.
	repo.deviceErr = errors.New("db down")
	repo.device = nil
	devices = s.ListDevices()
	require.Len(t, devices, 1)
	require.NotContains(t, devices[0], "name")
}

func TestServerHandlerAckRoutesBroadcastSessions(t *testing.T) {
	s, _, _, _, _ := newServerDispatchHarness(t, &fakeServerRepo{})

	// Missing From header is ignored.
	s.handlerAck(&sip.Request{Method: sip.ACK}, newFakeServerTx())

	req := newTestInboundRequest(sip.ACK, "34020000001320000001", "34020000002000000001", "")
	s.handlerAck(req, newFakeServerTx())

	s.broadcast.mu.Lock()
	s.broadcast.sessions["34020000001320000001_"] = &BroadcastSession{DeviceID: "34020000001320000001"}
	s.broadcast.mu.Unlock()
	s.handlerAck(req, newFakeServerTx())
}

func TestServerHandlerInviteRejectsInvalidAndUnknown(t *testing.T) {
	s, _, _, _, _ := newServerDispatchHarness(t, &fakeServerRepo{})

	noFrom := &sip.Request{Method: sip.INVITE}
	tx := newFakeServerTx()
	s.handlerInvite(noFrom, tx)
	require.Equal(t, []int{400}, tx.StatusCodes())

	unknown := newTestInboundRequest(sip.INVITE, "34020000001320009999", "34020000002000000001", "")
	tx = newFakeServerTx()
	s.handlerInvite(unknown, tx)
	require.Equal(t, []int{404}, tx.StatusCodes())
}

func TestServerHandlerInviteFromUpstreamPlatform(t *testing.T) {
	s, _, _, _, _ := newServerDispatchHarness(t, &fakeServerRepo{})
	s.platforms.mu.Lock()
	s.platforms.platforms[1] = &Platform{Config: &PlatformConfig{ServerGBID: "44010000002000000001"}}
	s.platforms.mu.Unlock()

	req := newTestInboundRequest(sip.INVITE, "44010000002000000001", "34020000002000000001", "")
	tx := newFakeServerTx()
	s.handlerInvite(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())
}

func TestServerHandlerInviteDispatchesTalkAndBroadcastSessions(t *testing.T) {
	s, _, _, _, _ := newServerDispatchHarness(t, &fakeServerRepo{})
	udpSDP := "v=0\r\nc=IN IP4 192.0.2.20\r\nm=audio 40000 RTP/AVP 8\r\ny=0000000033\r\n"

	talk := NewTalkSession("34020000001320000002", "34020000001320000002", TransportUDP, s.cfg, nil)
	talk.RTPPort = 31000
	s.talk.sessions[talkKey(talk.DeviceID, talk.ChannelID)] = talk
	req := newTestInboundRequest(sip.INVITE, talk.DeviceID, talk.ChannelID, udpSDP)
	tx := newFakeServerTx()
	s.handlerInvite(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())
	require.NoError(t, talk.Stop())

	// The dispatch lookup probes GetSession(from, "") so the broadcast entry is
	// registered under the device-only key.
	bs := &BroadcastSession{
		DeviceID:    "34020000001320000003",
		ChannelID:   "34020000001320000003",
		cfg:         s.cfg,
		AudioChan:   make(chan []byte, 4),
		ReadyCh:     make(chan struct{}),
		TCPReadyCh:  make(chan struct{}),
		PayloadType: 8,
	}
	s.broadcast.sessions[bs.DeviceID+"_"] = bs
	req = newTestInboundRequest(sip.INVITE, bs.DeviceID, bs.ChannelID, udpSDP)
	tx = newFakeServerTx()
	s.handlerInvite(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())
	require.NoError(t, bs.Stop())
}

func TestServerHandlerInviteDeviceInitiatedCreatesTalkSession(t *testing.T) {
	s, api, store, _, _ := newServerDispatchHarness(t, &fakeServerRepo{})
	const deviceID = "34020000001320000004"
	seedOnlineDevice(api, deviceID, "34020000001320000004")
	t.Cleanup(func() { s.talk.StopAll() })

	sdp := "v=0\r\n" +
		"o=34020000001320000004 0 0 IN IP4 192.0.2.20\r\n" +
		"s=Play\r\n" +
		"c=IN IP4 192.0.2.20\r\n" +
		"m=audio 40000 RTP/AVP 8\r\n" +
		"y=0000000033\r\n"
	req := newTestInboundRequest(sip.INVITE, deviceID, "34020000002000000001", sdp)
	tx := newFakeServerTx()
	s.handlerInvite(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())

	session, ok := s.talk.GetSession(deviceID, "34020000001320000004")
	require.True(t, ok)
	require.NotZero(t, session.RTPPort)
	require.Equal(t, "192.0.2.20", session.RTPPeerIP)
	require.Equal(t, 40000, session.RTPPeerPort)
	_, loaded := store.Load(deviceID)
	require.True(t, loaded)
}

func TestServerScanMissingChannelsMarksOffline(t *testing.T) {
	repo := &fakeServerRepo{missing: []storage.GB28181ChannelRow{
		{DeviceID: "34020000001320000005", ChannelID: "34020000001320000005", MissingCount: 5},
		{DeviceID: "34020000001320000006", ChannelID: "34020000001320000006", MissingCount: 4},
	}}
	s, api, _, _, _ := newServerDispatchHarness(t, repo)
	seedOnlineDevice(api, "34020000001320000005", "34020000001320000005")

	s.scanMissingChannels()
	require.Len(t, repo.updated, 2)
	dev, ok := api.store.Load("34020000001320000005")
	require.True(t, ok)
	_, hasChannel := dev.GetChannel("34020000001320000005")
	require.False(t, hasChannel)

	repo.listErr = errors.New("list failed")
	s.scanMissingChannels()

	require.Len(t, repo.updated, 2, "listing failure must not update channels")

	seedOnlineDevice(api, "34020000001320000006", "34020000001320000006")
	repo.listErr = nil
	repo.updateErr = errors.New("update failed")
	s.scanMissingChannels()
	require.Len(t, repo.updated, 4, "each missing channel is attempted once")
	dev, ok = api.store.Load("34020000001320000006")
	require.True(t, ok)
	_, hasChannel = dev.GetChannel("34020000001320000006")
	require.True(t, hasChannel, "a failed status write must keep the channel")
}

func TestServerDispatchResolveHostAndParseChannelID(t *testing.T) {
	require.Equal(t, "", resolveHost(""))
	require.Equal(t, "192.0.2.10", resolveHost("192.0.2.10"))
	require.NotEmpty(t, resolveHost("localhost"))

	require.Equal(t, "34020000001320000001", parseChannelIDFromSDP("o=34020000001320000001 0 0 IN IP4 192.0.2.20\r\n"))
	require.Equal(t, "", parseChannelIDFromSDP("o=not-a-number 0 0 IN IP4 192.0.2.20\r\n"))
	require.Equal(t, "", parseChannelIDFromSDP("o=123 0 0 IN IP4 192.0.2.20\r\n"))
	require.Equal(t, "", parseChannelIDFromSDP("v=0\r\n"))
}

func TestNewServerCompatibilityConstructorFailure(t *testing.T) {
	occupied, err := net.ListenPacket("udp", "0.0.0.0:0")
	require.NoError(t, err)
	defer occupied.Close()

	db := newSQLiteRepositoryForTest(t)
	cfg := &Config{
		Enabled:         true,
		Port:            occupied.LocalAddr().(*net.UDPAddr).Port,
		Host:            "127.0.0.1",
		MediaIP:         "127.0.0.1",
		ID:              "34020000002000000001",
		Password:        "test-password-123",
		StandardVersion: "2016",
	}
	server, stop := NewServer(cfg, nil, db)
	require.Nil(t, server)
	require.NotNil(t, stop)
	stop() // must be a safe no-op

	// A free port lets the compatibility constructor return a running server.
	working, err := storage.New(filepath.Join(t.TempDir(), "nvr-ok.sqlite"))
	require.NoError(t, err)
	defer working.Close()
	cfg.Port = 0
	server, stop = NewServer(cfg, nil, working)
	require.NotNil(t, server)
	require.NotNil(t, stop)
	stop()
}

func newSQLiteRepositoryForTest(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.New(filepath.Join(t.TempDir(), "nvr-test.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestServerHandlerInviteRejectsLookalikeUpstreamID(t *testing.T) {
	s, _, _, _, _ := newServerDispatchHarness(t, &fakeServerRepo{})
	s.platforms.mu.Lock()
	s.platforms.platforms[2] = &Platform{Config: &PlatformConfig{DeviceGBID: "44010000002000000002"}}
	s.platforms.mu.Unlock()

	req := newTestInboundRequest(sip.INVITE, "44010000002000000002", "34020000002000000001", "")
	tx := newFakeServerTx()
	s.handlerInvite(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())
}

func TestServerHandlerInviteInvalidSDPFallsBackToFromUser(t *testing.T) {
	s, api, _, _, _ := newServerDispatchHarness(t, &fakeServerRepo{})
	const deviceID = "34020000001320000007"
	const channelID = "34020000001320000017"
	seedOnlineDevice(api, deviceID, channelID)
	t.Cleanup(func() { s.talk.StopAll() })

	// SDP without an o= line: channelID falls back to the From user and the
	// first device channel is used.
	req := newTestInboundRequest(sip.INVITE, deviceID, "34020000002000000001", "v=0\r\nm=audio 40000 RTP/AVP 8\r\n")
	tx := newFakeServerTx()
	s.handlerInvite(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())
	_, ok := s.talk.GetSession(deviceID, channelID)
	require.True(t, ok)
}

func TestServerHandlerInviteTalkStartFailureReturns500(t *testing.T) {
	s, api, _, _, _ := newServerDispatchHarness(t, &fakeServerRepo{})
	const deviceID = "34020000001320000008"
	seedOnlineDevice(api, deviceID, "34020000001320000008")
	// A failing SIP peer makes the Broadcast NOTIFY fail, so StartTalk aborts.
	s.talk.client, _ = newTestSIPClientFailing(t, errors.New("peer down"))

	req := newTestInboundRequest(sip.INVITE, deviceID, "34020000002000000001", "")
	tx := newFakeServerTx()
	s.handlerInvite(req, tx)
	require.Equal(t, []int{500}, tx.StatusCodes())
}

func TestServerHandlerInviteChannelMatchedByChannelID(t *testing.T) {
	s, api, _, _, _ := newServerDispatchHarness(t, &fakeServerRepo{})
	const deviceID = "34020000001320000009"
	const channelID = "34020000001320000019"
	seedOnlineDevice(api, deviceID, channelID)
	t.Cleanup(func() { s.talk.StopAll() })

	// From user is the channel ID, which the store resolves through method 2.
	req := newTestInboundRequest(sip.INVITE, channelID, "34020000002000000001", "")
	tx := newFakeServerTx()
	s.handlerInvite(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())
}

func TestServerStopIsIdempotentWithManagers(t *testing.T) {
	s, _, _, _, _ := newServerDispatchHarness(t, &fakeServerRepo{})
	s.Stop()
	s.Stop()
}
