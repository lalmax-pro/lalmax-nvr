package transcode

import (
	"context"
	"sync"
	"time"
)

// ResolvedInput is supplied by the media adapter after authorization. Endpoints
// are runtime-only and must not be exposed through job/status APIs.
type ResolvedInput struct {
	URL        string
	VideoCodec string
	AudioCodec string
	Width      int
	Height     int
}
type OutputObservation struct {
	Active     bool
	VideoCodec string
	LastFrame  time.Time
}

// MediaAdapter owns engine routing, authorization, and internal-output registry.
// Reserve must reject collisions, and Release must revoke internal credentials.
type MediaAdapter interface {
	Resolve(context.Context, SourceRef) (ResolvedInput, error)
	Reserve(context.Context, SourceRef, string) (string, error)
	Observe(context.Context, string) (OutputObservation, error)
	Release(context.Context, string) error
}

type BackendExecutor struct {
	Adapter  MediaAdapter
	Config   Config
	mu       sync.Mutex
	reserved map[string]time.Time
}

func (e *BackendExecutor) Start(ctx context.Context, source SourceRef, p Profile, id string) (Process, error) {
	if e.Adapter == nil {
		return nil, &Error{Code: "engine_unavailable", Message: "media adapter is unavailable"}
	}
	input, err := e.Adapter.Resolve(ctx, source)
	if err != nil {
		return nil, err
	}
	if err = p.CheckMedia(input.VideoCodec, input.AudioCodec); err != nil {
		return nil, err
	}
	if p.Backend == "gstreamer" && p.Video.Codec != "copy" && (input.Width == 0 || input.Height == 0) {
		input.Width, input.Height, err = ProbeInputDimensions(ctx, e.Config.Tools().FFprobe, input.URL, CommandProbeRunner{})
		if err != nil {
			return nil, err
		}
	}
	output, err := e.Adapter.Reserve(ctx, source, id)
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		// Retry transient engine/session errors before leaving a fail-closed
		// reservation. Never attach cleanup to a canceled request context.
		for attempt := 0; attempt < 3; attempt++ {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			err := e.Adapter.Release(cleanupCtx, id)
			cancel()
			if err == nil {
				return
			}
		}
	}
	cfg := e.Config
	cfg.ApplyDefaults()
	if err = cfg.Validate(); err != nil {
		cleanup()
		return nil, err
	}
	spec := JobSpec{Profile: p, InputURL: input.URL, OutputURL: output, VideoCodec: input.VideoCodec, AudioCodec: input.AudioCodec, Threads: cfg.ThreadsPerJob}
	var process Process
	if p.Backend == "ffmpeg" {
		process, err = StartFFmpeg(ctx, cfg.Tools().FFmpeg, spec)
	} else {
		process, err = StartGStreamer(ctx, cfg.Tools().GSTLaunch, spec, input.Width, input.Height)
	}
	if err != nil {
		cleanup()
		return nil, err
	}
	e.mu.Lock()
	if e.reserved == nil {
		e.reserved = map[string]time.Time{}
	}
	e.reserved[id] = time.Now()
	e.mu.Unlock()
	return &reservedProcess{Process: process, cleanup: func() { cleanup(); e.mu.Lock(); delete(e.reserved, id); e.mu.Unlock() }}, nil
}
func (e *BackendExecutor) Ready(ctx context.Context, id string) bool {
	if e.Adapter == nil {
		return false
	}
	observation, err := e.Adapter.Observe(ctx, id)
	if err != nil || !observation.Active || observation.VideoCodec != "h264" {
		return false
	}
	e.mu.Lock()
	started, ok := e.reserved[id]
	e.mu.Unlock()
	// Engine frame statistics have second resolution. Compare against the
	// start's second boundary; output reservation rejects an existing publisher.
	return ok && !observation.LastFrame.Before(started.Truncate(time.Second)) && time.Since(observation.LastFrame) < 5*time.Second && time.Since(observation.LastFrame) >= 0
}

type reservedProcess struct {
	Process
	once    sync.Once
	cleanup func()
}

func (p *reservedProcess) Wait() error { err := p.Process.Wait(); p.once.Do(p.cleanup); return err }
