package livekitsfu

import (
	"context"
	"errors"
	"time"
)

// Runtime is implemented by LiveKit's service.LivekitServer.
type Runtime interface {
	Start() error
	Stop(force bool)
	IsRunning() bool
}

var ErrInvalidRuntime = errors.New("livekitsfu: invalid runtime")

// Run starts one LiveKit server and stops it after cancellation. LiveKit's
// Stop is ineffective until IsRunning becomes true, so cancellation during
// startup waits for either readiness or a startup error before stopping.
// Stop waits for LiveKit's own shutdown and is not bounded by the context.
func Run(ctx context.Context, server Runtime) error {
	if ctx == nil || server == nil {
		return ErrInvalidRuntime
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- server.Start() }()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	cancelled := false
	cancel := ctx.Done()
	for {
		if cancelled && server.IsRunning() {
			server.Stop(true)
			if err := <-done; err != nil {
				return err
			}
			return ctx.Err()
		}
		select {
		case err := <-done:
			return err
		case <-cancel:
			cancelled = true
			cancel = nil
		case <-tick.C:
		}
	}
}
