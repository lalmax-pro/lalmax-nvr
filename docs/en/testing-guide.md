# Testing Guide

This document describes lalmax-nvr's test strategy, layer standards, and media acceptance requirements. The primary toolchain is `go test`; media E2E tests are opt-in via environment variables.

---

## Test Layers

```
Unit tests          — no external tools or network, run in any environment
Media integration   — requires local FFmpeg / GStreamer, uses synthetic streams
Container smoke     — CI runs in actual Docker containers, validates reachability
Manual / E2E        — physical cameras, real HA instance; documented in PR body
```

---

## Unit Test Standards

### Requirements

- All unit tests must pass `go test -race ./...`
- No dependency on locally installed FFmpeg / GStreamer / physical cameras
- External dependencies (DB, media engine, backend processes) injected via interfaces; use mocks/stubs in tests
- Do not persist files outside the test temp directory; clean up on test completion

### Injectable Test Boundaries

`internal/transcode` provides the following injection points:

```go
// Executor interface can be replaced with a mock to test Manager / Service without real FFmpeg
type Executor interface {
    Start(context.Context, SourceRef, Profile, string) (Process, error)
    Ready(context.Context, string) bool
}

// Clock can be injected to test backoff, timeout, and other time-dependent logic
```

### Race Testing

Packages with concurrency must run dedicated race regression:

```bash
go test -race -count=1 ./internal/transcode/...
go test -race -count=1 ./internal/api/...
go test -race -count=1 ./internal/media/...
```

Key scenarios to cover:
- Concurrent `Manager.Acquire` / `Release` calls
- Concurrent job generation update and old-version rejection
- Context cancellation propagation during service shutdown

---

## Media Integration Tests

Enabled via environment variable; requires local tool installation.

### FFmpeg Media Self-Test

```bash
NVR_TRANSCODE_MEDIA_TEST=1 go test -race -v ./internal/transcode/...
```

**Prerequisites**: `ffmpeg` and `ffprobe` available in PATH.

Scenarios covered:
- Synthetic H.264/AAC input → H.264/AAC output
- Synthetic H.265/AAC input → H.264/AAC transcode
- No-audio input → video-only derived output
- Video copy + audio transcode
- Video copy + no audio (audio: none)

Each scenario synthesizes a short video using FFmpeg (no real camera footage), encodes it, probes the output with ffprobe, then decodes with FFmpeg to verify.

### GStreamer Integration Tests

```bash
NVR_GSTREAMER_INTEGRATION_TEST=1 go test -race -v ./internal/transcode/...
```

**Prerequisites**: `gst-launch-1.0` and `gst-inspect-1.0` available in PATH, plus required plugins (base/good/bad).

On macOS with Homebrew GStreamer, you may need to set the dynamic library path:

```bash
export DYLD_LIBRARY_PATH=/opt/homebrew/lib:$DYLD_LIBRARY_PATH
NVR_GSTREAMER_INTEGRATION_TEST=1 go test -race -v ./internal/transcode/...
```

---

## Media Acceptance Standards

Media E2E acceptance does **not** treat process start success as a pass. Requirements:

1. **Actually retrieved**: derived output retrieved over a real protocol (RTSP / HTTP-FLV / HLS)
2. **Actually decoded**: use `ffmpeg` or `ffprobe` to decode and confirm video/audio codec specs
3. **Exit code 0**: decode command exits 0 with no decode error output
4. **Protocol consistent**: "API returned 200" does not equal "playback chain works"

### Record Format

Append to the PR body or related Issue after each stage acceptance:

```markdown
### Acceptance Record — YYYY-MM-DD

- **Commit / PR**: `abcdef1`
- **Environment**: macOS ARM64 / Linux ARM64 container / ...
- **Backend**: FFmpeg 8.0.1 / GStreamer 1.24.x
- **Input**: Synthetic H.264/AAC 640x360 / Hikvision H.264/AAC physical / ...
- **Output**: H.264/AAC derived RTSP
- **Verification command**:
  ```sh
  ffmpeg -rtsp_transport tcp -i rtsp://... -t 3 -f null - 2>&1
  ```
- **Result**: exit 0, ffprobe confirmed H.264 640x360 / AAC 48000
- **Not covered**: physical H.265, long-running, multi-arch
```

### Synthetic vs Physical Camera

| Level | Description |
|-------|-------------|
| Synthetic stream | FFmpeg/GStreamer-generated test stream; reproducible in CI and locally |
| Physical device | Real camera (note brand/model/codec); synthetic stream cannot substitute |
| Real HA | Validated in an actual Home Assistant instance: HLS playback, snapshot, events |

Physical acceptance results use command-line exit codes and ffprobe stats as evidence — no real footage is committed.

---

## Frontend Tests

```bash
cd web
npm install
npm test        # single-run via vitest --run (no watch mode)
```

- Component tests cover primary user interaction flows
- Actual media player (mpegts.js / WebCodecs) playback is validated by manual acceptance

---

## CI Test Execution

CI (`build.yml`) runs:

```bash
go test -race ./...
go vet ./...
```

Media integration tests (`NVR_TRANSCODE_MEDIA_TEST`, `NVR_GSTREAMER_INTEGRATION_TEST`) are **not automatically enabled in CI**. Run them manually in an environment with the required tools and document results in the PR.

Container smoke tests (`container-smoke.yml`) verify NVR startup and basic API reachability in a Docker container; they do not run the media transcode chain.

---

## Things to Avoid

- Don't substitute a green CI for media acceptance
- Don't substitute process start / API 200 for decode success
- Don't record synthetic stream acceptance as physical camera acceptance
- Don't introduce external network dependencies in tests (downloading FFmpeg, connecting to public cameras)
- Don't hardcode local paths in test code
- Don't discover race conditions after merge — run `-race` locally before pushing
