# Configuration Reference

lalmax-nvr uses a YAML file for all modules. Layers, ports, and data paths: [Architecture](architecture.md). Install: [Getting Started](getting-started.md).

Example file: [`config/config.example.yaml`](../../config/config.example.yaml). The binary default is `config/lalmax-nvr.yaml`.

## Configuration File Structure

This excerpt shows selected fields; start a new deployment from the [configuration example](../../config/config.example.yaml). Cameras are normally added through the Web UI and stored in the database.

```yaml
server:
  listen: ":9090"
storage:
  root_dir: "/var/lib/lalmax-nvr"
  segment_duration: "30s"
media:
  mode: "embedded"
auth:
  username: "admin"
  password_hash: ""
  password: ""
cameras:
  - id: "cam1"
    name: "Camera Name"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://..."
    enabled: true
    onvif_endpoint: ""           # ONVIF specific
    profile_token: ""            # ONVIF specific  
    stream_encoding: ""          # ONVIF auto-detect (H264/H265)
    sub_stream_url: "rtsp://..."  # Sub-stream for live preview
    snapshot_url: "http://..."    # JPEG snapshot for thumbnails
    sample_interval: 1            # MJPEG frame sampling
    hls_max_fps: 0               # HLS frame rate limit
    vendor: ""                   # Xiaomi transport vendor
    merge:                       # Per-camera merge overrides
      enabled: false
      check_interval: "1h"
      window_size: "1h"
      batch_limit: 150
      min_segment_age: "5m"
      min_segments_to_merge: 2
cleanup:
  retention_days: 30
  check_interval: "1h"
  disk_threshold_percent: 95
merge:
  enabled: false
  check_interval: "1h"
  window_size: "1h"
  batch_limit: 200
  min_segment_age: "10m"
  min_segments_to_merge: 3
  rolling_enabled: true
  rolling_debounce: "5s"
ftp:
  enabled: true
  port: 2121
  passive_port_range: "2122-2140"
mqtt:
  enabled: false
  broker: "tcp://localhost:1883"
  topic: "lalmax-nvr"
  client_id: "lalmax-nvr"
  username: ""
  password: ""
webdav:
  enabled: true
  path_prefix: "/dav"
  read_write: false
dlna:
  enabled: false
  port: 8200
iptv:
  max_concurrent_pulls: 32
hls:
  enabled: false                 # Enable HLS/LL-HLS playback (default off)
  on_demand: true                # On-demand slicing: slice only when viewers connect (default on)
  idle_timeout: "60s"            # Stop slicing after no HLS access (default 60s)
  write_buffer_size: 100         # Async frame buffer per stream
  segment_max_size_mb: 10        # HLS segment max size in MB
  segment_count: 7               # Segments per stream (range: 3-10)
  max_streams: 4                 # Max concurrent streams (range: 1-20, RPi constraint: 4)
xiaomi:
  user_id: ""                    # Xiaomi account user ID (from auth response)
  token: ""                      # Xiaomi passToken for API access
  region: "cn"                   # Region code: cn, sg, de, etc.
observability:
  log_level: "info"              # Log level: debug, info, warn, error
  log_format: "text"             # Log format: json or text
  enable_pprof: false            # Enable pprof debug endpoints
  trace_retention: "24h"         # Local trace retention period
  max_local_traces: 10000        # Maximum locally retained traces
  local_trace_enabled: true      # Store traces locally; can also be toggled in Web Settings
  trace_ui_url_template: ""      # Jaeger/Tempo URL template containing {trace_id}
  otlp:
    enabled: false               # Export traces, metrics, and logs to an OTLP/HTTP collector
    endpoint: "http://127.0.0.1:4318"
    service_name: "lalmax-nvr"
    traces_enabled: true
    metrics_enabled: true
    logs_enabled: true
    timeout: "10s"
    metrics_export_interval: "30s"
    logs_export_interval: "1s"
version: "1.0"
```

## Server Configuration

### `server.listen`
- **Type**: string
- **Default**: `":9090"`
- **Description**: The address and port for the web server to listen on
- **Example**: `":8080"` or `"192.168.1.100:9090"`

## Storage Configuration

### `storage.root_dir`
- **Type**: string
- **Default**: `/var/lib/lalmax-nvr` (binary) or `/data` (Docker)
- **Description**: Root directory for storing recordings, database, and temporary files. All camera recordings are stored under `{root_dir}/{camera_id}/`.
- **Docker**: When running in Docker, this is set via the `NVR_DATA_DIR` environment variable. The volume mount and `NVR_DATA_DIR` must match.
- **Binary**: Can be set via `--data-dir` flag with `lalmax-nvr init`, or directly in the YAML config.
- **Example**: `/var/lib/lalmax-nvr`, `/mnt/external/nvr`, `/data`

### `storage.segment_duration`
- **Type**: string
- **Default**: `"30s"`
- **Description**: Duration of video segments (memory intensive)
- **Important**: Each segment holds all video data in RAM until completion
- **Memory Usage**:
  - 30s segments: ~15-20MB per segment
  - 60s segments: ~30-40MB per segment
  - 120s segments: ~60-80MB per segment
