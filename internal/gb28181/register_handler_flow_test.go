package gb28181

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	coreauth "github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

type fakeAlarmRepo struct {
	storage.GB28181AlarmRepository
	err  error
	rows []*storage.AlarmRow
}

func (f *fakeAlarmRepo) CreateAlarm(_ context.Context, alarm *storage.AlarmRow) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	cp := *alarm
	f.rows = append(f.rows, &cp)
	return int64(len(f.rows)), nil
}

func newRegisterTestAPI(t *testing.T, password string) (*GB28181API, *fakeDeviceRepo, *DeviceStore, *fakeTxRequester) {
	t.Helper()
	client, requester := newTestSIPClient(t, nil)
	repo := newFakeDeviceRepo()
	store := NewDeviceStore(repo, NewWSHub())
	api := NewGB28181API(&Config{
		ID:       "34020000002000000001",
		Password: password,
		Host:     "192.0.2.10",
		MediaIP:  "192.0.2.10",
		Port:     5060,
	}, store, client, nil)
	return api, repo, store, requester
}

func registerRequest(deviceID, expires string) *sip.Request {
	req := newTestInboundRequest(sip.REGISTER, deviceID, "34020000002000000001", "")
	if expires != "" {
		req.AppendHeader(sip.NewHeader("Expires", expires))
	}
	return req
}

// answerDigestChallenge performs the 401 challenge/response handshake so the
// authenticated REGISTER success path can be exercised.
func answerDigestChallenge(t *testing.T, api *GB28181API, req *sip.Request, password string) *fakeServerTx {
	t.Helper()
	challengeTx := newFakeServerTx()
	api.handlerRegister(req, challengeTx)
	require.Equal(t, []int{401}, challengeTx.StatusCodes())
	header := challengeTx.LastResponse().GetHeader("WWW-Authenticate")
	require.NotNil(t, header)
	params, ok := coreauth.ParseDigestAuthorization(header.Value())
	require.True(t, ok)

	auth, err := coreauth.BuildDigestAuthorization(
		"REGISTER", req.Recipient.String(), req.From().Address.User, password,
		params["realm"], params["nonce"], params["qop"])
	require.NoError(t, err)
	req.AppendHeader(sip.NewHeader("Authorization", auth))

	tx := newFakeServerTx()
	api.handlerRegister(req, tx)
	return tx
}

func TestHandlerRegisterRejectsInvalidDeviceAndMissingPassword(t *testing.T) {
	api, _, store, _ := newRegisterTestAPI(t, "secret")

	short := registerRequest("123", "")
	tx := newFakeServerTx()
	api.handlerRegister(short, tx)
	require.Equal(t, []int{400}, tx.StatusCodes())

	nonDigit := registerRequest("3402000000132000abcd", "")
	tx = newFakeServerTx()
	api.handlerRegister(nonDigit, tx)
	require.Equal(t, []int{400}, tx.StatusCodes())

	for _, password := range []string{"", "#", "   "} {
		client, _ := newTestSIPClient(t, nil)
		empty := NewGB28181API(&Config{ID: "34020000002000000001", Password: password},
			NewDeviceStore(newFakeDeviceRepo(), NewWSHub()), client, nil)
		req := registerRequest("34020000001320000001", "")
		tx := newFakeServerTx()
		empty.handlerRegister(req, tx)
		require.Equal(t, []int{403}, tx.StatusCodes())
	}

	// A non-digest Authorization header is never accepted.
	req := registerRequest("34020000001320000001", "")
	req.AppendHeader(sip.NewHeader("Authorization", "Basic dXNlcjpwYXNz"))
	tx = newFakeServerTx()
	api.handlerRegister(req, tx)
	require.Equal(t, []int{401}, tx.StatusCodes())

	// A malformed digest header is rejected with a fresh challenge.
	malformed := registerRequest("34020000001320000001", "")
	malformed.AppendHeader(sip.NewHeader("Authorization", "Digest garbage"))
	tx = newFakeServerTx()
	api.handlerRegister(malformed, tx)
	require.Equal(t, []int{401}, tx.StatusCodes())

	placeholder, ok := store.Load("34020000001320000001")
	require.True(t, ok)
	require.False(t, placeholder.IsOnline)
}

func TestHandlerRegisterDigestWrongPasswordIsChallengedAgain(t *testing.T) {
	api, _, store, _ := newRegisterTestAPI(t, "secret")
	req := registerRequest("34020000001320000001", "")
	challengeTx := newFakeServerTx()
	api.handlerRegister(req, challengeTx)
	params, ok := coreauth.ParseDigestAuthorization(challengeTx.LastResponse().GetHeader("WWW-Authenticate").Value())
	require.True(t, ok)
	auth, err := coreauth.BuildDigestAuthorization("REGISTER", req.Recipient.String(),
		req.From().Address.User, "wrong-password", params["realm"], params["nonce"], params["qop"])
	require.NoError(t, err)
	req.AppendHeader(sip.NewHeader("Authorization", auth))

	tx := newFakeServerTx()
	api.handlerRegister(req, tx)
	require.Equal(t, []int{401}, tx.StatusCodes())
	dev, loaded := store.Load("34020000001320000001")
	require.True(t, loaded) // the in-memory placeholder is created before auth
	require.False(t, dev.IsOnline)
}

