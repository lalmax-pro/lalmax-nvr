package recorder

import (
	"strings"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

// CameraUsesGroupRecording reports whether this camera's H.264/H.265 media
// already enters the in-process engine, so the group writer can record it.
// MJPEG, HTTP JPEG, and timelapse have no RtmpMsg and stay on their own collectors.
func CameraUsesGroupRecording(cam config.CameraConfig) bool {
	switch cam.Protocol {
	case string(model.ProtoHTTP):
		return false
	case string(model.ProtoXiaomi), string(model.ProtoGB28181), "rtmp-pull", "http-flv-pull", "udp-ts-pull":
		return true
	case string(model.ProtoRTSP), string(model.ProtoONVIF):
		enc := strings.ToLower(cam.Encoding)
		if enc == "" {
			enc = strings.ToLower(cam.StreamEncoding)
		}
		return enc == string(model.FormatH264) || enc == string(model.FormatH265)
	default:
		return false
	}
}
