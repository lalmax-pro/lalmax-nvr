package gb28181

import (
	"errors"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/stretchr/testify/require"
)

func storePlayChannel(t *testing.T, api *GB28181API, deviceID, channelID, transport string, online bool) *Device {
	t.Helper()
	dev := &Device{
		DeviceID:  deviceID,
		IsOnline:  online,
		Transport: transport,
		Address:   "192.0.2.20:5060",
	}
	ch := &Channel{ChannelID: channelID, device: dev}
	dev.Channels.Store(channelID, ch)
	api.store.Store(deviceID, dev)
	return dev
}

func TestPlayAndPlaybackRejectMissingOfflineUnknownTransport(t *testing.T) {
	api := &GB28181API{store: NewDeviceStore(nil, nil), streams: &streamsManager{}, cfg: &Config{MediaIP: "192.0.2.10"}}
	in := &PlayInput{DeviceID: "missing", ChannelID: "ch"}
	_, err := api.Play(in)
	require.ErrorIs(t, err, ErrChannelNotExist)
	_, err = api.Playback(&PlaybackInput{DeviceID: "missing", ChannelID: "ch"})
	require.ErrorIs(t, err, ErrChannelNotExist)

	storePlayChannel(t, api, "34020000001110000001", "ch1", "UDP", false)
	_, err = api.Play(&PlayInput{DeviceID: "34020000001110000001", ChannelID: "ch1"})
	require.ErrorIs(t, err, ErrDeviceOffline)
	_, err = api.Playback(&PlaybackInput{DeviceID: "34020000001110000001", ChannelID: "ch1"})
	require.ErrorIs(t, err, ErrDeviceOffline)

	storePlayChannel(t, api, "34020000001110000002", "ch2", "", true)
	_, err = api.Play(&PlayInput{DeviceID: "34020000001110000002", ChannelID: "ch2"})
	require.ErrorContains(t, err, "SIP transport unknown")
}

func TestEnsurePlayReusesExistingWithoutSIP(t *testing.T) {
	api := &GB28181API{
		store:   NewDeviceStore(nil, nil),
		streams: &streamsManager{},
		cfg:     &Config{MediaIP: "192.0.2.10"},
	}
	devID := "34020000001110000003"
	chID := "ch3"
	storePlayChannel(t, api, devID, chID, "UDP", true)
	key := playStreamKey(devID, chID)
	existing := &Streams{DeviceID: devID, ChannelID: chID, SSRC: "ssrc-keep", SessionID: "sess", StartedAt: time.Now()}
	api.streams.storeStream(key, existing)
	t.Cleanup(func() { api.streams.deleteStream(key) })

	in := &PlayInput{DeviceID: devID, ChannelID: chID, InternalID: StreamID(devID, chID)}
	_, err := api.EnsurePlay(in)
	require.ErrorContains(t, err, "media engine unavailable")

	api.mediaEngine = &stubMediaEngine{getErr: errors.New("media down")}
	_, err = api.EnsurePlay(in)
	require.ErrorContains(t, err, "query media status")

	api.mediaEngine = &stubMediaEngine{getStream: &media.StreamInfo{
		Active:    true,
		Publisher: &media.SessionInfo{SessionID: "pub"},
		InFPS:     12,
	}}
	ssrc, err := api.EnsurePlay(in)
	require.NoError(t, err)
	require.Equal(t, "ssrc-keep", ssrc)

	api.mediaEngine = &stubMediaEngine{getStream: &media.StreamInfo{
		Active:        true,
		Publisher:     &media.SessionInfo{SessionID: "pub"},
		LastFrameTime: time.Now().Add(-2 * time.Second),
	}}
	ssrc, err = api.EnsurePlay(in)
	require.NoError(t, err)
	require.Equal(t, "ssrc-keep", ssrc)

	api.mediaEngine = &stubMediaEngine{getStream: &media.StreamInfo{Active: false}}
	ssrc, err = api.EnsurePlay(in)
	require.NoError(t, err)
	require.Equal(t, "ssrc-keep", ssrc)

	existing.StartedAt = time.Now().Add(-time.Minute)
	api.recoveryAttempts.Store(key, time.Now())
	ssrc, err = api.EnsurePlay(in)
	require.NoError(t, err)
	require.Equal(t, "ssrc-keep", ssrc)
}