func TestHandlerRegisterDigestSuccessMarksDeviceOnline(t *testing.T) {
	api, repo, store, requester := newRegisterTestAPI(t, "secret")
	const deviceID = "34020000001320000001"
	req := registerRequest(deviceID, "3600")
	tx := answerDigestChallenge(t, api, req, "secret")
	require.Equal(t, []int{200}, tx.StatusCodes())

	dev, ok := store.Load(deviceID)
	require.True(t, ok)
	require.True(t, dev.IsOnline)
	require.Equal(t, "192.0.2.20:5060", dev.Address)
	require.Equal(t, "UDP", dev.Transport)
	require.Equal(t, GBVersion2016, dev.GBVersion)
	require.False(t, dev.LastRegisterAt.IsZero())
	require.NotEmpty(t, repo.infoRows)

	// The post-register DeviceInfo + Catalog queries go out over SIP.
	require.Eventually(t, func() bool { return requester.Count() >= 2 }, 2*time.Second, 10*time.Millisecond)

	// A second REGISTER with Expires: 0 logs the device out.
	logout := registerRequest(deviceID, "0")
	logoutTx := answerDigestChallenge(t, api, logout, "secret")
	require.Equal(t, []int{200}, logoutTx.StatusCodes())
	dev, ok = store.Load(deviceID)
	require.True(t, ok)
	require.False(t, dev.IsOnline)
	require.Equal(t, []bool{false}, repo.onlineCalls)
}

func TestHandlerRegisterLogoutStopsChannelStreams(t *testing.T) {
	api, _, store, _ := newRegisterTestAPI(t, "secret")
	const deviceID = "34020000001320000002"
	const channelID = "34020000001320000002"
	req := registerRequest(deviceID, "3600")
	require.Equal(t, []int{200}, answerDigestChallenge(t, api, req, "secret").StatusCodes())

	dev, _ := store.Load(deviceID)
	dev.Channels.Store(channelID, &Channel{ChannelID: channelID, device: dev})
	key := playStreamKey(deviceID, channelID)
	api.streams.storeStream(key, &Streams{DeviceID: deviceID, ChannelID: channelID})
	t.Cleanup(func() { api.streams.deleteStream(key) })

	logout := registerRequest(deviceID, "0")
	require.Equal(t, []int{200}, answerDigestChallenge(t, api, logout, "secret").StatusCodes())
	require.False(t, api.streams.isPlaying(deviceID, channelID))
}

func TestHandlerMessageDispatch(t *testing.T) {
	api, _, store, _ := newRegisterTestAPI(t, "secret")
	const deviceID = "34020000001320000003"
	dev := &Device{DeviceID: deviceID, IsOnline: true, Address: "192.0.2.20:5060", LastRegisterAt: time.Now()}
	dev.Channels.Store("ch", &Channel{ChannelID: "ch", device: dev})
	store.Store(deviceID, dev)

	alarmRepo := &fakeAlarmRepo{}
	api.svr = &Server{alarm: NewAlarmManager(nil, api.cfg, alarmRepo)}

	bodies := map[string]string{
		"empty":     "",
		"malformed": "<not-xml",
		"catalog": `<Response><CmdType>Catalog</CmdType><SN>1</SN><DeviceID>` + deviceID +
			`</DeviceID><SumNum>1</SumNum><DeviceList Num="1"><Item><DeviceID>34020000001320000001</DeviceID><Name>Cam</Name></Item></DeviceList></Response>`,
		"device info": `<Response><CmdType>DeviceInfo</CmdType><SN>2</SN><DeviceID>` + deviceID +
			`</DeviceID><Manufacturer>Vendor</Manufacturer><Model>M</Model><Firmware>1</Firmware></Response>`,
		"record info": `<Response><CmdType>RecordInfo</CmdType><SN>3</SN><DeviceID>` + deviceID + `</DeviceID><SumNum>0</SumNum></Response>`,
		"keepalive":   `<Notify><CmdType>Keepalive</CmdType><SN>4</SN><DeviceID>` + deviceID + `</DeviceID><Status>OK</Status></Notify>`,
		"alarm":       `<Notify><CmdType>Alarm</CmdType><SN>5</SN><DeviceID>` + deviceID + `</DeviceID><AlarmType>videoLoss</AlarmType><AlarmTime>2026-01-02T03:04:05</AlarmTime></Notify>`,
		"unhandled":   `<Notify><CmdType>Unknown</CmdType><SN>6</SN></Notify>`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			req := newTestInboundRequest(sip.MESSAGE, deviceID, "34020000002000000001", body)
			tx := newFakeServerTx()
			api.handlerMessage(req, tx)
			require.Equal(t, []int{200}, tx.StatusCodes())
		})
	}
	require.Len(t, alarmRepo.rows, 1)
	require.Equal(t, "videoLoss", alarmRepo.rows[0].AlarmType)
	wantAlarmTime, err := time.ParseInLocation("2006-01-02T15:04:05", "2026-01-02T03:04:05", time.Local)
	require.NoError(t, err)
	require.True(t, alarmRepo.rows[0].AlarmTime.Equal(wantAlarmTime))
	require.False(t, dev.LastKeepaliveAt.IsZero())

	// An alarm repository failure must not break SIP processing.
	alarmRepo.err = errors.New("alarm store down")
	req := newTestInboundRequest(sip.MESSAGE, deviceID, "34020000002000000001",
		`<Notify><CmdType>Alarm</CmdType><SN>7</SN><DeviceID>`+deviceID+`</DeviceID></Notify>`)
	tx := newFakeServerTx()
	api.handlerMessage(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())

	// Malformed alarm XML is tolerated.
	bad := newTestInboundRequest(sip.MESSAGE, deviceID, "34020000002000000001",
		`<Notify><CmdType>Alarm</CmdType><SN>8</SN>`)
	api.svr.alarm.HandleAlarm(deviceID, bad.Body())
}

