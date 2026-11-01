package gb28181

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// fakePlatformStore captures the persistence side effects of platform
// signaling so register/keepalive flows can be asserted without a database.
type fakePlatformStore struct {
	storage.GB28181PlatformCatalogRepository

	mu          sync.Mutex
	platforms   []storage.PlatformRow
	channels    []storage.PlatformChannelRow
	events      []storage.PlatformEventRow
	statuses    []bool
	deleted     []int64
	nextID      int64
	listErr     error
	channelErr  error
	createErr   error
	deleteErr   error
	statusErr   error
	eventErr    error
	updateCalls []int64
}

func newFakePlatformStore() *fakePlatformStore {
	return &fakePlatformStore{nextID: 1}
}

func (f *fakePlatformStore) ListPlatforms(context.Context) ([]storage.PlatformRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.platforms, nil
}

func (f *fakePlatformStore) CreatePlatform(_ context.Context, platform *storage.PlatformRow) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return 0, f.createErr
	}
	id := f.nextID
	f.nextID++
	cp := *platform
	cp.ID = id
	f.platforms = append(f.platforms, cp)
	return id, nil
}

func (f *fakePlatformStore) DeletePlatform(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return f.deleteErr
}

func (f *fakePlatformStore) UpdatePlatformStatus(_ context.Context, id int64, status bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, status)
	f.updateCalls = append(f.updateCalls, id)
	return f.statusErr
}

func (f *fakePlatformStore) ListPlatformChannels(_ context.Context, _ int64, _ *bool) ([]storage.PlatformChannelRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.channelErr != nil {
		return nil, f.channelErr
	}
	return f.channels, nil
}

func (f *fakePlatformStore) AddPlatformEvent(_ context.Context, event storage.PlatformEventRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return f.eventErr
}

func (f *fakePlatformStore) Events() []storage.PlatformEventRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]storage.PlatformEventRow, len(f.events))
	copy(out, f.events)
	return out
}

func (f *fakePlatformStore) Statuses() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]bool, len(f.statuses))
	copy(out, f.statuses)
	return out
}

func defaultTestPlatformConfig() *PlatformConfig {
	return &PlatformConfig{
		ID:              1,
		Name:            "upstream",
		Enable:          false,
		ServerGBID:      "44010000002000000001",
		ServerIP:        "192.0.2.30",
		ServerPort:      5060,
		DeviceGBID:      "34020000002000000001",
		DeviceIP:        "192.0.2.10",
		DevicePort:      5060,
		Username:        "device-user",
		Password:        "device-pass",
		Transport:       "UDP",
		Expires:         3600,
		KeepTimeout:     60,
		MaxTimeoutCount: 3,
	}
}

func newTestPlatform(t *testing.T, client *sipgo.Client, store storage.GB28181PlatformCatalogRepository, mutate func(*PlatformConfig)) *Platform {
	t.Helper()
	cfg := defaultTestPlatformConfig()
	if mutate != nil {
		mutate(cfg)
	}
	return &Platform{
		Config:   cfg,
		client:   client,
		sipIP:    "192.0.2.10",
		mediaIP:  "192.0.2.10",
		serial:   cfg.DeviceGBID,
		password: cfg.Password,
		store:    store,
		quit:     make(chan struct{}),
	}
}

func digestChallengeResponse(req *sip.Request, header string) []*sip.Response {
	res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
	res.AppendHeader(sip.NewHeader("WWW-Authenticate", header))
	return []*sip.Response{res}
}

func TestPlatformRegisterSucceedsAfterDigestChallenge(t *testing.T) {
	store := newFakePlatformStore()
	client, requester := newTestSIPClient(t, nil)
	p := newTestPlatform(t, client, store, nil)

	requester.handler = func(req *sip.Request) []*sip.Response {
		if requester.Count() == 1 {
			return digestChallengeResponse(req, `Digest realm="3402000000",qop="auth",algorithm=MD5,nonce="nonce-abc"`)
		}
		return []*sip.Response{sip.NewResponseFromRequest(req, 200, "OK", nil)}
	}

	require.NoError(t, p.Register())
	require.True(t, p.status.Load())
	require.NotEmpty(t, p.registerCallID)
	require.Equal(t, 2, requester.Count())
	require.Equal(t, []bool{true}, store.Statuses())
	events := store.Events()
	require.Len(t, events, 1)
	require.Equal(t, storage.PlatformEventRegister, events[0].EventType)

	requests := requester.Requests()
	require.NotNil(t, requests[1].GetHeader("Authorization"))
	require.Contains(t, requests[1].GetHeader("Authorization").Value(), "Digest")
	require.NotNil(t, requests[1].Via())
	require.Equal(t, uint32(1), requests[1].CSeq().SeqNo)
}

