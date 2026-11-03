package xiaomi

import (
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCameraStreamOptionsPropagateToRecorder(t *testing.T) {
	cfg := config.CameraConfig{ID: "cam", URL: "xiaomi://123?channel=1&quality=sd"}
	plugin := &XiaomiPlugin{}
	for _, rec := range []*XiaomiRecorder{
		plugin.NewRecorder(cfg, nil, nil).(*XiaomiRecorder),
		plugin.NewRecorderWithMediaEngine(cfg, nil, nil, nil).(*XiaomiRecorder),
	} {
		require.Equal(t, "123", rec.cfg.DID)
		require.Equal(t, "1", rec.cfg.Channel)
		require.Equal(t, "sd", rec.cfg.Quality)
	}
}

func TestStartMediaRejectsInvalidOptionsBeforeSending(t *testing.T) {
	client, conn := newTestMISSClient()
	for _, tc := range []struct{ channel, quality string }{
		{"3", "hd"}, {"0", "invalid"}, {"1", `1,"enableaudio":0`},
	} {
		require.Error(t, client.StartMedia(tc.channel, tc.quality))
	}
	_, _, ok := conn.lastWrittenCmd()
	require.False(t, ok)
}

func TestStartMediaChannelTwo(t *testing.T) {
	client, conn := newTestMISSClient()
	require.NoError(t, client.StartMedia("2", "sd"))
	_, payload, ok := conn.lastWrittenCmd()
	require.True(t, ok)
	decoded, err := Decode(payload, client.key)
	require.NoError(t, err)
	require.Contains(t, string(decoded), `"videoquality2":1`)
}
