package gb28181

import (
	"path/filepath"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestPlatformManagerCRUDWithoutSIP(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "plat.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Init(t.Context()))

	pm := NewPlatformManager(nil, "192.0.2.10", "192.0.2.10", "34020000002000000001", "secret", db)
	require.NoError(t, pm.LoadPlatforms())
	require.Empty(t, pm.ListPlatforms())
	_, ok := pm.GetPlatform(1)
	require.False(t, ok)

	cfg := &PlatformConfig{
		Name:       "upstream",
		Enable:     false,
		ServerGBID: "44010000002000000001",
		ServerIP:   "192.0.2.1",
		ServerPort: 5060,
		DeviceGBID: "34020000002000000001",
		DeviceIP:   "192.0.2.10",
		DevicePort: 5060,
	}
	require.NoError(t, pm.AddPlatform(cfg))
	require.NotZero(t, cfg.ID)
	got, ok := pm.GetPlatform(cfg.ID)
	require.True(t, ok)
	require.Equal(t, "upstream", got.Config.Name)
	require.Len(t, pm.ListPlatforms(), 1)
	require.Empty(t, pm.GetSharedChannels(cfg.ID))
	require.Nil(t, got.getManager())

	require.NoError(t, pm.RemovePlatform(cfg.ID))
	_, ok = pm.GetPlatform(cfg.ID)
	require.False(t, ok)
	require.Empty(t, pm.ListPlatforms())
}