func TestPlatformRegisterWithoutQopChallenge(t *testing.T) {
	store := newFakePlatformStore()
	client, requester := newTestSIPClient(t, nil)
	p := newTestPlatform(t, client, store, func(cfg *PlatformConfig) {
		cfg.DeviceGBDomain = "3402000000"
		cfg.ServerGBDomain = "4401000000"
	})
	requester.handler = func(req *sip.Request) []*sip.Response {
		if requester.Count() == 1 {
			return digestChallengeResponse(req, `Digest realm="3402000000",algorithm=MD5,nonce="nonce-noqop"`)
		}
		return []*sip.Response{sip.NewResponseFromRequest(req, 200, "OK", nil)}
	}
	require.NoError(t, p.Register())
	require.True(t, p.status.Load())
	require.Equal(t, 2, requester.Count())
}

func TestPlatformRegisterFailurePaths(t *testing.T) {
	cases := []struct {
		name    string
		handler func(req *sip.Request) []*sip.Response
		want    string
	}{
		{
			name: "challenge without header",
			handler: func(req *sip.Request) []*sip.Response {
				return []*sip.Response{sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)}
			},
			want: "no auth challenge",
		},
		{
			name: "malformed challenge",
			handler: func(req *sip.Request) []*sip.Response {
				return digestChallengeResponse(req, "Basic realm=x")
			},
			want: "invalid digest challenge",
		},
		{
			name: "challenge missing nonce",
			handler: func(req *sip.Request) []*sip.Response {
				return digestChallengeResponse(req, `Digest realm="3402000000"`)
			},
			want: "missing realm or nonce",
		},
		{
			name: "unsupported algorithm",
			handler: func(req *sip.Request) []*sip.Response {
				return digestChallengeResponse(req, `Digest realm="3402000000",nonce="n",algorithm=SHA-256`)
			},
			want: "unsupported digest algorithm",
		},
		{
			name: "unsupported qop",
			handler: func(req *sip.Request) []*sip.Response {
				return digestChallengeResponse(req, `Digest realm="3402000000",nonce="n",algorithm=MD5,qop="auth-int"`)
			},
			want: "unsupported digest challenge",
		},
		{
			name: "register rejected",
			handler: func(req *sip.Request) []*sip.Response {
				return []*sip.Response{sip.NewResponseFromRequest(req, 403, "Forbidden", nil)}
			},
			want: "register rejected: 403",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakePlatformStore()
			client, requester := newTestSIPClient(t, tc.handler)
			p := newTestPlatform(t, client, store, nil)
			err := p.Register()
			require.ErrorContains(t, err, tc.want)
			require.False(t, p.status.Load())
			require.Equal(t, 1, requester.Count())
		})
	}

	t.Run("transport failure", func(t *testing.T) {
		client, _ := newTestSIPClientFailing(t, errors.New("no route"))
		p := newTestPlatform(t, client, newFakePlatformStore(), nil)
		require.ErrorContains(t, p.Register(), "register send failed")
	})

	t.Run("rejected register records failure event", func(t *testing.T) {
		store := newFakePlatformStore()
		client, _ := newTestSIPClient(t, func(req *sip.Request) []*sip.Response {
			return []*sip.Response{sip.NewResponseFromRequest(req, 500, "Server Error", nil)}
		})
		p := newTestPlatform(t, client, store, nil)
		require.ErrorContains(t, p.Register(), "register rejected: 500")
		events := store.Events()
		require.Len(t, events, 1)
		require.Contains(t, events[0].Details, "rejected: 500")
	})
}

