# Module Guide

This document describes the responsibilities, core interfaces, and extension points of each `internal/` package. For overall layering see [Architecture](architecture.md); actual package dependencies follow `go.mod` and real imports.

---

## Entry Point

### `cmd/lalmax-nvr`

Program entry. Responsible for:
- CLI argument parsing (`main.go`)
- Application assembly: create module instances from config, inject dependencies
- Lifecycle management: graceful start / stop
- Integration tests also live here (`*_test.go`)

---

## Core Business Layer

### `internal/api`

HTTP routing and request handling.

- Uses the [chi](https://github.com/go-chi/chi) router
- All handlers receive dependencies via constructors, no global state
- Auth middleware: `RequireAuth` / `RequireOperatePermission` (superadmin)
- Route registration is centralized in `handler.go`; handler files are split by domain:
  - `handlers_cameras.go` — camera CRUD
  - `handlers_recording.go` — recording plans and playback
  - `handlers_home_assistant.go` / `handlers_home_assistant_webrtc.go` — HA catalog and WHEP
  - `handlers_transcoding.go` — transcode management (Profile / Job / Variant)
  - `handlers_snapshot.go` — snapshot frame extraction (requires FFmpeg in PATH)

**Extension point**: add new routes in `handler.go`, create a matching handler file; cross-cutting concerns (auth, logging, CORS) live in `middleware/`.

---

### `internal/camera`

Camera lifecycle management.

- Owns per-camera runtime state (connecting / online / offline)
- Initiates stream pull: calls `internal/media` to establish RTSP / ONVIF pull
- Sub-stream management
- IP self-healing (auto-detect and repair IP changes after camera reboot)
- Group writer override: cameras already subscribed via `recorder.GroupWriter` don't open a second recorder

**Note**: MJPEG / HTTP JPEG sources are pulled directly by this package (lalmax does not handle these input types).

---

### `internal/media`

Go adapter layer for the lalmax media engine.

- Wraps lalmax / lal's pull interface, RTP ingest, GetStream, BuildPlayURL, and stream events
- `mode: embedded`: engine runs in the same process, called via lalmax Go API
- `mode: http`: connects to an external lalmax HTTP interface (for independent deployment)
- Provides `IngestHandler`: maps lalmax publish events to NVR stream_id / camera_id business identity

**Key interface** (`types.go`):
```go
type Engine interface {
    Pull(ctx, source SourceSpec) error
    GetStream(streamID string) (StreamInfo, error)
    BuildPlayURL(streamID, protocol string) (string, error)
    Subscribe(handler EventHandler)
    // ...
}
```

---

### `internal/recorder`

Recording management, two modes:

1. **Group Writer (GroupWriter)**: directly subscribes to a lalmax group, receives raw H.264/H.265 frames, writes segmented MP4 to disk. This is the primary recording path in embedded mode.
2. **Record task**: used for `adaptive` strategy and `media.mode: http`, recording via lalmax HTTP.

A recording Plan only controls the write-to-disk switch (`SetWriting(true/false)`), not how writing is done.

---

### `internal/merge`

Segment merging:

- **Rolling merge**: on `segment.completed` event, debounce (default 5s) and merge into the current UTC-hour file
- **Periodic merge**: scheduled scan for historical gap-fill, handles orphan segments from mid-recording restarts
- Output is "hour-bucket" MP4; VOD playback cuts fMP4 from these

---

### `internal/vod`

On-demand playback service.

- Serves playback requests by cutting init segment + fMP4 chunks (~6s each) from hour-bucket MP4s
- Generates HLS VOD playlist (`#EXT-X-DISCONTINUITY` for recording gaps)
- Operates directly on MP4 boxes, no external transcoding tool required

---

### `internal/health`

Multi-layer liveness probing and auto-recovery.

- Periodically probes each camera's connectivity
- Triggers `internal/camera` self-healing (reconnect, IP refresh)
- Aggregated health exposed via `/api/health/details` and `/api/health/cameras`

---

## Media Ingest Layer

### `internal/gb28181`

GB28181 SIP upper-platform implementation.

- SIP service: device REGISTER, Catalog query, INVITE to trigger device push
- Device catalog: `gb28181_devices` + `gb28181_channels`
- Cascading: `PlatformManager` registers with upstream platform and shares channels (`gb28181_platforms` + `gb28181_platform_channels`)
- Media: device pushes PS/RTP, demuxed and injected into lalmax group

---

### `internal/voip`

Independent SIP terminal / PBX feature.

- Completely separate from GB28181, serves different scenarios (desk phones, video doorbells, PBX extensions)
- Features: SIP terminal registration, inbound/outbound calls, DTMF, call history
- Web intercom via WebRTC (browser mic ↔ SIP media bridge)
- Call media published to lalmax group, optional recording

---

### `internal/onvif`

ONVIF device discovery and control.

- WS-Discovery / Hello multicast discovery
- `GetStreamUri` to obtain RTSP URL, handed to the `camera` layer for pull
- PTZ control (ContinuousMove / AbsoluteMove / Stop)

---

## Transcode Layer (`internal/transcode`)

General real-time transcoding. See [Transcoding Architecture Design](transcoding-design.md) for full details.

**Core files**:

| File | Content |
|------|---------|
| `types.go` | `SourceRef`, `Profile`, `Job`, `State`, validation, shared key (`WorkKey`) |
| `manager.go` | `Manager`: job scheduling, shared dedup, quota control |
| `service.go` | `Service`: coordinator main loop, state machine |
| `executor.go` | `Executor`: resolve media addresses, start backend, health check |
| `ffmpeg.go` | FFmpeg backend implementation |
| `gstreamer.go` | GStreamer backend (fixed pipeline) |
| `capabilities.go` | Capability detection and self-test cache |
| `repository.go` | `transcode_intent_v1` / `transcode_outputs_v1` database operations |
| `variants.go` | Logical output (Variant) management and playback URL resolution |

**Extension point**: implement the `Backend` interface (`executor.go`) and register in the `NewExecutor` factory.

---

## Storage Layer

### `internal/storage`

Database abstraction, supports SQLite / PostgreSQL / MySQL.

- Uses `database/sql` standard library, no ORM
- Migration versioning: DDL executed in order in `db.go`
- **Note**: transcode tables (`transcode_intent_v1` / `transcode_outputs_v1`) use `CREATE TABLE IF NOT EXISTS`, not reusing previously skipped migration versions

---

### `internal/model`

Shared data models: common structs used across layers (Camera, Recording, Event, etc.).

---

## Infrastructure Layer

### `internal/auth`

Authentication and authorization.

- NVR HTTP account management (users / password hashes)
- Playback token management (`auth.playback_tokens`)
- RTSP auth configuration (`media.rtsp_auth_enable`)

### `internal/event`

Event bus, decouples inter-module state notifications.

- Recording complete → triggers merge
- Health state change → triggers camera self-healing
- AI detection event → MQTT push

### `internal/middleware`

HTTP middleware: logging, CORS, auth, request ID, rate limiting.

### `internal/observability`

OpenTelemetry integration: Trace / Metric / Log export via OTLP.

---

## Other Feature Packages

| Package | Responsibility |
|---------|---------------|
| `internal/ai` | AI object detection (YOLO) subprocess management, `CGO_ENABLED=0` |
| `internal/dlna` | DLNA / SSDP service discovery and media serving |
| `internal/emby` | Emby media library pull and publish as live stream |
| `internal/iptv` | M3U subscription management, channel checking, HLS proxy |
| `internal/mqtt` | MQTT event push |
| `internal/ftp` | FTP service (`fclairamb/ftpserverlib`) |
| `internal/webdav` | WebDAV file browsing |
| `internal/jt808` | JT/T 808 signaling: registration, live, playback commands |
| `internal/ban` | IP ban management |
| `internal/cleanup` | Recording file expiry cleanup |
| `internal/rediscovery` | Camera IP-change re-discovery |
| `internal/streamhistory` | Historical stream query |
| `internal/civilcode` | GB/T 2260 administrative division code utilities |
| `internal/linkage` | Linkage rules (event → recording / relay etc.) |
| `internal/metrics` | Prometheus metrics export |
| `internal/docsportal` | HTTP service embedding OpenAPI docs |
| `internal/ui` | Svelte frontend static files embedded via `go:embed` |

---

## Third-Party Packages

| Path | Description |
|------|-------------|
| `third/lalmax` | Embedded lalmax / lal media engine (Go module replace) |
| `onvif/` | ONVIF protocol support library |

---

## Common Extension Scenarios

**Add a new media ingest protocol**
1. Extend the `Engine` interface in `internal/media` or add a new ingest handler
2. Add startup logic in `internal/camera`
3. Update the ingest path table in `architecture.md`

**Add a new API endpoint**
1. Register the route in `internal/api/handler.go`
2. Create a `handlers_<domain>.go` file
3. Optionally add the OpenAPI definition in `internal/docsportal/openapi.yaml`

**Add a new transcode backend**
1. Implement the `Backend` interface in `executor.go`
2. Register in the factory function
3. Add corresponding self-test logic in `capabilities.go`
