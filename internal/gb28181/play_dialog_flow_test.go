package gb28181

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/require"
)

// playDialogClient answers INVITE with a confirmed dialog (Contact + To tag)
// and accepts the follow-up ACK, INFO and BYE exchanges in-process.
func playDialogClient(t *testing.T) (*GB28181API, *fakeTxRequester) {
	t.Helper()
	client, requester := newTestSIPClient(t, func(req *sip.Request) []*sip.Response {
		switch req.Method {
		case sip.INVITE:
			res := sip.NewResponseFromRequest(req, 200, "OK", []byte("v=0\r\n"))
			res.AppendHeader(sip.NewHeader("Contact", "<sip:34020000001320000001@127.0.0.1:5060>"))
			return []*sip.Response{sip.NewResponseFromRequest(req, 100, "Trying", nil), res}
		default:
			return []*sip.Response{sip.NewResponseFromRequest(req, 200, "OK", nil)}
		}
	})
	store := NewDeviceStore(nil, nil)
	dev := &Device{
		DeviceID:  "34020000001320000001",
		IsOnline:  true,
		Transport: "UDP",
		Address:   "192.0.2.20:5060",
	}
	ch := &Channel{ChannelID: "34020000001320000002", device: dev}
	dev.Channels.Store(ch.ChannelID, ch)
	store.Store(dev.DeviceID, dev)

	api := &GB28181API{
		cfg: &Config{
			ID:      "34020000002000000001",
			Host:    "192.0.2.10",
			MediaIP: "192.0.2.10",
		},
		store:       store,
		streams:     &streamsManager{},
		client:      client,
		mediaEngine: &stubMediaEngine{},
	}
	return api, requester
}

func TestPlayFullDialogLifecycle(t *testing.T) {
	api, requester := playDialogClient(t)
	engine := api.mediaEngine.(*stubMediaEngine)

	ssrc, err := api.Play(&PlayInput{
		DeviceID: "34020000001320000001", ChannelID: "34020000001320000002",
	})
	require.NoError(t, err)
	require.NotEmpty(t, ssrc)
	require.Equal(t, "udp", engine.lastRTPReq.Protocol)
	require.Equal(t, "rtp", engine.lastRTPReq.AppName)
	require.True(t, api.streams.isPlaying("34020000001320000001", "34020000001320000002"))

	stream, ok := api.streams.loadStream(playStreamKey("34020000001320000001", "34020000001320000002"))
	require.True(t, ok)
	require.NotEmpty(t, stream.CallID)
	require.NotEmpty(t, stream.FromTag)
	require.NotEmpty(t, stream.ToTag)
	require.NotEmpty(t, stream.RemoteURI)
	require.NotZero(t, stream.InviteCSeq)
	require.Equal(t, "UDP", stream.Transport)
	require.Equal(t, "sess", stream.SessionID)

	require.NoError(t, api.PlaySpeed(&PlaySpeedInput{DeviceID: stream.DeviceID, ChannelID: stream.ChannelID, Speed: 2}))
	require.NoError(t, api.PlaySeek(&PlaySeekInput{DeviceID: stream.DeviceID, ChannelID: stream.ChannelID, SeekTime: 30}))
	require.NoError(t, api.PlayPause(&PlayPauseInput{DeviceID: stream.DeviceID, ChannelID: stream.ChannelID}))
	require.NoError(t, api.PlayResume(&PlayPauseInput{DeviceID: stream.DeviceID, ChannelID: stream.ChannelID}))

	var infoBodies []string
	for _, req := range requester.Requests() {
		if req.Method == sip.INFO {
			infoBodies = append(infoBodies, string(req.Body()))
			require.NotNil(t, req.GetHeader("Content-Type"))
			require.Equal(t, "Application/MANSRTSP", req.GetHeader("Content-Type").Value())
		}
	}
	require.Len(t, infoBodies, 4)
	require.Contains(t, infoBodies[0], "Scale: 2.000000")
	require.Contains(t, infoBodies[1], "Range: npt=30.000-")
	require.Contains(t, infoBodies[2], "PAUSE RTSP/1.0")
	require.Contains(t, infoBodies[3], "Range: npt=now-")

	require.NoError(t, api.StopPlay(&StopPlayInput{DeviceID: stream.DeviceID, ChannelID: stream.ChannelID}))
	require.False(t, api.streams.isPlaying(stream.DeviceID, stream.ChannelID))
	require.Contains(t, engine.stopped, "sess")
	require.NoError(t, api.StopPlay(&StopPlayInput{DeviceID: stream.DeviceID, ChannelID: stream.ChannelID}))

	var sawBye bool
	for _, req := range requester.Requests() {
		if req.Method == sip.BYE {
			sawBye = true
			require.Equal(t, stream.InviteCSeq+1, req.CSeq().SeqNo)
		}
	}
	require.True(t, sawBye, "StopPlay must terminate the SIP dialog")
}

