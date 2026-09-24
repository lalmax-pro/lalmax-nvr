# Documentation

[中文](../zh/README.md) · [Project README](../../README.md)

Find guides by task. For configuration examples, see [`config/config.example.yaml`](../../config/config.example.yaml). A running NVR serves its OpenAPI documentation at `/docs/`.

## Getting started and operations

| Guide | Covers |
|-------|--------|
| [Getting started](getting-started.md) | Install, start, and add a first camera |
| [Deployment](deployment.md) | Docker, binary deployment, and reverse proxies |
| [Configuration](configuration.md) | Main settings and examples; compare with the configuration example above |
| [Troubleshooting](troubleshooting.md) | Common runtime issues |
| [Architecture](architecture.md) | The NVR, media engine, and common ingest paths |

## Media sources and playback

| Guide | Covers |
|-------|--------|
| [Cameras](camera-guide.md) | RTSP and HTTP cameras, codecs |
| [ONVIF](onvif-guide.md) | Discovery, stream access, and PTZ |
| [GB28181](gb28181-guide.md) | GB28181 device ingest and operations |
| [IPTV](iptv.md) | Import and probe M3U, play in the browser, publish to NVR for other protocols, or create recording plans |
| [Xiaomi cameras](xiaomi-setup.md) | Xiaomi camera ingest |
| [MediaMTX](mediamtx-guide.md) | Optional camera ingest through MediaMTX |
| [DLNA](dlna.md) | Discover and play live streams and recordings on LAN players |

## Recording and integrations

| Guide | Covers |
|-------|--------|
| [Recording plans](recording-plans.md) | Continuous, scheduled, and event recording for streams |
| [Recording flow](recording-flow.md) | How recording tasks start, write, and stop |
| [API reference](api-reference.md) | REST API usage; the OpenAPI spec is [`openapi.yaml`](../../internal/docsportal/openapi.yaml) |
| [MQTT](mqtt-integration.md) | Event-triggered recording |
| [WebDAV](webdav-integration.md) / [FTP](ftp-integration.md) | Browse stored files; the FTP guide covers the current auth limitation |
| [AI detection](ai-setup-guide.md) | Deploy and configure detection services |
