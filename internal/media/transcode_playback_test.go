package media

import (
	"context"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
)

// Embedding the engine interface limits this test double to URL generation.
type derivedURLEngine struct {
	Engine
	request PlayURLRequest
}

func (e *derivedURLEngine) BuildPlayURL(_ context.Context, r PlayURLRequest) (*PlayURL, error) {
	e.request = r
	return &PlayURL{URL: "rtsp://localhost/live/output?token=scoped", Protocol: r.Protocol}, nil
}
func TestDerivedPlaybackRequiresSourceOwnership(t *testing.T) {
	source := transcode.SourceRef{EngineID: "embedded", StreamID: "camera", AppName: "live"}
	engine := &derivedURLEngine{}
	adapter := &TranscodeAdapter{Engine: engine, outputs: map[string]derivedOutput{"physical": {source: source, playback: "scoped"}}}
	if _, err := adapter.DerivedPlayURL(context.Background(), source, "physical", "rtsp"); err != nil {
		t.Fatal(err)
	}
	if engine.request.Token != "scoped" || engine.request.StreamID != "physical" {
		t.Fatal("incorrect scoped playback request")
	}
	other := source
	other.StreamID = "other"
	if _, err := adapter.DerivedPlayURL(context.Background(), other, "physical", "rtsp"); err == nil {
		t.Fatal("cross-source access allowed")
	}
	if _, err := adapter.DerivedPlayURL(context.Background(), source, "absent", "rtsp"); err == nil {
		t.Fatal("unreserved output allowed")
	}
	if _, err := adapter.DerivedPlayURL(context.Background(), source, "physical", "file"); err == nil {
		t.Fatal("invalid protocol allowed")
	}
}