func TestPlayControlAndStopWithoutSIP(t *testing.T) {
	api := &GB28181API{store: NewDeviceStore(nil, nil), streams: &streamsManager{}, cfg: &Config{ID: "34020000002000000001", Host: "192.0.2.10"}}
	speed := &PlaySpeedInput{DeviceID: "d", ChannelID: "c", Speed: 2}
	require.ErrorContains(t, api.PlaySpeed(speed), "no active playback")
	require.ErrorContains(t, api.PlaySeek(&PlaySeekInput{DeviceID: "d", ChannelID: "c"}), "no active playback")
	require.ErrorContains(t, api.PlayPause(&PlayPauseInput{DeviceID: "d", ChannelID: "c"}), "no active playback")
	require.ErrorContains(t, api.PlayResume(&PlayPauseInput{DeviceID: "d", ChannelID: "c"}), "no active playback")
	require.NoError(t, api.StopPlay(&StopPlayInput{DeviceID: "d", ChannelID: "c"}))

	key := "play:d:c"
	api.streams.storeStream(key, &Streams{DeviceID: "d", ChannelID: "c"})
	t.Cleanup(func() { api.streams.deleteStream(key) })
	require.ErrorContains(t, api.PlaySpeed(speed), "missing dialog info")
	require.ErrorContains(t, api.PlaySeek(&PlaySeekInput{DeviceID: "d", ChannelID: "c"}), "missing dialog info")
	require.ErrorContains(t, api.PlayPause(&PlayPauseInput{DeviceID: "d", ChannelID: "c"}), "missing dialog info")
	require.ErrorContains(t, api.PlayResume(&PlayPauseInput{DeviceID: "d", ChannelID: "c"}), "missing dialog info")
}

func TestStopPlayRTPReceiverAndSendPlayByeGuards(t *testing.T) {
	api := &GB28181API{store: NewDeviceStore(nil, nil), streams: &streamsManager{}}
	require.NoError(t, api.stopPlayRTPReceiver(&Streams{}))
	require.ErrorContains(t, api.sendPlayBye(&Streams{DeviceID: "d", ChannelID: "c"}), "missing confirmed SIP dialog")
	require.ErrorContains(t, api.sendPlayBye(&Streams{
		DeviceID: "missing", ChannelID: "c", CallID: "cid", FromTag: "f", ToTag: "t",
	}), "device unavailable")

	engine := &stubMediaEngine{stopRTPErr: errors.New("rtp stop failed")}
	api.mediaEngine = engine
	err := api.stopPlayRTPReceiver(&Streams{SessionID: "sess", DeviceID: "d"})
	require.ErrorContains(t, err, "stop RTP receiver")
}

func TestPlayStartRTPReceiveErrorPaths(t *testing.T) {
	api := &GB28181API{
		store:   NewDeviceStore(nil, nil),
		streams: &streamsManager{},
		cfg:     &Config{MediaIP: "192.0.2.10", MediaPort: 10000},
	}
	storePlayChannel(t, api, "34020000001110000004", "ch4", "TCP", true)

	engine := &stubMediaEngine{startRTPErr: errors.New("rtp down")}
	api.mediaEngine = engine
	_, err := api.Play(&PlayInput{DeviceID: "34020000001110000004", ChannelID: "ch4", StreamMode: 1})
	require.ErrorContains(t, err, "open RTP server failed")
	require.Equal(t, "tcp", engine.lastRTPReq.Protocol)
	require.Equal(t, 10000, engine.lastRTPReq.Port)

	engine = &stubMediaEngine{
		startRTPErr:  errors.New("already exists"),
		startRTPErr2: errors.New("still exists"),
	}
	api.mediaEngine = engine
	_, err = api.Play(&PlayInput{DeviceID: "34020000001110000004", ChannelID: "ch4"})
	require.ErrorContains(t, err, "open RTP server failed")
	require.Equal(t, 2, engine.calls)

	engine = &stubMediaEngine{
		startRTPErr:  errors.New("code -300 busy"),
		startRTPErr2: errors.New("still -300"),
	}
	api.mediaEngine = engine
	_, err = api.Playback(&PlaybackInput{
		DeviceID: "34020000001110000004", ChannelID: "ch4", StreamMode: 2, InternalID: "pb",
	})
	require.ErrorContains(t, err, "open RTP server failed")
	require.Equal(t, "tcp", engine.lastRTPReq.Protocol)
	require.Equal(t, 2, engine.calls)
}

func TestPlayStopsExistingIncompleteDialogWithoutSIPClient(t *testing.T) {
	api := &GB28181API{
		store:   NewDeviceStore(nil, nil),
		streams: &streamsManager{},
		cfg:     &Config{MediaIP: "192.0.2.10"},
	}
	storePlayChannel(t, api, "34020000001110000005", "ch5", "UDP", true)
	key := playStreamKey("34020000001110000005", "ch5")
	api.streams.storeStream(key, &Streams{DeviceID: "34020000001110000005", ChannelID: "ch5", SSRC: "old"})
	t.Cleanup(func() { api.streams.deleteStream(key) })
	_, err := api.Play(&PlayInput{DeviceID: "34020000001110000005", ChannelID: "ch5"})
	require.ErrorContains(t, err, "missing confirmed SIP dialog")
}
