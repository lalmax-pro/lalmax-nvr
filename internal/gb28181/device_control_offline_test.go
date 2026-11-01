package gb28181

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeviceControlRejectsMissingAndOfflineDevices(t *testing.T) {
	store := NewDeviceStore(nil, NewWSHub())
	api := NewGB28181API(&Config{ID: "34020000002000000001", Password: "secret", MediaIP: "192.0.2.10"}, store, nil, nil)

	require.ErrorContains(t, api.Snapshot("missing", "ch"), "device not found")
	require.ErrorContains(t, api.DeviceReset("missing", "ch"), "device not found")
	require.ErrorContains(t, api.RecordControl("missing", "ch", "Record"), "device not found")
	require.ErrorContains(t, api.SetHomePosition("missing", "ch", 1, 10, 1), "device not found")
	require.ErrorContains(t, api.DeviceConfigQuery("missing", "ch"), "device not found")
	require.ErrorContains(t, api.DeviceStatusQuery("missing", "ch"), "device not found")
	require.ErrorContains(t, api.DeviceInfoQuery("missing", "ch"), "device not found")
	require.ErrorContains(t, api.PTZControl("missing", "ch", "A50F010000000000"), "device not found")

	offline := &Device{DeviceID: "34020000001320000001", IsOnline: false}
	store.LoadOrStore(offline.DeviceID, offline)
	require.ErrorIs(t, api.Snapshot(offline.DeviceID, "ch"), ErrDeviceOffline)
	require.ErrorIs(t, api.DeviceReset(offline.DeviceID, "ch"), ErrDeviceOffline)
	require.ErrorIs(t, api.RecordControl(offline.DeviceID, "ch", "Record"), ErrDeviceOffline)
	require.ErrorIs(t, api.SetHomePosition(offline.DeviceID, "ch", 1, 10, 1), ErrDeviceOffline)
	require.ErrorIs(t, api.DeviceConfigQuery(offline.DeviceID, "ch"), ErrDeviceOffline)
	require.ErrorIs(t, api.DeviceStatusQuery(offline.DeviceID, "ch"), ErrDeviceOffline)
	require.ErrorIs(t, api.DeviceInfoQuery(offline.DeviceID, "ch"), ErrDeviceOffline)
	require.ErrorContains(t, api.PTZControl(offline.DeviceID, "ch", "A50F010000000000"), "offline")
}

func TestDeviceInfoXML(t *testing.T) {
	body := deviceInfoXML("34020000001320000001")
	require.Contains(t, string(body), "<CmdType>DeviceInfo</CmdType>")
	require.Contains(t, string(body), "<DeviceID>34020000001320000001</DeviceID>")
	var msg MessageDeviceInfoResponse
	require.NoError(t, xmlUnmarshal(body, &msg))
	require.Equal(t, "DeviceInfo", msg.CmdType)
	require.Equal(t, "34020000001320000001", msg.DeviceID)
}
