package gb28181

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseTalkSDPEdgeCases(t *testing.T) {
	peerIP, peerPort, isTCP, setupActive, ssrc, payloadType, err := parseTalkSDP("v=0\n\nc=IN IP4 10.0.0.2\nm=audio 9000 RTP/AVP/TCP 0\na=setup:passive\ny=42\n")
	require.NoError(t, err)
	require.Equal(t, "10.0.0.2", peerIP)
	require.Equal(t, 9000, peerPort)
	require.True(t, isTCP)
	require.False(t, setupActive)
	require.Equal(t, "42", ssrc)
	require.Equal(t, 0, payloadType)

	_, _, _, _, _, _, err = parseTalkSDP("m=audio bad RTP/AVP 8")
	require.ErrorContains(t, err, "invalid port")

	_, _, _, _, _, _, err = parseTalkSDP("m=audio 8000 RTP/AVP xx")
	require.ErrorContains(t, err, "invalid payload type")

	peerIP, peerPort, isTCP, setupActive, ssrc, payloadType, err = parseTalkSDP("v=0")
	require.NoError(t, err)
	require.Empty(t, peerIP)
	require.Equal(t, 0, peerPort)
	require.False(t, isTCP)
	require.False(t, setupActive)
	require.Empty(t, ssrc)
	require.Equal(t, 8, payloadType)
}

func TestBuildPlaySDPVariants(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	end := time.Unix(1_700_003_600, 0)

	udp := string(buildPlaySDP("dev", "ch", "192.0.2.1", 10000, 0, "0100000001", false, time.Time{}, time.Time{}, 0))
	require.Contains(t, udp, "s=Play")
	require.Contains(t, udp, "t=0 0")
	require.Contains(t, udp, "m=video 10000 RTP/AVP 96")
	require.Contains(t, udp, "y=0100000001")
	require.Contains(t, udp, "f=")
	require.NotContains(t, udp, "a=setup:")

	passive := string(buildPlaySDP("dev", "ch", "192.0.2.1", 10001, 1, "0100000002", true, start, end, 1))
	require.Contains(t, passive, "s=Playback")
	require.Contains(t, passive, fmt.Sprintf("t=%d %d", start.Unix(), end.Unix()))
	require.Contains(t, passive, "m=video 10001 TCP/RTP/AVP 96")
	require.Contains(t, passive, "a=setup:passive")
	require.Contains(t, passive, "a=connection:new")
	require.Contains(t, passive, "f= v/2/1///a/1///")

	active := string(buildPlaySDP("dev", "ch", "192.0.2.1", 10002, 2, "0100000003", false, time.Time{}, time.Time{}, 0))
	require.Contains(t, active, "a=setup:active")
	require.True(t, strings.HasSuffix(active, "\r\n"))
}

func TestBuildDownloadSDP(t *testing.T) {
	start := time.Unix(100, 0)
	end := time.Unix(200, 0)
	udp := string(buildDownloadSDP("dev", "ch", "192.0.2.8", 20000, 0, "0100000099", start, end))
	require.Contains(t, udp, "s=Download")
	require.Contains(t, udp, "u=ch:0")
	require.Contains(t, udp, "t=100 200")
	require.Contains(t, udp, "m=video 20000 RTP/AVP 96")
	require.Contains(t, udp, "y=0100000099")

	tcp := string(buildDownloadSDP("dev", "ch", "192.0.2.8", 20001, 1, "0100000100", start, end))
	require.Contains(t, tcp, "m=video 20001 TCP/RTP/AVP 96")
}

func TestParseBroadcastSDPUDPAndMalformed(t *testing.T) {
	offer := parseBroadcastSDP("v=0\no=ch1 0 0 IN IP4 10.1.1.1\nc=IN IP4 10.1.1.1\nm=audio 4000 RTP/AVP 96\ny=not-a-number\n")
	require.Equal(t, "ch1", offer.ChannelID)
	require.Equal(t, "10.1.1.1", offer.IP)
	require.Equal(t, 4000, offer.Port)
	require.False(t, offer.IsTCP)
	require.Equal(t, 96, offer.PayloadType)
	require.Equal(t, uint32(0), offer.SSRC)

	empty := parseBroadcastSDP("")
	require.Equal(t, 8, empty.PayloadType)
	require.Equal(t, 0, empty.Port)
}