func TestPlaybackAndPlaybackControlDialog(t *testing.T) {
	api, requester := playDialogClient(t)
	now := time.Now()
	ssrc, err := api.Playback(&PlaybackInput{
		DeviceID:  "34020000001320000001",
		ChannelID: "34020000001320000002",
		StartTime: now.Add(-time.Hour),
		EndTime:   now,
	})
	require.NoError(t, err)
	require.NotEmpty(t, ssrc)

	var invite *sip.Request
	for _, req := range requester.Requests() {
		if req.Method == sip.INVITE {
			invite = req
		}
	}
	require.NotNil(t, invite)
	require.Contains(t, string(invite.Body()), "s=Playback")
	require.Contains(t, string(invite.Body()), "t=")

	require.NoError(t, api.PlaySpeed(&PlaySpeedInput{DeviceID: "34020000001320000001", ChannelID: "34020000001320000002", Speed: 4}))
	require.NoError(t, api.StopPlay(&StopPlayInput{DeviceID: "34020000001320000001", ChannelID: "34020000001320000002"}))
}

func TestPlayTCPModeAndSinglePortRequest(t *testing.T) {
	api, _ := playDialogClient(t)
	api.cfg.MediaPort = 10000
	engine := api.mediaEngine.(*stubMediaEngine)

	ssrc, err := api.Play(&PlayInput{
		DeviceID: "34020000001320000001", ChannelID: "34020000001320000002",
		StreamMode: 1, StreamNumber: 1,
	})
	require.NoError(t, err)
	require.NotEmpty(t, ssrc)
	require.Equal(t, "tcp", engine.lastRTPReq.Protocol)
	require.Equal(t, 10000, engine.lastRTPReq.Port)

	stream, _ := api.streams.loadStream(playStreamKey("34020000001320000001", "34020000001320000002"))
	require.NoError(t, api.StopPlay(&StopPlayInput{DeviceID: stream.DeviceID, ChannelID: stream.ChannelID}))
}

func TestPlayEnsurePlayRestartsStaleSession(t *testing.T) {
	api, _ := playDialogClient(t)
	key := playStreamKey("34020000001320000001", "34020000001320000002")
	// A dialog that is older than the publish grace period restarts.
	api.streams.storeStream(key, &Streams{
		DeviceID:   "34020000001320000001",
		ChannelID:  "34020000001320000002",
		SSRC:       "old-ssrc",
		SessionID:  "old-session",
		CallID:     "old-call",
		FromTag:    "from-tag",
		ToTag:      "to-tag",
		InviteCSeq: 1,
		Transport:  "UDP",
		StartedAt:  time.Now().Add(-2 * time.Minute),
	})
	t.Cleanup(func() { api.streams.deleteStream(key) })

	ssrc, err := api.EnsurePlay(&PlayInput{DeviceID: "34020000001320000001", ChannelID: "34020000001320000002"})
	require.NoError(t, err)
	require.NotEmpty(t, ssrc)
	require.NotEqual(t, "old-ssrc", ssrc)
	engine := api.mediaEngine.(*stubMediaEngine)
	require.Contains(t, engine.stopped, "old-session")
	_, attempted := api.recoveryAttempts.Load(key)
	require.True(t, attempted)
	require.NoError(t, api.StopPlay(&StopPlayInput{DeviceID: "34020000001320000001", ChannelID: "34020000001320000002"}))
}

func TestPlayInviteRejectedAndSendFailure(t *testing.T) {
	t.Run("rejected", func(t *testing.T) {
		api, _ := playDialogClient(t)
		client, _ := newTestSIPClient(t, func(req *sip.Request) []*sip.Response {
			return []*sip.Response{sip.NewResponseFromRequest(req, 403, "Forbidden", nil)}
		})
		api.client = client
		_, err := api.Play(&PlayInput{DeviceID: "34020000001320000001", ChannelID: "34020000001320000002"})
		require.ErrorContains(t, err, "INVITE rejected: 403")
		require.False(t, api.streams.isPlaying("34020000001320000001", "34020000001320000002"))
		engine := api.mediaEngine.(*stubMediaEngine)
		require.Contains(t, engine.stopped, "sess")
	})

	t.Run("send failure", func(t *testing.T) {
		api, _ := playDialogClient(t)
		client, _ := newTestSIPClientFailing(t, errors.New("no route to device"))
		api.client = client
		_, err := api.Play(&PlayInput{DeviceID: "34020000001320000001", ChannelID: "34020000001320000002"})
		require.ErrorContains(t, err, "INVITE send failed")
	})

	t.Run("playback rejected", func(t *testing.T) {
		api, _ := playDialogClient(t)
		client, _ := newTestSIPClient(t, func(req *sip.Request) []*sip.Response {
			return []*sip.Response{sip.NewResponseFromRequest(req, 486, "Busy Here", nil)}
		})
		api.client = client
		_, err := api.Playback(&PlaybackInput{DeviceID: "34020000001320000001", ChannelID: "34020000001320000002"})
		require.ErrorContains(t, err, "INVITE rejected: 486")
	})
}

