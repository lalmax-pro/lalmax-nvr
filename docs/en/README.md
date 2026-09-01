# Documentation (English)

[中文](../zh/README.md) · [Repository README](../../README.md)

## Start here

| Document | Description |
|----------|-------------|
| [Getting Started](getting-started.md) | Install, first camera, Web UI |
| [Architecture](architecture.md) | Layers, live/recording paths, pull vs push, ports |
| [Configuration](configuration.md) | Full YAML reference |
| [Deployment](deployment.md) | Docker, reverse proxy, cross-compile |
| [Troubleshooting](troubleshooting.md) | Common issues |

The release [QUICKSTART](../QUICKSTART.md) is a short cheat sheet; this tree is the source of truth.

## Devices and protocols

| Document | Description |
|----------|-------------|
| [Camera Guide](camera-guide.md) | RTSP / HTTP / codecs |
| [ONVIF Guide](onvif-guide.md) | Discovery, GetStreamUri then RTSP pull, PTZ |
| [GB28181 Guide](gb28181-guide.md) | SIP platform, device PS/RTP push, playback, talk |
| [Xiaomi](xiaomi-setup.md) | CS2 P2P fetch and inject |

## Integrations

| Document | Description |
|----------|-------------|
| [API Reference](api-reference.md) | REST API |
| [MQTT](mqtt-integration.md) | Event-triggered recording |
| [FTP](ftp-integration.md) | FTP access to recordings |
| [WebDAV](webdav-integration.md) | WebDAV access to recordings |
| [AI detection](ai-setup-guide.md) | Inference and overlay |
| [MediaMTX](mediamtx-guide.md) | CSI and similar via MediaMTX |

## Design notes

| Document | Description |
|----------|-------------|
| [Stream management](stream-management-design.md) | Push ingest vs camera binding |
| [WebCodecs player](wasm-player-design.md) | Low-latency live |
| [GB catalog](gb-catalog-design.md) | GB28181 catalog tree |
| [Hikvision SDK](hikvision-sdk-integration.md) | Optional SDK path |
| [Map analysis](map-feature-analysis.md) | Map-related notes |
