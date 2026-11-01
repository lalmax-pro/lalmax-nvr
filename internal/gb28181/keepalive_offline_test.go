package gb28181

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRestoreHistoricalTCPRouteExtraGuards(t *testing.T) {
	const id = "32010000001327000003"
	store := NewDeviceStore(nil, nil)
	g := &GB28181API{store: store}

	g.restoreHistoricalTCPRoute("short", "192.0.2.1:5060", "TCP")
	g.restoreHistoricalTCPRoute(id, "192.0.2.1:5060", "TCP")

	dev := &Device{
		DeviceID:       id,
		Address:        "192.0.2.10:5060",
		LastRegisterAt: time.Now(),
		Transport:      "UDP",
	}
	store.Store(id, dev)
	g.restoreHistoricalTCPRoute(id, "not-a-host", "TCP")
	g.restoreHistoricalTCPRoute(id, "hostname:5060", "TCP")
	g.restoreHistoricalTCPRoute(id, "192.0.2.10:5099", "TCP")
	require.Equal(t, "UDP", dev.Transport)
	require.Equal(t, "192.0.2.10:5060", dev.Address)

	dev.Address = "hostname:5060"
	dev.Transport = ""
	g.restoreHistoricalTCPRoute(id, "hostname:5099", "TCP")
	require.Empty(t, dev.Transport)
}

func TestHandleKeepaliveWithoutLiveSockets(t *testing.T) {
	repo := newFakeDeviceRepo()
	store := NewDeviceStore(repo, nil)
	g := &GB28181API{store: store}

	const id = "34020000001110000001"
	g.handleKeepaliveFromRequest(id, "192.0.2.10:5060", "OK", "UDP")

	dev := &Device{
		DeviceID:       id,
		IsOnline:       false,
		Address:        "192.0.2.10:5060",
		LastRegisterAt: time.Now(),
		GBVersion:      GBVersion2016,
	}
	dev.Channels.Store("ch1", &Channel{ChannelID: "ch1", device: dev})
	store.Store(id, dev)

	g.handleKeepaliveFromRequest(id, "198.51.100.9:5060", "OK", "TCP")
	require.False(t, dev.IsOnline)

	g.handleKeepaliveFromRequest(id, "192.0.2.10:5099", "OK", "UDP")
	require.True(t, dev.IsOnline)
	require.False(t, dev.LastKeepaliveAt.IsZero())
	require.Len(t, repo.ops, 1)
	require.Equal(t, "device.online", repo.ops[0].Action)

	g.handleKeepalive(id, "192.0.2.10:5099", "OFF")
	require.False(t, dev.IsOnline)
	require.Len(t, repo.ops, 2)
	require.Equal(t, "device.offline", repo.ops[1].Action)

	empty := &Device{
		DeviceID:  "34020000001110000002",
		IsOnline:  true,
		GBVersion: GBVersion2016,
	}
	empty.Channels.Store("ch1", &Channel{ChannelID: "ch1", device: empty})
	store.Store(empty.DeviceID, empty)
	g.handleKeepalive(empty.DeviceID, "192.0.2.30:5060", "ON")
	require.True(t, empty.IsOnline)
	require.Equal(t, "192.0.2.30:5060", empty.Address)

	repo.upsertErr = errors.New("db down")
	g.handleKeepalive(id, "192.0.2.10:5099", "OK")

	repo.insertOpErr = errors.New("log down")
	repo.upsertErr = nil
	dev.IsOnline = false
	g.handleKeepalive(id, "192.0.2.10:5099", "OK")
}

func TestSendDeviceInfoQuerySkipsOffline(t *testing.T) {
	store := NewDeviceStore(nil, nil)
	g := &GB28181API{store: store}
	g.sendDeviceInfoQuery("missing")
	store.Store("34020000001110000001", &Device{DeviceID: "34020000001110000001", IsOnline: false})
	g.sendDeviceInfoQuery("34020000001110000001")
}
