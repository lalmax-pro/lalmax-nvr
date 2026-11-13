package transcode

import (
	"context"
	"io"
	"os/exec"
	"regexp"
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
	owner    *processOwner
	done     chan struct{}
	err      error
	stderr   *boundedOutput
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
	// Keep a bounded stderr prefix for post-failure diagnosis. It is exposed
	// only through Diagnostic and is redacted before entering service logs.
	cmd.Stdout = io.Discard
	stderr := &boundedOutput{limit: 256 * 1024}
	cmd.Stderr = stderr
	owner, err := startOwnedProcess(cmd)
	if err != nil {
		return nil, &Error{Code: "process_start_failed", Message: "could not start media backend; check executable and permissions"}
	}
	p := &commandProcess{cmd: cmd, owner: owner, done: make(chan struct{}), stderr: stderr}
	go func() {
		if err := cmd.Wait(); err != nil {
			p.err = &Error{Code: "process_failed", Message: "media backend exited unsuccessfully"}
		}
		owner.close()
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

func (p *commandProcess) Diagnostic() string {
	if p.stderr == nil {
		return ""
	}
	return redactMediaDiagnostic(string(p.stderr.Bytes()))
}

var mediaCredentialPattern = regexp.MustCompile("(?i)(token|password|secret|stream_key)=([^&\\s]+)")

func redactMediaDiagnostic(value string) string {
	return mediaCredentialPattern.ReplaceAllString(value, "$1=[REDACTED]")
}
func (p *commandProcess) Stop(ctx context.Context) error {
	select {
	case <-p.done:
		return nil
	default:
	}
	p.stopOnce.Do(func() { _ = p.owner.interrupt() })
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		_ = p.owner.kill()
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