func TestPlatformKeepaliveAndMessageFlows(t *testing.T) {
	store := newFakePlatformStore()
	client, requester := newTestSIPClient(t, nil)
	p := newTestPlatform(t, client, store, nil)
	p.status.Store(true)

	require.NoError(t, p.Keepalive())
	req := requester.LastRequest()
	require.Equal(t, sip.MESSAGE, req.Method)
	require.Contains(t, string(req.Body()), "<CmdType>Keepalive</CmdType>")
	require.Contains(t, string(req.Body()), p.Config.DeviceGBID)

	// Domain fallbacks exercise the truncation branch of Keepalive.
	store2 := newFakePlatformStore()
	client2, _ := newTestSIPClient(t, nil)
	p2 := newTestPlatform(t, client2, store2, func(cfg *PlatformConfig) {
		cfg.DeviceGBDomain = "3402000000"
	})
	require.NoError(t, p2.Keepalive())

	rejected, _ := newTestSIPClient(t, func(req *sip.Request) []*sip.Response {
		return []*sip.Response{sip.NewResponseFromRequest(req, 500, "Server Error", nil)}
	})
	p3 := newTestPlatform(t, rejected, newFakePlatformStore(), nil)
	require.ErrorContains(t, p3.Keepalive(), "keepalive rejected: 500")

	failing, _ := newTestSIPClientFailing(t, errors.New("udp down"))
	p4 := newTestPlatform(t, failing, newFakePlatformStore(), nil)
	require.ErrorContains(t, p4.Keepalive(), "keepalive send failed")
}

func TestPlatformOnMessageDispatchesCommands(t *testing.T) {
	bodies := map[string]string{
		"empty":          "",
		"malformed":      "<not-xml",
		"catalog":        `<?xml version="1.0"?><Response><CmdType>Catalog</CmdType><SN>7</SN><DeviceID>34020000002000000001</DeviceID></Response>`,
		"device control": `<?xml version="1.0"?><Control><CmdType>DeviceControl</CmdType><SN>8</SN><DeviceID>34020000001320000001</DeviceID></Control>`,
		"record info":    `<?xml version="1.0"?><Query><CmdType>RecordInfo</CmdType><SN>9</SN><DeviceID>34020000001320000001</DeviceID></Query>`,
		"device info":    `<?xml version="1.0"?><Query><CmdType>DeviceInfo</CmdType><SN>10</SN><DeviceID>34020000001320000001</DeviceID></Query>`,
		"device status":  `<?xml version="1.0"?><Query><CmdType>DeviceStatus</CmdType><SN>11</SN><DeviceID>34020000001320000001</DeviceID></Query>`,
		"unhandled":      `<?xml version="1.0"?><Notify><CmdType>Unknown</CmdType><SN>12</SN></Notify>`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			client, requester := newTestSIPClient(t, nil)
			p := newTestPlatform(t, client, newFakePlatformStore(), nil)
			p.status.Store(name == "device status")
			req := newTestInboundRequest(sip.MESSAGE, "44010000002000000001", "34020000002000000001", body)
			tx := newFakeServerTx()
			p.OnMessage(req, tx)
			require.Equal(t, []int{200}, tx.StatusCodes())
			switch name {
			case "device info", "device status":
				require.Equal(t, 1, requester.Count())
				sent := requester.LastRequest()
				require.Equal(t, sip.MESSAGE, sent.Method)
				if name == "device status" {
					require.Contains(t, string(sent.Body()), "<Online>ONLINE</Online>")
				} else {
					require.Contains(t, string(sent.Body()), "<CmdType>DeviceInfo</CmdType>")
				}
			default:
				require.Zero(t, requester.Count())
			}
		})
	}

	// OFFLINE status reporting when the platform has not registered.
	client, requester := newTestSIPClient(t, nil)
	p := newTestPlatform(t, client, newFakePlatformStore(), nil)
	req := newTestInboundRequest(sip.MESSAGE, "44010000002000000001", "34020000002000000001",
		`<Query><CmdType>DeviceStatus</CmdType><SN>1</SN></Query>`)
	p.OnMessage(req, newFakeServerTx())
	require.Contains(t, string(requester.LastRequest().Body()), "<Online>OFFLINE</Online>")
}

