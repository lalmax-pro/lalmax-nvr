package media

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
)

type derivedOutput struct {
	source   transcode.SourceRef
	playback string
}

// TranscodeAdapter is initially embedded-only. It never modifies listener
// configuration or camera configuration to make a task succeed.
type TranscodeAdapter struct {
	Engine         Engine
	Auth           *auth.MediaAuthorizer
	Config         *config.Config
	RegisterOutput func(context.Context, string, string) error
	StopDerivedRecording func(context.Context, string) error
	mu             sync.RWMutex
	outputs        map[string]derivedOutput
}

func (a *TranscodeAdapter) Resolve(ctx context.Context, source transcode.SourceRef) (transcode.ResolvedInput, error) {
	if err := source.Validate(); err != nil {
		return transcode.ResolvedInput{}, err
	}
	if a.Config == nil || a.Engine == nil || a.Config.Media.Mode != "embedded" || source.EngineID != "embedded" {
		return transcode.ResolvedInput{}, &transcode.Error{Code: "engine_unavailable", Message: "embedded media engine required"}
	}
	if a.Auth != nil && a.Auth.InternalPlaybackRequired(source.StreamID) {
		return transcode.ResolvedInput{}, fmt.Errorf("derived source cannot be transcoded")
	}
	info, err := a.Engine.GetStream(ctx, source.StreamID)
	if err != nil || info == nil || !info.Active {
		return transcode.ResolvedInput{}, &transcode.Error{Code: "source_offline", Message: "source is offline"}
	}
	app := info.AppName
	if app == "" {
		app = "live"
	}
	if app != source.AppName {
		return transcode.ResolvedInput{}, fmt.Errorf("source application mismatch")
	}
	token := a.Config.Auth.PlaybackTokens[source.StreamID]
	if len(a.Config.Auth.PlaybackTokens) > 0 && token == "" {
		return transcode.ResolvedInput{}, fmt.Errorf("source playback credential unavailable")
	}
	play, err := a.Engine.BuildPlayURL(ctx, PlayURLRequest{StreamID: source.StreamID, AppName: app, Protocol: "rtsp", Token: token})
	if err != nil || play == nil {
		return transcode.ResolvedInput{}, fmt.Errorf("source RTSP unavailable")
	}
	u, err := url.Parse(play.URL)
	if err != nil || u.Scheme != "rtsp" {
		return transcode.ResolvedInput{}, fmt.Errorf("invalid RTSP endpoint")
	}
	if a.Config.Media.RTSPAuthEnable {
		u.User = url.UserPassword(a.Config.Media.RTSPUsername, a.Config.Media.RTSPPassword)
	}
	return transcode.ResolvedInput{URL: u.String(), VideoCodec: strings.ToLower(info.VideoCodec), AudioCodec: strings.ToLower(info.AudioCodec)}, nil
}
func (a *TranscodeAdapter) Reserve(ctx context.Context, source transcode.SourceRef, id string) (string, error) {
	if a.Config == nil || a.Config.Media.Mode != "embedded" || a.Engine == nil || a.Auth == nil {
		return "", fmt.Errorf("embedded media authorization unavailable")
	}
	if a.Config.RTMP.Enabled == nil || !*a.Config.RTMP.Enabled {
		return "", &transcode.Error{Code: "rtmp_disabled", Message: "RTMP listener is disabled"}
	}
	// Listing must succeed; inability to check collisions must fail closed.
	streams, err := a.Engine.ListStreams(ctx)
	if err != nil {
		return "", fmt.Errorf("stream collision check unavailable")
	}
	for _, s := range streams {
		if s.StreamID == id {
			return "", fmt.Errorf("output stream already exists")
		}
	}
	publish, playback := rand.Text(), rand.Text()
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.outputs[id]; ok {
		return "", fmt.Errorf("output already reserved")
	}
	if err = a.Auth.ReserveInternalStream(id, publish, playback); err != nil {
		return "", err
	}
	if a.RegisterOutput != nil {
		if err := a.RegisterOutput(ctx, id, source.StreamID); err != nil {
			a.Auth.RevokeInternalStream(id)
			return "", fmt.Errorf("internal output registry unavailable")
		}
	}
	if a.outputs == nil {
		a.outputs = map[string]derivedOutput{}
	}
	a.outputs[id] = derivedOutput{source: source, playback: playback}
	port := a.Config.RTMP.Port
	if port == 0 {
		port = config.DefaultLalRTMPPort
	}
	u := url.URL{Scheme: "rtmp", Host: fmt.Sprintf("127.0.0.1:%d", port), Path: "/live/" + id}
	q := url.Values{}
	q.Set("stream_key", publish)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func (a *TranscodeAdapter) Observe(ctx context.Context, id string) (transcode.OutputObservation, error) {
	info, err := a.Engine.GetStream(ctx, id)
	if err != nil {
		return transcode.OutputObservation{}, err
	}
	if info == nil {
		return transcode.OutputObservation{}, nil
	}
	var mediaBytes uint64
	if info.Publisher != nil {
		mediaBytes = info.Publisher.WroteBytesSum
		if info.Publisher.ReadBytesSum > mediaBytes {
			mediaBytes = info.Publisher.ReadBytesSum
		}
	}
	return transcode.OutputObservation{Active: info.Active, VideoCodec: strings.ToLower(info.VideoCodec), LastFrame: info.LastFrameTime, MediaBytes: mediaBytes}, nil
}
func (a *TranscodeAdapter) Release(ctx context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.outputs[id]; !ok {
		return nil
	}

	// Revoke first. The deny-only tombstone must be installed before any
	// fallible cleanup so a failed recorder stop cannot leave old grants live.
	a.Auth.RevokeInternalStream(id)
	var cleanupErr error
	if a.StopDerivedRecording != nil {
		cleanupErr = a.StopDerivedRecording(ctx, id)
	}

	// Continue best-effort session cleanup even when recording teardown fails;
	// retain the reservation until every cleanup step succeeds, preventing a
	// new task from inheriting the old output, publisher, or subscribers.
	info, err := a.Engine.GetStream(ctx, id)
	if err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	} else if info != nil {
		for _, subscriber := range info.Subscribers {
			if kickErr := a.Engine.KickSession(ctx, subscriber.SessionID); kickErr != nil {
				cleanupErr = errors.Join(cleanupErr, kickErr)
			}
		}
	}
	if cleanupErr == nil {
		delete(a.outputs, id)
	}
	return cleanupErr
}

var _ transcode.MediaAdapter = (*TranscodeAdapter)(nil)
