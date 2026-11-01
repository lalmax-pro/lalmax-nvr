package gb28181

import (
	"errors"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestLoadFromDBSuccessAndChannelError(t *testing.T) {
	now := time.Now()
	repo := newFakeDeviceRepo()
	repo.devices = []storage.GB28181DeviceRow{
		{
			DeviceID:        "34020000001110000001",
			Address:         "192.0.2.10:5060",
			GBVersion:       "2016",
			LastKeepaliveAt: &now,
			LastRegisterAt:  &now,
		},
		{DeviceID: "34020000001110000002", Address: "192.0.2.11:5060"},
	}
	repo.channels["34020000001110000001"] = []storage.GB28181ChannelRow{
		{ChannelID: "ch1", Name: "cam1"},
	}
	store := NewDeviceStore(repo, nil)
	require.NoError(t, store.LoadFromDB())
	dev, ok := store.Load("34020000001110000001")
	require.True(t, ok)
	require.False(t, dev.IsOnline)
	require.Equal(t, GBVersion2016, dev.GBVersion)
	ch, ok := dev.GetChannel("ch1")
	require.True(t, ok)
	require.Equal(t, "cam1", ch.Name)
	require.Equal(t, now, dev.LastKeepaliveAt)
	require.Equal(t, now, dev.LastRegisterAt)

	repo.listErr = errors.New("db down")
	require.ErrorContains(t, store.LoadFromDB(), "db down")

	repo.listErr = nil
	repo.channelErr = errors.New("channels down")
	fresh := NewDeviceStore(repo, nil)
	require.NoError(t, fresh.LoadFromDB())
	loaded, ok := fresh.Load("34020000001110000001")
	require.True(t, ok)
	_, ok = loaded.GetChannel("ch1")
	require.False(t, ok)
}

func TestLoadOrStoreMergesSourceAndVersion(t *testing.T) {
	store := NewDeviceStore(nil, nil)
	incoming := &Device{DeviceID: "d1", source: "192.0.2.1:5060"}
	store.LoadOrStore("d1", incoming)
	got, ok := store.Load("d1")
	require.True(t, ok)
	require.Equal(t, GBVersionUnknown, got.GBVersion)

	store.LoadOrStore("d1", &Device{source: "192.0.2.9:5070", GBVersion: GBVersion2022})
	got, ok = store.Load("d1")
	require.True(t, ok)
	require.Equal(t, "192.0.2.9:5070", got.source)
	require.Equal(t, GBVersion2022, got.GBVersion)
}

func TestDeviceStoreChangeSaveAndRange(t *testing.T) {
	repo := newFakeDeviceRepo()
	store := NewDeviceStore(repo, nil)

	_, ok := store.GetChannel("missing", "ch")
	require.False(t, ok)
	require.ErrorIs(t, store.Change("missing", func(*Device) {}), ErrDeviceNotExist)

	_, _, err := store.ChangeAndSaveExistingDevice("missing", func(*Device) error { return nil })
	require.ErrorIs(t, err, ErrDeviceNotExist)

	wasOnline, err := store.ChangeAndSaveDevice("d1", nil, func(d *Device) {
		d.IsOnline = true
		d.Address = "192.0.2.10:5060"
	})
	require.NoError(t, err)
	require.False(t, wasOnline)
	dev, ok := store.Load("d1")
	require.True(t, ok)
	require.True(t, dev.IsOnline)
	require.Equal(t, GBVersionUnknown, dev.GBVersion)

	repo.upsertErr = errors.New("save failed")
	_, err = store.ChangeAndSaveDevice("d1", &Device{DeviceID: ""}, func(d *Device) {
		d.Address = "changed"
	})
	require.ErrorContains(t, err, "save failed")

	seen := 0
	store.RangeDevices(func(key string, value *Device) bool {
		seen++
		require.Equal(t, "d1", key)
		require.Equal(t, "changed", value.Address)
		return true
	})
	require.Equal(t, 1, seen)
}

func TestDeviceStoreStatusRegistrationChannelsAndDelete(t *testing.T) {
	repo := newFakeDeviceRepo()
	hub := NewWSHub()
	t.Cleanup(hub.Stop)
	store := NewDeviceStore(repo, hub)

	require.NoError(t, store.UpdateDeviceStatus("d1", true, "192.0.2.1:5060"))
	require.NoError(t, store.UpdateDeviceStatus("d1", false, "192.0.2.1:5060"))
	require.Len(t, repo.statusCalls, 2)
	require.True(t, repo.statusCalls[0].isOnline)
	require.False(t, repo.statusCalls[1].isOnline)

	require.NoError(t, store.UpdateDeviceRegistration("d1", "192.0.2.2:5060"))
	require.Equal(t, []string{"192.0.2.2:5060"}, repo.regCalls)
	require.NoError(t, store.UpdateDeviceOnlineStatus("d1", false))
	require.Equal(t, []bool{false}, repo.onlineCalls)

	store.recordOperation("d1", true, "192.0.2.1:5060")
	require.Equal(t, "device.online", repo.ops[0].Action)
	store.recordOperation("d1", false, "192.0.2.1:5060")
	require.Equal(t, "device.offline", repo.ops[1].Action)
	nilStore := NewDeviceStore(nil, nil)
	nilStore.recordOperation("d1", true, "addr")

	require.NoError(t, store.SaveChannels("d1", []Channel{{
		ChannelID: "ch1", Name: "cam", Manufacturer: "Hik", Model: "DS",
		SecurityLevelCode: "1", RecordSaveDays: 7, Longitude: 1.5,
	}}))
	require.Len(t, repo.savedChannels["d1"], 1)
	require.Equal(t, "ch1", repo.savedChannels["d1"][0].ChannelID)
	require.Equal(t, "1", repo.savedChannels["d1"][0].SecurityLevelCode)

	require.NoError(t, store.SaveDeviceInfo("d1", "Hik", "DS", "1.0"))
	require.Equal(t, "Hik", repo.infoRows[len(repo.infoRows)-1].Manufacturer)

	require.ErrorIs(t, store.DeleteDevice("missing"), ErrDeviceNotExist)

	dev := &Device{DeviceID: "d2", IsOnline: false}
	dev.Channels.Store("ch1", &Channel{ChannelID: "ch1", device: dev})
	store.Store("d2", dev)
	globalStreams.storeStream(playStreamKey("d2", "ch1"), &Streams{DeviceID: "d2", ChannelID: "ch1"})
	t.Cleanup(func() { globalStreams.deleteStream(playStreamKey("d2", "ch1")) })
	require.ErrorIs(t, store.DeleteOfflineDevice("d2"), ErrDeviceHasActiveStream)
}
