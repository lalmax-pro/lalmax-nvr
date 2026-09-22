package recorder

import (
	"context"
	"strings"
	"time"
)

func runFrameWatchdog(ctx context.Context, timeout time.Duration, alive <-chan struct{}, stop <-chan struct{}, done chan struct{}, onTimeout func()) {
	defer close(done)
	if timeout <= 0 {
		timeout = defaultFrameWatchdogTimeout
	}
	watchdog := time.NewTimer(timeout)
	defer watchdog.Stop()
	for {
		select {
		case <-alive:
			if !watchdog.Stop() {
				select {
				case <-watchdog.C:
				default:
				}
			}
			watchdog.Reset(timeout)
		case <-watchdog.C:
			onTimeout()
			return
		case <-stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

// streamIDOrCameraID is the stream a recording belongs to.
// Camera-backed recorders fall back to the camera ID.
func streamIDOrCameraID(streamID, cameraID string) string {
	if s := strings.TrimSpace(streamID); s != "" {
		return s
	}
	return cameraID
}
