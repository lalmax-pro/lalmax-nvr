package gb28181

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHandleAlarmEmptyAndInvalidTimeUsesNow(t *testing.T) {
	store := &fakeAlarmStore{}
	am := NewAlarmManager(nil, &Config{ID: "34020000002000000001"}, store)

	before := time.Now().Add(-time.Second)
	am.HandleAlarm("34020000001110000001", []byte(`<?xml version="1.0"?>
<Notify>
<CmdType>Alarm</CmdType>
<SN>1</SN>
<DeviceID>34020000001320000001</DeviceID>
<AlarmType>1</AlarmType>
<Priority>1</Priority>
<Method>2</Method>
<Description>empty-time</Description>
</Notify>`))
	after := time.Now().Add(time.Second)
	require.Len(t, store.rows, 1)
	require.Equal(t, "empty-time", store.rows[0].Description)
	require.False(t, store.rows[0].AlarmTime.Before(before))
	require.False(t, store.rows[0].AlarmTime.After(after))

	am.HandleAlarm("34020000001110000001", []byte(`<?xml version="1.0"?>
<Notify>
<CmdType>Alarm</CmdType>
<SN>2</SN>
<DeviceID>34020000001320000002</DeviceID>
<AlarmType>3</AlarmType>
<AlarmTime>not-a-timestamp</AlarmTime>
<Priority>9</Priority>
<Method>1</Method>
<Description>bad-time</Description>
</Notify>`))
	require.Len(t, store.rows, 2)
	require.Equal(t, "bad-time", store.rows[1].Description)
	require.Equal(t, 9, store.rows[1].Priority)
	require.False(t, store.rows[1].AlarmTime.IsZero())
}

func TestSubscribeAlarmOfflineLoadedDevice(t *testing.T) {
	store := NewDeviceStore(nil, nil)
	am := NewAlarmManager(nil, &Config{}, nil)
	dev := &Device{DeviceID: "34020000001110000001", IsOnline: false}
	store.Store(dev.DeviceID, dev)
	require.ErrorIs(t, am.SubscribeAlarm(dev.DeviceID, store), ErrDeviceOffline)
}