- **RPi Constraint**: Maximum 30 seconds on Raspberry Pi 3B
- **Example**: `"30s"`, `"1m"`, `"5m"`

## Authentication Configuration

### `auth.username`
- **Type**: string
- **Required**: Yes (for web UI and FTP)
- **Description**: Username for authentication
- **Example**: `"admin"`

### `auth.password_hash`
- **Type**: string
- **Required**: Yes (for web UI and FTP)
- **Description**: bcrypt hashed password. Use `lalmax-nvr hash-password <password>` to generate.
- **Priority**: `password_hash` takes precedence if both `password` and `password_hash` are set
- **Note**: If only `auth.password` (plaintext) is provided, the server auto-generates the hash on startup and persists it back to the config file
- **Example**: `$2a$10$N9qo8uLOickgx2ZMRZoMy...`

### `auth.password`
- **Type**: string
- **Optional**: Yes
- **Description**: Plaintext password for convenient initial setup. On first run, the server auto-hashes this value and writes it to `password_hash`, then clears the `password` field.
- **Priority**: Only used when `password_hash` is empty
- **Example**: `"admin123"`

## Camera Configuration

### Camera Structure
Each camera configuration requires these basic fields:

```yaml
cameras:
  - id: "cam1"
    name: "Camera Name"
    protocol: "rtsp"
    encoding: "h264"
    url: "camera_url"
    enabled: true
```

### `cameras[].id`
- **Type**: string
- **Required**: Yes
- **Description**: Unique identifier for the camera (auto-generated if not provided)
- **Format**: Alphanumeric, recommended kebab-case (e.g., "front-door")
- **Example**: `"front-door"`, `"cam-01"`

### `cameras[].name`
- **Type**: string
- **Required**: Yes
- **Description**: Human-readable camera name
- **Example**: `"Front Door Camera"`, `"Back Yard"`

### `cameras[].protocol`
- **Type**: string
- **Required**: Yes
- **Description**: Camera transport protocol
- **Options**: `"rtsp"`, `"http"`, `"onvif"`, `"xiaomi"`
- **Legacy Format**: Also supports `"rtsp_h264"`, `"rtsp_h265"`, `"rtsp_mjpeg"`, `"http_jpeg"` (automatically parsed to new format)
- **Note**: Legacy format is automatically parsed to the new protocol+encoding format for backward compatibility
- **Compatibility**: Both formats are supported

### `cameras[].encoding`
- **Type**: string
- **Optional**: Yes (auto-detected from legacy protocol or defaults based on protocol)
- **Description**: Video encoding format
- **Options**: `"h264"`, `"h265"`, `"mjpeg"`, `"jpeg"`
- **Valid Combinations**:
  - `protocol: "rtsp"` → `encoding: "h264"`, `"h265"`, or `"mjpeg"`
  - `protocol: "http"` → `encoding: "jpeg"`
  - `protocol: "onvif"` → `encoding: "h264"` or `"h265"` (auto-detect if not specified)
  - `protocol: "xiaomi"` → `encoding: "h264"` or `"h265"` (auto-detect)

### `cameras[].url`
- **Type**: string
- **Required**: Yes (except for ONVIF and Xiaomi cameras)
- **Description**: Camera URL or stream endpoint
- **Examples**:
  - RTSP: `"rtsp://192.168.1.100:554/stream"`
  - HTTP: `"http://192.168.1.101/capture"`
  - ONVIF: `"http://192.168.1.102:80/onvif/device_service"` (or use `onvif_endpoint`)
- **Validation**: Must have valid scheme (http/rtsp) and host

### `cameras[].username`
- **Type**: string
- **Optional**: Yes
- **Description**: Username for camera authentication
- **Example**: `"admin"`

### `cameras[].password`
- **Type**: string
- **Optional**: Yes
- **Description**: Password for camera authentication
- **Example**: `"camera-password"`

### `cameras[].enabled`
- **Type**: boolean
- **Default**: `true`
- **Description**: Whether the camera recording is enabled
- **Example**: `true` or `false`

### `cameras[].onvif_endpoint`
- **Type**: string
- **Optional**: Yes (required for ONVIF cameras if no URL provided)
- **Description**: ONVIF device service endpoint URL
- **Example**: `"http://192.168.1.100:80/onvif/device_service"`
- **Note**: If URL is set for ONVIF camera, it's automatically copied to onvif_endpoint

### `cameras[].profile_token`
- **Type**: string
- **Optional**: Yes
- **Description**: ONVIF media profile token for specific stream selection
- **Example**: `"profile_1"`
- **Note**: Optional, uses default profile if not specified

### `cameras[].stream_encoding`
- **Type**: string
- **Optional**: Yes
- **Description**: Stream encoding for ONVIF cameras (H264 or H265)
- **Options**: `"H264"`, `"H265"`
- **Note**: Empty = auto-detect from ONVIF device capabilities

### `cameras[].sub_stream_url`
- **Type**: string
- **Optional**: Yes
- **Description**: RTSP URL of a lower-resolution sub-stream for live HLS preview. When configured, the Dashboard uses this stream instead of the main stream, reducing bandwidth usage.
- **Note**: Sub-stream must use the same codec (H.264/H.265) as the main stream
- **Example**: `"rtsp://192.168.1.100:554/stream2"`

