# Getting Started with lalmax-nvr

## What is lalmax-nvr

lalmax-nvr is a business NVR on top of an embedded lal / lalmax media engine. H.264 / H.265 is ingested once: RTSP/ONVIF **pull**, GB28181 **PS/RTP push** after INVITE, and RTMP/SRT/WHIP **publish** share the same live fan-out. The NVR owns devices, recording, storage, and the web UI. Diagrams and ports: [Architecture](architecture.md).

**Key Features:**

- RTSP (H.264, H.265, MJPEG), HTTP JPEG, ONVIF, GB28181, Xiaomi CS2
- Web UI: multi-protocol live view, 24h timeline, continuous VOD (HLS fMP4)
- Recording modes: continuous / scheduled / event / adaptive / off; rolling hour merge
- WebDAV and FTP for recordings; MQTT event-triggered recording
- Single static binary with embedded web UI — no runtime dependencies

Documentation index: [docs/en/README.md](README.md).

## Quick Start (5 Minutes)

### Option 1: Pre-built Binary (Recommended)

Download the latest binary for your architecture from [GitHub Releases](https://github.com/lalmax-pro/lalmax-nvr/releases):

```bash
# AMD64 (most PCs/servers)
wget https://github.com/lalmax-pro/lalmax-nvr/releases/latest/download/lalmax-nvr-amd64
chmod +x lalmax-nvr-amd64

# ARM64 (Raspberry Pi, etc.)
wget https://github.com/lalmax-pro/lalmax-nvr/releases/latest/download/lalmax-nvr-arm64
chmod +x lalmax-nvr-arm64
```

Initialize config and start:

```bash
./lalmax-nvr-amd64 init --password yourpassword
./lalmax-nvr-amd64 -config lalmax-nvr.yaml
```

Open http://localhost:9090 in your browser.

### Option 2: Docker

```bash
docker compose up -d
```

Open http://localhost:9090 in your browser.

> **Note**: No config preparation needed! lalmax-nvr auto-initializes when started without a config file.

#### Changing the Storage Location

By default, recordings are stored in `./data` on the host (mapped to `/data` inside the container).
To store recordings on an external drive or a different directory, edit `docker-compose.yml`:

```yaml
    volumes:
      - /mnt/external/nvr:/data    # ← change to your host path
    environment:
      - NVR_DATA_DIR=/data          # must match the right side of the volume mount
      # - NVR_UID=1000               # match the UID that owns the host directory
      # - NVR_GID=1000               # match the GID that owns the host directory
```

> **Important**: The right side of the volume mount (`:data`) and `NVR_DATA_DIR` must always match.
> If the container fails to start or keeps restarting, check that the host directory exists and is
> writable by the configured UID/GID (default: 1000:1000).

### Option 3: Build from Source

Requires Go 1.26+ and Node.js (for frontend):

```bash
git clone https://github.com/lalmax-pro/lalmax-nvr.git
cd lalmax-nvr
./scripts/unix/build.sh
./lalmax-nvr init --password yourpassword
./lalmax-nvr -config lalmax-nvr.yaml
```

For cross-compiling to ARM64 (e.g. Raspberry Pi):

```bash
GOOS=linux GOARCH=arm64 ./scripts/unix/build.sh
```

## First-Time Setup

### Using `lalmax-nvr init`

The `init` subcommand creates a config file with secure defaults:

```bash
./lalmax-nvr init --password yourpassword
```

Options:

| Flag | Default | Description |
|------|---------|-------------|
| `--password` | (prompted) | Admin password for Web UI |
| `--username` | `admin` | Admin username |
| `--data-dir` | `/var/lib/lalmax-nvr` | Data directory for recordings and DB |
| `--listen` | `:9090` | HTTP listen address |
| `--config` | `lalmax-nvr.yaml` | Config file path |
| `--force` | false | Overwrite existing config |

### Password Setup

There are three ways to set the admin password:

1. **`lalmax-nvr init --password <pw>`** — sets the hashed password during setup (recommended)
2. **Plaintext in config** — set `auth.password` in YAML; auto-hashed to `password_hash` on first start
3. **Manual hash** — generate with `lalmax-nvr hash-password <pw>` and paste into `auth.password_hash`

### Default Paths

| Path | Description |
|------|-------------|
| `/var/lib/lalmax-nvr/` | Data directory (recordings, database) |
| `/var/lib/lalmax-nvr/lalmax-nvr.db` | SQLite database |
| `lalmax-nvr.yaml` | Configuration file |

## Adding Your First Camera

lalmax-nvr uses a **separate transport + encoding** format for camera protocols:

- **Transport**: `rtsp`, `http`, `onvif`, `xiaomi`
- **Encoding**: `h264`, `h265`, `mjpeg`, `jpeg`

> The old combined format (`rtsp_h264`, `rtsp_h265`, `rtsp_mjpeg`, `http_jpeg`) still works for backward compatibility.

### RTSP H.264 Camera

```yaml
cameras:
  - id: "front-door"
    name: "Front Door"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
```

### RTSP H.265 Camera

```yaml
cameras:
  - id: "driveway"
    name: "Driveway"
    protocol: "rtsp"
    encoding: "h265"
    url: "rtsp://192.168.1.103:554/stream"
    enabled: true
```

### HTTP JPEG Camera

```yaml
cameras:
  - id: "garage"
    name: "Garage"
    protocol: "http"
    encoding: "jpeg"
    url: "http://192.168.1.102:8080/snapshot"
    enabled: true
```

### ONVIF Camera

```yaml
cameras:
  - id: "lobby"
    name: "Lobby"
    protocol: "onvif"
    url: "http://192.168.1.104:80/onvif/device_service"
    username: "admin"
    password: "camera123"
    enabled: true
```

> ONVIF auto-detects the encoding. The `encoding` field can be omitted.

### Using the Old Combined Format

All of these still work:

```yaml
cameras:
  - id: "cam1"
    name: "Legacy Cam"
    protocol: "rtsp_h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
```

After editing the config, restart lalmax-nvr or use the Web UI to add cameras at runtime. National-standard devices: [GB28181 Guide](gb28181-guide.md).

## Accessing lalmax-nvr

### Web UI

Open http://your-server:9090 and log in with your configured credentials. From the Web UI you can:

- View live camera streams (HLS / FLV / WebRTC / fMP4 / WebCodecs; copyable RTSP)
- Play back and download recordings (single file or continuous day VOD)
- Add, edit, and remove cameras; ONVIF discovery and PTZ
- View storage statistics and trends
- Configure settings (including rolling merge)

### WebDAV

WebDAV is available at `/dav/` (enabled by default, read-only by default):

```bash
curl -u admin:password http://your-server:9090/dav/
```

Mount in a file manager: `davs://your-server:9090/dav/`

To enable read-write access, set `webdav.read_write: true` in config.

### FTP

FTP is available on port 2121 (enabled by default):

```bash
ftp your-server 2121
# Username: admin
# Password: (your password)
```

## Common Issues

### Service won't start

- Check config file syntax: `cat lalmax-nvr.yaml`
- Verify the data directory exists and is writable: `ls -la /var/lib/lalmax-nvr/`
- If using Docker, check logs with `docker logs lalmax-nvr`
- If running the binary directly, check the terminal or log file used to start the process

### Permission errors

- Make sure the configured storage directory is writable by the user or container UID/GID running lalmax-nvr.

### Port conflicts

- Default port is 9090. If it's in use, change `server.listen` in config (e.g. `":8080"`)

### Can't connect to camera

- Verify the camera URL works with VLC or ffplay: `ffplay rtsp://192.168.1.100:554/stream`
- Check network connectivity: `ping 192.168.1.100`
- Ensure camera credentials are correct
- Check if the camera requires a specific sub-stream URL for H.265

### High memory usage on Raspberry Pi

- Reduce `segment_duration` to `30s` (default). Longer durations hold more data in RAM.
- The RPi 3B has ~900MB RAM. With 4 cameras at 30s segments, expect ~300MB stable usage.
