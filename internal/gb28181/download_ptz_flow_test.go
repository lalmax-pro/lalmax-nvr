package gb28181

import (
	"errors"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/require"
)

func downloadTestStore(t *testing.T) (*DeviceStore, *Device) {
	t.Helper()
	store := NewDeviceStore(nil, NewWSHub())
	dev := &Device{DeviceID: "34020000001320000001", IsOnline: true, Address: "192.0.2.20:5060"}
	ch := &Channel{ChannelID: "34020000001320000002", device: dev}
	dev.Channels.Store(ch.ChannelID, ch)
	store.Store(dev.DeviceID, dev)
	return store, dev
}

func TestDownloadManagerStartInviteAndStop(t *testing.T) {
	store, dev := downloadTestStore(t)
	repo := newFakeDownloadStore()
	engine := &stubMediaEngine{}
	client, requester := newTestSIPClient(t, func(req *sip.Request) []*sip.Response {
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		res.AppendHeader(sip.NewHeader("Contact", "<sip:34020000001320000002@192.0.2.20:5060>"))
		return []*sip.Response{res}
	})
	dm := NewDownloadManager(client, &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"}, engine, repo, t.TempDir())

	start := time.Now().Add(-time.Hour)
	end := time.Now()
	ds, err := dm.StartDownload(dev.DeviceID, "34020000001320000002", start, end, store)
	require.NoError(t, err)
	require.NotZero(t, ds.ID)
	require.NotEmpty(t, ds.SSRC)
	require.Equal(t, "downloading", ds.Status)
	require.Contains(t, ds.FilePath, "34020000001320000002_")
	var invites int
	var invite *sip.Request
	for _, req := range requester.Requests() {
		if req.Method == sip.INVITE {
			invites++
			invite = req
		}
	}
	require.Equal(t, 1, invites)
	require.Contains(t, string(invite.Body()), "s=Download")

	row, err := dm.GetDownloadProgress(ds.ID)
	require.NoError(t, err)
	require.Equal(t, "downloading", row.Status)

	require.NoError(t, dm.StopDownload(dev.DeviceID, "34020000001320000002", ds.ID))
	require.Contains(t, repo.updates, "completed")
	require.Contains(t, engine.stopped, "sess")
	// Stopping an unknown download is a no-op.
	require.NoError(t, dm.StopDownload(dev.DeviceID, "34020000001320000002", 999))
}

func TestDownloadManagerInviteRejectionCleansUp(t *testing.T) {
	store, dev := downloadTestStore(t)
	for name, handler := range map[string]func(req *sip.Request) []*sip.Response{
		"rejected": func(req *sip.Request) []*sip.Response {
			return []*sip.Response{sip.NewResponseFromRequest(req, 403, "Forbidden", nil)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := newFakeDownloadStore()
			engine := &stubMediaEngine{}
			client, _ := newTestSIPClient(t, handler)
			dm := NewDownloadManager(client, &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"}, engine, repo, t.TempDir())
			_, err := dm.StartDownload(dev.DeviceID, "34020000001320000002", time.Now().Add(-time.Hour), time.Now(), store)
			require.ErrorContains(t, err, "download INVITE failed")
			require.Contains(t, repo.updates, "failed")
			require.Contains(t, engine.stopped, "sess")
		})
	}

	t.Run("transport failure", func(t *testing.T) {
		repo := newFakeDownloadStore()
		engine := &stubMediaEngine{}
		client, _ := newTestSIPClientFailing(t, errors.New("device unreachable"))
		dm := NewDownloadManager(client, &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"}, engine, repo, t.TempDir())
		_, err := dm.StartDownload(dev.DeviceID, "34020000001320000002", time.Now().Add(-time.Hour), time.Now(), store)
		require.ErrorContains(t, err, "INVITE send failed")
		require.Contains(t, repo.updates, "failed")
	})
}

func TestPTZBuildersClampOutOfRangeArguments(t *testing.T) {
	cases := []struct {
		name string
		fn   func(v int) string
	}{
		{"set preset", BuildSetPresetCmd},
		{"call preset", BuildCallPresetCmd},
		{"delete preset", BuildDeletePresetCmd},
		{"cruise start", BuildCruiseStartCmd},
		{"cruise stop", BuildCruiseStopCmd},
		{"scan left", BuildScanSetLeftCmd},
		{"scan right", BuildScanSetRightCmd},
		{"scan start", BuildScanStartCmd},
		{"scan stop", BuildScanStopCmd},
	}
	for _, tc := range cases {
		for _, value := range []int{-7, 0, 300} {
			cmd := tc.fn(value)
			require.Len(t, cmd, 16, "%s(%d)", tc.name, value)
			require.Equal(t, "a5", cmd[:2], "%s(%d)", tc.name, value)
		}
	}

	pairs := []struct {
		name string
		fn   func(a, b int) string
	}{
		{"cruise add point", BuildCruiseAddPointCmd},
		{"cruise delete point", BuildCruiseDeletePointCmd},
		{"cruise speed", BuildCruiseSpeedCmd},
		{"scan speed", BuildScanSetSpeedCmd},
	}
	for _, tc := range pairs {
		for _, values := range [][2]int{{-5, -5}, {0, 0}, {999, 999}} {
			cmd := tc.fn(values[0], values[1])
			require.Len(t, cmd, 16, "%s(%v)", tc.name, values)
			require.Equal(t, "a5", cmd[:2], "%s(%v)", tc.name, values)
		}
	}

	require.Len(t, BuildSetPositionCmd(-30, -10, -5), 16)
	require.Len(t, BuildSetPositionCmd(720, 180, 300), 16)
}
