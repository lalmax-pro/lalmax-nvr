package media

import (
	"context"
	"net/http"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/wsstream"
)

type WS interface {
	RegisterStream(cameraID string, codec model.Format, sps, pps, vps []byte, hub *model.StreamHub) error
	SetAudioConfig(cameraID string, aci *wsstream.AudioCodecInfo) error
	IsActive(cameraID string) bool
	ServeWS(cameraID string, w http.ResponseWriter, r *http.Request) error
	StopAll()
}

type Runtime struct {
	ws WS
}

func NewRuntime(cfg *config.Config, _ *metrics.Metrics) *Runtime {
	return &Runtime{
		ws: wsstream.NewManager(
			wsstream.WithMaxViewers(cfg.WebSocket.MaxViewers),
			wsstream.WithWriteBufSize(cfg.WebSocket.WriteBufSize),
			wsstream.WithIdleTimeout(cfg.WebSocket.IdleTimeout),
		),
	}
}

func (r *Runtime) WS() WS { return r.ws }

func (r *Runtime) Start(_ context.Context) error {
	return nil
}

func (r *Runtime) Stop() error {
	if r.ws != nil {
		r.ws.StopAll()
	}
	return nil
}
