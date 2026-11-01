package gb28181

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestStartDownloadRejectsMissingAndOfflineChannels(t *testing.T) {
	dm := NewDownloadManager(nil, &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"}, nil, newFakeDownloadStore(), t.TempDir())
	store := NewDeviceStore(nil, nil)
	start := time.Unix(100, 0)
	end := time.Unix(200, 0)

	_, err := dm.StartDownload("missing", "ch", start, end, store)
	require.ErrorIs(t, err, ErrChannelNotExist)

	dev := &Device{DeviceID: "34020000001110000001", IsOnline: false}
	ch := &Channel{ChannelID: "ch1", device: dev}
	dev.Channels.Store(ch.ChannelID, ch)
	store.Store(dev.DeviceID, dev)

	_, err = dm.StartDownload(dev.DeviceID, ch.ChannelID, start, end, store)
	require.ErrorIs(t, err, ErrDeviceOffline)
}

func TestStartDownloadDirAndStoreAndRTPErrorPaths(t *testing.T) {
	store := NewDeviceStore(nil, nil)
	dev := &Device{DeviceID: "34020000001110000001", IsOnline: true, Address: "192.0.2.20:5060"}
	ch := &Channel{ChannelID: "34020000001320000001", device: dev}
	dev.Channels.Store(ch.ChannelID, ch)
	store.Store(dev.DeviceID, dev)
	start := time.Unix(100, 0)
	end := time.Unix(200, 0)

	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(blocked, []byte("x"), 0o644))
	dm := NewDownloadManager(nil, &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"}, nil, newFakeDownloadStore(), blocked)
	_, err := dm.StartDownload(dev.DeviceID, ch.ChannelID, start, end, store)
	require.ErrorContains(t, err, "create download dir failed")

	dlStore := newFakeDownloadStore()
	dlStore.createErr = errors.New("db down")
	dm = NewDownloadManager(nil, &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"}, nil, dlStore, t.TempDir())
	_, err = dm.StartDownload(dev.DeviceID, ch.ChannelID, start, end, store)
	require.ErrorContains(t, err, "create download record failed")

	engine := &stubMediaEngine{startRTPErr: errors.New("rtp bind failed")}
	dlStore = newFakeDownloadStore()
	dm = NewDownloadManager(nil, &Config{ID: "34020000002000000001", MediaIP: "192.0.2.10"}, engine, dlStore, t.TempDir())
	_, err = dm.StartDownload(dev.DeviceID, ch.ChannelID, start, end, store)
	require.ErrorContains(t, err, "open RTP server failed")
	require.Equal(t, []string{"failed"}, dlStore.updates)
	require.Equal(t, "udp", engine.lastRTPReq.Protocol)
}

func TestStopDownloadAndGetProgress(t *testing.T) {
	dlStore := newFakeDownloadStore()
	engine := &stubMediaEngine{}
	dm := NewDownloadManager(nil, &Config{}, engine, dlStore, t.TempDir())
	require.NoError(t, dm.StopDownload("d", "c", 99))

	id, err := dlStore.CreateDownload(nil, &storage.DownloadRecordRow{
		DeviceID: "d", ChannelID: "c", Status: "downloading",
	})
	require.NoError(t, err)
	dm.sessions["d:c:1"] = &DownloadSession{ID: id, SessionID: "sess-1"}
	require.NoError(t, dm.StopDownload("d", "c", 1))
	require.Equal(t, []string{"sess-1"}, engine.stopped)
	require.Equal(t, []string{"completed"}, dlStore.updates)
	_, ok := dm.sessions["d:c:1"]
	require.False(t, ok)

	got, err := dm.GetDownloadProgress(id)
	require.NoError(t, err)
	require.Equal(t, "completed", got.Status)

	dlStore.getErr = errors.New("missing")
	_, err = dm.GetDownloadProgress(id)
	require.ErrorContains(t, err, "missing")
}

func TestBuildDownloadSDPStreamMode2(t *testing.T) {
	start := time.Unix(10, 0)
	end := time.Unix(20, 0)
	tcp := string(buildDownloadSDP("dev", "ch", "192.0.2.8", 20002, 2, "0100000101", start, end))
	require.Contains(t, tcp, "m=video 20002 TCP/RTP/AVP 96")
	require.Contains(t, tcp, "s=Download")
	require.Contains(t, tcp, "t=10 20")
}
