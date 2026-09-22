package media

import (
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/stretchr/testify/require"
)

func TestNewRuntime_AlwaysInitializesWS(t *testing.T) {
	cfg := &config.Config{}
	cfg.ApplyDefaults()

	rt := NewRuntime(cfg, nil)
	require.NotNil(t, rt)
	require.NotNil(t, rt.WS())
}
