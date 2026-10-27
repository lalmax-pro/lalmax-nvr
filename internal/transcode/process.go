package transcode

import (
	"context"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Process owns exactly one Wait call. No process output or endpoint credentials
// are included in returned errors. Readiness is verified by the media adapter.
type Process interface {
	Wait() error
	Stop(context.Context) error
}

type commandProcess struct {
	cmd      *exec.Cmd
	done     chan struct{}
	err      error
	stopOnce sync.Once
}

func startCommand(ctx context.Context, path string, args []string) (Process, error) {
	if ctx.Err() != nil {
		return nil, &Error{Code: "process_start_canceled", Message: "media backend start canceled"}
	}
	// Cancellation is handled through Stop below: CommandContext's immediate
	// kill would bypass the graceful termination interval during normal shutdown.
	cmd := exec.Command(path, args...)
	configureProcess(cmd)
	cmd.Cancel = nil
	cmd.WaitDelay = 2 * time.Second
	// Deliberately discard raw stderr until a credential-safe diagnostic sink is
	// available; exec errors are converted to public domain errors below.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, &Error{Code: "process_start_failed", Message: "could not start media backend; check executable and permissions"}
	}
	p := &commandProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		if err := cmd.Wait(); err != nil {
			p.err = &Error{Code: "process_failed", Message: "media backend exited unsuccessfully"}
		}
		close(p.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = p.Stop(stopCtx)
		case <-p.done:
		}
	}()
	return p, nil
}
func (p *commandProcess) Wait() error { <-p.done; return p.err }
func (p *commandProcess) Stop(ctx context.Context) error {
	select {
	case <-p.done:
		return nil
	default:
	}
	p.stopOnce.Do(func() { _ = interruptProcess(p.cmd) })
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		_ = killProcess(p.cmd)
		// WaitDelay bounds inherited pipe cleanup; the process reaper still owns Wait.
		return ctx.Err()
	}
}

func StartFFmpeg(ctx context.Context, path string, spec JobSpec) (Process, error) {
	args, err := FFmpegArgs(spec)
	if err != nil {
		return nil, err
	}
	return startCommand(ctx, path, args)
}
