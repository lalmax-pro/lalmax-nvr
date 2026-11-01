package main

import (
	"context"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/voip"
	"github.com/stretchr/testify/require"
)

func TestVoIPWrappersWhenDisabled(t *testing.T) {
	app := &App{}
	status := app.VoIPStatus()
	require.Empty(t, status.Endpoints)
	require.Empty(t, status.Calls)

	require.False(t, app.HangupVoIP("call"))
	require.ErrorContains(t, app.AnswerVoIPCall("call"), "VoIP is disabled")
	require.ErrorContains(t, app.RejectVoIPCall("call"), "VoIP is disabled")
	_, err := app.DialVoIP("1001", "", "")
	require.ErrorContains(t, err, "VoIP is disabled")
	_, err = app.AttachVoIPTalk(context.Background(), "call", "token", "")
	require.ErrorContains(t, err, "VoIP is disabled")
	require.False(t, app.KeepVoIPTalk("call", "token"))
	_, err = app.ClaimVoIPTalk("call")
	require.ErrorContains(t, err, "VoIP is disabled")
	require.False(t, app.DetachVoIPTalk("call", "token"))
	require.ErrorContains(t, app.SendVoIPDTMF("call", "5"), "VoIP is disabled")
}

func TestRestartVoIPStopsAfterShutdown(t *testing.T) {
	app := &App{mediaEngine: &media.EmbeddedLalmax{}}
	next := voipConfigForTest(t)
	app.stopVoIP()
	require.ErrorContains(t, app.RestartVoIP(context.Background(), &next), "shutting down")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	app = &App{mediaEngine: &media.EmbeddedLalmax{}}
	require.ErrorIs(t, app.RestartVoIP(cancelled, &next), context.Canceled)

	parent, parentCancel := context.WithCancel(context.Background())
	parentCancel()
	app = &App{mediaEngine: &media.EmbeddedLalmax{}, startCtx: parent}
	require.ErrorContains(t, app.RestartVoIP(context.Background(), &next), "shutting down")
}

func TestVoIPWrappersWithRunningServer(t *testing.T) {
	app := &App{mediaEngine: &media.EmbeddedLalmax{}}
	cfg := voipConfigForTest(t)
	require.NoError(t, app.RestartVoIP(context.Background(), &cfg))
	t.Cleanup(app.stopVoIP)

	// Applying the same configuration twice is a no-op.
	require.NoError(t, app.RestartVoIP(context.Background(), &cfg))

	status := app.VoIPStatus()
	require.True(t, status.Enabled)
	require.NotNil(t, status.Endpoints)
	require.NotNil(t, status.Calls)

	require.False(t, app.HangupVoIP("missing-call"))
	require.Error(t, app.AnswerVoIPCall("missing-call"))
	require.Error(t, app.RejectVoIPCall("missing-call"))
	_, err := app.DialVoIP("1001", "", "")
	require.Error(t, err)
	_, err = app.AttachVoIPTalk(context.Background(), "missing-call", "token", "")
	require.Error(t, err)
	require.False(t, app.KeepVoIPTalk("missing-call", "token"))
	_, err = app.ClaimVoIPTalk("missing-call")
	require.Error(t, err)
	require.False(t, app.DetachVoIPTalk("missing-call", "token"))
	require.Error(t, app.SendVoIPDTMF("missing-call", "5"))

	// Disabling the service closes the runtime and restores the disabled state.
	disabled := cfg
	disabled.Enable = false
	require.NoError(t, app.RestartVoIP(context.Background(), &disabled))
	require.Nil(t, app.voipSvr)
	require.False(t, app.VoIPStatus().Enabled)
	require.ErrorContains(t, app.AnswerVoIPCall("missing-call"), "VoIP is disabled")
}

func TestRestartVoIPRejectsInvalidConfig(t *testing.T) {
	app := &App{mediaEngine: &media.EmbeddedLalmax{}}
	cfg := voip.Config{Enable: true, SipListenAddr: "invalid-listen-address"}
	require.Error(t, app.RestartVoIP(context.Background(), &cfg))
	require.Nil(t, app.voipSvr)
	app.stopVoIP()
}
