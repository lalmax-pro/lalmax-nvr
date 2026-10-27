package media

import (
	"context"
	"errors"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
)

type reservationEngine struct {
	Engine
	streams []StreamInfo
}

func (e *reservationEngine) ListStreams(context.Context) ([]StreamInfo, error) { return e.streams, nil }
func TestTranscodeDisabledRTMPHasActionableCode(t *testing.T) {
	disabled := false
	adapter := &TranscodeAdapter{Engine: &reservationEngine{}, Auth: auth.NewMediaAuthorizer(), Config: &config.Config{Media: config.MediaConfig{Mode: "embedded"}, RTMP: config.RTMPConfig{Enabled: &disabled}}}
	_, err := adapter.Reserve(context.Background(), transcode.SourceRef{EngineID: "embedded", StreamID: "source", AppName: "live"}, "output")
	var domain *transcode.Error
	if !errors.As(err, &domain) || domain.Code != "rtmp_disabled" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTranscodeRejectsExternalEngineWithActionableCode(t *testing.T) {
	adapter := &TranscodeAdapter{Engine: &reservationEngine{}, Config: &config.Config{Media: config.MediaConfig{Mode: "http"}}}
	_, err := adapter.Resolve(context.Background(), transcode.SourceRef{EngineID: "embedded", StreamID: "source", AppName: "live"})
	var domain *transcode.Error
	if !errors.As(err, &domain) || domain.Code != "engine_unavailable" {
		t.Fatalf("unexpected error: %v", err)
	}
}
func TestTranscodeRejectsInactiveGroupWithPreexistingSubscriber(t *testing.T) {
	enabled := true
	engine := &reservationEngine{streams: []StreamInfo{{StreamID: "candidate", Active: false, Subscribers: []SessionInfo{{SessionID: "unauthenticated-old-session"}}}}}
	policy := auth.NewMediaAuthorizer()
	adapter := &TranscodeAdapter{Engine: engine, Auth: policy, Config: &config.Config{Media: config.MediaConfig{Mode: "embedded"}, RTMP: config.RTMPConfig{Enabled: &enabled}}}
	_, err := adapter.Reserve(context.Background(), transcode.SourceRef{EngineID: "embedded", StreamID: "source", AppName: "live"}, "candidate")
	if err == nil {
		t.Fatal("inactive occupied group accepted")
	}
	if policy.InternalPlaybackRequired("candidate") {
		t.Fatal("failed reservation installed credentials")
	}
	if len(adapter.outputs) != 0 {
		t.Fatal("failed reservation registered an output")
	}
}