func TestSendSIPInfoFailureModes(t *testing.T) {
	api, _ := playDialogClient(t)
	stream := &Streams{
		DeviceID: "34020000001320000001", ChannelID: "34020000001320000002",
		CallID: "call-id", FromTag: "from", ToTag: "to",
	}

	t.Run("missing device", func(t *testing.T) {
		require.ErrorContains(t, api.sendSIPInfo(&Streams{DeviceID: "missing"}, "body", "Application", "MANSRTSP"), "device not found")
	})

	t.Run("rejected", func(t *testing.T) {
		client, _ := newTestSIPClient(t, func(req *sip.Request) []*sip.Response {
			return []*sip.Response{sip.NewResponseFromRequest(req, 403, "Forbidden", nil)}
		})
		api.client = client
		err := api.sendSIPInfo(stream, "body", "Application", "MANSRTSP")
		require.ErrorContains(t, err, "SIP INFO rejected: 403")
	})

	t.Run("transport failure", func(t *testing.T) {
		client, _ := newTestSIPClientFailing(t, errors.New("device gone"))
		api.client = client
		err := api.sendSIPInfo(stream, "body", "Application", "MANSRTSP")
		require.ErrorContains(t, err, "SIP INFO failed")
	})
}

func TestSendPlayByeStatusHandling(t *testing.T) {
	api, requester := playDialogClient(t)
	originalClient := api.client
	stream := &Streams{
		DeviceID: "34020000001320000001", ChannelID: "34020000001320000002",
		CallID: "call-id", FromTag: "from", ToTag: "to", InviteCSeq: 5,
		SessionID: "sess", Transport: "TCP", RemoteURI: "sip:34020000001320000002@192.0.2.20:5060",
	}

	require.NoError(t, api.sendPlayBye(stream))
	byes := 0
	for _, req := range requester.Requests() {
		if req.Method == sip.BYE {
			byes++
			require.Equal(t, "TCP", req.Transport())
			require.Equal(t, "sip:34020000001320000002@192.0.2.20:5060", req.Recipient.String())
		}
	}
	require.Equal(t, 1, byes)

	// 481 means the remote dialog is already gone and cleanup may continue.
	client481, _ := newTestSIPClient(t, func(req *sip.Request) []*sip.Response {
		return []*sip.Response{sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil)}
	})
	api.client = client481
	require.NoError(t, api.sendPlayBye(stream))

	// A real rejection must be surfaced so the session is retained.
	client500, _ := newTestSIPClient(t, func(req *sip.Request) []*sip.Response {
		return []*sip.Response{sip.NewResponseFromRequest(req, 500, "Server Error", nil)}
	})
	api.client = client500
	require.ErrorContains(t, api.sendPlayBye(stream), "SIP BYE rejected")

	clientFail, _ := newTestSIPClientFailing(t, errors.New("network down"))
	api.client = clientFail
	require.ErrorContains(t, api.sendPlayBye(stream), "SIP BYE")

	// An unparsable RemoteURI falls back to the device address.
	api.client = originalClient
	noRemote := &Streams{
		DeviceID: "34020000001320000001", ChannelID: "34020000001320000002",
		CallID: "call-id", FromTag: "from", ToTag: "to", RemoteURI: "::not a uri::",
	}
	require.NoError(t, api.sendPlayBye(noRemote))
}

func TestHandlerByeStopsMatchingStream(t *testing.T) {
	api, _ := playDialogClient(t)
	engine := api.mediaEngine.(*stubMediaEngine)
	key := playStreamKey("34020000001320000001", "34020000001320000002")
	api.streams.storeStream(key, &Streams{
		DeviceID: "34020000001320000001", ChannelID: "34020000001320000002", SessionID: "sess-bye",
	})
	t.Cleanup(func() { api.streams.deleteStream(key) })

	req := newTestInboundRequest(sip.BYE, "34020000001320000001", "34020000002000000001", "")
	tx := newFakeServerTx()
	api.handlerBye(req, tx)
	require.Equal(t, []int{200}, tx.StatusCodes())
	require.False(t, api.streams.isPlaying("34020000001320000001", "34020000001320000002"))
	require.Contains(t, engine.stopped, "sess-bye")

	// A BYE for an unknown device still gets a 200 without touching streams.
	other := newTestInboundRequest(sip.BYE, "34020000001320009999", "34020000002000000001", "")
	otherTx := newFakeServerTx()
	api.handlerBye(other, otherTx)
	require.Equal(t, []int{200}, otherTx.StatusCodes())
}

func TestBuildPlaySDPStreamNumberAndPlaybackFields(t *testing.T) {
	sdp := string(buildPlaySDP("dev", "ch", "192.0.2.10", 30000, 0, "0000000033", true,
		time.Unix(1700000000, 0), time.Unix(1700003600, 0), 1))
	require.True(t, strings.Contains(sdp, "s=Playback"))
	require.True(t, strings.Contains(sdp, "t=1700000000 1700003600"))
	require.True(t, strings.Contains(sdp, "y=0000000033"))
	require.True(t, strings.Contains(sdp, "f= v/2/1///a/1///"))
}
