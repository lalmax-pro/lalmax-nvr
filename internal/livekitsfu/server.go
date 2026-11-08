package livekitsfu

import (
	"fmt"

	"github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/livekit-server/pkg/routing"
	"github.com/livekit/livekit-server/pkg/service"
	"github.com/livekit/livekit-server/pkg/telemetry/prometheus"
)

// NewServer constructs a LiveKit server from its native YAML configuration.
// LiveKit registers metrics and installs global state in this process; callers
// should manage one embedded server per process.
func NewServer(yamlConfig string) (*service.LivekitServer, error) {
	conf, err := config.NewConfig(yamlConfig, true, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("livekitsfu: parse configuration: %w", err)
	}
	if err := conf.ValidateKeys(); err != nil {
		return nil, fmt.Errorf("livekitsfu: validate keys: %w", err)
	}
	if err := conf.LoadTURNSecrets(); err != nil {
		return nil, fmt.Errorf("livekitsfu: load TURN secrets: %w", err)
	}
	node, err := routing.NewLocalNode(conf)
	if err != nil {
		return nil, fmt.Errorf("livekitsfu: create node: %w", err)
	}
	if err := prometheus.Init(string(node.NodeID()), node.NodeType()); err != nil {
		return nil, fmt.Errorf("livekitsfu: initialize metrics: %w", err)
	}
	server, err := service.InitializeServer(conf, node)
	if err != nil {
		return nil, fmt.Errorf("livekitsfu: initialize server: %w", err)
	}
	return server, nil
}
