# ADR-002: Dual transcode backends (FFmpeg / GStreamer) with shared Manager

- **Status**: Accepted
- **Date**: 2026-10
- **Related**: Issue #66, PR #67, [Transcoding Architecture Design](../transcoding-design.md)

## Context

The NVR needs real-time transcoding to convert camera streams (H.265 / non-standard audio) to downstream-compatible formats (H.264/AAC). The backend tool and architecture organization needed to be decided.

## Alternatives

**Option A: FFmpeg only**
- Simplest deployment; pre-built packages available for virtually all platforms
- Tradeoff: locked to one tool; can't leverage platform-specific hardware acceleration APIs (macOS VideoToolbox, Raspberry Pi MMAL, etc.)

**Option B: FFmpeg + GStreamer dual backends, shared Manager**
- Unified `Backend` interface; Manager is backend-agnostic
- FFmpeg is default; GStreamer is optional (`WITH_GSTREAMER=true` build flag)
- Preserves interfaces for hardware acceleration (GStreamer + platform plugins) and complex dynamic pipelines (standalone GStreamer worker)
- Tradeoff: larger test matrix, GStreamer deployment requires more plugins

**Option C: Direct platform media API via CGO**
- Best performance, zero-copy hardware paths
- Tradeoff: breaks `CGO_ENABLED=0` constraint, difficult cross-platform compilation — see ADR-004

## Decision

**Option B**: FFmpeg + GStreamer dual backends with a shared `Manager` and `Service`.

Core interface:
```go
type Backend interface {
    Probe(ctx context.Context) (Capabilities, error)
    Start(ctx context.Context, spec JobSpec) (Process, error)
}
```

GStreamer initially uses `gst-launch-1.0` fixed pipelines, with no CGO in the Go main process; complex dynamic pipelines upgrade to a standalone worker.

## Tradeoffs and Known Limitations

- Two separate test matrices; only FFmpeg runs in standard CI — GStreamer requires an extra build flag
- GStreamer P1 supports only fixed pipelines; dynamic pad and EOS handling require separate acceptance
- Silent cross-backend fallback (FFmpeg failure → auto-switch to GStreamer) is not allowed, to prevent resource exhaustion
- WebRTC audio output requires Opus encapsulation; the AAC ingest path cannot be reused for WebRTC without separate acceptance (P4)
