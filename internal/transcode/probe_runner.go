package transcode

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// boundedOutput drains subprocess output while retaining only a fixed prefix.
// A malicious/noisy tool cannot grow the probe's memory without bound.
type boundedOutput struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func (b *boundedOutput) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...)
}

type CommandProbeRunner struct{}

func (CommandProbeRunner) Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	configureProcess(cmd)
	cmd.WaitDelay = time.Second
	output := &boundedOutput{limit: 256 * 1024}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return nil, &Error{Code: "probe_failed", Message: "media capability check failed or timed out"}
	}
	return output.Bytes(), nil
}
