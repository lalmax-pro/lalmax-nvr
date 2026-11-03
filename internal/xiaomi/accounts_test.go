package xiaomi

import (
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/stretchr/testify/require"
)

func TestRecorderSelectsNamedAccountAndRefreshesCredentials(t *testing.T) {
	cfg := config.XiaomiConfig{UserID: "default", Token: "default-token", Accounts: map[string]config.XiaomiAccount{
		"home": {UserID: "home-user", Token: "home-token", Region: "sg"},
	}}
	SetCloudConfig(cfg)
	t.Cleanup(func() { SetCloudConfig(config.XiaomiConfig{}) })
	r := (&XiaomiPlugin{}).NewRecorder(config.CameraConfig{ID: "cam", URL: "xiaomi://123?account=home"}, nil, nil).(*XiaomiRecorder)
	require.Equal(t, "123", r.cfg.DID)
	require.Equal(t, "home-token", r.currentCloudConfig().Token)
	cfg.Accounts["home"] = config.XiaomiAccount{UserID: "home-user", Token: "new-token", Region: "sg"}
	SetCloudConfig(cfg)
	require.Equal(t, "new-token", r.currentCloudConfig().Token)
	require.Equal(t, "default-token", cloudConfigForAccount("").Token)
	require.Empty(t, cloudConfigForAccount("missing"))
}
