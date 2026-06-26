# lalmax-nvr

[![GitHub Release](https://img.shields.io/github/v/release/lalmax-pro/lalmax-nvr?style=flat&label=Release)](https://github.com/lalmax-pro/lalmax-nvr/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/lalmax-pro/lalmax-nvr/ci.yml?style=flat&label=CI)](https://github.com/lalmax-pro/lalmax-nvr/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev/)
[![Svelte](https://img.shields.io/badge/Svelte-FF3E00?style=flat&logo=svelte&logoColor=white)](https://svelte.dev/)
[![SQLite](https://img.shields.io/badge/SQLite-003B57?style=flat&logo=sqlite&logoColor=white)](https://www.sqlite.org/)
[![Docker](https://img.shields.io/badge/Docker-2496ED?style=flat&logo=docker&logoColor=white)](https://www.docker.com/)
[![Raspberry Pi](https://img.shields.io/badge/Raspberry_Pi-A22846?style=flat&logo=raspberrypi&logoColor=white)](https://www.raspberrypi.com/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow?style=flat)](LICENSE)

A lightweight Network Video Recorder built on [lalmax](https://github.com/q191201771/lal) media engine. Single binary, zero dependencies — designed for Raspberry Pi and low-power devices.

This project was inspired by MiBeeNVR, and has since been developed into a dedicated NVR focused on the `lal` / `lalmax` media stack.

[**中文**](README.zh.md)

## Screenshots

![Login](docs/images/login-light.png)
![Dashboard](docs/images/dashboard-light.png)
![Recordings](docs/images/recordings-light.png)
![Settings](docs/images/settings-light.png)

## Architecture

lalmax-nvr uses a two-layer architecture:

```
Camera ──→ lalmax (media engine) ──→ HLS / HTTP-FLV / WebRTC / fMP4 playback
                │
                └──→ Recording (H264/H265 MP4 segments)
```

- **lalmax** handles media relay, protocol conversion, and stream distribution
- **NVR layer** handles camera lifecycle, ONVIF signaling, recording, storage, and web UI
- When `media.enabled=true`, all H264/H265 streams flow through lalmax — no duplicate pulls

## Streaming Protocols

| Protocol | Latency | Backend | Codec Support |
|----------|---------|---------|---------------|
| **WebCodecs** (WebSocket) | <100ms | Builtin WS | H.264, H.265 |
| **fMP4** (MSE) | ~200ms | lalmax | H.264, H.265 |
| **WebRTC** (WHEP) | ~300ms | lalmax | H.264 |
| **HTTP-FLV** | ~500ms | lalmax | H.264, H.265 |
| **HLS** / **LL-HLS** | 1-3s | lalmax | H.264, H.265 |

## Core Features

- **Media Engine**: lalmax-powered relay — unified ingest, no duplicate camera pulls
- **Camera Protocols**: RTSP (H.264/H.265/MJPEG), HTTP JPEG, ONVIF discovery & management
- **GB28181**: Full SIP-based device management, cascade platform support, recording query & playback with timeline, multi-protocol streaming (ws-flv, flv, hls, webrtc, etc.), playback control (pause/resume/speed/seek), batch download, platform event history, voice broadcast/intercom (SIP INVITE, UDP/TCP)
- **Recording**: Automatic MP4 segments, multi-camera concurrent, per-camera recording mode (continuous / scheduled / off), per-camera retention, audio capture (AAC + G.711)
- **Recording Playback**: Visual timeline with 24h overview, hour-level zoom, inline video player, mouse wheel zoom/pan, month calendar marking days with recordings
- **Live View**: Multi-protocol — WebCodecs, fMP4, WebRTC, HTTP-FLV, HLS, LL-HLS
- **RTMP/SRT Ingest**: Accept pushed streams from cameras or encoders
- **Segment Merge**: Auto or manual merge, global + per-camera policies
- **ONVIF**: Device discovery, PTZ control, imaging settings, stream URI resolution, encoding auto-detection
- **Stream Management**: Runtime stream inventory, camera binding, stream promotion
- **Web UI**: Dark/light theme, responsive, i18n (EN/ZH), Chart.js dashboards
- **Smart Home**: MQTT trigger-based recording, WebDAV/FTP file access
- **Health Monitoring**: Multi-layer camera health detection, auto-remediation, connection quality metrics (uptime, MTBF)
- **Single Binary**: Zero dependencies, embedded SPA, `CGO_ENABLED=0`
- **Xiaomi Support**: CS2 P2P protocol, cloud auth (community-driven)

## Quick Start

### Option 1: Docker

```bash
docker compose up -d
```

Open `http://localhost:9090` and complete the setup wizard in the browser.

See [`docker-compose.yml`](docker-compose.yml) for volume mounts and environment variables.

### Option 2: Build from Source

```bash
git clone https://github.com/lalmax-pro/lalmax-nvr.git
cd lalmax-nvr
./scripts/unix/build.sh
./scripts/unix/start.sh
```

Open `http://localhost:9090`.

Other scripts:

```bash
./scripts/unix/stop.sh        # Stop background process
./scripts/unix/restart.sh     # Restart
./scripts/unix/status.sh      # Show PID and health check
./scripts/unix/logs.sh        # Follow logs
./scripts/unix/run.sh         # Run in foreground
./scripts/unix/test.sh        # Run all Go tests
```

See [`scripts/README.md`](scripts/README.md) for environment variable overrides.

## Configuration

Key config section for the media engine:

```yaml
media:
  enabled: true    # Enable lalmax relay (recommended)
```

With `media.enabled=true`:
- All H264/H265 RTSP/ONVIF cameras are pulled through lalmax
- HLS/FLV/WebRTC/fMP4 playback is served by lalmax
- Recording consumes the unified lalmax stream
- MJPEG and HTTP/JPEG cameras still pull directly (lalmax limitation)

See [Configuration](docs/en/configuration.md) for full reference.

## Documentation

| Document | Description |
|----------|-------------|
| [Getting Started](docs/en/getting-started.md) | Installation, first camera setup |
| [Configuration](docs/en/configuration.md) | Full config reference |
| [API Reference](docs/en/api-reference.md) | REST API documentation |
| [GB28181 Guide](docs/en/gb28181-guide.md) | GB28181 device setup, recording, playback, intercom |
| [ONVIF Guide](docs/en/onvif-guide.md) | ONVIF camera setup, PTZ, troubleshooting |
| [Camera Guide](docs/en/camera-guide.md) | Camera protocol setup |
| [Stream Management](docs/en/stream-management-design.md) | Runtime stream inventory and binding |
| [Deployment](docs/en/deployment.md) | Reverse proxy, cross-compile |
| [MQTT Integration](docs/en/mqtt-integration.md) | MQTT trigger recording |
| [WebDAV Integration](docs/en/webdav-integration.md) | WebDAV file access |
| [Troubleshooting](docs/en/troubleshooting.md) | Common issues and fixes |

## Project Structure

```
cmd/lalmax-nvr/        # Entry point
internal/              # Core packages
  ai/                  # AI inference
  api/                 # REST API handlers + stream proxy
  ban/                 # Stream ban management
  camera/              # Camera lifecycle manager
  cleanup/             # Data cleanup tasks
  config/              # YAML config
  event/               # Event bus
  ftp/                 # FTP server
  gb28181/             # GB28181 SIP server (device mgmt, cascade, playback, intercom)
  health/              # Camera health monitoring
  media/               # lalmax engine adapter
  merge/               # Segment merge manager
  metrics/             # Prometheus metrics
  middleware/          # HTTP middleware
  model/               # Data models
  mqtt/                # MQTT client
  muxer/               # MP4 muxer
  onvif/               # ONVIF client adapter (NVR-side)
  recorder/            # H264/H265/MJPEG/HTTP-JPEG recording engines
  storage/             # SQLite DB + file manager
  streamhistory/       # Stream history tracking
  ui/                  # Embedded SPA static files
  upload/              # File upload handling
  webdav/              # WebDAV server
  wsstream/            # WebSocket stream manager (WebCodecs)
  xiaomi/              # Xiaomi camera support
onvif/                 # Standalone ONVIF library (SOAP, discovery, PTZ, imaging, events)
third/
  lal/                 # Vendored lal media library
  lalmax/              # Vendored lalmax (lal + extensions)
web/                   # Svelte 5 frontend
scripts/               # Build and management scripts
docker/                # Docker build assets
tests/                 # Integration tests
docs/                  # Documentation (EN/ZH)
```

> Playback protocols (HLS, HTTP-FLV, WebRTC, fMP4) and RTMP/SRT ingest are served
> by the embedded lalmax engine, not by separate `internal/` packages.

## Contributing

1. Run `go vet ./...` before submitting
2. Add tests for new features
3. Write clear commit messages

## License

[MIT License](LICENSE)
