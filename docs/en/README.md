# lalmax-nvr Documentation

[中文](../zh/README.md) · [Project README](../../README.md)

Documentation is organized by reader type.

---

## 👤 User Documentation

For operators and end users.

### Getting Started & Operations

| Document | Content |
|----------|---------|
| [Getting Started](getting-started.md) | Install, start, and add your first camera |
| [Deployment Guide](deployment.md) | Docker, binary deployment, and reverse proxy |
| [Configuration](configuration.md) | Key config fields and examples; see [`config/config.example.yaml`](../../config/config.example.yaml) |
| [Architecture](architecture.md) | Layered design, ingest paths, default ports, module responsibilities |
| [Database Migration](database-migration.md) | PostgreSQL / MySQL setup and online migration from SQLite |
| [Troubleshooting](troubleshooting.md) | Common runtime issues |
| [Releases & CI](releases.md) | Version release process and upgrade notes |

### Media Ingest

| Document | Content |
|----------|---------|
| [Media Sources Overview](media-sources.md) | Unified entry for cameras, live URLs, IPTV, Emby, and encoder push streams |
| [Camera Guide](camera-guide.md) | RTSP, HTTP cameras and encoding configuration |
| [ONVIF](onvif-guide.md) | Device discovery, stream retrieval, and PTZ control |
| [GB28181](gb28181-guide.md) | National standard device integration |
| [JT1078 / JT808](configuration.md#jt1078) | `:1078` media ingest and optional `:808` signaling |
| [VoIP / Intercom](voip.md) | SIP terminals and PBX, web dial-out, WebRTC two-way intercom, call history |
| [Xiaomi Cameras](xiaomi-setup.md) | Xiaomi (CS2) camera integration |
| [MediaMTX](mediamtx-guide.md) | Optional MediaMTX-based camera ingest |
| [IPTV](iptv.md) | Import and check M3U, browser playback; publish to NVR for other protocols or recording |
| [Emby Media Library](emby.md) | Browse movies/series and publish as live streams; auto end-of-file or loop |

### Recording & Storage

| Document | Content |
|----------|---------|
| [Recording Plans](recording-plans.md) | Set up continuous, scheduled, or event-based recording |
| [Recording Flow](recording-flow.md) | How group writers subscribe to lalmax groups and plans toggle write state |
| [WebDAV](webdav-integration.md) | Browse stored files |
| [FTP](ftp-integration.md) | FTP access and current authentication limitations |

### Features & Integrations

| Document | Content |
|----------|---------|
| [Home Assistant](home-assistant.md) | Stream publishing, live preview, thumbnails, online status, PTZ, and events |
| [AI Detection](ai-setup-guide.md) | Deploy and configure detection service |
| [MQTT](mqtt-integration.md) | Event-triggered recording |
| [DLNA](dlna.md) | Discover and watch live streams and recordings on LAN players |
| [Event Hub](events.md) | Filter, real-time updates, and acknowledge health, recording, AI, and MQTT events |

### API

| Document | Content |
|----------|---------|
| [API Reference](api-reference.md) | REST API reference; OpenAPI spec at [`openapi.yaml`](../../internal/docsportal/openapi.yaml) |

> A running NVR also serves browsable OpenAPI docs at `/docs/`.

---

## 🔧 Developer Documentation

For contributors and internal developers.

| Document | Content |
|----------|---------|
| [Contributing Guide](contributing.md) | Dev setup, PR process, commit conventions, CI overview |
| [Module Guide](module-guide.md) | Responsibilities, interfaces, and extension points for each `internal/` package |
| [Testing Guide](testing-guide.md) | Test strategy, race testing, media acceptance standards |
| [ADR Index](adr/README.md) | Architecture Decision Records |

---

## 📐 Design Documents

Internal design drafts and technical decisions, for review and reference.

> These documents reflect design intent at a point in time. Some features are incomplete or still evolving — check the status notes within each document.

### General Real-Time Transcoding (Issue #66)

| Document | Status |
|----------|--------|
| [Transcoding Architecture Design](transcoding-design.md) | Proposed, P0 merged |
| [P0 Decision Record](transcoding-p0-decisions.md) | Technical review complete |
| [Transcoding Deployment Notes](transcoding-deployment.md) | Updated with P1 implementation |
| [Transcoding Acceptance Record](transcoding-testing.md) | Continuously updated |

### Other Design Drafts

| Document | Status |
|----------|--------|
| [Stream Management Design](stream-management-design.md) | Draft, P1 direction |
| [WASM Player & AI Detection Design](wasm-player-design.md) | Draft |