func TestAggregateRecordList(t *testing.T) {
	empty := aggregateRecordList(nil)
	require.Equal(t, 0, empty.DayTotal)
	require.Empty(t, empty.Data)

	day := time.Date(2024, 6, 1, 0, 0, 0, 0, time.Local)
	a := day.Add(1 * time.Hour).Unix()
	b := day.Add(2 * time.Hour).Unix()
	c := day.Add(90 * time.Minute).Unix()
	d := day.Add(3 * time.Hour).Unix()
	e := day.Add(4 * time.Hour).Unix()
	f := day.Add(5 * time.Hour).Unix()

	merged := aggregateRecordList([][]int64{{c, d}, {a, b}, {e, f}})
	require.Equal(t, 1, merged.DayTotal)
	require.Equal(t, 2, merged.TimeNum)
	require.Len(t, merged.Data, 1)
	require.Equal(t, "2024-06-01", merged.Data[0].Date)
	require.Len(t, merged.Data[0].Items, 2)
	require.Equal(t, a, merged.Data[0].Items[0].Start)
	require.Equal(t, d, merged.Data[0].Items[0].End)
	require.Equal(t, e, merged.Data[0].Items[1].Start)
	require.Equal(t, f, merged.Data[0].Items[1].End)

	nextDay := day.Add(26 * time.Hour).Unix()
	span := aggregateRecordList([][]int64{{day.Add(23 * time.Hour).Unix(), nextDay}})
	require.Equal(t, 2, span.DayTotal)
	require.Equal(t, 2, span.TimeNum)
	require.Equal(t, "2024-06-01", span.Data[0].Date)
	require.Equal(t, "2024-06-02", span.Data[1].Date)
}

func TestHandleRecordInfoResponse(t *testing.T) {
	api := &GB28181API{}
	api.handleRecordInfoResponse("dev", []byte("<bad"))

	start := time.Date(2024, 6, 1, 8, 0, 0, 0, time.Local)
	end := time.Date(2024, 6, 1, 18, 0, 0, 0, time.Local)
	key := "34020000001320000001:88"
	state := &recordListState{
		channelID: "34020000001320000001",
		sn:        88,
		resp:      make(chan Records, 1),
		data:      make([][]int64, 0),
		start:     start.Unix(),
		end:       end.Unix(),
	}
	recordListStore.Store(key, state)
	t.Cleanup(func() { recordListStore.Delete(key) })

	body := []byte(`<?xml version="1.0"?>
<Response>
<CmdType>RecordInfo</CmdType>
<SN>88</SN>
<DeviceID>34020000001320000001</DeviceID>
<SumNum>2</SumNum>
<RecordList>
<Item>
<DeviceID>34020000001320000001</DeviceID>
<StartTime>2024-06-01T07:00:00</StartTime>
<EndTime>2024-06-01T09:00:00</EndTime>
</Item>
<Item>
<DeviceID>34020000001320000001</DeviceID>
<StartTime>2024-06-01T17:00:00</StartTime>
<EndTime>2024-06-01T19:00:00</EndTime>
</Item>
</RecordList>
</Response>`)
	api.handleRecordInfoResponse("dev", body)
	select {
	case got := <-state.resp:
		require.Equal(t, 1, got.DayTotal)
		require.Equal(t, 2, got.TimeNum)
		require.Equal(t, start.Unix(), got.Data[0].Items[0].Start)
		require.Equal(t, end.Unix(), got.Data[0].Items[1].End)
	case <-time.After(time.Second):
		t.Fatal("expected complete RecordInfo result")
	}

	emptyKey := "34020000001320000001:89"
	emptyState := &recordListState{resp: make(chan Records, 1), data: make([][]int64, 0)}
	recordListStore.Store(emptyKey, emptyState)
	t.Cleanup(func() { recordListStore.Delete(emptyKey) })
	api.handleRecordInfoResponse("dev", []byte(`<?xml version="1.0"?><Response><CmdType>RecordInfo</CmdType><SN>89</SN><DeviceID>34020000001320000001</DeviceID><SumNum>0</SumNum></Response>`))
	select {
	case got := <-emptyState.resp:
		require.Equal(t, Records{}, got)
	case <-time.After(time.Second):
		t.Fatal("expected empty RecordInfo result")
	}

	api.handleRecordInfoResponse("dev", []byte(`<?xml version="1.0"?><Response><CmdType>RecordInfo</CmdType><SN>1</SN><DeviceID>unknown</DeviceID><SumNum>0</SumNum></Response>`))
}

func TestNewDownloadManager(t *testing.T) {
	dm := NewDownloadManager(nil, &Config{}, nil, nil, t.TempDir())
	require.NotNil(t, dm)
	require.NoError(t, dm.StopDownload("d", "c", 1))
}
