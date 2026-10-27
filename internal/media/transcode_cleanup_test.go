package media

import (
	"context"
	"errors"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
)

type cleanupEngine struct {
	Engine
	fail bool
}

func (e *cleanupEngine) GetStream(context.Context, string) (*StreamInfo, error) {
	if e.fail {
		return nil, errors.New("unavailable")
	}
	return &StreamInfo{}, nil
}
func TestTranscodeCleanupFailureKeepsReservation(t *testing.T) {
	authorization := auth.NewMediaAuthorizer()
	if err := authorization.ReserveInternalStream("output", "publish", "playback"); err != nil {
		t.Fatal(err)
	}
	engine := &cleanupEngine{fail: true}
	adapter := &TranscodeAdapter{Engine: engine, Auth: authorization, outputs: map[string]derivedOutput{"output": {playback: "playback"}}}
	if adapter.Release(context.Background(), "output") == nil {
		t.Fatal("cleanup error ignored")
	}
	if _, ok := adapter.outputs["output"]; !ok {
		t.Fatal("failed cleanup lost registry")
	}
	if authorization.AuthorizePlayback("rtsp", "output", "playback") == nil {
		t.Fatal("failed cleanup did not revoke token")
	}
	engine.fail = false
	if err := adapter.Release(context.Background(), "output"); err != nil {
		t.Fatal(err)
	}
	if _, ok := adapter.outputs["output"]; ok {
		t.Fatal("successful retry retained reservation")
	}
}
