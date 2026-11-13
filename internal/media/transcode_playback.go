package media

import (
	"context"
	"fmt"

	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
)

// DerivedPlayURL must only be called after consumer authorization. Source
// ownership is rechecked against the runtime registry before using credentials.
func (a *TranscodeAdapter) DerivedPlayURL(ctx context.Context, source transcode.SourceRef, id, protocol string) (*PlayURL, error) {
	switch protocol {
	case "rtsp", "webrtc", "hls", "flv", "rtmp":
	default:
		return nil, fmt.Errorf("unsupported derived playback protocol")
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	output, ok := a.outputs[id]
	if !ok || output.source != source {
		return nil, fmt.Errorf("derived output does not belong to source")
	}
	return a.Engine.BuildPlayURL(ctx, PlayURLRequest{StreamID: id, AppName: "live", Protocol: protocol, Token: output.playback})
}