### `cameras[].snapshot_url`
- **Type**: string
- **Optional**: Yes
- **Description**: HTTP URL returning a JPEG snapshot image. When configured, the Dashboard displays snapshot thumbnails instead of live HLS streams, significantly reducing bandwidth.
- **Behavior**: Snapshots are cached for 10 seconds; stale cache is served when the camera is temporarily unreachable
- **Example**: `"http://192.168.1.100/snapshot"`, `"http://192.168.1.100/cgi-bin/snapshot.cgi"`

### `cameras[].sample_interval`
- **Type**: integer
- **Optional**: Yes
- **Default**: 1 (for MJPEG cameras only)
- **Description**: Interval for sampling MJPEG frames (seconds). Higher values reduce CPU usage but decrease frame rate.
- **Example**: `1`, `2`, `5`

### `cameras[].hls_max_fps`
- **Type**: integer
- **Optional**: Yes
- **Default**: 0 (no limit)
- **Description**: Maximum frame rate for HLS streaming. 0 = no limit.
- **Example**: `30`, `15`, `25`

### `cameras[].vendor`
- **Type**: string
- **Optional**: Yes
- **Description**: Transport vendor for Xiaomi cameras
- **Options**: `"cs2"` (default)
- **Example**: `"cs2"`

### `cameras[].audio_enabled`

- **Type**: boolean
- **Default behavior**: Loading YAML enables audio for H.264/H.265 RTSP, ONVIF, and Xiaomi cameras. JPEG/MJPEG does not support audio. When creating a device through the API, pass `false` explicitly to disable it.
- **Description**: Controls whether supported source audio is written to recordings; the source must provide a usable audio track.
- **Example**: `true`, `false`

### `cameras[].did`

- **Type**: string
- **Optional**: Yes (required for Xiaomi cameras)
- **Description**: Xiaomi Device ID from cloud service
- **Example**: `"camera_did_123"`

