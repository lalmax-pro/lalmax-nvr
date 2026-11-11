package gb28181

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// catalogFailRepo injects persistence failures into the catalog apply path.
type catalogFailRepo struct {
	*fakeDeviceRepo
	batchErr   error
	missingErr error
	upsertErr  error
}

func (r *catalogFailRepo) BatchUpsertChannels(context.Context, string, []storage.GB28181ChannelRow) error {
	return r.batchErr
}

func (r *catalogFailRepo) IncrementMissingCount(context.Context, string, string) error {
	return r.missingErr
}

func (r *catalogFailRepo) UpsertGB28181Device(_ context.Context, device *storage.GB28181DeviceRow) error {
	if r.upsertErr != nil {
		return r.upsertErr
	}
	r.fakeDeviceRepo.mu.Lock()
	defer r.fakeDeviceRepo.mu.Unlock()
	cp := *device
	r.fakeDeviceRepo.infoRows = append(r.fakeDeviceRepo.infoRows, &cp)
	return nil
}

func validCatalogBody(deviceID string) []byte {
	return []byte(`<Response><CmdType>Catalog</CmdType><SN>9</SN><DeviceID>` + deviceID +
		`</DeviceID><SumNum>1</SumNum><DeviceList Num="1"><Item><DeviceID>34020000001320000001</DeviceID>` +
		`<Name>Cam</Name><Status>ON</Status></Item></DeviceList></Response>`)
}

func TestCatalogApplyFailureAndVersionBranches(t *testing.T) {
	const deviceID = "34020000001320000091"

	newAPI := func(t *testing.T, repo *catalogFailRepo, standardVersion string) *GB28181API {
		t.Helper()
		client, _ := newTestSIPClient(t, nil)
		store := NewDeviceStore(repo, NewWSHub())
		store.Store(deviceID, &Device{DeviceID: deviceID, IsOnline: true, GBVersion: GBVersionUnknown})
		api := NewGB28181API(&Config{ID: "34020000002000000001", StandardVersion: standardVersion}, store, client, nil)
		t.Cleanup(func() { api.streams.deleteStream(playStreamKey(deviceID, "ch")) })
		return api
	}

	t.Run("decode error", func(t *testing.T) {
		api := newAPI(t, &catalogFailRepo{fakeDeviceRepo: newFakeDeviceRepo()}, "2016")
		api.handleCatalogResponse(deviceID, []byte("<not-xml"))
	})

	t.Run("empty catalog is a no-op", func(t *testing.T) {
		api := newAPI(t, &catalogFailRepo{fakeDeviceRepo: newFakeDeviceRepo()}, "2016")
		api.handleCatalogResponse(deviceID, []byte(`<Response><CmdType>Catalog</CmdType><SN>1</SN><SumNum>0</SumNum></Response>`))
	})

	t.Run("2016 detection and missing channel counting", func(t *testing.T) {
		repo := &catalogFailRepo{fakeDeviceRepo: newFakeDeviceRepo()}
		repo.channels = map[string][]storage.GB28181ChannelRow{
			deviceID: {{DeviceID: deviceID, ChannelID: "34020000001320000099"}},
		}
		api := newAPI(t, repo, "2016")
		api.handleCatalogResponse(deviceID, validCatalogBody(deviceID))

		dev, ok := api.store.Load(deviceID)
		require.True(t, ok)
		require.Equal(t, GBVersion2016, dev.GBVersion)
		_, hasNew := dev.GetChannel("34020000001320000001")
		require.True(t, hasNew)
		_, hasStale := dev.GetChannel("34020000001320000099")
		require.False(t, hasStale)
	})

	t.Run("2022 detection", func(t *testing.T) {
		repo := &catalogFailRepo{fakeDeviceRepo: newFakeDeviceRepo()}
		api := newAPI(t, repo, "2022")
		api.handleCatalogResponse(deviceID, validCatalogBody(deviceID))
		dev, _ := api.store.Load(deviceID)
		require.Equal(t, GBVersion2022, dev.GBVersion)
	})

	t.Run("persistence failures are logged", func(t *testing.T) {
		repo := &catalogFailRepo{
			fakeDeviceRepo: newFakeDeviceRepo(),
			batchErr:       errors.New("batch failed"),
			missingErr:     errors.New("missing failed"),
			upsertErr:      errors.New("upsert failed"),
		}
		repo.channels = map[string][]storage.GB28181ChannelRow{
			deviceID: {{DeviceID: deviceID, ChannelID: "34020000001320000099"}},
		}
		api := newAPI(t, repo, "2016")
		// A failing device save still applies the catalog and channel writes.
		api.handleCatalogResponse(deviceID, validCatalogBody(deviceID))
	})

	t.Run("existing channel listing failure", func(t *testing.T) {
		repo := &catalogFailRepo{fakeDeviceRepo: newFakeDeviceRepo(), batchErr: nil}
		repo.channelErr = errors.New("list failed")
		api := newAPI(t, repo, "2016")
		api.handleCatalogResponse(deviceID, validCatalogBody(deviceID))
	})
}