func TestPlatformSendCatalogItemEmptyItemAndAuthChallenge(t *testing.T) {
	t.Run("empty catalog", func(t *testing.T) {
		client, requester := newTestSIPClient(t, nil)
		p := newTestPlatform(t, client, newFakePlatformStore(), nil)
		p.sendCatalogItem(sip.Uri{Scheme: "sip", User: p.Config.ServerGBID}, 42, p.Config, nil, 0)
		require.Equal(t, 1, requester.Count())
		body := string(requester.LastRequest().Body())
		require.Contains(t, body, "<SumNum>0</SumNum>")
		require.Contains(t, body, "<DeviceList Num=\"0\">")
	})

	t.Run("single item", func(t *testing.T) {
		client, requester := newTestSIPClient(t, nil)
		p := newTestPlatform(t, client, newFakePlatformStore(), nil)
		item := catalogItem{DeviceID: "34020000001320000001", Name: "Gate", ParentID: "34020000002000000001"}
		p.sendCatalogItem(sip.Uri{Scheme: "sip", User: p.Config.ServerGBID}, 42, p.Config, &item, 3)
		require.Equal(t, 1, requester.Count())
		body := string(requester.LastRequest().Body())
		require.Contains(t, body, "<SumNum>3</SumNum>")
		require.Contains(t, body, "34020000001320000001")
	})

	t.Run("401 triggers auth retry hook", func(t *testing.T) {
		client, _ := newTestSIPClient(t, func(req *sip.Request) []*sip.Response {
			return []*sip.Response{sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)}
		})
		p := newTestPlatform(t, client, newFakePlatformStore(), nil)
		p.sendCatalogItem(sip.Uri{Scheme: "sip", User: p.Config.ServerGBID}, 42, p.Config, nil, 0)
	})

	t.Run("transport failure is logged not panicked", func(t *testing.T) {
		client, _ := newTestSIPClientFailing(t, errors.New("peer down"))
		p := newTestPlatform(t, client, newFakePlatformStore(), nil)
		p.sendCatalogItem(sip.Uri{Scheme: "sip", User: p.Config.ServerGBID}, 42, p.Config, nil, 0)
	})

	t.Run("device info and status 401 retry hooks", func(t *testing.T) {
		client, requester := newTestSIPClient(t, func(req *sip.Request) []*sip.Response {
			return []*sip.Response{sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)}
		})
		p := newTestPlatform(t, client, newFakePlatformStore(), nil)
		req := newTestInboundRequest(sip.MESSAGE, p.Config.ServerGBID, p.Config.DeviceGBID, "")
		p.handleDeviceInfoResponse(req, 1, p.Config.DeviceGBID)
		p.handleDeviceStatusResponse(req, 2, p.Config.DeviceGBID)
		require.Equal(t, 2, requester.Count())
	})

	t.Run("handleAuthAndRetry is a logging no-op", func(t *testing.T) {
		p := newTestPlatform(t, nil, newFakePlatformStore(), nil)
		p.handleAuthAndRetry(&sip.Request{}, nil, p.Config, "Message")
		require.Nil(t, p.getManager())
	})
}

func TestPlatformManagerLoadPlatformsDefaultsAndSharedChannels(t *testing.T) {
	store := newFakePlatformStore()
	store.platforms = []storage.PlatformRow{{
		ID:         7,
		Name:       "disabled up",
		Enable:     false,
		ServerGBID: "44010000002000000001",
		ServerIP:   "192.0.2.30",
		ServerPort: 5060,
		DeviceGBID: "34020000002000000001",
	}}
	store.channels = []storage.PlatformChannelRow{{
		ChannelID: "34020000001320000001", DeviceID: "34020000001320000001",
		CustomID: "34020000001320000001", CustomName: "Gate", StreamPath: "rtp/34020000001320000001",
	}}

	pm := NewPlatformManager(nil, "192.0.2.10", "192.0.2.10", "34020000002000000001", "pass", store)
	require.NoError(t, pm.LoadPlatforms())
	p, ok := pm.GetPlatform(7)
	require.True(t, ok)
	require.Equal(t, 3600, p.Config.Expires)
	require.Equal(t, 60, p.Config.KeepTimeout)
	require.Equal(t, 3, p.Config.MaxTimeoutCount)
	require.Equal(t, "UDP", p.Config.Transport)
	require.Equal(t, "GB2312", p.Config.CharacterSet)

	channels := pm.GetSharedChannels(7)
	require.Len(t, channels, 1)
	require.Equal(t, "Gate", channels[0].CustomName)
	require.Equal(t, "rtp/34020000001320000001", channels[0].StreamPath)

	store.channelErr = errors.New("channels unavailable")
	require.Nil(t, pm.GetSharedChannels(7))

	store.listErr = errors.New("platforms unavailable")
	require.ErrorContains(t, pm.LoadPlatforms(), "platforms unavailable")
}