### `cameras[].merge`
- **Type**: object
- **Optional**: Yes
- **Description**: Per-camera merge configuration overrides
- **Note**: Only non-zero fields override the global merge config
- **Example**: See [Merge Configuration](#merge-configuration)

## Cleanup Configuration

### `cleanup.retention_days`
- **Type**: integer
- **Default**: 30
- **Range**: 1-3650
- **Description**: Delete recordings older than N days
- **Example**: `7`, `30`, `90`

### `cleanup.check_interval`
- **Type**: string
- **Default**: `"1h"`
- **Description**: How often to check for expired recordings
- **Example**: `"30m"`, `"1h"`, `"2h"`

### `cleanup.disk_threshold_percent`
- **Type**: integer
- **Default**: 95
- **Range**: 50-99
- **Description**: Start cleanup when disk usage exceeds N%
- **Example**: `90`, `95`, `98`

## Merge Configuration

### `merge.enabled`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable segment merging functionality

### `merge.check_interval`
- **Type**: string
- **Default**: `"1h"`
- **Description**: How often to check for merge candidates
- **Example**: `"30m"`, `"1h"`, `"2h"`

### `merge.window_size`
- **Type**: string
- **Default**: `"1h"`
- **Description**: Time window for merging segments (segments within this window can be merged)
- **Example**: `"30m"`, `"1h"`, `"2h"`

### `merge.batch_limit`
- **Type**: integer
- **Default**: 200
- **Description**: Maximum number of segments to merge in one batch
- **Example**: `100`, `200`, `500`

### `merge.min_segment_age`
- **Type**: string
- **Default**: `"10m"`
- **Description**: Minimum age before a segment can be merged
- **Example**: `"5m"`, `"10m"`, `"30m"`

### `merge.min_segments_to_merge`
- **Type**: integer
- **Default**: 3
- **Description**: Minimum number of segments required to trigger a periodic merge
- **Example**: `2`, `3`, `5`

### `merge.rolling_enabled`
- **Type**: boolean
- **Default**: `true` (only when `merge.enabled=true`)
- **Description**: After a segment closes, debounce then append it into the current UTC hour file

### `merge.rolling_debounce`
- **Type**: string
- **Default**: `"5s"`
- **Description**: How long rolling merge waits after the last closed segment

## FTP Configuration

FTP currently checks a plaintext password, so the hash saved by normal initialization cannot authenticate FTP clients. See the [FTP guide](ftp-integration.md) before using it.

### `ftp.enabled`
- **Type**: boolean
- **Default**: `true`
- **Description**: Enable FTP server

### `ftp.port`
- **Type**: integer
- **Default**: 2121
- **Range**: 1-65535
- **Description**: FTP control port
- **Example**: `2121`, `2021`

### `ftp.passive_port_range`
- **Type**: string
- **Default**: `"2122-2140"`
- **Description**: Passive mode port range (start-end)
- **Example**: `"2122-2140"`, `"40000-40100"`

## MQTT Configuration

### `mqtt.enabled`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable MQTT client for trigger-based recording

### `mqtt.broker`
- **Type**: string
- **Required**: Yes (if enabled)
- **Description**: MQTT broker URL
- **Example**: `"tcp://localhost:1883"`, `"tcp://192.168.1.100:1883"`

### `mqtt.topic`
- **Type**: string
- **Required**: Yes (if enabled)
- **Description**: Topic prefix; the NVR subscribes to `{topic}/trigger/+`
- **Example**: `"lalmax-nvr"`, `"home/security"`

### `mqtt.client_id`
- **Type**: string
- **Default**: none; set it explicitly when enabling MQTT
- **Description**: MQTT client identifier
- **Example**: `"lalmax-nvr"`, `"nvr-client-01"`

### `mqtt.username`
- **Type**: string
- **Optional**: Yes
- **Description**: MQTT broker authentication username
- **Example**: `"mqtt-user"`, `"admin"`

### `mqtt.password`
- **Type**: string
- **Optional**: Yes
- **Description**: MQTT broker authentication password
- **Example**: `"mqtt-password"`

## WebDAV Configuration

### `webdav.enabled`
- **Type**: boolean
- **Default**: `true`
- **Description**: Enable WebDAV server

### `webdav.path_prefix`
- **Type**: string
- **Default**: `"/dav"`
- **Description**: URL path prefix for WebDAV access
- **Example**: `"/dav"`, `"/files"`

### `webdav.read_write`
- **Type**: boolean
- **Default**: `false`
- **Description**: Allow write operations (PUT, MKCOL, DELETE, etc.)
- **Example**: `true`, `false`

## HLS Configuration

HLS playback is independent of recording pulls: disabling `hls.enabled` or using on-demand slicing does not interrupt active recording.

### `hls.enabled`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable HLS / LL-HLS playback. When disabled, the API returns 503 and background slicing stops immediately without restarting lalmax
- **Example**: `true`, `false`

### `hls.on_demand`
- **Type**: boolean
- **Default**: `true`
- **Description**: On-demand HLS slicing. When enabled, TS muxers and LL-HLS sessions start only when viewers request m3u8/ts; when disabled, slicing begins as soon as the pull stream is active
- **Example**: `true`, `false`

### `hls.idle_timeout`
- **Type**: string (duration)
- **Default**: `"60s"`
- **Description**: In on-demand mode, stop slicing and clean up temporary TS/fMP4 files after no HLS requests for this duration. Recording pulls are unaffected
- **Example**: `"30s"`, `"60s"`, `"2m"`, `"5m"`

### `hls.write_buffer_size`
- **Type**: integer
- **Default**: 100
- **Description**: Async frame buffer size per stream (units of frames)
- **Example**: `40`, `100`, `200`

### `hls.segment_max_size_mb`
- **Type**: integer
- **Default**: 10
- **Description**: Maximum HLS segment size in megabytes
- **Example**: `5`, `10`, `20`

### `hls.segment_count`
- **Type**: integer
- **Default**: 7
- **Range**: 3-10
- **Description**: Number of HLS segments per stream
- **Example**: `5`, `7`, `10`

### `hls.max_streams`
- **Type**: integer
- **Default**: 4
- **Range**: 1-20
- **RPi Constraint**: Maximum 4 on Raspberry Pi 3B
- **Description**: Maximum number of concurrent HLS streams
- **Example**: `4`, `8`, `16`

## Xiaomi Configuration

### `xiaomi.user_id`
- **Type**: string
- **Required**: Yes (if Xiaomi cameras configured)
- **Description**: Xiaomi cloud account user ID (obtained after authentication)
- **Example**: `"1234567890"`

### `xiaomi.token`
- **Type**: string
- **Required**: Yes (if Xiaomi cameras configured)
- **Description**: Xiaomi passToken for API access (obtained via `/api/xiaomi/auth`)
- **Example**: `"xiaomi_token_123"`

### `xiaomi.region`
- **Type**: string
- **Default**: `"cn"`
- **Description**: Xiaomi cloud region code
- **Options**: `"cn"`, `"sg"`, `"de"`, etc.
- **Example**: `"cn"`, `"sg"`

## Observability Configuration

### `observability.log_level`
- **Type**: string
- **Default**: `"info"`
- **Options**: `"debug"`, `"info"`, `"warn"`, `"error"`
- **Description**: NVR process log level; in embedded mode this also sets lal/lalmax internal logging (`lal.log.level` in `lalmax.conf.json`). Embedded lal/lalmax records are forwarded into the NVR service log with a `lal/lalmax` component tag. Lal stdout logging stays disabled to avoid duplicate output in `logs/lalmax-nvr.log`.
- **Example**: `"debug"`, `"info"`, `"error"`

### `observability.log_format`
- **Type**: string
- **Default**: `"text"`
- **Options**: `"json"`, `"text"`
- **Description**: Log output format
- **Example**: `"json"`, `"text"`

### `observability.enable_pprof`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable pprof debug endpoints for performance profiling
- **Note**: Use with caution in production

### `observability.otlp`

- **Description**: Optional external OTLP/HTTP export. The application reads this configuration only; `OTEL_*` environment variables are neither required nor read
- Local traces are stored in the application database, with a default 24-hour retention and a cap of 10,000 traces. Expired/excess traces are pruned periodically. Writes use an asynchronous queue; spans may be dropped with a warning if it fills
- **`local_trace_enabled`**: Controls local span storage, default `true`; it can be toggled immediately in the Web Settings Tracing tab. Turning it off keeps existing traces
- **`trace_ui_url_template`**: Optional third-party UI URL template. It must use HTTP(S) and include `{trace_id}`, for example `http://localhost:16686/trace/{trace_id}`. The web UI can show local spans and link to the configured trace UI
- **`enabled`**: Enables external export, default `false`. The frontend observability API continues to work while disabled
- **`endpoint`**: Collector base URL, default `http://127.0.0.1:4318`. The application appends `/v1/traces`, `/v1/metrics`, and `/v1/logs`
- **`service_name`**: OTel service name, default `lalmax-nvr`
- A unique `service.instance.id` is generated at process startup to distinguish replicas and restart cycles
- **`traces_enabled` / `metrics_enabled` / `logs_enabled`**: Toggle trace, metric, and structured log export independently; all default to `true`, and at least one must be enabled
- **`timeout`**: Per-export timeout, default `10s`
- **`metrics_export_interval`**: Periodic metric export interval, default `30s`
- **`logs_export_interval`**: Batched log export interval, default `1s`
- **`headers`**: Optional HTTP header map for collector authentication. Protect configuration files containing tokens as sensitive files
- Traces and logs share W3C Trace Context. Request logs include their trace/span IDs so failures can be joined to request traces
- Business spans cover camera add/update/start and ONVIF preparation, media pull startup, recording-plan reconciliation, recording-task creation, connection retries, and recording-segment persistence. Background recorders retain the starting trace context without inheriting HTTP request cancellation
- Business spans use `nvr.*` attributes to correlate `camera.id`, `stream.id`, protocol, encoding, retry count, and stop reason. Full stream URLs, usernames, and passwords are not exported
- Calls to an external lalmax engine in HTTP mode create client spans and propagate `traceparent` / `tracestate`. They record only the request method, host, path, and status code; query parameters and baggage are omitted. Enable OpenTelemetry HTTP server instrumentation in the external lalmax process as well to join both sides into one trace
- In `embedded` mode, the built-in lalmax control API, HLS/FMP4, and WebRTC HTTP requests create server spans. Publish, subscribe, and relay-pull lifecycle events carry a short event span context back to NVR over its internal SSE stream. Long-lived SSE and FLV connections are excluded from whole-connection spans
- Metrics include API request duration, status, response size, and Go goroutine, memory, GC, and scheduler measurements
- The Go OTLP logs SDK is experimental; review compatibility when upgrading OpenTelemetry Go dependencies

## Media Engine Configuration

### `media.mode`
- **Type**: string
- **Default**: `"embedded"`
- **Description**: lalmax communication mode

### `media.lalmax_http_addr`
- **Type**: string
- **Default**: `"http://127.0.0.1:12090"`
- **Description**: lalmax HTTP API address

### `media.lalmax_public_url`
- **Type**: string
- **Default**: same as `media.lalmax_http_addr`
- **Description**: Hostname used in playback URLs. Must be reachable by clients; otherwise RTSP URLs will use `127.0.0.1`

### `media.rtmp_port` / `media.rtsp_port` / `media.http_port`
- **Type**: integer
- **Description**: External lal protocol ports used to build playback URLs in `media.mode: http`. Defaults: RTMP **11935**, RTSP **15544**, HTTP-FLV/HLS-TS **18080**. In embedded mode, set the RTMP ingest port with `rtmp.port`; the embedded engine uses its default RTSP and HTTP listen ports. Set `media.lalmax_public_url` to a hostname clients can reach.

### RTSP server authentication

`media.rtsp_auth_enable` enables authentication for RTSP playback. To use it, configure both `media.rtsp_username` and `media.rtsp_password`; `media.rtsp_auth_method` is `0` for Basic or `1` for Digest. The password is encrypted in the saved config when a configuration encryption key is enabled.

In embedded mode, direct playback routes on the lalmax HTTP port (default `12090`)—WHEP, LL-HLS, HTTP-fMP4, and the HTTP-FLV proxy—use NVR local account authentication. Players must support HTTP Basic Auth or the API's `token` query parameter. If per-stream `auth.playback_tokens` are also configured, pass that media token as `?token=...` and use HTTP Basic Auth for the NVR account, since both use the same query parameter. WHEP, Jessibuca, and ZLM-compatible WebRTC playback also enforce the shared per-stream playback token. In-app `/api/cameras/.../stream` playback remains protected by NVR API authentication. RTMP, SRT, the separate lal HTTP listener (default `18080`), and native RTSP listeners do not pass through this HTTP middleware; configure their protocol authentication separately.

## DLNA configuration

The optional LAN MediaServer is disabled by default. `dlna.port` defaults to **8200**, separate from `server.listen`; `include_live` and `include_recordings` default to true. Use `interface` to select a network interface and `allowed_cidrs` to restrict clients. `max_browse_count` defaults to **200**, and `gop_cache` to **1**. The `max_media_viewers` setting exists but is not currently enforced. See the [DLNA guide](dlna.md).

## IPTV configuration

IPTV import and browser proxy playback do not start media pulls. Publishing a channel or pulling it for a recording plan requires `media.mode: embedded`. `iptv.max_concurrent_pulls` caps HLS pulls shared by publishing and recording (default **32**, range **1–128**). See the [IPTV guide](iptv.md).

## Streaming Configuration

### `streaming.default_protocol`
- **Type**: string
- **Default**: `"hls"`
- **Options**: `webrtc`, `flv`, `ws-flv`, `hls`, `ll-hls`
- **Description**: Default streaming protocol

### `streaming.preview_auto_stop_sec`
- **Type**: integer
- **Default**: `60`
- **Description**: Idle timeout (seconds) for on-demand sub-stream pulls

### `streaming.webrtc`
```yaml
streaming:
  webrtc:
    enabled: true       # default true
    max_viewers: 2      # default 2, range [1,10]
    idle_timeout: "60s" # default 60s
```

### `streaming.flv`
```yaml
streaming:
  flv:
    enabled: true        # default true
    max_viewers: 10      # default 10, range [1,50]
    idle_timeout: "60s"  # default 60s
    gop_cache_size: 1    # default 1
```

## WebSocket Configuration

```yaml
websocket:
  max_viewers: 10        # max concurrent viewers
  write_buf_size: 1024   # write buffer size
  idle_timeout: "60s"    # idle timeout
```

## RTMP Ingest Configuration

```yaml
rtmp:
  enabled: false         # default false; embedded engine listens on 11935 when enabled
  port: 11935            # optional RTMP ingest port
  stream_keys:           # stream_id → required publish token
    cam1: "your_secret_key"
```

Publish with `...?stream_key=<token>`. Streams without a configured key reject RTMP publishing. Keys are encrypted when `config.encryption_key` is configured.

## SRT Ingest Configuration

```yaml
srt:
  enabled: false         # default false; embedded engine listens on 19000 when enabled
  port: 19000            # optional SRT ingest port
  stream_keys:           # stream_id → required publish token
    cam1: "your_secret_key"
```

> **Push format**: SRT pushes should use streamid format `#!::h=<stream_id>,m=publish,u=<token>` (or `r=<stream_id>`). The `u` field carries the token. Streams without a configured key reject publishing. Keys are encrypted when `config.encryption_key` is configured.

## JT1078 Ingest Configuration

> **Experimental feature**: JT1078/JT808 has only been validated with simulated terminals and automated tests. It has not been tested with real vehicle devices; device compatibility and operational stability still require validation.

JT/T 1078 is ingested by embedded lalmax on **TCP and UDP `:1078`**. Packets are demuxed by `SIM + logic channel`, published as `{sim}_{channel}`, then fed into the group with `FeedAvPacket`. Video is H.264 / H.265. Audio is AAC / G.711A / G.711U. MP3 and G.726 frames are dropped and are not fed into lal. Embedded mode enables this by default when the field is missing. Idle UDP streams are reclaimed after 15 seconds. `phone_len` 0 auto-detects 2016 (6-byte) and 2019 (10-byte) terminal phones; set 6 or 10 to force one width.

Configure it in `lalmax.conf.json`:

```json
{
  "lalmax": {
    "jt1078_config": {
      "enable": true,
      "addr": ":1078",
      "udp_enable": true,
      "udp_addr": ":1078",
      "udp_idle_timeout_ms": 15000,
      "phone_len": 0,
      "allowed_streams": ["1003_1"]
    }
  }
}
```

Enable the JT808 signaling service separately in `lalmax-nvr.yaml` (disabled by default):

```yaml
jt808:
  enabled: true
  port: 808
  media_ip: "192.168.1.100" # must be reachable by terminals
  auth_code: "set-a-private-jt808-secret" # issued on register, verified on 0x0102
  media_tcp_port: 1078
  media_udp_port: 1078
  transport: tcp            # tcp or udp; selects the 0x9101 / 0x9201 port field
  timeout: "3s"
  # ftp_port: 2121          # optional 0x9206 override; default is ftp.port
  # ftp_user: ""            # optional; default is auth.username
  # ftp_password: ""        # optional plaintext; required on upload if only password_hash is set
```

JT1078 publishing uses an exact `{SIM}_{channel}` allowlist. When JT808 is enabled, streams granted by a live or playback command are also allowed. With JT808 disabled, only IDs in `allowed_streams` can publish; an empty list denies all JT1078 publishers. This setting is in `lalmax.conf.json`.

After registration and authentication, the following routes require an authenticated caller; all POST routes additionally require the super_admin role. Playback URLs stay on the existing HLS / FLV / WebRTC paths, for example `http://host:18080/live/1003_1.flv`.

- `GET /api/jt808/terminals` lists terminals.
- `POST /api/jt808/play` sends 0x9101. `data_type`: 0 audio+video, 1 video, 2 intercom, 3 listen, 4 broadcast. `stream`: 0 main, 1 sub.
- `POST /api/jt808/stop` closes audio and video. `POST /api/jt808/control` sends the full 0x9102: `control` 0 close, 1 switch stream, 2 pause, 3 resume, 4 close talk; `close_av` 0 both, 1 audio, 2 video.
- `POST /api/jt808/loss` sends 0x9105 with `rate` 0–100.
- `GET /api/jt808/av?key=1003` queries 0x9003.
- `POST /api/jt808/playback` and `POST /api/jt808/playback/control` send 0x9201 / 0x9202. Times are RFC3339 or `2006-01-02 15:04:05`. Drag uses `control` 5 plus `drag_to`.
- `POST /api/jt808/resources` queries 0x9205.
- `POST /api/jt808/upload` and `POST /api/jt808/upload/control` send 0x9206 / 0x9207. Empty FTP host, port, and account fall back to the local FTP server; `jt808.ftp_port`, `ftp_user`, and `ftp_password` override that. If only a password hash is configured, the request must include the password.
- `POST /api/jt808/ptz` accepts `action` `rotate`, `focus`, `iris`, `wiper`, `infrared`, or `zoom`.
- `POST /api/jt808/downlink` writes intercom audio on the terminal's existing TCP media connection. `payload` is base64. `pt` accepts 6 (G.711A), 7 (G.711U), and 19 (AAC).

There is no automatic camera enrollment and no JT808 page. Location, alarms, geofences, and encrypted frames are outside these routes.

### Simulation validation and known limitations

Run `go test ./internal/jt808 ./tools/jtsim` from the repository root and `go test ./jt1078 ./config` from `third/lalmax`. The simulator covers TCP/UDP ingest, 2013/2019 headers, and rejection of unrequested media and media sent after stop. Regression tests cover standard 0x1003 responses, authorization cleanup, shutdown, and downlink sequence numbers.

Media restart functional tests pass. The extended restart race test still reports existing races in the underlying lal logger initialization and HTTP startup/shutdown. This limitation remains unresolved. No real-device validation has been performed.

## WHIP Ingest Configuration

```yaml
whip:
  enabled: true          # default true; false only hides ingest URLs, WHEP playback stays on
  stream_keys:           # optional camera_id → streamid alias mapping
    cam1: "obs-front"
  publish_tokens:        # stream_id → required publish token
    cam1: "your_secret_key"
```

> **Push URL**: `http://<host>:12090/webrtc/whip?streamid=<stream_id>&stream_key=<token>` (OBS → Settings → Stream → WHIP); WHIP clients can also send `Authorization: Bearer <token>`. Streams without a configured key reject publishing. Keys are encrypted when `config.encryption_key` is configured. ICE mux port **4888/udp** (and TCP) must be reachable. Docker: map `12090` and `4888`. Set `media.lalmax_public_url` to a public hostname.

## Health Monitoring Configuration

```yaml
health:
  enabled: true
  events_retention: "168h"       # event retention (7 days)
  alerts:
    cooldown: "5m"               # alert cooldown
    mqtt: false                  # send alerts via MQTT
  layer1:                        # connection health
    offline_threshold: "5m"      # offline detection threshold
  layer2:                        # stream quality
    bitrate_change_threshold: 0.5
    min_fps: 5
    max_idr_interval: "10s"
  layer2_5:                      # freeze detection
    freeze_timeout: "30s"
  auto_remediation:
    enabled: false               # auto-restart failed cameras
    max_restarts_per_hour: 3
    cooldown_minutes: 10
    blacklist_hours: 1
    global_max_per_min: 5
  rediscovery:
    enabled: true                # unicast IP self-healing by ONVIF serial
    max_parallel: 16
    probe_timeout: "2s"
```

## ONVIF auto-discover (plug-and-play)

Disabled by default. When enabled, the NVR listens for WS-Discovery Hello and periodically Probes the LAN, then enrolls new ONVIF cameras (pending activation if credentials are missing).

```yaml
auto_discover:
  enabled: false
  listen_for_hello: true
  scan_interval: 60s
  default_username: ""
  default_password: ""
```

## Event recording

Used when a stream recording plan `mode` is `event` (MQTT / ONVIF motion) or `adaptive` (sparse IDR while calm). Plans hang off `stream_id`; see [Recording plans](recording-plans.md).

```yaml
event:
  post_roll: 30s
  max_duration: 5m
```

Cameras may also set `adaptive.timelapse_interval` (default `30s`) for calm-state keyframe spacing.

## Remote Log Configuration

```yaml
remote_log:
  enabled: false
  endpoint: "http://localhost:9428/insert/jsonline"  # VictoriaLogs URL
  format: "jsonline"             # jsonline or loki
```

## Metrics Auth Configuration

```yaml
metrics_auth:
  username: "metrics_user"
  password: "metrics_password"
  # Or use password hash:
  # password_hash: "$2a$10$..."
```

> When `username` and `password` (or `password_hash`) are set, `/metrics` uses those Basic Auth credentials. When empty, it requires the NVR API credentials. `/metrics` is never public.

## AI Detection Configuration

```yaml
ai:
  enabled: false                 # enable AI detection
  backend: "browser"             # browser (browser-side ONNX) / webhook (third-party detector) / disabled
  model_url: "/models/yolo11n.onnx" # browser model URL; place model file in data-dir/models/
  inference_timeout_ms: 30000    # inference timeout (default 30000ms)
  frame_skip_rate: 3             # detect every N frames (default 3)
  confidence_threshold: 0.3      # confidence threshold (default 0.3)
```

Browser inference runs in the WebCodecs player, preferring WebGPU with a WASM fallback. Third-party mode keeps the `POST /api/ai/webhook` endpoint for external detectors that subscribe to camera RTSP streams.

## Camera Protocol Examples

### RTSP Camera
```yaml
cameras:
  - id: "front-door"
    name: "Front Door Camera"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    username: "admin"
    password: "camera-password"
    enabled: true
    sub_stream_url: "rtsp://192.168.1.100:554/stream2"
    snapshot_url: "http://192.168.1.100:8080/snapshot"
```

### HTTP JPEG Camera
```yaml
cameras:
  - id: "backyard"
    name: "Back Yard Camera"
    protocol: "http"
    encoding: "jpeg"
    url: "http://192.168.1.101/capture"
    sample_interval: 1
    enabled: true
```

### ONVIF Camera
```yaml
cameras:
  - id: "lobby"
    name: "Lobby Camera"
    protocol: "onvif"
    url: "http://192.168.1.102:80/onvif/device_service"
    enabled: true
    # Optional: specify encoding
    encoding: "h264"
    # Optional: specify stream encoding
    stream_encoding: "H264"
```

### Xiaomi Camera
```yaml
xiaomi:
  user_id: "1234567890"
  token: "xiaomi_token_123"
  region: "cn"

cameras:
  - id: "xiaomi-cam"
    name: "Xiaomi Camera"
    protocol: "xiaomi"
    encoding: "h264"
    did: "xiaomi_device_id"
    vendor: "cs2"
    enabled: true
```

## Migration from Legacy Format

Legacy protocol formats like `"rtsp_h264"` are automatically converted to the new separate `protocol` and `encoding` fields:

```yaml
# Old format (still supported)
cameras:
  - id: "cam1"
    protocol: "rtsp_h264"
    url: "rtsp://..."

# Automatically converted to new format:
# protocol: "rtsp"
# encoding: "h264"
```

## Validation Rules

The configuration is validated on startup with these constraints:

- **Camera IDs**: Must be unique across all cameras
- **Camera URLs**: Must have valid scheme (http/rtsp) and host
- **ONVIF Cameras**: Must have either URL or onvif_endpoint
- **Xiaomi Cameras**: Must have xiaomi.token configured
- **Port Numbers**: Must be in range 1-65535
- **Segment Duration**: Maximum 30 seconds on RPi 3B
- **Retention Days**: Must be between 1 and 3650
- **Disk Threshold**: Must be between 50% and 99%
- **Merge Configuration**: All duration fields must be valid
- **HLS Configuration**: 
  - Segment count: 3-10
  - Max streams: 1-20 (4 on RPi 3B)
  - `idle_timeout`: must be a valid positive duration

## File Paths and Locations

- **Default config path**: `./lalmax-nvr.yaml`
- **Default storage**: `/var/lib/lalmax-nvr`
- **Recordings**: `{root_dir}/recordings/{encoding}/{camera_id}/`
- **Segments**: `{root_dir}/recordings/{encoding}/{camera_id}/`
- **Snapshots**: `{root_dir}/snapshots/{camera_id}/`
- **WebDAV**: `{root_dav}{root_dir}/` (where root_dav is reverse proxy path)

## Quick Configuration

### Basic Setup
```yaml
server:
  listen: ":9090"
storage:
  root_dir: "/var/lib/lalmax-nvr"
  segment_duration: "30s"
auth:
  username: "admin"
  password: "your-password-here"
cameras:
  - id: "cam1"
    name: "Camera 1"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
cleanup:
  retention_days: 30
  disk_threshold_percent: 95
```

### Complete Setup with All Features
```yaml
server:
  listen: ":9090"
storage:
  root_dir: "/mnt/data/nvr"
  segment_duration: "30s"
auth:
  username: "admin"
  password_hash: "$2a$10$N9qo8uLOickgx2ZMRZoMy..."
cameras:
  - id: "front-door"
    name: "Front Door"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
    sub_stream_url: "rtsp://192.168.1.100:554/sub"
  - id: "xiaomi-cam"
    name: "Xiaomi Camera"
    protocol: "xiaomi"
    encoding: "h264"
    did: "xiaomi_device_id"
    vendor: "cs2"
    enabled: true
xiaomi:
  user_id: "1234567890"
  token: "xiaomi_token_123"
  region: "cn"
cleanup:
  retention_days: 30
  disk_threshold_percent: 90
merge:
  enabled: true
  check_interval: "1h"
  batch_limit: 200
ftp:
  enabled: true
  port: 2121
mqtt:
  enabled: true
  broker: "tcp://192.168.1.100:1883"
  topic: "lalmax-nvr"
webdav:
  enabled: true
  read_write: false
hls:
  max_streams: 4
observability:
  log_level: "info"
  otlp:
    enabled: true
    endpoint: "http://otel-collector:4318"
    service_name: "lalmax-nvr"
    traces_enabled: true
    metrics_enabled: true
    logs_enabled: true
    metrics_export_interval: "30s"
    logs_export_interval: "1s"
```