func TestHandlerNotifyKeepaliveAndMalformed(t *testing.T) {
	api, _, store, requester := newRegisterTestAPI(t, "secret")
	const deviceID = "34020000001320000004"
	dev := &Device{DeviceID: deviceID, IsOnline: true, Address: "192.0.2.20:5060", LastRegisterAt: time.Now()}
	store.Store(deviceID, dev)

	// An online device without channels triggers async DeviceInfo and Catalog
	// queries. Wait for both outbound MESSAGEs so the background goroutines
	// finish reading the device before the test updates it again.
	queryDone := make(chan struct{})
	var queryMu sync.Mutex
	queryCount := 0
	requester.handler = func(req *sip.Request) []*sip.Response {
		if req.Method == sip.MESSAGE {
			queryMu.Lock()
			queryCount++
			if queryCount == 2 {
				close(queryDone)
			}
			queryMu.Unlock()
		}
		return []*sip.Response{okResponse(req)}
	}

	valid := newTestInboundRequest(sip.NOTIFY, deviceID, "34020000002000000001",
		`<Notify><CmdType>Keepalive</CmdType><DeviceID>`+deviceID+`</DeviceID><Status>OK</Status></Notify>`)
	tx := newFakeServerTx()
	api.handlerNotify(valid, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())
	require.False(t, dev.LastKeepaliveAt.IsZero())
	select {
	case <-queryDone:
	case <-time.After(2 * time.Second):
		t.Fatal("device bootstrap queries did not complete")
	}

	malformed := newTestInboundRequest(sip.NOTIFY, deviceID, "34020000002000000001", "<not-xml")
	tx = newFakeServerTx()
	api.handlerNotify(malformed, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())

	// An empty NOTIFY body still refreshes the keepalive.
	empty := newTestInboundRequest(sip.NOTIFY, deviceID, "34020000002000000001", "")
	tx = newFakeServerTx()
	api.handlerNotify(empty, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())
}

func TestQueryRecordInfoAnswersFromDeviceReply(t *testing.T) {
	api, _, store, _ := newRegisterTestAPI(t, "secret")
	const deviceID = "34020000001320000005"
	const channelID = "34020000001320000005"
	dev := &Device{DeviceID: deviceID, IsOnline: true, Address: "192.0.2.20:5060"}
	dev.Channels.Store(channelID, &Channel{ChannelID: channelID, device: dev})
	store.Store(deviceID, dev)

	client, _ := newTestSIPClient(t, func(req *sip.Request) []*sip.Response {
		// The query is in flight: reply with an empty RecordInfo list using the
		// SN from the outgoing XML so the waiting state is completed.
		sn := extractRecordInfoSN(string(req.Body()))
		go func() {
			time.Sleep(5 * time.Millisecond)
			api.handleRecordInfoResponse(deviceID, []byte(
				`<Response><CmdType>RecordInfo</CmdType><SN>`+sn+`</SN><DeviceID>`+channelID+
					`</DeviceID><SumNum>0</SumNum></Response>`))
		}()
		return []*sip.Response{sip.NewResponseFromRequest(req, 200, "OK", nil)}
	})
	api.client = client

	start := time.Now().Add(-time.Hour)
	end := time.Now()
	records, err := api.QueryRecordInfo(deviceID, channelID, start, end)
	require.NoError(t, err)
	require.NotNil(t, records)
	require.Empty(t, records.Data)
}

func extractRecordInfoSN(body string) string {
	const marker = "<SN>"
	idx := indexOf(body, marker)
	if idx < 0 {
		return "0"
	}
	rest := body[idx+len(marker):]
	end := indexOf(rest, "</SN>")
	if end < 0 {
		return "0"
	}
	return rest[:end]
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