func TestPlatformManagerEnabledPlatformStartsAndStops(t *testing.T) {
	store := newFakePlatformStore()
	store.platforms = []storage.PlatformRow{{
		ID:              9,
		Name:            "enabled up",
		Enable:          true,
		ServerGBID:      "44010000002000000001",
		ServerIP:        "192.0.2.30",
		ServerPort:      5060,
		DeviceGBID:      "34020000002000000001",
		DeviceIP:        "192.0.2.10",
		DevicePort:      5060,
		Transport:       "UDP",
		Expires:         3600,
		KeepTimeout:     60,
		MaxTimeoutCount: 3,
	}}
	client, _ := newTestSIPClientFailing(t, errors.New("platform peer down"))
	pm := NewPlatformManager(client, "192.0.2.10", "192.0.2.10", "34020000002000000001", "pass", store)
	require.NoError(t, pm.LoadPlatforms())
	p, ok := pm.GetPlatform(9)
	require.True(t, ok)
	// Register fails and the platform waits for its retry timer; Stop wakes it.
	p.Stop()
	require.NoError(t, pm.RemovePlatform(9))
	require.Equal(t, []int64{9}, store.deleted)
}

func TestPlatformStartRetriesThenReRegistersOnKeepaliveFailure(t *testing.T) {
	store := newFakePlatformStore()
	var registerCalls int
	var mu sync.Mutex
	client, _ := newTestSIPClient(t, func(req *sip.Request) []*sip.Response {
		mu.Lock()
		defer mu.Unlock()
		if req.Method == sip.REGISTER {
			registerCalls++
			return []*sip.Response{sip.NewResponseFromRequest(req, 200, "OK", nil)}
		}
		return []*sip.Response{sip.NewResponseFromRequest(req, 503, "Unavailable", nil)}
	})
	p := newTestPlatform(t, client, store, func(cfg *PlatformConfig) {
		cfg.Expires = 4
		cfg.KeepTimeout = 1
		cfg.MaxTimeoutCount = 1
	})

	done := make(chan struct{})
	go func() { p.Start(); close(done) }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return registerCalls >= 2
	}, 3*time.Second, 20*time.Millisecond, "keepalive failure should trigger a re-register")

	// Interrupt via quit so the Stop read of Platform.status never races with
	// the still-running Start loop; then Stop is a safe idempotent no-op.
	close(p.quit)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Platform.Start did not exit after being interrupted")
	}
	p.Stop()
	require.False(t, p.status.Load())
	require.NotEmpty(t, store.Statuses())
	p.Stop() // idempotent
}

func TestPlatformStopRecordsUnregisterEvent(t *testing.T) {
	store := newFakePlatformStore()
	p := newTestPlatform(t, nil, store, nil)
	p.status.Store(true)
	p.Stop()
	events := store.Events()
	require.Len(t, events, 1)
	require.Equal(t, storage.PlatformEventUnregister, events[0].EventType)
	require.Equal(t, "platform stopped", events[0].Details)
	require.False(t, p.status.Load())
}

func TestPlatformAddPlatformPersistsAndStartsDisabled(t *testing.T) {
	store := newFakePlatformStore()
	pm := NewPlatformManager(nil, "192.0.2.10", "192.0.2.10", "34020000002000000001", "pass", store)
	cfg := defaultTestPlatformConfig()
	cfg.Enable = false
	require.NoError(t, pm.AddPlatform(cfg))
	require.NotZero(t, cfg.ID)
	got, ok := pm.GetPlatform(cfg.ID)
	require.True(t, ok)
	require.Equal(t, "upstream", got.Config.Name)

	store.createErr = errors.New("insert failed")
	require.ErrorContains(t, pm.AddPlatform(defaultTestPlatformConfig()), "insert failed")
}

func TestPlatformManagerRemoveMissingPlatformStillDeletesRow(t *testing.T) {
	store := newFakePlatformStore()
	pm := NewPlatformManager(nil, "192.0.2.10", "192.0.2.10", "34020000002000000001", "pass", store)
	require.NoError(t, pm.RemovePlatform(404))
	require.Equal(t, []int64{404}, store.deleted)
}