func TestAlarmManagerSubscribeAlarm(t *testing.T) {
	store := NewDeviceStore(newFakeDeviceRepo(), NewWSHub())
	const deviceID = "34020000001320000101"
	store.Store(deviceID, &Device{DeviceID: deviceID, IsOnline: true, Address: "192.0.2.20:5060"})

	client, requester := newTestSIPClient(t, nil)
	am := NewAlarmManager(client, &Config{ID: "34020000002000000001"}, nil)
	require.NoError(t, am.SubscribeAlarm(deviceID, store))
	req := requester.LastRequest()
	require.Equal(t, sip.MESSAGE, req.Method)
	require.Contains(t, string(req.Body()), "<CmdType>Alarm</CmdType>")
	require.Contains(t, string(req.Body()), deviceID)

	failing, _ := newTestSIPClientFailing(t, errors.New("device unreachable"))
	failingManager := NewAlarmManager(failing, &Config{ID: "34020000002000000001"}, nil)
	require.ErrorContains(t, failingManager.SubscribeAlarm(deviceID, store), "device unreachable")
}

func TestWSHubBroadcastMarshalFailureAndSlowClientDropped(t *testing.T) {
	h := NewWSHub()
	runDone := make(chan struct{})
	go func() { h.Run(); close(runDone) }()

	// A client whose send queue is already full is closed and removed.
	full := &Client{hub: h, send: make(chan []byte, 256)}
	h.register <- full
	require.Eventually(t, func() bool { return hubClientCount(h) == 1 }, 2*time.Second, 10*time.Millisecond)
	for i := 0; i < 256; i++ {
		full.send <- []byte("x")
	}

	// An unmarshalable payload is dropped by the run loop without stopping it.
	h.Broadcast(Event{Type: EventDeviceOnline, Data: map[string]interface{}{"bad": func() {}}})
	// The next event reaches the saturated client, which is dropped.
	h.Broadcast(Event{Type: EventChannelUpdate})
	require.Eventually(t, func() bool { return hubClientCount(h) == 0 }, 2*time.Second, 10*time.Millisecond)

	h.Stop()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("WSHub.Run did not stop")
	}
}

func TestTalkManagerOnInviteRespondFailure(t *testing.T) {
	const deviceID = "34020000001320000111"
	const channelID = "34020000001320000112"
	store, _ := talkTestStore(deviceID, channelID)
	client, _ := newTestSIPClient(t, nil)
	tm := NewTalkManager(client, &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10", Host: "192.0.2.10", Port: 5060}, store)
	t.Cleanup(tm.StopAll)
	_, err := tm.StartTalk(deviceID, channelID, TransportUDP, store)
	require.NoError(t, err)

	tx := newFakeServerTx()
	tx.respondErr = errors.New("transaction terminated")
	tm.OnInvite(newTestInboundRequest(sip.INVITE, deviceID, channelID,
		"v=0\r\nc=IN IP4 192.0.2.20\r\nm=audio 40000 RTP/AVP 8\r\n"), tx)
	require.Equal(t, []int{200}, tx.StatusCodes())
}

func TestTalkSendBroadcastNotifyEdgeCases(t *testing.T) {
	client, requester := newTestSIPClient(t, nil)
	tm := NewTalkManager(client, &Config{}, NewDeviceStore(nil, NewWSHub()))

	// Device without a port keeps the SIP defaults.
	store, dev := talkTestStore("34020000001320000121", "34020000001320000122")
	dev.Address = "192.0.2.20"
	session := &TalkSession{DeviceID: dev.DeviceID, ChannelID: "34020000001320000122"}
	require.NoError(t, tm.sendBroadcastNotify(session, store))
	require.Contains(t, string(requester.LastRequest().Body()), "<SourceID>"+session.DeviceID+"</SourceID>")

	// A session whose device is gone is rejected before any SIP traffic.
	orphan := &TalkSession{DeviceID: "missing", ChannelID: "ch"}
	require.ErrorIs(t, tm.sendBroadcastNotify(orphan, store), ErrDeviceNotExist)
}
