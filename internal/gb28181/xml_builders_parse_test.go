package gb28181

import (
	"context"
	"encoding/xml"
	"errors"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestKeepaliveNotifyXMLParse(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
<Notify>
<CmdType>Keepalive</CmdType>
<SN>1001</SN>
<DeviceID>34020000001110000001</DeviceID>
<Status>OK</Status>
<Info>online</Info>
</Notify>`)
	var msg MessageNotify
	require.NoError(t, xmlUnmarshal(body, &msg))
	require.Equal(t, "Keepalive", msg.CmdType)
	require.Equal(t, 1001, msg.SN)
	require.Equal(t, "34020000001110000001", msg.DeviceID)
	require.Equal(t, "OK", msg.Status)
	require.Equal(t, "online", msg.Info)
}

func TestDeviceInfoQueryAndResponseXML(t *testing.T) {
	query := deviceInfoXML("34020000001110000001")
	var q struct {
		XMLName  xml.Name `xml:"Query"`
		CmdType  string   `xml:"CmdType"`
		SN       int      `xml:"SN"`
		DeviceID string   `xml:"DeviceID"`
	}
	require.NoError(t, xmlUnmarshal(query, &q))
	require.Equal(t, "DeviceInfo", q.CmdType)
	require.Equal(t, "34020000001110000001", q.DeviceID)

	var resp MessageDeviceInfoResponse
	require.NoError(t, xmlUnmarshal([]byte(`<?xml version="1.0"?>
<Response>
<CmdType>DeviceInfo</CmdType>
<SN>2</SN>
<DeviceID>34020000001110000001</DeviceID>
<DeviceName>Front NVR</DeviceName>
<Manufacturer>Hik</Manufacturer>
<Model>N9000</Model>
<Firmware>V1.0</Firmware>
<Result>OK</Result>
</Response>`), &resp))
	require.Equal(t, "Front NVR", resp.DeviceName)
	require.Equal(t, "Hik", resp.Manufacturer)
	require.Equal(t, "N9000", resp.Model)
	require.Equal(t, "V1.0", resp.Firmware)
	require.Equal(t, "OK", resp.Result)
}

func TestSnapshotAndControlXMLBuilders(t *testing.T) {
	snap := SnapshotRequest{CmdType: "DeviceControl", SN: 1, DeviceID: "ch1"}
	snap.Info.ControlPriority = 5
	b, err := xml.Marshal(snap)
	require.NoError(t, err)
	require.Contains(t, string(b), "<Control>")
	require.Contains(t, string(b), "<CmdType>DeviceControl</CmdType>")
	require.Contains(t, string(b), "<DeviceID>ch1</DeviceID>")
	require.Contains(t, string(b), "<ControlPriority>5</ControlPriority>")

	reset := DeviceResetRequest{CmdType: "DeviceControl", SN: 2, DeviceID: "ch1"}
	reset.Info.ControlPriority = 5
	b, err = xml.Marshal(reset)
	require.NoError(t, err)
	require.Contains(t, string(b), "<DeviceID>ch1</DeviceID>")

	rec := RecordControlRequest{CmdType: "DeviceControl", SN: 3, DeviceID: "ch1", RecordCmd: "record"}
	rec.Info.ControlPriority = 5
	b, err = xml.Marshal(rec)
	require.NoError(t, err)
	require.Contains(t, string(b), "<RecordCmd>record</RecordCmd>")

	home := HomePositionRequest{CmdType: "DeviceControl", SN: 4, DeviceID: "ch1"}
	home.HomePosition.Enabled = 1
	home.HomePosition.ResetTime = 30
	home.HomePosition.PresetIndex = 2
	b, err = xml.Marshal(home)
	require.NoError(t, err)
	require.Contains(t, string(b), "<Enabled>1</Enabled>")
	require.Contains(t, string(b), "<ResetTime>30</ResetTime>")
	require.Contains(t, string(b), "<PresetIndex>2</PresetIndex>")

	ptz := PTZControlRequest{
		CmdType:  "DeviceControl",
		SN:       5,
		DeviceID: "ch1",
		PTZCmd:   "A50F010800000000",
		Info:     PTZControlInfo{ControlPriority: 5},
	}
	b, err = xml.Marshal(ptz)
	require.NoError(t, err)
	require.Contains(t, string(b), "<PTZCmd>A50F010800000000</PTZCmd>")

	pos := PTZPositionQueryRequest{CmdType: "PTZPosition", SN: 6, DeviceID: "ch1"}
	b, err = xml.Marshal(pos)
	require.NoError(t, err)
	require.Contains(t, string(b), "<CmdType>PTZPosition</CmdType>")
}

func TestRecordInfoXMLBuilder(t *testing.T) {
	start := time.Date(2024, 6, 1, 8, 0, 0, 0, time.Local)
	end := time.Date(2024, 6, 1, 18, 0, 0, 0, time.Local)
	body := recordInfoXML("34020000001320000001", 123456, start, end)
	var msg struct {
		XMLName   xml.Name `xml:"Query"`
		CmdType   string   `xml:"CmdType"`
		SN        int      `xml:"SN"`
		DeviceID  string   `xml:"DeviceID"`
		StartTime string   `xml:"StartTime"`
		EndTime   string   `xml:"EndTime"`
		Secrecy   int      `xml:"Secrecy"`
		Type      string   `xml:"Type"`
	}
	require.NoError(t, xmlUnmarshal(body, &msg))
	require.Equal(t, "RecordInfo", msg.CmdType)
	require.Equal(t, 123456, msg.SN)
	require.Equal(t, "34020000001320000001", msg.DeviceID)
	require.Equal(t, "2024-06-01T08:00:00", msg.StartTime)
	require.Equal(t, "2024-06-01T18:00:00", msg.EndTime)
	require.Equal(t, 0, msg.Secrecy)
	require.Equal(t, "time", msg.Type)
}

func TestAlarmSubscribeXMLAndItoa(t *testing.T) {
	body := alarmSubscribeXML("34020000001110000001", "34020000002000000001")
	var msg struct {
		XMLName            xml.Name `xml:"Query"`
		CmdType            string   `xml:"CmdType"`
		SN                 string   `xml:"SN"`
		DeviceID           string   `xml:"DeviceID"`
		StartAlarmPriority string   `xml:"StartAlarmPriority"`
		EndAlarmPriority   string   `xml:"EndAlarmPriority"`
		AlarmMethod        string   `xml:"AlarmMethod"`
	}
	require.NoError(t, xmlUnmarshal(body, &msg))
	require.Equal(t, "Alarm", msg.CmdType)
	require.Equal(t, "34020000001110000001", msg.DeviceID)
	require.Equal(t, "0", msg.StartAlarmPriority)
	require.Len(t, msg.SN, 1)
	require.Equal(t, "9", itoa(9))
	require.Equal(t, "0", itoa(10))
}

type fakeAlarmStore struct {
	rows []*storage.AlarmRow
	err  error
}

func (f *fakeAlarmStore) CreateAlarm(_ context.Context, alarm *storage.AlarmRow) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	cp := *alarm
	f.rows = append(f.rows, &cp)
	return int64(len(f.rows)), nil
}

func (f *fakeAlarmStore) ListAlarms(context.Context, string, int, int) ([]storage.AlarmRow, int, error) {
	return nil, 0, nil
}

func TestHandleAlarmParsesXMLWithoutSIP(t *testing.T) {
	store := &fakeAlarmStore{}
	am := NewAlarmManager(nil, &Config{ID: "34020000002000000001"}, store)
	require.NotNil(t, am)

	am.HandleAlarm("34020000001110000001", []byte("<not-xml"))
	require.Empty(t, store.rows)

	body := []byte(`<?xml version="1.0"?>
<Notify>
<CmdType>Alarm</CmdType>
<SN>9</SN>
<DeviceID>34020000001320000001</DeviceID>
<AlarmType>2</AlarmType>
<AlarmTime>2024-06-01T12:30:00</AlarmTime>
<Priority>3</Priority>
<Method>5</Method>
<Description>motion</Description>
</Notify>`)
	am.HandleAlarm("34020000001110000001", body)
	require.Len(t, store.rows, 1)
	got := store.rows[0]
	require.Equal(t, "34020000001110000001", got.DeviceID)
	require.Equal(t, "34020000001320000001", got.ChannelID)
	require.Equal(t, "2", got.AlarmType)
	require.Equal(t, 3, got.Priority)
	require.Equal(t, "5", got.Method)
	require.Equal(t, "motion", got.Description)
	require.Equal(t, time.Date(2024, 6, 1, 12, 30, 0, 0, time.Local), got.AlarmTime)

	store.err = errors.New("db down")
	am.HandleAlarm("34020000001110000001", body)
}

func TestSubscribeAlarmAndQueryRecordInfoWithoutLiveDevice(t *testing.T) {
	store := NewDeviceStore(nil, nil)
	am := NewAlarmManager(nil, &Config{}, nil)
	require.ErrorIs(t, am.SubscribeAlarm("missing", store), ErrDeviceOffline)

	api := &GB28181API{store: store}
	_, err := api.QueryRecordInfo("missing", "ch", time.Now(), time.Now())
	require.ErrorIs(t, err, ErrDeviceOffline)

	dev := &Device{DeviceID: "34020000001110000001", IsOnline: true}
	store.Store(dev.DeviceID, dev)
	_, err = api.QueryRecordInfo(dev.DeviceID, "missing-ch", time.Now(), time.Now())
	require.ErrorIs(t, err, ErrChannelNotExist)
}

func TestHandleDeviceInfoResponseInvalidXMLIsOfflineSafe(t *testing.T) {
	api := &GB28181API{store: NewDeviceStore(nil, nil)}
	api.handleDeviceInfoResponse("34020000001110000001", []byte("<bad"))
}
